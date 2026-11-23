#!/bin/bash
# docs/image.md, "The filesystem"
set -euo pipefail

OUT="${OUT:-/out/image/rootfs.qcow2}"

SHARE="${SHARE:-$(dirname "$(dirname "$OUT")")}"
# docs/image.md, "The filesystem"
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-0}"

# docs/image.md, "The toolchain"
bin=/out/bin
for tool in mkfs.ext4 e2fsck dumpe2fs debugfs; do
    test -x "$bin/$tool" || {
        echo "ERROR: $bin/$tool is missing — spin-machine-dev build e2fsprogs" >&2
        exit 1; }
done
export MKE2FS_CONFIG=/out/e2fsprogs/mke2fs.conf
test -f "$MKE2FS_CONFIG" || { echo "ERROR: $MKE2FS_CONFIG is missing — spin-machine-dev build e2fsprogs" >&2; exit 1; }

mkdir -p "$(dirname "$OUT")" "$SHARE" /work/out /cache
cd /work

echo "==> mkosi: assembling the userland"
compgen -G '/debs/systemd/systemd_*+spin[0-9]*_*.deb' > /dev/null ||
    { echo "ERROR: /debs/systemd has no systemd package of ours - spin-machine-dev build systemd" >&2; exit 1; }
compgen -G '/debs/openssh/openssh-server_*+spin[0-9]*_*.deb' > /dev/null ||
    { echo "ERROR: /debs/openssh has no openssh-server package of ours - spin-machine-dev build openssh" >&2; exit 1; }
mkosi --force --package-directory=/debs/systemd --package-directory=/debs/openssh build
tree=/work/out/rootfs
test -d "$tree" || { echo "ERROR: mkosi produced no tree at $tree" >&2; exit 1; }

install -D -m 0755 /out/guest/spin-machine-pressure "$tree/usr/libexec/spin-machine-pressure"

# docs/image.md, "What build.sh checks, and why"
: > "$tree/etc/machine-id"
rm -f "$tree/var/lib/dbus/machine-id"
rm -f "$tree/etc/hostname" "$tree/etc/resolv.conf"
rm -f "$tree"/etc/ssh/ssh_host_*
rm -f "$tree/var/lib/systemd/random-seed"

for d in etc tmp proc sys dev run; do
    mkdir -p "$tree/$d"
done
chmod 1777 "$tree/tmp"

# docs/image.md, "The filesystem"
harvested="$tree/var/lib/spin-machine-licenses.txt"
test -s "$harvested" || {
    echo "ERROR: no licence texts were collected — the image would be redistributed" >&2
    echo "       without the copyright notices its licences require. See" >&2
    echo "       image/mkosi.postinst.chroot." >&2
    exit 1; }
mv "$harvested" "$SHARE/licenses.txt"
echo "==> $(grep -c '^======== ' "$SHARE/licenses.txt" || true) licence texts, $(du -h "$SHARE/licenses.txt" | cut -f1)"

dpkg-query --admindir="$tree/var/lib/dpkg" -W -f='${Package} ${Version} ${Architecture}\n' \
    2>/dev/null | sort > "$SHARE/packages.txt"
echo "==> $(wc -l < "$SHARE/packages.txt") packages recorded"

# docs/image.md, "The rebuilt packages"
grep -qE '^systemd [^ ]+\+spin[0-9]+ ' "$SHARE/packages.txt" ||
    { echo "ERROR: the image's systemd is the archive's, not image/systemd's" >&2; exit 1; }
grep -qE '^openssh-server [^ ]+\+spin[0-9]+ ' "$SHARE/packages.txt" ||
    { echo "ERROR: the image's openssh-server is the archive's, not image/openssh's" >&2; exit 1; }
while read -r pkg version _; do
    case "$version" in *+spin[0-9]*) ;; *) continue ;; esac
    want=$(dpkg-query --admindir="$tree/var/lib/dpkg" -W -f='${db:Status-Want}' "$pkg")
    [ "$want" = hold ] || { echo "ERROR: $pkg $version is ours and not held (it is '$want')" >&2; exit 1; }
    echo "==> held: $pkg $version"
done < "$SHARE/packages.txt"

# docs/image.md, "The update stamps". Last before the inventory: nothing after it changes /usr.
usr_ns=$(stat -c %.9Y "$tree/usr" | tr -d . | sed 's/^0*//')
usr_ns=${usr_ns:-0}
for d in etc var; do
    printf 'TIMESTAMP_NSEC=%s\n' "$usr_ns" > "$tree/$d/.updated"
    touch -r "$tree/usr" "$tree/$d/.updated"
done

# docs/image.md, "The inventory".
python3 -I /usr/local/share/spin-image/deviations.py "$tree" > /work/deviations.txt
diff -u /usr/local/share/spin-image/deviations.txt /work/deviations.txt || {
    echo "ERROR: the image differs from its packages in ways image/deviations.txt does not list (above):" >&2
    echo "       update it, and docs/image.md says why for each" >&2
    exit 1; }
echo "==> $(wc -l < /work/deviations.txt) deviations from the packages, as image/deviations.txt lists"

# docs/image.md, "The filesystem"
mkbase() {
    rm -f /work/base.raw
    "$bin/mkfs.ext4" -q -F -L "" -U clear -J size=64 -m 0 -b 4096 -O ^orphan_file \
        -E root_owner=0:0,lazy_itable_init=0 -d "$tree" /work/base.raw "${1}k"
}
used_kb() {
    "$bin/dumpe2fs" -h /work/base.raw 2>/dev/null | awk -F: '
        /^Block count:/ { blocks = $2 }
        /^Free blocks:/ { free = $2 }
        /^Block size:/  { bs = $2 }
        END { printf "%d\n", (blocks - free) * bs / 1024 }'
}
margin_kb=131072
kb=$(du -sk "$tree" | cut -f1)
first_kb=$(( kb + kb / 10 + 262144 ))
mkbase "$first_kb"
used=$(used_kb)
size_kb=$(( (used + margin_kb + 1023) / 1024 * 1024 ))
echo "==> mkfs.ext4: ${size_kb} KiB (${used} KiB used at ${first_kb} KiB, plus ${margin_kb} KiB) from a ${kb} KiB tree"
mkbase "$size_kb"

# docs/image.md, "What build.sh checks, and why"
echo "==> checking the filesystem"
"$bin/e2fsck" -fn /work/base.raw
header=$("$bin/dumpe2fs" -h /work/base.raw 2>/dev/null)
made_kb=$(awk -F: '/^Block count:/ { n = $2 } /^Block size:/ { bs = $2 } END { printf "%d\n", n * bs / 1024 }' <<<"$header")
test "$made_kb" = "$size_kb" || { echo "ERROR: the filesystem is $made_kb KiB, and $size_kb KiB was asked for" >&2; exit 1; }
reserved=$(awk -F: '/^Reserved block count:/ { gsub(/ /, "", $2); print $2 }' <<<"$header")
test "$reserved" = 0 || { echo "ERROR: the filesystem reserves $reserved blocks for root, want 0" >&2; exit 1; }
grep -q '^Filesystem features:.* has_journal' <<<"$header" || { echo "ERROR: the filesystem has no journal" >&2; exit 1; }
grep -q '^Filesystem features:.* needs_recovery' <<<"$header" && { echo "ERROR: the filesystem's journal needs recovery" >&2; exit 1; }
grep -q '^Total journal size: *64M$' <<<"$header" || { echo "ERROR: the journal is not 64M: $(grep '^Total journal size' <<<"$header")" >&2; exit 1; }
grep -q '^Filesystem state: *clean$' <<<"$header" || { echo "ERROR: the filesystem is not clean" >&2; exit 1; }
grep -q '^Filesystem features:.* orphan_file' <<<"$header" && {
    echo "ERROR: the filesystem has orphan_file; every mount reads it block by block" >&2; exit 1; }
groups=$("$bin/dumpe2fs" /work/base.raw 2>/dev/null | grep '^Group [0-9]*:')
want=$(awk -F: '/^Block count:/ { b = $2 } /^Blocks per group:/ { g = $2 } END { printf "%d\n", (b + g - 1) / g }' <<<"$header")
zeroed=$(grep -c 'ITABLE_ZEROED' <<<"$groups" || true)
test "$want" -gt 0 && test "$zeroed" = "$want" || {
    echo "ERROR: $zeroed of $want groups have zeroed inode tables; each overlay would run ext4lazyinit" >&2; exit 1; }

# docs/image.md, "What build.sh checks, and why"
stat_in_image() { "$bin/debugfs" -R "stat $1" /work/base.raw 2>&1; }
in_image() { stat_in_image "$1" | grep -q '^Inode:'; }

for f in /sbin/init /bin/sh /bin/bash /usr/bin/docker /usr/local/bin/task /usr/bin/bwrap /usr/bin/socat /usr/bin/rg; do
    in_image "$f" || { echo "ERROR: the image has no $f" >&2; exit 1; }
done

in_image /usr/sbin/hwclock || {
    echo "ERROR: the image has no /usr/sbin/hwclock; check util-linux-extra" >&2
    exit 1; }

for d in /etc /tmp /proc /sys /dev /run; do
    in_image "$d" || { echo "ERROR: the image has no $d for the guest to mount over" >&2; exit 1; }
done

for f in /etc/hostname /etc/resolv.conf /var/lib/dbus/machine-id /var/lib/systemd/random-seed; do
    if in_image "$f"; then
        echo "ERROR: $f is baked into the base image; every VM would share it" >&2
        exit 1
    fi
done
if in_image /etc/ssh/ssh_host_ed25519_key; then
    echo "ERROR: an SSH host key is baked into the base image" >&2
    exit 1
fi

in_image /etc/machine-id || { echo "ERROR: /etc/machine-id is missing" >&2; exit 1; }
mid_size=$(stat_in_image /etc/machine-id | sed -n 's/.*Size: \([0-9]\+\).*/\1/p' | head -1)
[ "${mid_size:-unset}" = "0" ] || {
    echo "ERROR: /etc/machine-id is ${mid_size} bytes; every VM would share that identity" >&2
    exit 1; }

for u in systemd-udevd.service systemd-udevd-kernel.socket systemd-udevd-control.socket \
         systemd-udev-trigger.service; do
    if stat_in_image "/etc/systemd/system/$u" | grep -q '/dev/null'; then
        echo "ERROR: $u is masked - it is free and it buys a getty and hot-plug events" >&2
        exit 1
    fi
done

in_image /etc/tmpfiles.d/lru-gen.conf || {
    echo "ERROR: the image has no /etc/tmpfiles.d/lru-gen.conf; MGLRU runs without min_ttl_ms" >&2
    exit 1; }
"$bin/debugfs" -R "cat /etc/systemd/system/tmp.mount.d/10-size.conf" /work/base.raw 2>/dev/null |
    grep -q '^Options=.*size=25%%' || {
    echo "ERROR: /tmp is not capped at 25% of memory; see tmp.mount.d/10-size.conf" >&2
    exit 1; }

in_image /etc/udev/rules.d/40-spin-hotadd.rules || {
    echo "ERROR: the image has no 40-spin-hotadd.rules; a hot-plugged CPU stays offline" >&2
    exit 1; }

for f in /etc/systemd/system/serial-getty@.service.d/term.conf \
         /etc/systemd/system/getty@.service.d/term.conf; do
    in_image "$f" || { echo "ERROR: the image has no $f; a login would inherit TERM=dumb" >&2; exit 1; }
done

# docs/image.md, "The update stamps"
usr_mtime=$(stat_in_image /usr | sed -n 's/^ *mtime: \(0x[0-9a-f:]*\).*/\1/p')
test -n "$usr_mtime" || { echo "ERROR: debugfs gave no mtime for /usr" >&2; exit 1; }
for f in /etc/.updated /var/.updated; do
    m=$(stat_in_image "$f" | sed -n 's/^ *mtime: \(0x[0-9a-f:]*\).*/\1/p')
    test "$m" = "$usr_mtime" || {
        echo "ERROR: $f's mtime is '$m', not /usr's $usr_mtime; the units of ConditionNeedsUpdate would run in every boot" >&2
        exit 1; }
done

for u in systemd-random-seed.service; do
    stat_in_image "/etc/systemd/system/$u" | grep -q '/dev/null' || {
        echo "ERROR: $u is not masked - optimize-systemd.sh masked it and something put it back" >&2
        exit 1; }
done

for dir in /etc/systemd/system /etc/systemd/system-generators /etc/systemd/user-generators /etc/tmpfiles.d; do
    links=$("$bin/debugfs" -R "ls -l $dir" /work/base.raw 2>/dev/null | awk '$2 ~ /^120/ {print $NF}')
    for name in $links; do
        stat_in_image "$dir/$name" | grep -q 'Fast link dest: "/dev/null"' || continue
        case $dir in
            */system-generators) shadowed=/usr/lib/systemd/system-generators/$name ;;
            */user-generators) shadowed=/usr/lib/systemd/user-generators/$name ;;
            */tmpfiles.d) shadowed=/usr/lib/tmpfiles.d/$name ;;
            *) unit=$name
               case $unit in *@?*.*) unit="${unit%%@*}@.${unit##*.}" ;; esac
               shadowed=/usr/lib/systemd/system/$unit ;;
        esac
        in_image "$shadowed" || {
            echo "ERROR: $dir/$name masks nothing: the image has no $shadowed" >&2
            exit 1; }
    done
done

for u in tmp.mount systemd-tmpfiles-setup.service; do
    if stat_in_image "/etc/systemd/system/$u" | grep -q '/dev/null'; then
        echo "ERROR: $u is masked - /tmp would be on the disk, and in every copy of it" >&2
        exit 1
    fi
done

if stat_in_image /etc/systemd/system/multi-user.target.wants/systemd-logind.service |
        grep -q '/dev/null'; then
    echo "ERROR: the want for systemd-logind is overridden; the first login would start it" >&2
    exit 1
fi
in_image /usr/lib/systemd/system/multi-user.target.wants/systemd-logind.service || {
    echo "ERROR: the package no longer wants systemd-logind from multi-user.target" >&2
    exit 1; }
in_image /var/lib/systemd/linger/root || {
    echo "ERROR: root does not linger; spin's first login would wait for root's whole manager" >&2
    exit 1; }
! in_image /var/lib/systemd/linger/spin || {
    echo "ERROR: spin lingers; its manager would start in every boot" >&2
    exit 1; }
in_image /etc/systemd/system/systemd-logind-varlink.socket.d/10-seats.conf || {
    echo "ERROR: the image has no logind seats drop-in; an early login would lose its session" >&2
    exit 1; }
"$bin/debugfs" -R "cat /etc/systemd/system/sshd@.service.d/keygen.conf" /work/base.raw 2>/dev/null |
        grep -qx 'After=spin-machine-sshd-keygen.service' || {
    echo "ERROR: sshd@.service does not wait for spin-machine-sshd-keygen.service" >&2
    exit 1; }
"$bin/debugfs" -R "cat /etc/ssh/sshd_config.d/10-spin-kex.conf" /work/base.raw 2>/dev/null |
        grep -qx 'KexAlgorithms mlkem768x25519-sha256' || {
    echo "ERROR: sshd does not offer mlkem768x25519-sha256 alone" >&2
    exit 1; }
sshd_version=$(awk '$1 == "openssh-server" { v = $2; sub(/^[0-9]+:/, "", v); print v }' "$SHARE/packages.txt")
sshd_says="OpenSSH_${sshd_version%%-*} Ubuntu-${sshd_version#*-}"
"$bin/debugfs" -R "dump -p /usr/lib/openssh/sshd-auth /work/sshd-auth" /work/base.raw 2>/dev/null
grep -aqF "$sshd_says" /work/sshd-auth || {
    echo "ERROR: the image's /usr/lib/openssh/sshd-auth does not say $sshd_says" >&2
    exit 1; }
rm -f /work/sshd-auth
stat_in_image /etc/systemd/system/sysinit.target.wants/spin-machine-sshd-keygen.service |
        grep -qF '/usr/lib/systemd/system/spin-machine-sshd-keygen.service' || {
    echo "ERROR: spin-machine-sshd-keygen.service is not wanted by sysinit.target" >&2
    exit 1; }
"$bin/debugfs" -R "cat /usr/lib/systemd/system/spin-machine-sshd-keygen.service" /work/base.raw 2>/dev/null |
        grep -qx 'DefaultDependencies=no' || {
    echo "ERROR: spin-machine-sshd-keygen.service has default dependencies; it starts after sysinit.target" >&2
    exit 1; }
"$bin/debugfs" -R "cat /usr/lib/systemd/system/spin-machine-sshd-keygen.service" /work/base.raw 2>/dev/null |
        grep -qx 'ConditionPathExists=!/run/spin/ssh/ssh_host_ed25519_key' || {
    echo "ERROR: spin-machine-sshd-keygen.service runs when spin has handed over its host key" >&2
    exit 1; }
stat_in_image /etc/systemd/system/default.target |
        grep -qF '/usr/lib/systemd/system/spin-machine-login.target' || {
    echo "ERROR: the default target is not spin-machine-login.target; the whole boot runs before the first login" >&2
    exit 1; }
"$bin/debugfs" -R "cat /usr/lib/systemd/system/spin-machine-login.target" /work/base.raw 2>/dev/null |
        grep -qx 'Wants=spin-machine-boot-rest.service' || {
    echo "ERROR: spin-machine-login.target does not start the rest of the boot" >&2
    exit 1; }
"$bin/debugfs" -R "cat /usr/lib/systemd/system/spin-machine-boot-rest.service" /work/base.raw 2>/dev/null |
        grep -qx 'ExecStart=/usr/bin/systemctl --no-block start multi-user.target' || {
    echo "ERROR: spin-machine-boot-rest.service does not start multi-user.target" >&2
    exit 1; }
"$bin/debugfs" -R "dump -p /usr/libexec/spin-machine-pressure /work/pressure.bin" /work/base.raw 2>/dev/null
cmp -s /work/pressure.bin /out/guest/spin-machine-pressure && test -x /work/pressure.bin || {
    echo "ERROR: /usr/libexec/spin-machine-pressure is not in the image as built" >&2
    exit 1; }
rm -f /work/pressure.bin
stat_in_image /etc/systemd/system/timers.target.wants/spin-machine-pressure.timer |
        grep -qF '/usr/lib/systemd/system/spin-machine-pressure.timer' || {
    echo "ERROR: spin-machine-pressure.timer is not wanted by timers.target" >&2
    exit 1; }
"$bin/debugfs" -R "cat /usr/lib/systemd/system/spin-machine-pressure.timer" /work/base.raw 2>/dev/null |
        grep -qx 'OnBootSec=10s' || {
    echo "ERROR: spin-machine-pressure.timer does not start the sender 10 s after the boot, past the first login" >&2
    exit 1; }
"$bin/debugfs" -R "cat /usr/lib/systemd/system/spin-machine-pressure.service" /work/base.raw 2>/dev/null |
        grep -q '^WantedBy=' && {
    echo "ERROR: spin-machine-pressure.service is enabled itself, and would start beside the first login" >&2
    exit 1; }
for f in sshd-vsock.socket.d/10-lane.conf sshd@.service.d/10-lane.conf systemd-logind.service.d/10-lane.conf \
         systemd-logind-varlink.socket.d/20-lane.conf user-runtime-dir@.service.d/10-lane.conf \
         user@.service.d/10-lane.conf ssh.socket.d/10-lane.conf ssh.service.d/10-lane.conf \
         sshd-keygen.service.d/10-lane.conf; do
    "$bin/debugfs" -R "cat /etc/systemd/system/$f" /work/base.raw 2>/dev/null |
            grep -qx 'DefaultDependencies=no' || {
        echo "ERROR: ${f%%.d/*} is not in the first login's lane ($f)" >&2
        exit 1; }
done
"$bin/debugfs" -R "cat /etc/environment.d/10-user-sockets.conf" /work/base.raw 2>/dev/null |
        grep -qxF 'DBUS_SESSION_BUS_ADDRESS=unix:path=${XDG_RUNTIME_DIR}/bus' || {
    echo "ERROR: the user manager's environment has no bus address (environment.d/10-user-sockets.conf)" >&2
    exit 1; }
for f in dbus.socket.d/10-env.conf ssh-agent.socket.d/10-env.conf; do
    "$bin/debugfs" -R "cat /etc/systemd/user/$f" /work/base.raw 2>/dev/null | grep -qx 'ExecStartPost=' || {
        echo "ERROR: ${f%%.d/*} still runs systemctl --user while the first login waits ($f)" >&2
        exit 1; }
done
for f in ssh.socket.d/10-lane.conf ssh.service.d/10-lane.conf sshd-keygen.service.d/10-lane.conf; do
    "$bin/debugfs" -R "cat /etc/systemd/system/$f" /work/base.raw 2>/dev/null |
            grep -qx 'Conflicts=shutdown.target' || {
        echo "ERROR: ${f%%.d/*} has no default dependencies and is not stopped at shutdown ($f)" >&2
        exit 1; }
done
"$bin/debugfs" -R "cat /etc/systemd/system/ssh.service.d/20-no-config-test.conf" /work/base.raw 2>/dev/null |
        grep -qx 'ExecStartPre=' || {
    echo "ERROR: ssh.service still runs sshd -t before it starts (ssh.service.d/20-no-config-test.conf)" >&2
    exit 1; }
for f in systemd-logind.service.d/30-privatetmp.conf dbus-broker.service.d/30-privatetmp.conf; do
    "$bin/debugfs" -R "cat /etc/systemd/system/$f" /work/base.raw 2>/dev/null |
            grep -qx 'PrivateTmp=no' || {
        echo "ERROR: ${f%%.d/*} has a private /tmp and waits for systemd-tmpfiles-setup ($f)" >&2
        exit 1; }
done
for f in systemd-udevd.service.d/20-startup.conf systemd-udev-trigger.service.d/20-startup.conf; do
    "$bin/debugfs" -R "cat /etc/systemd/system/$f" /work/base.raw 2>/dev/null |
            grep -qx 'StartupCPUWeight=1' || {
        echo "ERROR: ${f%%.d/*} runs at the normal weight while the boot runs ($f)" >&2
        exit 1; }
done
for u in systemd-udevd.service systemd-udev-trigger.service; do
    unit=$("$bin/debugfs" -R "cat /etc/systemd/system/$u" /work/base.raw 2>/dev/null)
    printf '%s\n' "$unit" | grep -q '^ExecStart=' || {
        echo "ERROR: /etc/systemd/system/$u is missing or is not udev's unit" >&2
        exit 1; }
    if printf '%s\n' "$unit" | grep -qx 'Before=sysinit.target'; then
        echo "ERROR: $u is ordered before sysinit.target; basic.target waits for udev" >&2
        exit 1
    fi
done
dbus=$("$bin/debugfs" -R "cat /etc/systemd/system/dbus.socket" /work/base.raw 2>/dev/null)
printf '%s\n' "$dbus" | grep -q '^ListenStream=/run/dbus/system_bus_socket' || {
    echo "ERROR: /etc/systemd/system/dbus.socket is missing or is not the bus's socket" >&2
    exit 1; }
if printf '%s\n' "$dbus" | grep -qx 'After=sysinit.target'; then
    echo "ERROR: dbus.socket is ordered after sysinit.target; logind waits for udev and journald" >&2
    exit 1
fi
stat_in_image /etc/tmpfiles.d/systemd-nologin.conf | grep -q '/dev/null' || {
    echo "ERROR: tmpfiles writes /run/nologin at boot; pam_nologin refuses the lane's first login" >&2
    exit 1; }
"$bin/debugfs" -R "cat /etc/pam.d/common-session" /work/base.raw 2>/dev/null |
        grep -q '^session.*pam_systemd\.so.*class=user-early$' || {
    echo "ERROR: sessions are not user-early; the first login's systemctl --user can come before its manager" >&2
    exit 1; }
"$bin/debugfs" -R "cat /etc/systemd/system/systemd-udev-trigger.service.d/10-coldplug.conf" /work/base.raw 2>/dev/null |
        grep -q -- '--subsystem-match=input' || {
    echo "ERROR: udev's coldplug is not filtered, or lost input (the power button logind shuts down on)" >&2
    exit 1; }
"$bin/debugfs" -R "cat /etc/systemd/system/systemd-udev-trigger.service.d/10-coldplug.conf" /work/base.raw 2>/dev/null |
        grep -q -- '--subsystem-match=cpu' || {
    echo "ERROR: udev's coldplug lost cpu; a core attached before systemd starts stays offline" >&2
    exit 1; }
if stat_in_image /etc/systemd/system/systemd-logind.service | grep -q '/dev/null'; then
    echo "ERROR: systemd-logind.service is masked - it never starts, no login gets a" \
         "session, and spin's manager never starts either" >&2
    exit 1
fi

for u in apt-daily.timer apt-daily-upgrade.timer motd-news.timer dpkg-db-backup.timer; do
    stat_in_image "/etc/systemd/system/$u" | grep -q '/dev/null' || {
        echo "ERROR: $u is not masked - configure-system.sh masked it and something put it back" >&2
        exit 1; }
done

"$bin/debugfs" -R "cat /etc/systemd/system/fstrim.timer.d/spin.conf" /work/base.raw 2>/dev/null | grep -qx 'OnCalendar=hourly' || {
    echo "ERROR: fstrim.timer does not run hourly - configure-system.sh's drop-in is missing" >&2
    exit 1; }
in_image /etc/systemd/system/timers.target.wants/fstrim.timer || {
    echo "ERROR: fstrim.timer is not enabled" >&2
    exit 1; }

for f in /etc/pam.d/sshd /etc/pam.d/login; do
    stack=$("$bin/debugfs" -R "cat $f" /work/base.raw 2>/dev/null)
    printf '%s\n' "$stack" | grep -q 'pam_unix\|common-session' || {
        echo "ERROR: $f is missing or is not a PAM stack" >&2
        exit 1; }
    if printf '%s\n' "$stack" | grep -q '^[[:space:]]*session.*pam_motd\.so'; then
        echo "ERROR: pam_motd is enabled in $f, and runs /etc/update-motd.d at every login" >&2
        exit 1
    fi
done

# docs/image.md, "The filesystem"
echo "==> qemu-img: wrapping it"
rm -f "$OUT.new"
qemu-img convert -f raw -O qcow2 /work/base.raw "$OUT.new"
qemu-img info --output=json "$OUT.new"
chmod 0444 "$OUT.new"
mv -f "$OUT.new" "$OUT"
echo "==> $OUT"
echo "    sha256 $(sha256sum "$OUT" | cut -d' ' -f1)"
echo "    size   $(du -h "$OUT" | cut -f1)"
