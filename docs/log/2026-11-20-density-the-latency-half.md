---
date: "2026-11-20"
order: 20
area: lab
title: density, the latency half (#273)
issues: [273]
runs: [44665519945, 44667235868]
---
#278 answered the memory half (an idle workspace costs its host ~165-170 MiB). `lab` probe `density` boots the row's number of workspaces, logs into each, lets them settle 10 s, times one more's first login, and ends them all before the next measurement. On the bench (c8id.4xlarge, 16 vCPUs, 32 GiB, nested), 2 vCPU / 2 GiB workspaces, idle, 10 of each interleaved: with 0, 4, 8 and 16 already running the first login is 332/343, 328/340, 328/345 and 326/338 ms (lab 44667235868). Each of the crowd took ~625 ms to boot and log into. Idle workspaces do not move the first number up to 16 on this host. The 25% point is past that. Not measured yet: a CPU-bound crowd (`load=cpu`), more than 16, and metal. A first run left every crowd up until the experiment ended (m.Cleanup runs then) and measured a host carrying a growing pile of machines (lab 44665519945); the probe ends its crowd itself.
