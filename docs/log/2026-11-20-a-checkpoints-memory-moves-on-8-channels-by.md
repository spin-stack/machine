---
date: "2026-11-20"
order: 50
area: machine/lab
title: a checkpoint's memory moves on 8 channels by default (this change, lab 44792429636)
runs: [44789688221, 44792429636]
---
One restore at a time, 16 channels were best in spin's shape (92.7 against 103 at 8, lab 44789688221), but each channel is a thread of QEMU's and restores share a host. `lab` probe `restore-together` lets 1, 4 and 8 restores go at once, each from its own checkpoint into its own QEMU started ahead, evicted from the page cache. On the bench (spin's shape and login, 30 bursts, first logins pooled; the pooled interval is narrower than the truth, since restores in one burst are not independent): alone 152 / 100 / 90.2 ms at 1 / 8 / 16 channels; 4 at once 184 / 140 / 143; 8 at once 400 / 329 / 337. Against 8, 16 gains −10.1 [−12.2, −7.6] alone and loses +3.2 [+1.1, +5.8] at 4 at once (+10.9 [−4.7, +30.4] at 8). The rule set before the run picked 8: it gains on 1 at every count. `machine.DefaultChannels` is 8. A Save or Load that names a count, and `spin-machine save/restore --channels`, still choose any. A fast load or a stream state stays at one thread. The release record's restore numbers measure each release at its own default, so this release's Δ against the one before it is the change. Eight restores at once take ~330-400 ms whatever the count: what a host does with a burst is its own question. *Status:* kept.
