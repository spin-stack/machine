---
date: "2026-11-13"
order: 110
area: kernel/qemu
title: branches `kernel-snapshot`, `qemu-snapshot` (rejected)
runs: [43514550646, 43514884598]
---
QEMU publishes bus 0's configuration space over fw_cfg after the firmware (etc/spin/pci-config) and the kernel answers boot-time configuration reads from it until each dword is written. It works (`PCI: configuration space of 5 devices on bus 0`) and is worth -14 ms against the same kernel without it (lab 43514550646), but only 2-3 ms against 0005, which already removes most repeated reads: 209/217 against 211/222 (lab 43514884598). Not worth a QEMU patch and a 250-line kernel patch.
