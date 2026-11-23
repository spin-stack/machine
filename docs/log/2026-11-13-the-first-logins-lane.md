---
date: "2026-11-13"
order: 100
area: image
title: the first login's lane (this change)
runs: [43518915618]
---
Where systemd's ~250 ms over the floor went, on the bench fleet: ~60 before sshd accepts (sysinit, udev, journald), ~105 in pam_systemd, and ~65 in a handshake competing for the CPU. logind answers a session of class `user` only once the user's manager has started, and orders its scope after `systemd-user-sessions` (systemd v259.9 source, `logind-session.c`). The vsock socket, `sshd@`, dbus, logind, `user-runtime-dir@` and `user@` without default dependencies, no `/run/nologin` at boot (pam_nologin refused the lane's first login: "Access denied for user spin by PAM account configuration"), sessions of class `user-early-light`, and udev's coldplug filtered to block, net, virtio, misc, input, rtc, ptp and ttyS0: first login 478 → 402 ms (lab 43518915618). Measured and left out: priority (`CPUWeight`, `Nice`) on the lane, −2 ms over the filter and a slower second login (63 vs 45 ms).
