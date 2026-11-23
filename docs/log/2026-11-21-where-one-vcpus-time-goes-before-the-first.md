---
date: "2026-11-21"
order: 20
area: image/lab
title: 'where one vCPU''s time goes before the first login, and what the CPU weights do (laptop leads, PR #329 closed)'
prs: [327, 329]
---
With the guest sized to 2048 MiB (`plugged=2048`) one vCPU adds ~85 ms to the first login against two, on the lane and on spin's path alike (4 boots each). The guest spends ~225 ms of CPU to the login either way, with 0 ms idle at one vCPU: the second CPU used to run beside the login what one CPU runs before it, and no unit waits longer of its own. Up to the login, in ms of CPU at one vCPU on spin's path: PID 1 ~49, udevd with its workers, trigger and credentials ~41, the kernel and its threads ~31, the lingering user@1000 ~20 (gone since #327), `ssh.service` with its `sshd -t` ~20, journald ~13, logind ~13, the session ~9-12, dbus-broker ~7, the host-key generation ~5, getty ~3. `StartupCPUWeight=1` on udevd and its coldplug trigger took the lane's first login 285 → 240 ms (Δp50 −45.7 [−49.3, −35.8], 12 boots each) and did nothing on spin's path (308 against 307): there `ssh.service` and the forwarder wait for `basic.target`, which waits for udev, so its CPU is on the login's path whatever its weight. `children_max=2` and journald's `ReadKMsg=no` gained nothing. On spin's path the host keys `spin-machine-sshd-keygen` makes are not loaded (spin's own key comes first), ~5 ms of CPU `ssh.service` waits for: spin could mask the unit at its handoff. Not benched. The lever is the order spin starts the login in (docs/boot-time.md, "Open experiments").
