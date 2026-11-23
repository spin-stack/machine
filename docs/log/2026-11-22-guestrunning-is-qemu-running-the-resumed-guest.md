---
date: "2026-11-22"
order: 130
area: machine
title: 'GuestRunning is QEMU running the resumed guest (labs 44987275584, 44987275543, #465, this change)'
issues: [459]
runs: [44987275543, 44987275584]
---
A fast load says `postcopy-active` in `qemu_loadvm_run_fast_snapshot_load` right after it schedules the run BH, and the BH starts the guest later, on QEMU's main loop (QEMU 11.2.0-rc0). `Resume` returned and told `GuestRunning` at `postcopy-active`, so #459's guest, which never ran, was told running at 6.80 ms. `Resume` now arms a wait on `RESUME` before `migrate-incoming` and returns, telling `GuestRunning`, once it arrives, in a full load too. A full load that leaves the machine neither running nor paused is now an error, where `Resume` returned nil. *Checked* (`boot:record` on the bench, lab 44987275584): the events test now asks QEMU's run state from inside the `GuestRunning` callback and finds it running, in full and fast. The fast load told `GuestRunning` at 8.77 ms and `StateLoaded` at 672 ms. *Measured* (bench, nested, spin's shape and login, v20261122.01, `fast=true`, 30 a side interleaved, against main's spin-machine, lab 44987275543): fast `running` −0.4 [−1.5, +1.3] and fast login −0.6 [−13.8, +22.0], both within noise. The cached row's login read −0.5 [−4.4, +4.3]. The evicted row's login read −2.7 [−5.1, −0.8] with its `running` −0.0 [−3.1, +2.7]. A full load's path changed only by a wait that has already matched, so that row is taken as noise until another run repeats it. *Status:* in place. `Resume` now returns when QEMU runs the guest, not when the load is merely handed over.
