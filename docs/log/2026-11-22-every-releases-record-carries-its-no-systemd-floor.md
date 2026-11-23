---
date: "2026-11-22"
order: 220
area: lab
title: every release's record carries its no-systemd floor (this change)
runs: [44978079694, 45018301368]
---
AGENTS.md and docs/measuring.md (rule 8) ask for the bounds each release, the no-systemd floor first, and report.yml measured only the goal's numbers and the diagnostics: the floor was last measured by hand, on v20261122.01 (lab 44978079694). `releases measure` now boots the release beside its floor after the ladder, `ssh-first` with `login=lane` over the release and the release with `--edit floor --append init=/usr/local/sbin/spin-floor`, 20 boots each, interleaved, as the hand-made runs did. The record keeps both first logins as `floor-lane` and `floor`, and docs/releases.md tables them under "Bounds" once a record has them. Only the release a record is made for is measured beside its floor: the one it is against has none, and no Δ is read of a bound. It is the one bound docs/boot-time.md lists as each release's. *Checked* by report.yml on this branch against v20261122.02 (run 45018301368, record not proposed): first login by the lane 406.8 / 420.2, no systemd before sshd 240.5 / 245.5 (p50 / p95, 20 boots each, bench, spin's shape), beside the ladder's cold 420.0. *Status:* in place.
