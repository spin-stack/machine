---
date: "2026-11-19"
order: 20
area: boot
title: '`boot:restore-first` (this change)'
prs: [246]
runs: [44533227886, 44535167653]
---
The goal's second number had had no probe in the tree since #246 closed. Every row of `boot:ssh-first` is booted cold, logged into and saved, then restored with its state evicted from the page cache (checked with mincore) and logged into again. Every cold boot runs before every restore. On the bench fleet a restore reaches the first login at 841/913 ms, ~790 of it before the guest runs, reading the state from the runner's gp3 volume (44533227886). #246 read 386 ms on 11-13. What moved is not known. On a c8i.metal-48xl it is 905/942 (44535167653), so the restore is the host's disk, not the virtualization.
