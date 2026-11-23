---
date: "2026-11-23"
order: 320
area: systemd
title: 'the executors'' sandbox, a bound: −14 ms (#574 experiment 4, labs 45120692382, 45121038332, #637, #638, this change)'
issues: [574, 637, 638]
prs: [581]
runs: [45120692382, 45121038332]
---
Read first, a release ledger (v20261123.04, spin's shape and login, 5 boots): before the first login the executors spend journald 6.0 ms, logind 10.3 and `user@0` 2.8, p50, beside the units' own binaries. *The bound:* one drop-in each for journald, logind and `user@` emptying `SystemCallFilter=`, `SystemCallArchitectures=`, `SystemCallErrorNumber=`, `RestrictAddressFamilies=`, `RestrictNamespaces=`, `RestrictRealtime=`, `RestrictSUIDSGID=`, `MemoryDenyWriteExecute=`, `LockPersonality=` and every `Protect*=`; `PrivateTmp=`, `IPAddressDeny=` and `DeviceAllow=` kept. udevd was left out: it starts after the first login since #581. *Measured* (bench, nested, 60 a side, branch `exp-batch3`): first login 374 → 360, −14.1 [−18.5, −11.5]; sshd accepting −13.2 [−16.0, −10.9]; accept to login −2.1 [−3.1, −0.1]. *Status:* a bound, not carried. #637 splits it into the seccomp and namespace halves and proposes one seccomp filter per unit in systemd. #574 closed with its five experiments: #579 in (−6.5), #580 rejected, #581 in (−21.5), #460 in (the kernel's), this bound to #637. The runner-up, the dynamic linker's share, is #638.
