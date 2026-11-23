---
date: "2026-11-19"
order: 110
area: kernel
title: branch `kernel-floor`, patches 0007-0011 and a config trim (this change)
prs: [291, 345]
runs: [44563496308, 44563512999, 44587603255]
---
The kernel was 78.0 ms nested against 45.0 on metal, same CPU (boot:initcalls, labs 44563496308, 44563512999). Five of its gaps, each a patch:
- 0007, ORC's lookup table by one merge pass instead of 59,650 binary searches: 3.5 ms on both hosts. Every block checked against the binary search, 0 differ.
- 0008, init's 1,989 ftrace records freed with one memmove: 2.6 ms on both hosts.
- 0009, the boot CPU polls for an AP by `local_clock()` while booting: 4.1 ms nested, under 1.4 on metal.
- 0010, no early-quirks PCI scan under KVM: 2.1 ms nested. pci=noearly also defers MMCONFIG; this skips only the scan.
- 0011, virtio-pci's `queue_select` written only when the queue changes: 84 → 22 writes.

Config: `X86_MSR`, `X86_CPUID` off (nothing in the image or spin opens `/dev/cpu`); `BLK_DEV_LOOP_MIN_COUNT=0` and `SERIAL_8250_RUNTIME_UARTS=1` (from `kernel-trim`). *Measured:* only on a laptop, not nested, 10 boots each interleaved, a lead: the ORC gap 4.59 → 0.93 and 4.45 → 0.72, the WAET gap 0.44 → 0.07 and 0.32 → 0.06, the ftrace gap 2.7 → under 1.1 ms. 0009 and 0011 cannot move there: the AP never waits and exits are cheap. The kernel's total read 61.5 → 55.7 in one run and 53.0 → 54.3 in the other on a host building QEMU at the time: noise. Not yet measured on the bench fleet or against the first login.

Bench, nested, workspace shape (2026-11-19, lab 44587603255, 20 boots each): kernel start to "Freeing" 96.4 → 86.6 ms (−9.8), in initcalls 61.7 → 58.7, outside them 35.1 → 27.8, where ORC and ftrace are. `virtio_pci_driver_init` 8.57 → 8.59: 0011 does not show even nested. *Status:* merged 11-19 (#291); 0009 and 0011 deleted 11-20 (#345); 0007, 0008, 0010 and the config trim in place.
