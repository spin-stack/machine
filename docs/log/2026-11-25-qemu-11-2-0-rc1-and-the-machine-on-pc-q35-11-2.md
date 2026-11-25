---
date: "2026-11-25"
order: 10
area: qemu, machine
title: QEMU 11.2.0-rc1, and the machine on pc-q35-11.2
---
QEMU moves from 11.2.0-rc0 to 11.2.0-rc1, and `machine.Type` from pc-q35-11.1 to pc-q35-11.2. *Why:* QEMU is 11.2 and nothing yet runs on a checkpoint that would be stranded, so the machine takes the type its QEMU was made for now, not after release. *What 11.2's type changes against 11.1* (`hw_compat_11_1`, `pc_compat_11_1` is empty): EHCI's `x-migrate-fetch-addr-64bit`, `pcie-root-port`'s `speed` and `virtio-blk-device`'s `fua`. The machine has no EHCI and no root port; its virtio-blk disks now offer FUA, which QEMU's file driver emulates with a flush after the write: the flush a guest without FUA sent itself. That it costs no flush more is inferred, not measured. *Found:* 11.2 writes "(secure)" after "(deprecated)" in `-machine help`, and `verify qemu` stopped seeing a deprecated pin; the pattern takes "(deprecated)" anywhere after the name, and the test reads 11.2.0-rc1's own help. Every QEMU patch applies without fuzz; 0005's hunk moved 14 lines. *Status:* in place; every checkpoint from a release before this one boots cold.
