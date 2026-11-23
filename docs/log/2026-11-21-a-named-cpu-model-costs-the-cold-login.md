---
date: "2026-11-21"
order: 170
area: machine/lab
title: a named CPU model costs the cold login 27 ms on the bench (lab 44855890737)
runs: [44855890737]
---
For memories that resume on any host of a pool, `ssh-first` compared `host` with `lab tree --cpu baseline`, which is `machine.Models` plus `machine.Baseline` on the runner. The nested c8id's newest runnable model is Cascadelake-Server-v5: Nitro's L1 does not expose enough for SapphireRapids or GraniteRapids. Against `host`: first login +27.2 [+13.0, +30.8], sshd accepting +23.1 [+9.7, +28.5], warm login −0.4 within noise, QEMU's RSS unmoved. *Why:* spin runs `-cpu host`, so a memory resumes only on a host showing every saved feature (spin-stack/spin#1417). *Result:* a fleet-wide named model is rejected for now. `Baseline` stays public for spin. *Status:* open. Next is `host` minus the features a pool's hosts do not share, measured the same way.
