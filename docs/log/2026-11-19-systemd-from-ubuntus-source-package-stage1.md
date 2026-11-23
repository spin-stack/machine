---
date: "2026-11-19"
order: 10
area: image
title: systemd from Ubuntu's source package, stage1 (this change)
issues: [261]
prs: [240]
runs: [44531842773, 44533227886, 44537549305, 44538877783]
---
The image installs Ubuntu's own systemd 259.9-0ubuntu0.1, rebuilt from its .dsc (versions.yaml `systemd-source`) by `image/systemd/Dockerfile`. The build uses Debian's `stage1` build profile (no BPF framework, audit, AppArmor, SELinux, OpenSSL, TPM2, homed, cryptsetup…) and links systemd-executor, udevd, systemctl and journalctl statically against libsystemd-shared. The packages are versioned `+spin1` and held. *Why:* PID 1 spent ~25 ms in the BPF verifier on socket-bind at every boot (#240, "measured, nothing done"), and restrict-fs failed to load under lockdown on every boot (#261). *Measured:*
- `boot:ssh-first` on the bench fleet, 20 boots of each, interleaved, first login p50 against the archive's systemd:
  - stage1: −47 (lab 44531842773), −38 (44537549305) and −47 ms (44538877783);
  - static over stage1: −10 and −1 ms, within noise. Accept to login was −7 ms in both runs.
- `boot:waits` on a laptop: PID 1's setup before the generators goes from 25.8 to 5.6 ms.
- `boot:restore-first` (new, below): unchanged. The guest's part, from running to login, is ~50 ms with either systemd (44533227886).

*Left out:* masking `systemd-udev-load-credentials` (−8, −6 ms) and `-Db_lto=true` (Ubuntu already builds with `-flto=auto`); see docs/boot-time.md, "Measured and rejected". The patch that would make restrict-fs load under lockdown, reading `s_magic` through BTF, loaded in this guest. It is for systemd upstream and is not needed here. *Cost:* a rebuild whenever Ubuntu publishes a systemd. The archive serves only the current source, so the build fails the day it moves, and `refresh:systemd` proposes the move.
