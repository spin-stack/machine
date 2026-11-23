---
date: "2026-11-21"
order: 90
area: machine/image
title: what earned nothing in spin's shape, deleted (lab 44818953987)
runs: [44818953987]
---
The first decisions of the carry ablation (docs/boot-time.md, "What the machine carries"; #349). `driver_async_probe=` for rng, balloon and vsock (11-13, 2 vCPUs, inside the noise at the floor) and `rcupdate.rcu_expedited=1` (09-07, never measured) leave the command line: taken out together, on the bench in spin's shape, 20 boots each against v20261120.04, the first login read −1.4 [−4.9, +3.1] ms cold, +1.4 restored and +1.7 into a QEMU started ahead. With one vCPU an asynchronous probe has no second CPU to run beside. The command line is not in `Spec.Contract`, so the fingerprint does not move. And 16 masks in optimize-systemd.sh named units no package installs (timesyncd, pcrmachine, both tpm2-setups and the tpm2 generator of a systemd built without TPM2, boot-system-token, avahi, cups, bluetooth, smartd, both e2scrubs, both multipathds, lvm2-monitor, run-lock.mount): deleted without a run, and image/build.sh now fails a mask that shadows nothing. *Status:* in place.
