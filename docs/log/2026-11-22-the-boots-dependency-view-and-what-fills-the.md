---
date: "2026-11-22"
order: 380
area: lab
title: 'the boot''s dependency view, and what fills the stretch before the first job (labs 45070438471, 45070718529, 45071125219, 45071549627, 45071102677, #521, #553, #554)'
issues: [553, 554]
runs: [45070438471, 45070718529, 45071102677, 45071125219, 45071549627]
---
The bootchart probe now keeps `systemd-analyze critical-chain` of the login's units, the login user's manager's chain, and `systemd-analyze dot --order` among the chain's units, drawn by graphviz (docs/measuring.md, "Boot charts"). *The stretch from ~109 to ~146 ms* (debug boots with `systemd.log_level=debug systemd.log_target=kmsg systemd.log_ratelimit_kmsg=0`; without the last, systemd's own kmsg rate limit cut PID 1's lines at 118 ms): PID 1 loads every unit `multi-user.target` reaches, ~36 ms in those slowed boots, with 56 lookups of mount units that do not exist, and then computes a transaction of 79 jobs in ~0.4 ms. `UnitsLoadFinishTimestamp` is taken before that loading, so the plot's empty stretch is the loading. *dev-vda.device:* no unit queues a job for it, and nothing depends on it. It is `tentative` from PID 1's catchup (root is mounted from it) until udevd announces it, and the plot draws tentative as activating. *The chain to the login* on v20261122.06: the forwarder that stands for spin's supervisor waits for `basic.target`, 28-38 ms after `ssh.service` is active. Taken off it (`--edit forwarder-early`), it was active ~33 ms sooner, and the goal numbers did not move: cold +4.2 [+0.0, +11.0], restored −0.0 [−2.3, +2.3] (`prestart`, 20 a side). The login waits for systemd-logind next (#553). *Status:* the view in place. The forwarder row moved to "Measured and rejected". The release copy is open (#554).
