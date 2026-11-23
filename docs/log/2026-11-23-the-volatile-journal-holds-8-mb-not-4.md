---
date: "2026-11-23"
order: 330
area: image
title: the volatile journal holds 8 MB, not 4 (#514, labs 45121159504, 45121159741, 45121159883, 45120692139, this change)
issues: [514]
runs: [45120692139, 45120950183, 45121159504, 45121159741, 45121159883]
---
*Found:* `/run` is a tmpfs of 20% of the RAM at its mount, and the guest mounts it at 128 MiB, before spin sizes it to 2 GiB: 19 MiB in spin's shape, whatever the guest grows to. Ubuntu's default cap, 10% of `/run`, would be ~2 MB there. *Measured* (bench, nested, spin's shape: 1 vCPU, 128 MiB sized to 2048 right after the start, a guest running `docker build --no-cache` of 20 `RUN` steps for 10 minutes, branch `exp-batch3`'s `boot:exp-journal`): the journal grows ~0.4 MB a minute, mostly systemd's and dockerd's lines for each build step. At 4 MB the oldest entry was the kernel's first line for 8 minutes, then the boot's first 90 s were gone by the end (lab 45121159741). At 8 MB the whole journal was kept, 3.6 MB used (lab 45121159883). A first try at 16 MB was left: it is 84% of `/run`, near journald's 15% keep-free. The first login, 8 MB against 4 MB in v20261123.04's overlay: −2.2 [−12.1, +6.4], 20 a side (lab 45121159504), and 16 MB −0.2 [−5.6, +9.1] (lab 45120950183). *Status:* in place; a busy workspace keeps ~15-20 minutes.
