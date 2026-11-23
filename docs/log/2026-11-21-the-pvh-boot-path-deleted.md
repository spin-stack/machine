---
date: "2026-11-21"
order: 140
area: machine/kernel/qemu
title: the PVH boot path deleted (this change)
---
Since v20261119.02 the machine boots a bzImage through qboot and fw_cfg, and PVH stayed only as a fallback: `machine.OpenRelease` booted a release's vmlinux through `pvh.bin` when it had no bzImage, `--kernel` took a vmlinux, and machine's QEMU tests loaded an ELF stub through `pvh.bin`. Nothing called any of it. `OpenRelease` now requires `kernel/bzImage` and `linuxboot_dma.bin` and names the missing file. `--kernel` refuses a file without the bzImage's `HdrS` header. `pvh.bin` no longer ships, and machine.env loses `pvh_sha256`. The machine's QEMU tests load a bzImage stub instead. The kernel config changes: `CONFIG_PVH` is off, so the kernel and its checksum change, and the vmlinux still ships for what reads its symbols. *What it costs:* `lab compat` stops at the first release without a bzImage, which is v20261119.01 and every release before it, so v20261119.02 is the oldest it checks. spin's Taskfile.yml:250 checks for `_output/qemu/pvh.bin` and must drop that line. The fingerprint does not move: it hashes neither the kernel nor the option ROM. *Status:* in place.
