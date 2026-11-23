#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo KASLR_CHECK_FAILED' EXIT

if grep -qw nokaslr /proc/cmdline; then
    echo "the kernel was booted with nokaslr: $(cat /proc/cmdline)"
    exit 1
fi
# 1 and not 0: at 0, %pK prints a hash that differs every boot whatever the layout, and only
# kallsyms printed a real address. At 1 root, holding CAP_SYSLOG, reads every address as it is.
echo 1 > /proc/sys/kernel/kptr_restrict
text=$(awk '$3 == "_text" { print $1 }' /proc/kallsyms)
phys=$(awk -F'[- :]+' '/Kernel code/ { print $2; exit }' /proc/iomem)
# The lowest vmalloc address, near vmalloc_base, and the lowest unix socket, an object in the
# direct map near page_offset_base: the two regions RANDOMIZE_MEMORY moves that root can see.
vmalloc=$(awk 'NR == 1 { split($1, a, "-"); sub("^0x", "", a[1]); print a[1] }' /proc/vmallocinfo)
directmap=$(awk -F: 'NR > 1 { print $1 }' /proc/net/unix | sort | head -1)
test -n "$text" && test -n "$phys" && test -n "$vmalloc" && test -n "$directmap"
test "$phys" != 00000000

trap - EXIT
echo "KASLR text=$text phys=$phys vmalloc=$vmalloc directmap=$directmap"
echo KASLR_CHECK_OK
