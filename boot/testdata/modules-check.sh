#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo MODULES_CHECK_FAILED' EXIT

echo "lockdown: $(cat /sys/kernel/security/lockdown)"
echo "modules: $(uname -r), $(find /lib/modules/"$(uname -r)" -name '*.ko' | wc -l) in the image"
test -s /lib/modules/"$(uname -r)"/modules.dep

# Refused first, before modprobe loads squashfs: a module loaded already would answer "File exists"
# whatever its signature. insmod's words are the kernel's errno, EKEYREJECTED. An unsigned module
# the kernel names on its log (kernel/module/signing.c). The foreign one carries the issuer and
# serial of spin-stack's certificate, as a forgery would, so the kernel finds spin-stack's key and
# the signature fails against it, which it refuses without a line of its own.
refused() {
    if out=$(insmod "$1" 2>&1); then
        echo "the kernel loaded $1"
        exit 1
    fi
    echo "$1: $out"
    case "$out" in *"Key was rejected by service"*) ;; *) exit 1 ;; esac
}
refused /unsigned.ko
dmesg | grep -q "Loading of unsigned module is rejected" || { echo "the kernel did not say: Loading of unsigned module is rejected"; exit 1; }
refused /foreign.ko

# Each feature, asked for as a workspace asks for it, loads its signed module. /dev/fuse is there
# before fuse is, from depmod's modules.devname (docs/image.md, "Kernel modules"), and opening it
# loads fuse.
test -c /dev/fuse || {
    echo "no /dev/fuse; kmod-static-nodes: $(systemctl show -p ActiveState -p Result kmod-static-nodes.service | tr '\n' ' ')"
    cat /run/tmpfiles.d/static-nodes.conf || true
    exit 1; }
grep -qw fuse /proc/filesystems && { echo "fuse was loaded before anything asked for it"; exit 1; }
: <> /dev/fuse
grep -qw fuse /proc/filesystems
ip link add spin0 type wireguard
ip link del spin0
modprobe squashfs
modprobe erofs
grep -qw squashfs /proc/filesystems
grep -qw erofs /proc/filesystems
echo "loaded: $(cut -d' ' -f1 /proc/modules | sort | tr '\n' ' ')"
dmesg | grep -q 'module verification failed' && { echo "a module loaded unverified: $(dmesg | grep -m1 'module verification failed')"; exit 1; }

trap - EXIT
echo MODULES_CHECK_OK
