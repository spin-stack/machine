---
date: "2026-11-21"
order: 120
area: kernel
title: '`DEFERRED_STRUCT_PAGE_INIT` deleted, the loop and 8250 counts kept (lab 44839538785, #356, #357)'
prs: [345]
runs: [43495583323, 44839538785]
---
The three options #345's arm meant to measure and did not, one arm each against main's kernel (d255113), prestart in spin's shape, 40 boots a side, v20261120.04's QEMU and image. `DEFERRED_STRUCT_PAGE_INIT` off, upstream's default: cold first login −2.5 [−9.3, +3.8] ms, restored +1.1 [−6.4, +7.9], into a QEMU started ahead −4.0 [−13.0, +13.3]. It was kept on 11-13 for a memmap of 5.2 ms at 2 GiB at boot (lab 43495583323). spin boots 128 MiB, which the kernel does not defer, and plugs the rest through virtio-mem, which it never defers. A boot option does not run in a restored guest, so the restored row's upper bound over +5 says the run's noise, not the option. Upstream's 8 loop devices and 4 runtime ports: cold +5.3 [−3.2, +15.0], restored +1.7 [−6.7, +12.7]: within noise, but the cold bound does not rule out the 2-4 ms the initcalls and udev's coldplug of 8 more block devices would cost, so the counts stay. *Status:* the deferred page init deleted; the counts kept.
