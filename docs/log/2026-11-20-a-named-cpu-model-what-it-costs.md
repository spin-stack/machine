---
date: "2026-11-20"
order: 10
area: machine
title: a named CPU model, what it costs (#222)
issues: [222]
runs: [44657973748, 44664362024, 44664603720]
---
spin-machine can show the guest a named model the fleet shares (`CanResume`, `Models`, `Baseline`, #293). The question left was its cost. The bench host is nested, and its L0 hides features the Xeon 6 has (la57, TSX, fsrm): QEMU refuses GraniteRapids-v1, SapphireRapids-v1 and Icelake-Server-v2 there ("Host doesn't support requested features", lab 44657973748, cancelled after 50 minutes of boots each waiting out its login; `lab` now refuses such a tree at its first boot). The newest model it runs, chosen on the runner (`lab tree --cpu baseline`), is Cascadelake-Server-v5. Against `-cpu host`, 20 boots each, interleaved: the cold boot's first login 327/342 → 343/353 ms (+16.4, sshd accepts +12.6; lab 44664362024); the restore's 160/170 → 161/170 evicted and 167/188 → 165/203 cached (lab 44664603720). A fleet that takes a baseline model pays ~16 ms on every cold boot for checkpoints that cross host generations. On one generation `-cpu host` with `CanResume`'s feature check is free.
