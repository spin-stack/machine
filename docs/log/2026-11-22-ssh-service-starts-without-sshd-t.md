---
date: "2026-11-22"
order: 40
area: image
title: ssh.service starts without `sshd -t` (lab 44977044826, this change)
runs: [44976328180, 44977044826]
---
Where PID 1 to sshd accepting goes, read from the guest after the first login (a `chain` probe on branch `exp/chain`, lab 44976328180, 10 boots, v20261122.01, spin's shape): PID 1 starts at 84.6 ms on the guest's clock, the first jobs dispatch at 139.3, sysinit.target at 280.5, basic.target 285.6 and the forwarder 286.6. The CPU used to the first login: the kernel's own 106 ms, PID 1 66, udevd 46, ssh.service 45 (both logins' sessions in it), journald 20, logind 16, the bus 11. One vCPU and no idle time, so the login waits for every exec before it. Ubuntu's ssh.service runs `sshd -t` as ExecStartPre: without it, the cold first login −3.2 [−7.2, −0.8] and sshd accepting −2.4 [−6.4, −1.1], 60 boots each interleaved, spin's shape and login. The same run took four other execs and settings out, one arm each, and none moved the login: systemd-udev-load-credentials masked −2.1 [−6.3, +0.3]; systemd-user-sessions' ExecStart emptied, its ExecStop kept, −0.6 [−5.2, +1.5]; sshd-socket-generator masked +0.8 [−4.0, +4.7]; and `DefaultMemoryAccounting=no` +4.1 [+0.8, +7.1], worse, with sshd accepting +6.2 [+2.2, +8.0], not explained. So an exec pair costs the login ~3 ms here, not the ~6 a getty's echo's 10 ms of CPU suggested. *Status:* `sshd -t` dropped at start (`ssh.service.d/20-no-config-test.conf`, asserted by build.sh, in the lab's lane edit); the restore unchanged (restore-first in the description).
