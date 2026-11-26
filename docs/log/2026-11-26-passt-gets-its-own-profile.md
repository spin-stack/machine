---
date: "2026-11-26"
order: 30
area: machine
title: passt gets its own AppArmor profile (#755)
issues: [755]
runs: [45798835676]
---
*Why:* passt sandboxes itself in a user namespace, and a host whose AppArmor refuses one to an unprivileged process, as the CI runners' Ubuntu 26.04 image does even to `unshare --user` (run 45798835676), left a reader's `spin-machine boot --net` without a network unless an administrator lifted `kernel.apparmor_restrict_unprivileged_userns` for the whole host. compat.yml did exactly that (`task userns`). *Done:* `spin-machine apparmor --release DIR` prints passt's own profile, its `contrib/apparmor/usr.bin.passt` with `abstractions/passt` inlined at the pinned 2026_10_02.cba3570, attached to `DIR/bin/passt` alone by its resolved path, and narrowed to passt as Start runs it: no pcap, PID or log file writes, no libvirt paths, no `passt.avx2` exec. A path a profile may not read as itself is refused, so no glob ever attaches. docs/machine.md gives the reader the two commands; compat.yml installs it with them (`task boot:passt-apparmor`) and leaves the restriction on, and `task userns` is gone. boot:net now asks passt itself whether it can sandbox, and a refusal names the commands. *Seen:* laptop, Ubuntu 26.04 with both restrictions at 1: under the profile passt ran as `spin-machine-passt (enforce)` and boot:net passed with no denial; the same profile without `userns,` had the kernel deny `userns_create` and boot:net stop at its check with passt's `Unable to create user namespace: Permission denied`. *Status:* a host that refuses needs root once, per release path.
