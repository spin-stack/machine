---
date: "2026-11-21"
order: 160
area: docs
title: every doc checked against the tree and spin's main (this change)
runs: [44847770977, 44848049631]
---
A pass over every document for claims the code no longer bears out: names, defaults, files, spin's policy (read at spin's d4f62bd), and *Status* lines. Numbers that only a run can settle are listed in docs/boot-time.md's open experiments, not changed. Two were measured: TCG reaches the first login in 7604 ms against KVM's 455, ~17 times, where the docs said fifty (`ssh-first`, lab 44848049631, 5 boots each). QEMU's mapped files are 6.6 MiB resident on v20261120.04 against the ~42 MB of 11-14, because the bzImage is read into QEMU's memory where the vmlinux was mapped (`ssh-first`'s new `qemu_rss_anon`, `qemu_rss_file` and `qemu_rss_shmem`, lab 44847770977). *Status:* in place.
