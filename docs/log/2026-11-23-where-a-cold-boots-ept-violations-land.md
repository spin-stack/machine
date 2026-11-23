---
date: "2026-11-23"
order: 280
area: lab
title: where a cold boot's EPT violations land (#568, lab 45111606645, this change)
issues: [561, 568]
runs: [45111606645]
---
#565 counted 172 EPT violations in a cold boot at ~115 µs each nested and asked whether prefaulting the base memory in a QEMU started ahead would take them off the path. `exits` now keeps a histogram of `kvm_page_fault` by guest-physical address in 128 MiB buckets. *Found* (bench, nested, spin's shape and login, v20261123.04, 3 boots, the same in each): 171 violations, 18.9 ms of the L1's time. 52 in the base (0-128 MiB) and 6 in the bucket above it, 48-51 in the memory virtio-mem plugs (QEMU puts its device memory at 4 GiB when there is no RAM above 4 GiB), 39 in the PCIe ECAM at 0xB0000000, 9 in 0xF8000000-0xFFFFFFFF (IOAPIC, local APIC, flash) and 17 in the 64-bit BARs at 12 GiB. The MMIO ones are inferred from the addresses: KVM's first fault on an MMIO page installs the misconfigured entry that every later access exits through. *Decided:* closed as off the goal's clock. Prefaulting the base could win only the base's ~58 faults, ~6 ms at most, and only in `prestart`, because `ssh-first` starts before QEMU's exec and a prefault there is on its path. The plugged faults cannot be prefaulted without zeroing all that is plugged. With the memory at boot (#561) they become base faults, and the split is the same question. *Status:* the histogram is kept in `exits`.
