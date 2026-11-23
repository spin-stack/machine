---
date: "2026-11-13"
order: 120
area: boot/qemu
title: branch `first-login-report` (this change)
prs: [250]
runs: [43524334205, 43524721490]
---
v20261113.03's report read +11-18% on every row: the report times the serial console's prompt, and the getty drop-in removed in #250 left it Type=idle, waiting for the lane's units. The first login it does not time went from 478/500 (v20261113.02, lab 43524334205) to 381/410 ms (v20261113.03, lab 43524721490), two runs and not interleaved. The report now logs into the release over SSH too (`first_login`), and compare writes it first. The QEMU build asserts static-pie, full RELRO, an NX stack and stack canaries, which come from QEMU's and Alpine's defaults and no flag here. Measured and left out: one sshd for every connection (the vsock socket with Accept=no, an `sshd -D` from the first dispatch), first login 382/393 against 381/410 and a second 40 against 48 ms (lab 43524721490).
