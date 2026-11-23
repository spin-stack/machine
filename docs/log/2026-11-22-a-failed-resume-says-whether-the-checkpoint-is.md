---
date: "2026-11-22"
order: 350
area: machine
title: a failed resume says whether the checkpoint is to blame, and a failed load does not (#516, this change)
issues: [516]
---
spin decides from `Resume`'s error whether to drop a suspended workspace's memory, and only `*Refusal` was typed. *Read in QEMU 11.2.0-rc0's source:* `MIGRATION` carries the status alone, and every failure of a load ends at `process_incoming_migration_co`'s `fail:` label, its reason on stderr before QEMU exits: a section QEMU rejects, a file that ends early and a failed read on the host (both `-EIO` from `qemu_fill_buffer`), and userfaultfd refused to a fast load. So no new type was made for a failed load, and the cases `Resume` can pin on the checkpoint fold into `*Refusal`: a format QEMU does not read (part `format`) and a `Saved.Added` that does not parse (part `added`). A stream checkpoint asked to load on several channels is the caller's options, not the checkpoint, so it stays a plain error. `boot:record`'s truncated state asserts that it is not a `Refusal`. *Opened:* #546. *Status:* in place.
