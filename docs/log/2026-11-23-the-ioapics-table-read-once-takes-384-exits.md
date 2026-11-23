---
date: "2026-11-23"
order: 170
area: kernel
title: 'the IOAPIC''s table read once takes 384 exits and no time the login can show (#590, parts run 45111505269, labs 45113437048, 45114405201, 45115330128, #627, this change)'
issues: [590, 592]
runs: [45111505269, 45113437048, 45114405201, 45115330128]
---
*Why:* #565's profile put 576 IOAPIC MMIO exits in `apic_intr_mode_init`: the 24 redirection entries read four times and written twice (the x2APIC path's save and restore, the ExtINT search, `clear_IO_APIC`'s read, mask and verify). *Built:* a patch (branch `exp/590-ioapic-once`) that reads each entry once for the ExtINT search and the clear, skips the clear's write and verify for an entry already masked and clear, and has `enable_IR_x2apic` restore only when `mask_ioapic_entries` masked something. Every entry is still read, so a kexec'd kernel or other firmware is handled as before. The last 96 reads, reusing the save's copy, were left: the copy goes stale when remapping masks entries. *Measured* (bench, nested, spin's shape and login, against v20261123.04): IOAPIC accesses 589 → 205 (`native_io_apic_read` 397 → 205, `__ioapic_write_entry` and `ioapic_mask_entry` 96 → 0), every exit −322 [−394, −222], the host's KVM handled them in −3.1 [−6.2, −1.1] ms less (3 a side). The first login −4.5 [−7.8, +0.4] at 60 a side and −2.0 [−4.3, +0.6] at 120, both within noise. They are KVM's in-kernel IOAPIC, ~11 µs each in the L1, so ~384 of them are 4-6 ms at most (inferred), and the 120-boot interval reaches −4.3. The five exit patches of #590-#592 together, a bound, read −7.4 [−12.1, −2.8] at 60 a side; #627 asks whether they are one change. *Status:* rejected. Archived as tag `archive/exp-590-ioapic-once`.
