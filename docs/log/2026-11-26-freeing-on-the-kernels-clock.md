---
date: "2026-11-26"
order: 90
area: lab
title: '"Freeing" is read on the kernel''s clock, not printk''s (labs 45885883883, 45885444150, #666)'
issues: [666]
runs: [45885439777, 45885883883, 45885444150, 45885695042]
---
From late_initcall on, printk stamps with the sched clock plus `__sched_clock_offset`, the offset that keeps it level with the CLOCK_MONOTONIC it followed before, which lost the ticks jiffies missed. So `initcalls`' `freeing`, the release record's `kernel-ms` from it, and `boot:firmware-stages`' "kernel to Freeing" read the kernel short by 10-15 ms, and "Freeing to SPIN-READY" long by as much. *Done:* `lab.PrintkLag` reads the offset from the "sched_clock: Marking stable" line, both instruments add it to "Freeing", and the probe init forwards the line. Without it they refuse, as the ledger does. *Measured* on the bench, spin's shape, 20 boots a tree: `freeing` on the kernel's clock is 95.1 for v20261120.04, whose record read 79.9, and 82.5 for v20261126.02 (lab 45885883883), whose record read 69.4. A second run read v20261126.02 at 80.6 (lab 45885439777). `boot:firmware-stages` on v20261126.02 read "kernel to Freeing" 62.5 and "Freeing to SPIN-READY" 23.5 with the fix (lab 45885444150), against 49.8 and 32.9 without it (lab 45885695042). *Status:* in place. A record measured with it reads `kernel-ms` ~12 ms above the records before it. Its Δ is unaffected, since both releases of a run are read by the same code. docs/boot-time.md's budget now has ~157 ms before PID 1.
