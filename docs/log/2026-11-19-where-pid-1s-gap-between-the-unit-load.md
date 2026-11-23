---
date: "2026-11-19"
order: 50
area: systemd/boot
title: where PID 1's gap between the unit load and the first job goes (experiment, tooling kept)
runs: [44561762624]
---
`image/systemd/experiments/pid1-marks.patch` makes PID 1 append its monotonic clock, its own CPU time, the units loaded and the time in `unit_load` after each step of `manager_startup` and `do_queue_default_job` to `/run/spin-pid1-marks`. `boot:waits` reads the file. `lab.yml`'s `systemd_experiment` builds the patched systemd on the runner and puts it into each overlay. Bench fleet, 10 boots (lab 44561762624), p50 ms:
- bus, varlink, coldplug, vacuum, ready: < 0.2 together;
- **loading what `default.target` reaches: 28.4 wall, 20.9 of it PID 1's CPU, 192 units** (~0.11 ms of CPU each; the rest is likely cold reads);
- building the transaction: 0.30;
- the first dispatch: 0.87.

The gap is unit loading, not the transaction that 11-13's single debug-logged boot named. 96 of the units a boot lists are inactive, loaded because another unit names them in `After=`/`Wants=`: `initrd-*`, `plymouth-*`, `modprobe@*`, hibernate, cryptsetup's targets, `firewalld` and `auditd` named by docker. Masking does not spare a load (a masked unit is still looked up and loaded as masked), which is why masking 11 units moved nothing (docs/boot-time.md, "Measured and rejected").
