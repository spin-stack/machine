#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo LOCKDOWN_CHECK_FAILED' EXIT

# The LSMs the kernel runs, in its own words, and the lockdown level it enforces: the brackets
# mark the one in force. LOCKDOWN is the machine's Lockdown (lockdown_test.go).
echo "lsm: $(cat /sys/kernel/security/lsm)"
echo "lockdown: $(cat /sys/kernel/security/lockdown)"
# capability is always there, whatever CONFIG_LSM says: the kernel puts it first among the rest.
test "$(cat /sys/kernel/security/lsm)" = "lockdown,capability,bpf"

# The level the kernel forces from its first instruction, below which no command line goes
# (security/lockdown/lockdown.c, lock_kernel_down only raises): integrity (#734).
forced=$(zcat /proc/config.gz | sed -n 's/^CONFIG_LOCK_DOWN_KERNEL_FORCE_\(.*\)=y$/\1/p' | tr A-Z a-z)
echo "forced by the kernel: $forced; asked of it: $(grep -o 'lockdown=[a-z]*' /proc/cmdline || echo nothing)"
if [ "$forced" != integrity ]; then
    echo "the kernel forces '$forced', not integrity"
    exit 1
fi
# What the machine asked for (machine.Lockdown): the level, and the .bpf and .dm-verity keyrings
# sealed, or left open for shirudo's init to enroll its certificates in.
case "$LOCKDOWN" in
confidentiality) asked=lockdown=confidentiality want=confidentiality keyrings=sealed ;;
shirudo) asked='bpf.keyring_unsealed=1 dm_verity.keyring_unsealed=1' want=$forced keyrings=open ;;
kernel) asked='' want=$forced keyrings=sealed ;;
*) echo "LOCKDOWN is confidentiality, shirudo or kernel, not '$LOCKDOWN'"; exit 1 ;;
esac
echo "cmdline: $(cat /proc/cmdline)"
said=$(grep -oE '(lockdown|bpf\.keyring_unsealed|dm_verity\.keyring_unsealed)=[a-z0-9]*' /proc/cmdline | tr '\n' ' ' || true)
if [ "${said% }" != "$asked" ]; then
    echo "a $LOCKDOWN machine asked for '${said% }', not '$asked'"
    exit 1
fi

# Whether each keyring takes a key, which keyring-probe asks by adding one.
probed=$(/keyring-probe)
echo "keyrings: $(echo "$probed" | tr '\n' ' ')"
if [ "$probed" != "$(printf '.bpf %s\n.dm-verity %s' "$keyrings" "$keyrings")" ]; then
    echo "a $LOCKDOWN machine's keyrings are not $keyrings"
    exit 1
fi
grep -q "\[$want\]" /sys/kernel/security/lockdown

# Root cannot read the kernel's memory, nor lower the level.
test ! -e /dev/mem
test ! -e /proc/kcore
if echo none > /sys/kernel/security/lockdown 2>/dev/null; then
    echo "root lowered lockdown"
    exit 1
fi
grep -q "\[$want\]" /sys/kernel/security/lockdown

# The kernel is at a random address (KASLR). Until 2026-11-19 the machine entered a vmlinux
# through PVH that could not move, and the kernel ran where it was linked, ffffffff81000000, with
# CONFIG_RANDOMIZE_BASE=y all the same. The vmlinux entered now moves itself (#645), and
# boot:kaslr asks where across boots; this asks one boot. So the address
# is asked of the kernel: _text, readable to root once kptr_restrict is 0, and not the linked
# one. A randomized kernel lands on that one 2 MiB slot about once in 480 boots (the slots of a
# 1 GiB kernel image space, less the first 16 MiB and the kernel's own size), so a failure here
# that does not repeat on a second run is that, and one that does is KASLR off.
if grep -qw nokaslr /proc/cmdline; then
    echo "the kernel was booted with nokaslr: $(cat /proc/cmdline)"
    exit 1
fi
echo 0 > /proc/sys/kernel/kptr_restrict
text=$(awk '$3 == "_text" { print $1 }' /proc/kallsyms)
echo "_text: ${text:-none}"
test -n "$text"
test "$text" != 0000000000000000
test "$text" != ffffffff81000000

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
