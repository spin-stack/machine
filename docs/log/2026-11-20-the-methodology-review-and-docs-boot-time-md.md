---
date: "2026-11-20"
order: 100
area: lab/docs
title: the methodology review, and docs/boot-time.md (this change)
runs: [44587603255, 44641240861, 44727451080]
---
Two adversarial reviews of how boot time is measured, each finding re-run on the ladders of runs 44641240861 and 44727451080 (five releases interleaved, 20 boots each). *Found:*
- No per-phase budget existed, so nothing said whether 100 ms is reachable. Split by phase, the cold boot spends ~125-135 ms in QEMU, qboot and the kernel before PID 1 runs. The no-systemd floor (209 ms) was measured once, on 11-13, on an 87 ms kernel.
- The goal probes boot a shape and a login path spin does not: no CPU or memory ceilings, where spin boots 1 CPU and 128 MiB with ceilings of 16 and 8 GiB (the kernel 86.6 ms against 62, lab 44587603255), and an `sshd-vsock.socket` that spin turns off.
- Effects inside the noise were accepted. Two releases booting the same guest read Δp50 of the first login in [−8.7, +4.8] ms (bootstrap, n=20), yet the logind socket's −4 and `driver_async_probe`'s −5 were kept while −8 and −6 were rejected as noise.
- `docs/releases.md`'s Δ is drift between runs: v20261120.01's restore login read 158.9 and 167.0 in the two runs.
- The bzImage's cost was recorded as zero by a clock that starts after decompression: sshd accepted ~10-13 ms later from v20261119.01 to .02 while "Freeing" went 76 → 62.5.
- `restore-first`'s `read` reads the mapped-ram file's holes (2 GiB for 155 MiB of data), and its `up` times the monitor socket.

*Decided by the maintainer:* the kernel, systemd, QEMU and qboot may all be patched without limit. systemd is a must, so a no-systemd init is only ever a bound. *Done:* `docs/boot-time.md` holds where both numbers stand, the per-phase budget, the method with one acceptance rule (a gain when the 95% interval of Δp50 excludes 0), the kept and rejected tables, the measurement pitfalls, and what is next. This log's former "Where we stand", "Measured and rejected", "Measurement pitfalls found" and "Open questions" moved there. *Next:* the probes in spin's shape (branch `lab-measure`); multifd channels on the restore and the no-systemd floor on today's kernel, both on the bench.
