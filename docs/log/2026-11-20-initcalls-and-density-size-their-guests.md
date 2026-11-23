---
date: "2026-11-20"
order: 130
area: lab
title: initcalls and density size their guests (this change)
runs: [44735598126, 44800662338, 44805222647]
---
v20261120.03's release report failed on a diagnostic: `initcalls` lost 3 of 40 boots, and the report of 63049ff 4 of 40, each `never saw "SPIN-DMESG-END" in 1m30s` with an empty console tail (report runs 44800662338, 44805222647). ssh-first and the restores lost none in either, and initcalls lost none in the 2-vCPU, 2 GiB shape (44735598126). `plugged=2048` became every probe's default, but only ssh-first, restore-first, prestart and restore-together called `Plug`: initcalls and density booted spin's shape and left it at 128 MiB, while the record said "sized to 2048 MiB". Such a guest had 6 MiB of its 72 free, and had already stalled on memory, when the dump ran (laptop, 3 boots; sized, 1.9 GiB free and no stall), so the bench's loss reads as a guest at the edge of its memory. That is a lead: the laptop lost none of 20 at 128 MiB, and the tail that would say so was empty because `readUntil` returned nothing on a timeout, which it now does not. Both probes now plug as the others do, and a test holds that every probe taking `plugged` needs `memory --to`. initcalls' numbers are now of the kernel booting while virtio-mem plugs, which is what spin's kernel does, so its record moves with this: "Freeing" read 43.1/50.2 sized against 33.6/212 unsized (laptop, 20 each, two runs apart: a lead, not a Δ). *Decided by:* 0 lost in 40+ bench boots (the dispatch on the PR).
