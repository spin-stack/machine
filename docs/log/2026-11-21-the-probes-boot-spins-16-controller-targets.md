---
date: "2026-11-21"
order: 150
area: lab
title: the probes boot spin's 16 controller targets (lab 44847364468, this change)
runs: [44847364468]
---
spin's c0ffbc0 (2026-11-20) gave the hotplug controller 16 targets, 4 for disks attached at run time and the rest for the extensions a workspace boots with, where the probes booted 4. `ssh-first` on v20261120.04, 20 boots each, interleaved: first login −3.8 [−11.1, +4.5], sshd accepting −2.3 [−6.3, +2.4], within noise. *Why:* the probes measure spin's shape. *Result:* `hotplug-disks` defaults to 16, and the budget's rows taken with 4 stand. *Status:* in place.
