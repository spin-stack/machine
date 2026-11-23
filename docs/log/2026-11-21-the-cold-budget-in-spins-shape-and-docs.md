---
date: "2026-11-21"
order: 130
area: docs
title: the cold budget in spin's shape, and docs/boot-time.md (lab 44841266997, this change)
runs: [44841266997]
---
`ssh-first` on v20261120.04 as published, spin's shape and login, 20 boots: sshd accepts 396/406, first login 465/479, accept to login 68.4, a second login 35.9. With the release's report (QEMU's exec to a guest ready ~28 by difference, "Freeing" 79.9) and `firmware-stages` (33.6 to the kernel's clock), PID 1 to sshd accepting is ~255 ms, more than half the cold boot. The budget measured before was in the ladder's shape and showed ~75-85. A QEMU started ahead removes the first ~28 ms only: firmware and the kernel run after `cont`. docs/lab.md is renamed docs/boot-time.md and reordered for the next investigation: the numbers, both budgets in spin's shape, the method with the deletion rule, one ledger of what the machine carries, the rejected ideas by part with the shape each was measured in, the open experiments with their budget entries, and the questions no run answers. *Status:* in place.
