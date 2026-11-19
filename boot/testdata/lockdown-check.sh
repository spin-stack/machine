#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo LOCKDOWN_CHECK_FAILED' EXIT

# The LSMs the kernel runs, in its own words, and the lockdown level it enforces: the brackets
# mark the one in force.
echo "lsm: $(cat /sys/kernel/security/lsm)"
echo "lockdown: $(cat /sys/kernel/security/lockdown)"
# capability is always there, whatever CONFIG_LSM says: the kernel puts it first among the rest.
test "$(cat /sys/kernel/security/lsm)" = "lockdown,capability,bpf"
grep -q '\[confidentiality\]' /sys/kernel/security/lockdown

# Root cannot read the kernel's memory, nor lower the level.
test ! -e /dev/mem
test ! -e /proc/kcore
if echo none > /sys/kernel/security/lockdown 2>/dev/null; then
    echo "root lowered lockdown"
    exit 1
fi
grep -q '\[confidentiality\]' /sys/kernel/security/lockdown

# The boot asks for nothing lockdown refuses (#261). The archive's systemd loads its restrict-fs
# BPF program at every start and lockdown refuses it the bpf_probe_read_kernel it uses; the
# systemd image/systemd builds has no BPF framework. Which one PID 1 is, the installed systemd
# says: PID 1 runs that file. The journal has to hold this boot's kernel start before an absent
# line means anything - PID 1 logs at err (optimize-systemd.sh), so its own banner is not there.
# compat.yml boots the release before's image, which until a release carries image/systemd's is
# the archive's.
version=$(systemctl --version | head -1)
echo "systemd: $version"
journalctl -b -k --no-pager -o cat | grep -q '^Linux version'
case "$version" in
*+spin[0-9]*)
    if journalctl -b --no-pager -o cat | grep -q 'bpf-restrict-fs'; then
        echo "systemd tried restrict-fs: $(journalctl -b --no-pager -o cat | grep -m1 'bpf-restrict-fs')"
        exit 1
    fi
    if dmesg | grep -q 'use of bpf to read kernel RAM'; then
        echo "lockdown refused a BPF read: $(dmesg | grep -m1 'use of bpf to read kernel RAM')"
        exit 1
    fi
    ;;
*) echo "the archive's systemd: its restrict-fs line is expected (#261)" ;;
esac

trap - EXIT
echo LOCKDOWN_CHECK_OK
