---
date: "2026-11-26"
order: 100
area: image
title: systemd 262's journal access socket masked in both managers; its sysinstall socket left as shipped (#794)
issues: [794, 795, 809]
prs: [806]
runs: [45885609644, 45864189674]
---
*Why:* 262 cost a machine without a TPM ~3.5 ms of the cold first login, and the ledger put it in PID 1 (+2.8 ms of CPU) and root's user manager (+2.2), whose start the login waits for (lab 45864189674, #794, after #795). *Found:* Ubuntu's 262.1 packages add two units to `sockets.target` that 259.9 did not have, `systemd-journalctl.socket` (the `io.systemd.JournalAccess` Varlink server, in the system manager and the user manager) and `systemd-sysinstall.socket` (the installer's Varlink API); no new process runs in the boot. *Measured* on the bench, spin's shape, each mask alone against main's 262, 100 a side: the journal socket masked, accept to login −2.0 [−3.3, −1.0], console prompt −5.1 [−8.0, −2.8], first login −2.9 [−5.2, +0.2] within noise; the sysinstall socket masked, first login −0.3 [−2.9, +2.7] (lab 45885609644). *Status:* the journal socket masked in both managers (#806); the sysinstall socket stays as shipped. PID 1's share, ~2.8 ms of CPU, is accepted: the goal moved to 250 ms on a machine with a TPM and shirudo, with stability, security and scalability before speed (#809), and #794 is closed as an accepted cost. A perf profile of PID 1 under 259 and 262 is where to look if it is reopened.
