---
date: "2026-11-19"
order: 30
area: lab
title: nested against metal
runs: [44534028478, 44535156851, 44535161371, 44537498507]
---
`bench-metal` ran for the first time (spin-stack/ci#20 keeps it to c8i.metal-48xl, on-demand when spot has none; spin-machine#272 caps the job at 60 minutes). The same CPU (Xeon 6975P-C) on both, p50:
- first login: 378-392 ms nested (two runs) against 332 metal (44537498507), so **nesting costs ~50-60 ms**;
- `boot:firmware-stages`: QEMU start 30.5 against 28.1, the PCI walk 5.00 against 2.22 (173 exits, ~29 against 13 µs each), kernel and init 108.7 against 69.5 (44535161371, 44535156851).

An r7i.metal-24xl (Sapphire Rapids, slower per core) read 296 ms (44534028478), faster than the c8i.metal. Unexplained. Repeat it before quoting it. Every bench-fleet number in this log and in docs/boot-time.md is nested.
