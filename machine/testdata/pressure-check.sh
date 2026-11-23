# Each pressure in turn, on a guest of one vCPU and 1 GiB, for TestThePressureProbeReportsEachPressure.
# Each phase is named on the console when it starts; the host reads which phase an event fell in.
set -u
# It starts with the boot's rest, after multi-user.target's first units.
for i in $(seq 200); do
    systemctl is-active --quiet spin-machine-pressure.service && break
    sleep 0.1
done
systemctl is-active --quiet spin-machine-pressure.service || { echo PRESSURE_CHECK_FAILED: the sender is not running; exit 1; }
# Once it has dialled the host, which the host also sees.
sleep 2
loops() { n=$1; s=$2; i=0; while [ "$i" -lt "$n" ]; do timeout "$s" sh -c 'while :; do :; done' & i=$((i + 1)); done; wait; }

echo PHASE idle; sleep 4
# Three runnable loops on one vCPU: the guest is short of CPU, and the host has a core for it.
echo PHASE cpu; loops 3 5; sleep 3
# One loop, which one vCPU runs without waiting, while the host runs its hogs on the vCPU's core.
echo PHASE steal; loops 1 8; sleep 3
# Reads the disk past the guest's cache, 4 KiB at a time from two readers: the guest waits on its
# disk for each. 1 MiB reads from one reader, off a disk on the host's tmpfs, stalled the guest
# just over the trigger, and not every time (laptop, 2026-11-21).
echo PHASE io
timeout 5 dd if=/dev/vda of=/dev/null bs=4k iflag=direct status=none &
timeout 5 dd if=/dev/vda of=/dev/null bs=4k skip=100000 iflag=direct status=none &
wait; sleep 3
# 700 MiB held in a tmpfs, which nothing can reclaim without swap, and the disk's first 400 MiB
# read again and again through what is left: what the guest reads it reads back after reclaiming
# it, and PSI counts a refault of a working set as a memory stall. A stream read once is
# reclaimed clean and stalls nothing (it did not, 2026-11-21).
echo PHASE memory
mkdir -p /run/hold && mount -t tmpfs -o size=2G hold /run/hold
head -c 700M /dev/zero > /run/hold/x
# Bounded by timeout and not by a clock read between reads: one read under the thrash took
# 90 s once (laptop, 2026-11-21).
timeout 12 sh -c 'while :; do dd if=/dev/vda of=/dev/null bs=1M count=400 status=none; done'
rm /run/hold/x; umount /run/hold
sleep 3
echo PHASE oom
head -c 3000M /dev/zero | tail > /dev/null
sleep 3
# Each limit spin sets on QEMU's cgroup, which the host sets as the phase starts and lifts as the
# next one does. One loop on the one vCPU, against cpu.max of a fifth of a CPU.
echo PHASE cpulimit; loops 1 5; sleep 2
# The second disk, which is on a block device of the host, read again and again: the host caches
# what QEMU reads in QEMU's cgroup, past the memory.high the host set just above what QEMU held.
echo PHASE memlimit
timeout 5 sh -c 'while :; do dd if=/dev/disk/by-id/virtio-lim of=/dev/null bs=1M iflag=direct status=none; done'
sleep 2
# The same disk, out of the host's cache, against io.max's rbps.
echo PHASE iolimit
timeout 5 dd if=/dev/disk/by-id/virtio-lim of=/dev/null bs=64k iflag=direct status=none
sleep 2
# The same disk against a limit inside QEMU (SetDiskLimit), which the cgroup never sees.
echo PHASE disklimit
timeout 5 dd if=/dev/disk/by-id/virtio-lim of=/dev/null bs=64k iflag=direct status=none
sleep 2
echo PHASE done; sleep 1
echo PRESSURE_CHECK_OK
