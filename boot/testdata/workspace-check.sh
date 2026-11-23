#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# What a workspace expects of the image, checked in a booted guest (docs/image.md, "What a
# workspace keeps" and "What a workspace gives up"). `must` lines fail the check; `lost` lines are
# what docs/image.md says the image gives up, asserted so that the document stays true in either
# direction.
set -u
failed=0
must() { name=$1; shift; if out=$("$@" 2>&1); then echo "WS ok   $name"; else echo "WS FAIL $name: $out"; failed=1; fi; }
lost() { name=$1; shift; if out=$("$@" 2>&1); then echo "WS FAIL $name is no longer lost, docs/image.md says it is: $out"; failed=1; else echo "WS lost $name"; fi; }

state=$(systemctl is-system-running --wait 2>&1)
echo "WS state $state"
must "no unit failed" sh -c 'test -z "$(systemctl list-units --failed --plain --no-legend)" || { systemctl list-units --failed --plain --no-legend; exit 1; }'

must "the journal takes a line" sh -c 'logger -t ws-check hello && sleep 0.5 && journalctl -t ws-check -o cat | grep -qx hello'
lost "a journal kept across a reboot" test -d /var/log/journal
echo "WS journal $(journalctl --header 2>/dev/null | grep -m1 'File path' | tr -s ' ')"

must "fstrim.timer is waiting" systemctl is-active fstrim.timer
must "systemd-tmpfiles-clean.timer is waiting" systemctl is-active systemd-tmpfiles-clean.timer
lost "apt-daily.timer" systemctl is-enabled apt-daily.timer

must "dpkg's database is whole" sh -c 'test -z "$(dpkg --audit)"'
must "apt's dependencies hold" apt-get check
lost "apt's package lists before an apt update" sh -c 'ls /var/lib/apt/lists/*_Packages'

must "en_US.UTF-8 is a locale" sh -c 'locale -a | grep -qx en_US.utf8'
must "the default locale sets without a warning" sh -c 'test -z "$(LANG=en_US.UTF-8 locale 2>&1 >/dev/null)"'
lost "time zones (tzdata)" test -e /usr/share/zoneinfo/Europe/Madrid
lost "timedatectl" timedatectl status

must "localhost resolves" getent hosts localhost
must "spin has sudo" su -l spin -c 'sudo -n true'
must "/tmp is a tmpfs" sh -c 'test "$(findmnt -no FSTYPE /tmp)" = tmpfs'
echo "WS lockdown $(cat /sys/kernel/security/lockdown 2>&1)"
lost "tracefs, which lockdown=confidentiality keeps empty" sh -c 'mount -t tracefs nodev /sys/kernel/tracing 2>/dev/null; test -e /sys/kernel/tracing/trace'
lost "debugfs mounted at boot" mountpoint -q /sys/kernel/debug
must "debugfs mounts by hand" sh -c 'mount -t debugfs nodev /sys/kernel/debug && umount /sys/kernel/debug'
lost "/dev/hugepages mounted at boot" mountpoint -q /dev/hugepages

# docker by its socket: the first command starts dockerd and containerd. No network, so the image
# is imported: Task is a static binary, which runs in an image holding nothing else.
must "docker answers on its socket" docker info --format '{{.ServerVersion}}'
must "docker runs a container" sh -c 'd=$(mktemp -d) && cp /usr/local/bin/task "$d/" && tar -C "$d" -c . | docker import - ws-check >/dev/null && docker run --rm --network none ws-check /task --version'
must "docker's socket is Docker's, 0660 and group docker" sh -c 'm=$(stat -c "%a %U:%G" /run/docker.sock) && test "$m" = "660 root:docker" || { echo "$m"; exit 1; }'
must "spin runs a container, through the docker group" su -l spin -c 'docker run --rm --network none ws-check /task --version'
must "a user outside the docker group is refused" sh -c '! HOME=/tmp setpriv --reuid=65534 --regid=65534 --clear-groups docker info >/dev/null 2>&1'

for tool in python3 lsb_release mkfs.ext4 man xxd; do
    if command -v "$tool" >/dev/null; then echo "WS has $tool"; else echo "WS absent $tool"; fi
done

if [ "$failed" = 0 ]; then echo WORKSPACE_CHECK_OK; else echo WORKSPACE_CHECK_FAILED; fi
