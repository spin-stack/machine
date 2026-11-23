---
date: "2026-11-20"
order: 40
area: qemu/machine
title: the hotplug controller scans the places it has (#324, lab 44788216590)
prs: [324, 325, 326, 328]
runs: [44587603255, 44788216590]
---
In spin's shape the 4 hotplug disks' virtio-scsi controller made the guest scan 256 targets at boot: `virtio_scsi_init` 12.1 ms (lab 44587603255), ~460 of the boot's exits (QEMU's trace, laptop). `qemu/patches/0002` gives virtio-scsi a `max_target` property (default 255, a disk plugged above it is refused, the build asserts the property), and the machine sets it to the hotplug disks less one, so the guest scans the places a disk can be attached to and no more. Every place is still scanned, unlike `scsi_mod.scan=manual` (rejected: it loses disks); 4 disks attached to a running guest all appear. On the bench, spin's shape (`plugged=2048`, `login=spin`), 20 each against the same QEMU without the property: first login 512/531 → 503/514, Δp50 −8.9 [−15.5, −0.9], sshd accepts −10.3 [−18.8, −0.2], the console prompt −12.5 [−19.0, −5.3]. The fingerprint moves for machines with hotplug disks, so their older checkpoints boot cold. In the same run `CONFIG_SCSI_SCAN_ASYNC` (#325) read −2.6 [−8.9, +6.8]: rejected. The decompressor without its VGA cursor (#328) and an uncompressed payload (#326) are in "Measured and rejected" (docs/boot-time.md). *Status:* kept.
