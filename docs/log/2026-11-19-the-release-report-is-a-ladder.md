---
date: "2026-11-19"
order: 130
area: lab
title: the release report is a ladder (this change)
prs: [314]
runs: [44583379559]
---
The report measured each release alone, through the feature matrix (eight combinations of memory, vsock and hotplug, and the image's boot variants, each timed to the serial console's prompt), and compared its report.json with the previous release's, taken on another day on another instance. For v20261119.02 it said sshd accepted 7.3% later than v20261119.01 (202.6 → 217.3 ms); the two interleaved on one host said 199 → 198 (lab 44583379559). And the goal's second number, a restore to the first login, was not in it. Now `spin-machine-dev lab ladder` boots the newest five releases on one bench host, each with the spin-machine of its own commit, interleaved: a cold boot and a restore to the first login, both into a QEMU started ahead, and save, state, the kernel and QEMU's RSS as diagnostics. report.yml runs it after each release (ladder.json beside it, the table in its notes) and weekly. A release whose spin-machine cannot run a probe is a dash with the reason. boot:compat is now `lab compat`: every release since the fingerprint last moved, fetched against its sha256 and signature, and CanResume held to QEMU's one-way promise against each one's QEMU. The feature matrix is deleted: its last answer is v20261119.02's report.json asset and the "Feature matrix" section of its notes (no row moved beyond 5%, every row 344-389 ms usable), and git has the code (boot/report.go, boot/report_test.go). *Status:* the report measures each release alone since 11-20 (#314); `lab ladder` and `lab compat` in place.
