#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo KASLR_CHECK_FAILED' EXIT

if grep -qw nokaslr /proc/cmdline; then
    echo "the kernel was booted with nokaslr: $(cat /proc/cmdline)"
    exit 1
fi
# 1 and not 0: at 0 kallsyms prints zeros. At 1 root, holding CAP_SYSLOG, reads every symbol's
# address as it is.
echo 1 > /proc/sys/kernel/kptr_restrict
sym() { awk -v s="$1" '$3 == s { print $1 }' /proc/kallsyms; }
text=$(sym _text)
phys=$(awk -F'[- :]+' '/Kernel code/ { print $2; exit }' /proc/iomem)
# Where the three bases RANDOMIZE_MEMORY sets are kept: the host reads what they hold, as root
# here cannot under lockdown, and no file a 7.4 kernel prints says it.
bases="$(sym page_offset_base),$(sym vmalloc_base),$(sym vmemmap_base)"
test -n "$text" && test -n "$phys"
test "$phys" != 00000000
case "$bases" in *,,* | ,* | *,) echo "a region base is not in kallsyms: $bases"; exit 1 ;; esac

trap - EXIT
echo "KASLR text=$text phys=$phys bases=$bases"
echo KASLR_CHECK_OK
