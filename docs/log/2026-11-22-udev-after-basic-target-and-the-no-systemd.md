---
date: "2026-11-22"
order: 80
area: image/lab
title: udev after basic.target, and the no-systemd floor again (labs 44977226981, 44977605109, 44978034052, 44978079694, 44978927041)
issues: [452]
runs: [44977226981, 44977605109, 44978034052, 44978079694, 44978927041]
---
In the `chain` probe's timeline sysinit.target waited for udevd's READY, at 280 ms against ssh.service's 237: udevd runs at the lowest weight while the boot runs, so its start came after every early unit, and basic.target and spin's forwarder after it. Copies of systemd-udevd.service and systemd-udev-trigger.service without `Before=sysinit.target`: cold first login −46.3 [−49.4, −39.8] (60 boots each), basic.target 286 → 242 ms on the guest's clock, udevd ready after the login at 342. Restored, ~30% of restores take ~530 instead of ~112 ms (p95 539-547), and one run read restored into prestarted +317. A journal dumped from the slow ones (branch `exp/restore-diag`): the guest resumes at 0.430 s, and the next login is accepted at 0.889 with nothing logged between, 0.04 s idle in the guest's whole uptime. It is the forwarder-early arm's tail and likely the coldplug arm's +463.7: a guest saved before the boot's deferred work is done (#452). The same change at udev's normal weight gains nothing (+0.7 [−5.0, +6.5]), so the gain is the weight and the order together. The serial getty at the lowest weight +2.7 [−4.3, +7.9], four unused varlink sockets masked −0.8 [−4.8, +4.3]. *The floor:* on v20261122.01, no systemd before sshd, the lane's first login 404 → 250 ms, −153.9 [−162.5, −151.2], sshd accepting 242 → 197 (20 boots). *Status:* udev after basic.target open, blocked by #452; the getty and sockets rejected.
