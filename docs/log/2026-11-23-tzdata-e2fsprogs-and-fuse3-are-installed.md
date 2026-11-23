---
date: "2026-11-23"
order: 210
area: image
title: 'tzdata, e2fsprogs and fuse3 are installed (#512, labs 45112622373, 45112622091, 45112622071, 45113946185, image jobs on #616 and #625, this change)'
issues: [512]
prs: [616, 625]
runs: [45112622071, 45112622091, 45112622373, 45113946185]
---
*Decided* (the owner, #512, option b): the three a workspace cannot work around from inside, a zone loaded by name, a disk attached to a running workspace formatted and checked, and FUSE, which the kernel has. python3, lsb-release and the smaller tools stay out. *Changed:* `image/mkosi.conf` names them; `deviations.py` counts `/etc/localtime`, tzdata's postinst link to `Etc/UTC`, as a file a maintainer script writes on every Ubuntu, so the inventory is unchanged at 118 lines. `boot:workspace` loads `Europe/Madrid` by name, asserts the default zone is still UTC, finds `fusermount3` and `/dev/fuse`, and formats, mounts, writes and fscks an empty disk the host attaches while the guest runs; all pass with the packages in an overlay of v20261123.04 (lab 45112622373). *Measured* (bench, nested, spin's shape and login, the packages installed into the release's overlay by its own apt, branch `exp-512`, tagged `archive/exp-512`): first login −2.9 [−9.7, +3.6], sshd accepting −3.4 [−8.6, +4.0], 20 a side (lab 45112622091). Restored, 20 a side: evicted −0.3 [−1.8, +1.1], cached +1.8 [+0.3, +2.9], all of it in `running`, before any guest code (lab 45112622071); at 60 a side evicted −0.9 [−3.9, +2.0], cached +0.3 [−10.4, +12.5], within noise (lab 45113946185). The boot finishes +9.8 ms later and the state is +0.8 MiB, from `e2scrub_reap.service` in `multi-user.target` after the login; the console prompt +8.5. None is a goal number. *Size:* 217 → 224 packages, the tree +5.1 MiB (987408 → 992672 KiB), the qcow2 1013587968 → 1019158528 bytes (+5.3 MiB). *Status:* in place.
