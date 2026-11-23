---
date: "2026-11-22"
order: 330
area: lab
title: 'the boot chart''s first sample, wait graph and legend (lab 45064083873, spin-stack/systemd-bootchart#10, #534)'
issues: [538]
prs: [34, 38, 40, 44]
runs: [45060814905, 45064083873]
---
The write graph in lab 45060814905 had the read graph's shape from 85 to 215 ms, in a boot whose tables said it wrote 0.0-0.1 MB. It read the right counter, `pgpgout`. As PID 1 the sampler starts before /proc is mounted, and its first samples held zeros. The first sample that read /proc then differed from zero by everything since the kernel started: 21 MB read, 124 KB written and 79 ms of CPU in one 5 ms interval. The IO graphs' 250 ms average spread that jump into a ramp, and the CPU and pressure graphs began with a full bar. The fork at 3125dea gives the samples before /proc the first counters it reads. It also drops the "CPU wait" graph, which summed every task's run delay, so on one vCPU it sat at its ceiling and read like a boot bound by IO (upstream #13). CPU pressure now sits under CPU utilization. The process graph's title says what its colours and dashed lines are (upstream #39), and the header's "Graph data" and "Sampler" lines are drawn at 12px instead of 8px. `--proc` tests hold three upstream reports that the fork already got right: a "(systemctl)" name kept whole (#44), no nameless process without /proc/PID/sched (#34), and a short-lived `@`/`:` template instance drawn (#40). Upstream #38's leaks are cppcheck not modelling `_cleanup_fclose_`. *What lab 45064083873 shows* (bench, spin's shape, v20261122.05, 3 boots, the fork's branch at 1f00a0f, whose tree 3125dea merged unchanged): `pgpgout` is 124 KB in every sample of every boot, and each write graph is empty. No chart has a wait graph, and each carries the legend and the 12px header lines. The read graph still starts ~20 ms before the first read, from its 250 ms average (#538). *Status:* in place.
