---
date: "2026-11-22"
order: 310
area: lab
title: 'the boot chart from spin-stack''s systemd-bootchart (labs 45042034749, 45042509602, 45043434713, #525)'
runs: [45042034749, 45042509602, 45043434713]
---
Ubuntu's 235 named processes "9", read cgroup v1 and so labelled none with its unit, and filtered out 62 of the processes that ran. The `bootchart` edit now compiles spin-stack/systemd-bootchart at the commit versions.yaml pins, in a container on the runner that makes the tree (`internal/lab/bootchart.Dockerfile`), with `ControlGroup=yes` and `Cmdline=no`, and the probe keeps the fork's JSON beside the chart. *What v20261122.03's chart shows at 78dc08c* (lab 45043434713, bench, spin's shape, 3 boots): CPU by unit p50 init.scope 134 ms (PID 1 118 to 123 ms, and the sampler, named `bootchart`, ~10 ms), systemd-udevd.service 25, systemd-journald.service 19, systemd-logind.service 15, the root cgroup's kernel threads 15, session-1.scope 13, ssh.service 11, dbus-broker.service 10. systemd-udevd waits ~120 ms to run and systemd-udev-trigger ~110. The sampler says it used ~10 ms of CPU, ~0.2 ms a sample. `first_login` 433 ms p50, `chart_end` 330. Leads, not savings. *Status:* in place.
