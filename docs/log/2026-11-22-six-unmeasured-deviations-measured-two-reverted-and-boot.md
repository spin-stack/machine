---
date: "2026-11-22"
order: 300
area: image
title: 'six unmeasured deviations measured, two reverted, and boot:workspace (labs 45034963004, 45036254377, #508, #517, this change)'
issues: [515]
prs: [508, 517]
runs: [45034963004, 45036254377]
---
The inventory (#508) and docs/image.md (#517) left six deviations with no measurement in spin's shape on spin's login. Each was taken back to what Ubuntu ships by a `lab` edit, one arm each, interleaved against v20261122.03 with `--edit lane`, 20 a side, 1 of 16 vCPUs, 128 MiB, `prestart` (lab 45034963004). Taking back, Δp50 cold: `nsswitch.conf` +0.8 [−8.4, +5.2]; the volatile journal +6.1 [+2.3, +15.8]; docker and containerd by socket +13.9 [+11.4, +18.2], restored +55.7 [+12.0, +73.9]; the manager's timeouts +0.3 [−5.2, +3.3]; logind, the bus and the user units early +16.5 [+9.8, +20.4]; the host keys from the first dispatch −0.8 [−10.5, +3.5]. *Kept:* the journal, docker (a deferred row, which says the first `docker` command waits for the daemons), and the lane's logind units, now measured in spin's shape. *Reverted:* `nsswitch.conf` and `fast-boot.conf`, which earned nothing; their edits went with them, and so did `carry-logging`, which edited `fast-boot.conf`. The host keys stay pending #515. `boot:workspace` checks what a workspace expects and asserts what the image gives up; on v20261122.03 (lab 45036254377) it found tracefs empty under `lockdown=confidentiality`, where docs/image.md said it mounted by hand, and the first `docker` command waiting 0.47 s on two vCPUs. It runs in release.yml's bench job on the release's own image, beside `boot:logind`. *Opened:* #509-#515. *Status:* in place.
