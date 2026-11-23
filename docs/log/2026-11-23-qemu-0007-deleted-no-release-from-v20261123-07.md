---
date: "2026-11-23"
order: 420
area: qemu
title: 'QEMU 0007 deleted: no release from v20261123.07 boots a bzImage (#660, parts run 45256311661, labs 45258887660, 45258106255, this change)'
issues: [645, 646, 660]
runs: [45256311661, 45258106255, 45258887660]
---
0007 mapped a bzImage given to `-kernel` instead of reading it into QEMU's heap (#646, the 11-23 entry above). Since #645 the machine enters a release's vmlinux through PVH (`machine.OpenRelease` picks it by its two Xen notes), and QEMU's ELF loader maps that file without 0007. A release from .06 or before boots its bzImage with the QEMU it shipped, which carries 0007; only a lab tree of a new QEMU with an old release's kernel pays the read again. *Measured* (bench, nested, spin's shape and login, v20261123.07 against the same release with QEMU built without 0007, 40 a side, interleaved): first login +3.6 [−3.3, +7.8], sshd accepting −1.6 [−6.7, +5.6], QEMU's RSS +0.3 [−1.6, +2.1] MiB, its file RSS +0.2 [+0.2, +0.3], a different binary's pages (inferred) (lab 45258887660); restored login −0.4 [−1.5, +0.9] evicted, −0.1 [−0.9, +0.9] cached, the state unmoved (lab 45258106255). Neither number moves, as #660 expected: 0007 touched nothing on the PVH path. The QEMU build's check of the mapping goes with it. *Status:* deleted. The bzImage and `CONFIG_KERNEL_LZ4` stay until spin's `hack/machine/place.go` stops requiring `kernel/bzImage` (spin-stack/spin#1689).
