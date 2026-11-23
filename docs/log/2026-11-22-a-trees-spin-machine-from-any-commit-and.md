---
date: "2026-11-22"
order: 20
area: lab
title: 'a tree''s spin-machine from any commit, and #438 bisected (lab 44976271054, this change)'
issues: [416, 438]
prs: [446]
runs: [44976271054]
---
#438 read restore-first's evicted login +2.2 ms [+0.5, +4.0] with main's spin-machine against v20261121.01's, and asked which commit moved it. A tree could hold only this checkout's spin-machine or a release's, so `--spin-machine commit:SHA` now builds one from any commit, fetched alone from origin as a release's is. *Measured* (bench, nested, spin's shape and login, v20261122.01's parts, 30 restores each, eight spin-machines interleaved): against v20261121.01's, main's read evicted login −0.1 [−2.7, +4.6] and `running` +0.6 [−2.3, +3.5]. Of the five commits between, only 8d04bd4's `running` excluded 0, +4.5 [+0.1, +7.9], and main contains it. `listening` read −4.2 from eb70cb8 on, because `machine.Start` binds the monitor's socket before QEMU exists, so `listening` no longer marks QEMU's start. That instance was noisy: its cached restores were as slow as its evicted ones. *Not reproduced.* The difference was on the evicted row only, the disk's, and the read-ahead before the dial, asked for from four goroutines, leaves the evicted row nothing to wait on (#446, #447). *Status:* the lever in place. #438 and #416 answered in their comments.
