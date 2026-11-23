---
date: "2026-11-21"
order: 380
area: machine
title: a QEMU without `x-defer-disk-flush` checkpoints as before (compat 44964906186, this change)
prs: [435]
runs: [44964906186]
---
Compat on main 607797b failed for every release from v20261119.02 to v20261121.01 with `migrate-set-capabilities: GenericError: Parameter 'capability' does not accept value 'x-defer-disk-flush'`. #435 set the capability on every checkpoint, and only the QEMU `qemu/patches` builds has it. Compat checkpoints each release's QEMU with this checkout's spin-machine. spin does the same when a runner's machine library is upgraded while machines from an earlier release run, and none of those could have been checkpointed. *Fixed:* `Checkpoint` asks `query-migrate-capabilities` first and sets `x-defer-disk-flush` only when the QEMU lists it. That costs one round trip before the migration starts, with the guest running, and is on neither goal number's clock. A QEMU without it checkpoints as before #435, with the stop's flush on the host's disk. `qmpdef.Enum` still names the capability, so `verify qemu` fails a pinned QEMU that lost the patch. *Reproduced* (laptop, a tree of v20261121.01 with this checkout's spin-machine, boot:record's tests): main's `Checkpoint` failed `TestACheckpointedMachineIsHeldThenContinued` with compat's error, and this change passed all four of boot:record's tests. *Status:* in place.
