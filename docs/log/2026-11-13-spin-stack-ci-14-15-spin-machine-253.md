---
date: "2026-11-13"
order: 80
area: CI
title: 'spin-stack/ci#14, #15, spin-machine#253'
---
A `bench-metal` fleet (c8i.metal-48xl, then r7i.metal-24xl behind it) and lab.yml's `host` input. It has not run: metal spot scored 1-3 of 10 for placement and failed `UnfulfillableCapacity` in every zone; on-demand failed `VcpuLimitExceeded` at a quota of 96 vCPU. An increase to 288 was requested the same day (case open). *Status:* the fleet first ran 11-19 (spin-stack/ci#20; below).
