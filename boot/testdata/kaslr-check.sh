#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo KASLR_CHECK_FAILED' EXIT

if grep -qw nokaslr /proc/cmdline; then
    echo "the kernel was booted with nokaslr: $(cat /proc/cmdline)"
    exit 1
fi
echo 0 > /proc/sys/kernel/kptr_restrict
text=$(awk '$3 == "_text" { print $1 }' /proc/kallsyms)
phys=$(awk -F'[- :]+' '/Kernel code/ { print $2; exit }' /proc/iomem)
vmalloc=$(awk '{ split($1, a, "-"); print a[1]; exit }' /proc/vmallocinfo)
test -n "$text" && test -n "$phys" && test -n "$vmalloc"
test "$phys" != 00000000

trap - EXIT
echo "KASLR text=$text phys=$phys vmalloc=$vmalloc"
echo KASLR_CHECK_OK
