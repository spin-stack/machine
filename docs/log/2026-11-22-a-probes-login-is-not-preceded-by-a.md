---
date: "2026-11-22"
order: 30
area: lab
title: a probe's login is not preceded by a connection spin does not make (labs 44977850524, 44978246896, this change)
issues: [450]
prs: [341]
runs: [44977850524, 44978246896]
---
`Key.LoggedIn` polled `VsockAccepts(cid, 22)` until something accepted, then logged in. On spin's path (`login=spin`), that connection reached the forwarder, which opened one to 127.0.0.1:22, and sshd forked and exec'd `sshd-session` for it. The probe then closed it and logged in at once, so the guest's one vCPU ran that session beside the login being timed. spin's runner opens the user's connection once, through its supervisor, and asks nothing first (spin-stack/spin's `internal/runner/forward/forward.go`). LoggedIn now logs in until a login is done, with VsockAccepts' 50 ms connect timeout on the login's own connect, because a connect to a context id that does not exist yet waits 2 s. *Measured* as two rows of one tree, so one run compares the two probes (bench, nested, spin's shape and login, v20261122.01 with main's spin-machine, 40 each, lab 44978246896): restored login −4.1 [−6.0, −2.2], `running` −0.0 [−1.0, +0.9], the cold login before the save −3.6 [−7.2, +0.8], within noise. Only the restored login changed, in lab 44977850524 too: −3.6 [−7.3, −1.7] at 30. The number moves because the probe stops adding work, not because the machine changed, as with #341. LoggedIn is restore-first's, prestart's, restore-together's and density's. `ssh-first` keeps its own poll, because it times `accepts` with it, so its first login still carries the session (#450). *Status:* in place.
