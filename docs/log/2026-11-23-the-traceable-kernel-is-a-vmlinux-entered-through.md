---
date: "2026-11-23"
order: 430
area: lab
title: the traceable kernel is a vmlinux entered through PVH, as the release's (#697, lab 45260129735, this change)
issues: [645, 697, 698]
runs: [45260129735]
---
*Why:* since #645 the release boots its vmlinux through PVH, and its traceable kernel was still a bzImage, so v20261123.07's published ledger read firmware, entry and setup at 47.1 ms against .06's 45.0, where the release had dropped its decompressor. *Changed:* `release traceable-kernel` publishes a self-randomizing vmlinux, checked as the release's, and no bzImage; `traceable:TAG` places whichever the release published; the ledger records `pvh` and names the window by it. *Proof* (bench, spin's shape, v20261123.07, 10 boots a tree, interleaved, the old traceable bzImage against the new traceable vmlinux): firmware, entry and setup 44.3 → 19.4, −24.9 [−25.8, −23.4]; QEMU's start and held start 41.9 → 50.3, +8.4, the vmlinux's load (#698); the kernel's 0 −16.8 [−18.2, −15.1]; first login −21.6 [−32.9, −11.0]. *Status:* in place.
