---
date: "2026-11-26"
order: 92
area: lab
title: 'the ledger''s running is QEMU''s KVM_SET_CLOCK, traced on the host (labs 45888511048, 45888515410, #665)'
issues: [665]
runs: [45888511048, 45888515410]
---
*Why:* the ledger split the span before the kernel's clock at kvm-clock's 0, the kernel's 0 less its sched offset, and a marker kernel put that 0 2-5 ms before QEMU's KVM_SET_CLOCK (#665), so `qemu_start` read short and `guest_to_zero` long by as much. *Done:* the ledger probe traces the ioctl on the host, in a tracefs instance of its own on CLOCK_MONOTONIC with `sys_enter_ioctl` filtered to KVM_SET_CLOCK, and `running` is that stamp. kvm-clock's lead before it is kept as `kvmclock_lead`. *Measured,* v20261126.02 on its traceable kernel, spin's shape, 12 boots each: `kvmclock_lead` 2.78 ms (1.98 to 3.64) on bench (lab 45888511048) and 4.72 (4.44 to 4.98) on metal (lab 45888515410), where the marker kernel read 2.80 and 4.72, so the host's stamp and the kernel's port write agree. The split on bench is now 2.1, 28.3 and 17.8 ms, and on metal 6.4, 30.8 and 8.8. *Status:* in place. `boot:firmware-stages` still anchors the kernel's clock at KVM_SET_CLOCK plus the sched offset, late by the lead, since nothing in its run places the kernel's 0 on its own.
