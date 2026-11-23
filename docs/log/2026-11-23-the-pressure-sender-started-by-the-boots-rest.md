---
date: "2026-11-23"
order: 240
area: image
title: the pressure sender started by the boot's rest, its timer deleted (#401, labs 45120950183, 45120950314, 45120692382, 45120950141, this change)
issues: [401]
prs: [581]
runs: [45120692382, 45120950141, 45120950183, 45120950314]
---
The sender waited for a timer 10 s after the boot because, ordered after `multi-user.target` alone, it started beside the first login and cost it 2.6-3.3 ms (11-21). Since #581, `multi-user.target` is started by `spin-machine-boot-rest.service` once the first login's units are up, the first of #401's arms. *Changed:* `spin-machine-pressure.service` is `WantedBy=multi-user.target`, enabled by optimize-systemd.sh; `spin-machine-pressure.timer` is deleted; build.sh checks the want and refuses the timer; the lab's `pressure` edit makes the same want; `boot:workspace` asserts the sender runs. *Measured* (bench, nested, spin's shape and login, against v20261123.04 with the change in its overlay, branch `exp-batch3`): first login −0.8 [−7.8, +4.8], sshd accepting −0.2 [−9.1, +3.6], 20 a side (lab 45120950183); restored −0.8 [−2.9, +2.1] evicted, −0.4 [−3.6, +0.8] cached, the state +2.1 MiB because the sender is running at the save (lab 45120950314). In 5 ledgers it started at 0.37-0.38 s on the kernel's clock, after the login's end at ~0.29 s (lab 45120692382); in `boot:workspace` on two vCPUs at 0.29 s (lab 45120950141). *Status:* in place. A guest's first ~0.4 s go unreported, not its first 10 s.
