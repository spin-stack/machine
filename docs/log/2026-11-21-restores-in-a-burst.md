---
date: "2026-11-21"
order: 30
area: lab
title: restores in a burst (lab 44792429636)
runs: [44792429636]
---
Beside picking 8 channels (above), `restore-together` showed that 8 restores let go at once take ~330-400 ms each to the first login at any channel count, against ~100 alone and ~140 four at once. Not explained (docs/boot-time.md, "Open experiments").
