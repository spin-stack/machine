---
date: "2026-11-19"
order: 40
area: boot/image
title: where the first login's own time goes, and logind's socket early (this change)
runs: [44555905383, 44557162195, 44557605832, 44558035927, 44558053261]
---
`boot:login-lane` marks the guest's journal through the first login: accept → authenticated 55.5, PAM's session → logind's new session 29.0, → scope 4.3, → command 8.4 ms (v20261119.01, lab 44555905383). Its diagnostic rows (`SPIN_LANE_DIAG`, sshd at DEBUG1 and logind at debug, lab 44557162195) say what they are:
- **Before authentication.** PID 1 starting `sshd@` takes ~11 ms, and OpenSSH 10's three execs (`sshd` → `sshd-session` → `sshd-auth`) ~12. Loading PAM's modules cold takes 8.3 ms, against 0.9 at the second login. ML-KEM is 3.5.
- **The hop to logind** is logind not running yet. Its Varlink socket kept its default dependencies, so logind waited behind `sysinit.target` and udevd, ready ~230 ms into the boot (`boot:waits TARGET=systemd-logind.service`, lab 44557605832). The first login waited ~32 ms for it, the second 1.

The socket without default dependencies (`systemd-logind-varlink.socket.d/20-lane.conf`), 20 boots of each (labs 44558035927, 44558053261):
- the hop: 24.5/41.8 → 15.8/22.7;
- accept to login: 131/137 → 120/128;
- the first login: 332/355 → 328/337. The p50 moves little because logind starting sooner takes the CPU sshd was starting on.

Masking `modprobe@drm`, which logind is ordered after, added nothing (327/345). What is left of the login is CPU contention on 2 vCPUs, `user@1000` (38 ms) and the host key generation (21 ms) starting in the same window, rather than waits.
