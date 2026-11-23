---
date: "2026-11-13"
order: 70
area: boot probes
title: branch `floor-kernel` (experiment)
runs: [43492686353, 43493135230, 43494513791]
---
The floor without systemd: PID 1 a script that mounts what sshd needs, makes an Ed25519 key and has socat hand vsock connections to `sshd -i`. First login 467 → 220 ms (lab 43492686353), 506 → 240 (lab 43493135230); guest uptime puts init at ~90 ms of kernel and the keys 10-20 ms later. What is left at the floor: QEMU and firmware ~44 ms (boot:firmware-stages, lab 43494513791: QEMU's own start 35.9, qboot ~8.5, of QEMU's start 7.9 copying vmlinux), kernel ~80-90, init ~20, the login ~44. `/proc/uptime` starts partway into the kernel, so it hides ~35 ms that an earlier reading charged to QEMU.
