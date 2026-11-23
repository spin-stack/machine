---
date: "2026-11-23"
order: 390
area: release
title: 'v20261123.07 holds #645''s cold gain; its record''s 20 a side did not see it (labs 45252584128, 45219785493, release run 45241645340, #694)'
issues: [645, 694]
runs: [45219785493, 45241645340, 45252584128]
---
*Why:* the record read the cold first login at −3.5 [−12.9, +2.9], within noise, against #645's −16.8 [−20.9, −13.0], and spin was about to be told what the bump gains. *Ruled out* (read from the artefacts): the lab arm and the release boot the same machine. QEMU (`f271a946…`), qboot, `pvh.bin` and `linuxboot_dma.bin` are byte-identical, `spin-machine args` prints one command line but for paths, and both vmlinux files are stripped, 40,165,128 bytes, with the same two LOAD segments and the same config; the release's adds 0020. QEMU's RSS moves the same in both (anonymous −34.0 MiB, file +19.9/+20.1). *Measured* (bench, nested, spin's shape and login, both releases as published, each with its own spin-machine, 60 a side, interleaved): first login 378 → 359, −18.6 [−23.6, −14.0], sshd accepting −15.3 [−21.2, −12.6], the console prompt −18.7 [−24.0, −12.8] (lab 45252584128). The record's run disagreed with itself: its `restore-first` cold boot, the same boot at 40 a side, read −14.2 [−19.9, −8.6]; its 20 `.07` boots sat at a p50 of 370 where the lab's sit at 352-359, and 20 a side drawn from lab 45219785493's samples reach −3.5 in 5 of 100,000 draws. *Status:* the gain stands at ~−17 to −19 ms cold. How many boots the record takes is #694.
