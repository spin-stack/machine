---
date: "2026-11-23"
order: 110
area: kernel
title: '`CONFIG_HZ_250` removes 587 exits and no time from the login (#567, parts run 45110306519, labs 45111418504, 45111452844, this change)'
issues: [567]
runs: [45110306519, 45111418504, 45111452844]
---
*Why:* #565's profile counted 1043 TSC-deadline writes and 827 preemption-timer exits at HZ=1000, and #567 inferred ~3.5 ms from ~500 of them at ~7 µs a nested exit. Firecracker's x86_64 guest configs and Cloud Hypervisor's `ch_defconfig` build at 250, with no number published for it. *Measured* (bench, nested, spin's shape and login, v20261123.04 against the same release with a kernel built at `HZ_250`, branch `exp/567-hz250`): every exit −587 [−608, −445] at 3 a side, of them MSR writes 1018 → 755, preemption-timer exits 691 → 482, HLT 332 → 298 and external interrupts 396 → 360; the host's KVM handled them in −0.6 [−8.1, +7.3] ms less. The first login −0.6 [−5.9, +3.1], sshd accepting −1.4 [−7.7, +3.2] and the console prompt +4.7 [+0.4, +7.8] at 60 a side. *Why the inference missed:* a tick's MSR write and its preemption-timer exit take 1.0-1.5 µs each in the L1, against ~10 µs for an MMIO exit and ~18 for a port exit in the same profile, so ~7 µs an exit is the price of an exit handed to QEMU, not of every exit. *Status:* rejected; `HZ_1000` stays. Archived as tag `archive/exp-567-hz250`.
