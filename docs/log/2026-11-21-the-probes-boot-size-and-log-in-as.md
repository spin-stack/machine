---
date: "2026-11-21"
order: 10
area: lab
title: the probes boot, size and log in as spin does, unless asked (this change)
runs: [44750322384, 44784196287, 44785179901]
---
#319 made spin's shape the probes' default, but left the guest at 128 MiB and logged in by the lane, and both made numbers spin never sees: at 128 MiB the guest OOM-kills its way through the boot (lab 44784196287), and on spin's path the cold first login read 532 ms where the lane's read 445 (labs 44785179901, 44750322384). `plugged` is now 2048 and `login` spin unless a run says otherwise; `plugged=0 login=lane` with the old shape options is the shape before. The release records measure what the probes measure, so the first record after this one is the first in spin's shape, and its pair's Δ (`against`) is measured in the same shape. The `floor` edit's sshd serves the lane, so a floor run passes `login=lane`.
