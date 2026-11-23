---
date: "2026-11-23"
order: 150
area: lab
title: 'the ledger splits the span before the kernel''s clock (lab 45110630795, #537, #611, this change)'
issues: [611]
runs: [45110630795]
---
The ledger had one span from the moment before QEMU is exec'd to the kernel's 0, ~20 ms longer than `boot:firmware-stages`' 73.7 ms, and docs/measuring.md read the 20 ms as spin-machine's start and the held start, inferred. Now the host watches spin-machine's children for QEMU's exec (a bracket of ±0.11 to 0.14 ms), and the kernel's "kvm-clock: using sched offset" line puts the guest's first instruction at the kernel's 0 less it. *Six boots of v20261123.04 on its traceable kernel* (bench, spin's shape): spin-machine to QEMU's exec 2.1 ms, QEMU's exec to the guest's first instruction 49.4, the guest's first instruction to the kernel's clock 47.4 (63.2 on the run's first boot: its ~15 ms more are all in that part). *The premise that broke:* the 20 ms are not before QEMU. They are after its exec, on a machine that firmware-stages does not boot (#611). *Status:* in place.
