---
date: "2026-11-23"
order: 50
area: lab
title: 'each MMIO exit put on the guest code that made it (labs 45098316459, 45098579511, 45098880914, 45098879979, #566, this change)'
issues: [590, 591, 592]
runs: [45098316459, 45098579511, 45098879979, 45098880914]
---
`exits` now keys the MMIO and port exits by `guest_rip` and names each one's function through the guest's own kallsyms. A tree booting the traceable kernel with `ftrace=` keeps the guest's trace, so the accessors' callers come with stacks. The trace does not move the counts: 3,104-3,129 EPT_MISCONFIG exits with it and without it. *Found* (bench, nested, spin's shape, v20261123.01): IOAPIC 625, ~576 of them in `apic_intr_mode_init`, where the 24-pin table is read four times and written twice (#590). ECAM 1,078 exits: enumeration 469 accesses, virtio-pci's capability walk 258, `pci_save_state` at add 128, ACPI AML 65, MSI-X enable 64 (#591). virtio's BARs: ~527 emulated at device setup and ~205 MSI-X table writes. About 650 ioeventfd notifications, ~500-650 of them the root disk's requests and ~95 vsock resets to the probe's dials (#592). Port I/O: `rtc_cmos_read` 106, ACPI's ports 137, serial 83, firmware and decompressor 277. *Measured and rejected:* a 1 MiB readahead on the root disk (`opt_io_size=524288`) left the notifications as they were (895 → 927) and the first login at +1.4 [−2.7, +8.0], 40 a side. *Status:* the probe's change is in place. Every lever left is a kernel patch of ~1-3 ms, each in its issue.
