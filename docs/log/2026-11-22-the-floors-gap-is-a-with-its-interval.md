---
date: "2026-11-22"
order: 240
area: lab
title: the floor's gap is a Δ with its interval (#495, this change)
issues: [495]
runs: [45018301368]
---
The record kept the floor as two p50/p95 pairs, and the gap a reader wants, what systemd costs at most, was their difference with no interval, so it could not be read by the one acceptance rule. A record now keeps `bounds.floor`, the floor's Δp50 against the release's lane login from the same run, drawn by `DeltaP50`, the bootstrap every release's Δ uses, and docs/releases.md shows it in "Bounds" beside the two pairs. A bound is still never read against the release before it. *Checked* on the floor's samples from report.yml run 45018301368 (v20261122.02, bench, spin's shape, 20 boots each), kept as `internal/lab/testdata/floor-45018301368.json`: −166.3 [−174.7, −160.3]. The floor keeps its own lane-login arm. Its sshd does not serve spin's path, which is spin's forwarder before sshd, and the floor has no systemd to run it. *Status:* in place.
