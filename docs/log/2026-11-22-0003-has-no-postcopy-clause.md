---
date: "2026-11-22"
order: 260
area: qemu
title: 0003 has no postcopy clause (#501, this change)
issues: [501]
prs: [499]
runs: [45030863541, 45030863623, 45031602785]
---
`migrate_defer_disk_flush()` was false with `postcopy-ram`, and no test could ask it: a save to a file cannot run with `postcopy-ram`, because the source opens a return path at connect and waits for the destination's SHUT at completion (11.2.0-rc0), so holding it needed a second QEMU as a destination. No caller can reach it either. The machine turns on `x-defer-disk-flush` only in `Checkpoint`, which also turns `postcopy-ram` off and migrates to `file:`. `postcopy-ram` is turned on only by `Resume`, on a destination, for a fast load, and a destination's stop is not a switchover's. spin moves a workspace by checkpoint, upload and resume (`moveFor`, spin#1487), with no live migration. The clause also guarded nothing that precopy's stop doesn't share. A deferred flush is safe when the migration's reader shares this host's page cache, which is what the capability says, and that holds or fails the same way whichever phase stops the VM. So the clause is gone, and postcopy's stop defers as precopy's does. CPR's exclusion stays, because cpr-reboot's stop can precede a host reboot that loses the page cache, and #499's subtest holds it. The patch adds 70 lines where it added 71, and every branch it carries is asked by `verify args`. *Checked:* the series applies with `patch --fuzz=0` to the pinned tarball, and `migration/options.c` and `qapi/migration.json` match the edited files, which `scripts/qapi-gen.py` reads without error. On the rebuilt QEMU (parts run 45030863541), `verify args`' 0003 subtest passes, and so does compat (run 45030863623). `boot:checkpoint-pause` passes on the bench with the busy-disk guest's p50 stopped at 19.7 ms, 29.5 when short of memory (lab 45031602785). What a checkpoint does is unchanged, since it never sets `postcopy-ram`. *Status:* in place.
