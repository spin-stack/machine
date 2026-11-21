# spin's shape, then its ceiling, for TestAMachineRunsInARootOfItsFilesWithinItsOverhead. Each phase
# is named on the console when it starts. At "shape" the host plugs every vCPU, all the memory
# and four disks, which the guest waits for; at "plugged" it checkpoints the machine and continues
# it while the guest fills its memory; at the end it checkpoints it again.
set -u
fail() { echo "OVERHEAD_CHECK_FAILED: $*"; exit 1; }
mib() { awk -v k="$1" '$1 == k":" { print int($2 / 1024) }' /proc/meminfo; }
# Fills memory with a tmpfs file of $1 MiB from /dev/urandom, which no page sharing or zero page
# on the host can fold.
fill() {
    mount -t tmpfs -o size="$1"m fill /mnt || fail mounting a tmpfs of "$1" MiB
    dd if=/dev/urandom of=/mnt/fill bs=1M count="$1" status=none 2>/dev/null || true
}
# Reads each disk from several readers at once, direct and through the guest's cache, for $secs
# s, and writes and trims each but the root.
io() {
    for d in "$@"; do
        [ -b "$d" ] || fail no disk "$d"
        for n in 0 1 2 3; do
            timeout "$secs" dd if="$d" of=/dev/null bs=64k skip=$((n * 1000)) iflag=direct status=none &
            timeout "$secs" dd if="$d" of=/dev/null bs=1M skip=$((n * 50)) status=none &
        done
        [ "$d" = /dev/vda ] && continue
        timeout "$secs" dd if=/dev/urandom of="$d" bs=1M seek=128 count=64 oflag=direct conv=fsync status=none &
        timeout "$secs" blkdiscard -o 0 -l 67108864 "$d" &
    done
}

for i in $(seq 300); do [ "$(mib MemTotal)" -ge 2000 ] && break; sleep 0.1; done
[ "$(mib MemTotal)" -ge 2000 ] || fail the guest has "$(mib MemTotal)" MiB, not the 2048 spin sizes it to
fill 1400
secs=10
io /dev/vda /dev/vdb
fstrim / || true
wait
echo PHASE shape

for i in $(seq 600); do
    [ "$(nproc)" -ge 16 ] && [ "$(mib MemTotal)" -ge 8000 ] && [ -b /dev/sdd ] && break
    sleep 0.1
done
[ "$(nproc)" -ge 16 ] || fail "$(nproc)" vCPUs online, not 16
[ "$(mib MemTotal)" -ge 8000 ] || fail the guest has "$(mib MemTotal)" MiB, not 8192
echo PHASE plugged
umount /mnt
fill $(( $(mib MemAvailable) - 512 ))
echo PHASE ceiling
# A block every 512 MiB of the 64 GiB disk: each allocates one of its overlay's L2 tables, and all of
# them are its L2 cache.
for d in /dev/sd?; do [ "$(blockdev --getsize64 "$d")" -gt 60000000000 ] && big=$d; done
[ -n "${big:-}" ] || fail no 64 GiB disk
n=0
while [ "$n" -lt 128 ]; do
    dd if=/dev/urandom of="$big" bs=4k count=1 seek=$((n * 131072)) oflag=direct conv=notrunc status=none || fail writing "$big"
    n=$((n + 1))
done
i=0
while [ "$i" -lt 16 ]; do timeout 15 sh -c 'while :; do :; done' & i=$((i + 1)); done
secs=15
io /dev/vda /dev/vdb /dev/sda /dev/sdb /dev/sdc /dev/sdd
fstrim / || true
wait
echo PHASE done
echo OVERHEAD_CHECK_OK
