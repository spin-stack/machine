---
date: "2026-11-23"
order: 260
area: machine
title: a state of another length than its checkpoint wrote is refused before QEMU reads it (#546, CI run 45111149822, this change)
issues: [546, 586]
runs: [45111149822]
---
*Why:* a failed load says only that it failed. QEMU 11.2.0-rc0 ends every failure at `process_incoming_migration_co`'s `fail:` label, and `qemu_fill_buffer` reports a file that ends early and a host's failed read both as `-EIO` (read 2026-11-22). So spin kept a truncated state's memory and retried it forever. *Changed:* `Checkpoint` records the state file's length in `Saved.Length`. `Resume` fstats the file it is handed after `CanResume` and returns a `*Refusal` (part `state length`) before QEMU is asked anything. It costs one `fstat`. It does not catch bytes changed in place, which stay a plain error. `boot:record`'s "a truncated state" now wants that refusal and the state never read. *Compatibility:* the field is new and optional. A `Saved` without it, every checkpoint a released spin-machine wrote, resumes without the check, and the machine's record (`Saved.From`) does not move. In CI run 45111149822, `boot:record` refused a real state cut to half its length, `state length: checkpoint 8597660207, here 4298830103 (same)`, with the state never read. `boot:compat` resumed v20261123.04 and refused v20261123.03 first, by `cpu x-legacy-cpuid2-cache` as before (#586), so this change refuses no release main did not. *Status:* in place.
