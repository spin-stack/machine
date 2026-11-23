---
date: "2026-11-22"
order: 110
area: machine, boot
title: 'a resume takes its disks before the load (labs 44984233276, 44984233299, 44984162863, #459, this change)'
issues: [459]
runs: [44984162863, 44984233276, 44984233299]
---
A fast load in compat completed with no RESUME, and its guest never ran. QEMU's stderr said `Failed to get "write" lock` on the overlay. QEMU 11.2.0-rc0 adds a held machine's nodes inactive. Both run BHs, the full load's and the fast one's, activate them with `migration_block_activate(NULL)`. When that fails they skip `vm_start` and tell the monitor nothing, and the run state stays `inmigrate`. `Resume` returned nil for that, in a full load too. The lock came from the boot tests: `rootDisk` never closed its files, and QEMU's lock lives on the description the test's `*os.File` shares. So the lock outlived the killed "in full" machine until a garbage collection closed the file. `Resume` now sends `blockdev-set-active` before `migrate-incoming`, so QEMU's refusal is its error, before a byte of the state is read. `rootDisk` closes its files when the test ends. *Measured.* With `GOGC=off` on a laptop (KVM, userfaultfd on), main missed 10 of 10 fresh-process runs of the events test and the fix passed 50 of 50. On the bench (nested, v20261122.01, 100 fresh-process runs each), main missed 3 with #459's events and stderr, and the fix missed 0. restore-first, fix against main (bench, spin's shape and login, 30 a side interleaved, `fast=true`, lab 44984162863): evicted login −0.3 [−2.4, +4.8], `running` +0.2 [−1.6, +4.9], cached login −0.6 [−4.8, +1.7], all within noise. *Status:* in place. `boot:record`'s "a disk another machine holds" holds it on real KVM.
