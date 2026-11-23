---
date: "2026-11-22"
order: 190
area: kernel, boot
title: 'the host counts the port I/O exits 0004 and 0010 save (labs 45017602084, 45018204750, 45018204645, 45018410483, 45018410313, 45018410890, #486, this change)'
prs: [478]
runs: [45017420814, 45017423344, 45017602084, 45018204645, 45018204750, 45018410313, 45018410483, 45018410890]
---
The vmlinux check (#478) sees 0004 and 0010 compiled in, not earning. 0004 needs `raw_pci_ext_ops`, set from the ACPI MCFG table, and 0010 needs KVM's CPUID leaf, so a kernel that set up MMCONFIG after enumeration, or a machine without MCFG, would pass it. `boot:patches` now cold-boots spin's shape to `/bin/sh`, reads KVM's `io_exits` and `mmio_exits` for every vCPU through QEMU's `query-stats`, and holds the median of 3 boots' `io_exits` at or under 1,025 (`TestPCIConfigurationCostsNoPortExits`). *Calibrated* on the bench, 20 boots each, v20261122.02 with its own kernel and with kernels built without each patch (parts runs 45017420814, 45017423344, branches `exp/486-no0004` and `exp/486-no0010`, not for merge): the release 955-974, median 958, mmio 2,142; without 0010 1,075-1,097, median 1,094; without 0004 3,477-3,499, median 3,496, mmio 881. 0004 saves 2,520 port exits, not the issue's estimated 1,300: 1,261 configuration accesses move to ECAM, and mmio_exits falls by the same 1,261. 0010 saves ~120, clear of a spread of ~25, so both are guarded. *Checked:* the release passes (974/974/980). The guard fails without 0004 (median 3,481) and without 0010 (median 1,097). *Status:* in place, run by compat.yml with every change's kernel.
