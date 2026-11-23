---
date: "2026-11-22"
order: 200
area: qemu
title: 0003 says the same in fewer lines (#488, this change)
issues: [488]
runs: [45017731970, 45017732016, 45018667678]
---
`x-defer-disk-flush`'s patch was 369 lines, most of them a `defer_flush` argument carried through `migration_stop_vm`, a second `vm_stop_force_state` and the stop's helpers. Now `do_vm_stop` and `vm_stop_force_state`'s stopped branch start the deferral themselves, after the drain, when the run state is `finish-migrate` and `migrate_defer_disk_flush()` (public in `include/migration/misc.h`) holds: 249 lines, 71 added and 3 removed in QEMU against 107 and 22 before. Postcopy's and CPR's stops are `finish-migrate` too, so the accessor is false with `postcopy-ram` or a CPR mode; the machine sets neither on a save, so what it does is unchanged. *Checked* on the rebuilt QEMU (parts run 45017732016): `verify args`' 0003 subtest, one flush without the capability and none with it; compat (run 45017731970); `boot:checkpoint-pause` on the bench, busy-disk guest p50 stopped 20.6 ms, 22.1 short of memory (lab 45018667678). *Status:* in place.
