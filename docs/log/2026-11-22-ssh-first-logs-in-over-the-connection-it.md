---
date: "2026-11-22"
order: 140
area: lab
title: 'ssh-first logs in over the connection it timed (lab 44987441326, #450, this change)'
prs: [451]
runs: [44987441326]
---
`ssh-first` polled `VsockAccepts(cid, 22)` to time `accepts`, closed that connection, and logged in on a new one. On spin's path (`login=spin`) the closed connection had reached the forwarder, which opened one to 127.0.0.1:22, so sshd forked and exec'd `sshd-session` for it on the guest's one vCPU beside the first login timed. spin's runner opens the user's connection once. The probe now keeps the first connection accepted and runs the first login over it (`Key.LoginOn`); `accepts` is still that connect's return. If that login fails, the probe logs it and logs in again as before. `VsockAccepts` had no caller left and is gone. *Measured* as two rows of one tree, so one run compares the two probes (bench, nested, spin's shape and login, v20261122.01 with main's spin-machine, 40 each interleaved, lab 44987441326, branch `exp-ssh-first-rows`, not for merge): accept to login 69.6 → 65.3, −4.3 [−5.4, −3.0]; first login 463 → 458, −4.4 [−9.1, +0.3], within noise at 40, because `accepts` itself spreads ±4 ms; sshd accepting +1.8 [−3.7, +4.4]; the second login −0.6 [−2.0, +1.3]. The console prompt read −6.2 [−12.3, −2.2]: the echo getty runs after sshd accepts, on the same vCPU as the extra session. The size matches #451's −4.1 on the restored login. The number moves because the probe stops adding work, not because the machine changed. *Status:* in place. Compare a release measured before it only within one run, as the ladder does.
