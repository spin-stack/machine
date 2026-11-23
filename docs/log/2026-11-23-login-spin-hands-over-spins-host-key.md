---
date: "2026-11-23"
order: 10
area: lab
title: '`login=spin` hands over spin''s host key (labs 45088516505, 45088516571, 45088516492, #575, this change)'
issues: [515]
prs: [573]
runs: [45088516492, 45088516505, 45088516571]
---
Since #573 the image's host-key generation steps aside when spin's handoff has placed spin's key, and spin's vminitd always does. `login=spin` did not, so every probe on spin's path paid the keygen spin's own path skips, ~7 ms (#515's −6.9). *Decided* (the maintainer, #575's first option): the handoff is part of `login=spin`. Its overlay carries the drop-in naming `/run/spin/ssh/ssh_host_ed25519_key` sshd's only `HostKey` and a fresh key per boot, made on the host, and every boot on spin's path runs a shell as PID 1 that places the key and execs systemd (`Key.Init`). The `spin-handoff` edit is gone, and a tree's own `--append init=` still comes after the handoff's. *The stand-in's cost:* the shell runs three execs (two mounts and a `mkdir`) and writes the key with builtins. The ledger gives it a window of its own, `handoff`: 6.3, 7.3 and 7.9 ms from its exec to systemd's in three boots (lab 45088516505, bench, spin's shape, v20261123.01 on its traceable kernel). Of the 7.3, 3.3 ms was idle on cold reads of the shell, `mount` and their libraries, 2.7 the mounts and the `mkdir`, and 1.1 the shell. systemd mounts `/proc` and `/run` itself when no one has, so part of that is work moved, not added. vminitd does more before systemd on spin's path, and the lab measures none of it. *What moves:* a release that skips the keygen now reads it on spin's path. v20261123.01 does not carry #573 yet, so the first release that does will read ~7 ms against the one before it, from the image, not from the probe. Every probe ran with the handoff: ssh-first on v20261122.07 and v20261123.01, 20 a side, first login 430 and 416 (lab 45088516571), restore-first on v20261123.01, restored 82.3 evicted (lab 45088516492). *Status:* in place.
