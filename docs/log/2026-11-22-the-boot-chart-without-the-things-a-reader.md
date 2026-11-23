---
date: "2026-11-22"
order: 320
area: lab
title: 'the boot chart without the things a reader had to guess at (lab 45060814905, spin-stack/systemd-bootchart#9, #528)'
runs: [45043434713, 45060814905]
---
The charts at 78dc08c (lab 45043434713) named one executor "9", gave idle kernel threads no cgroup in one boot of three (so "/" counted 15 processes there and ~50 in the others), showed "systemd-user-sessions.service (deleted)" as a unit of its own, and drew the header and top tables overlapping on a page ~340 px taller than its chart. The fork at 096d61a names a process whose comm is digits by its exe link's basename, says "/" for a process the kernel lists in no cgroup (before systemd mounts one), cuts " (deleted)", gives its font sizes a unit (CSS ignored them, so browsers drew every label at 16px), and sizes the page from what it draws. *What lab 45060814905 shows* (bench, spin's shape, v20261122.05, 3 boots): no process without a cgroup, "/" at 51 processes in each boot, no name of digits and no " (deleted)", and each page 40 px below its last row. The executor and the removed cgroup did not recur in these boots, so the fork's `--proc` tests are what show those two fixed. A name in parentheses such as "(systemd-user-)" is a child systemd forked, seen once before it exec'd: that is what it was then. *Status:* in place.
