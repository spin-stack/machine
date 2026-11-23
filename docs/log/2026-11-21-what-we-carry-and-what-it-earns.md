---
date: "2026-11-21"
order: 80
area: lab
title: what we carry, and what it earns (this change, lab 44818953987)
prs: [345, 352, 354]
runs: [44818953987]
---
The reverse of a kept change: every boot-time modification the tree carries, with the shape and host it was kept on, in docs/boot-time.md's section of that name, grouped for removal one arm a group. Found before the bench: 15 masks and 1 disabled generator name units the image does not have (deleted, #352). The 5 masks of `ConditionNeedsUpdate` units are load-bearing, because the image has no `/etc/.updated`, and unmasked they rebuild hwdb.bin and the linker cache at every boot (first login +260 ms, QEMU's RSS +90 MiB, laptop). *Measured* (prestart, spin's shape, 20 each against v20261120.04, the removal's Δp50 of the cold first login): every kernel patch +67.5 [+62.5, +72.4]; SeaBIOS for qboot +68.5; the 16 other masks +57.5; the generators +16.7; the coldplug filter +14.9, and without it a restore +464 (a guest saved with its coldplug still running, a guess); QEMU 0001 with qboot's PAM patch +11.5 [+6.7, +20.2]; `driver_async_probe` with `rcu_expedited` −1.4 [−4.9, +3.1]; kernel 0006, 0009, 0011 and the in-doubt options +1.2 [−4.5, +8.3]; PID 1's quiet logging +2.4 [−2.3, +7.8]. In one vCPU what runs before the login is on its path, so most of what was kept on two still earns. *Status:* the command-line pair deleted (#352); A1 and the logging measured again at 40 boots, cold; the rest kept, with what it earns in the table. A1 deleted 11-20 (#345), the logging 11-20 (#354).
