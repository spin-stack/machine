---
date: "2026-11-13"
order: 130
area: image
title: udev's coldplug keeps cpu (this change)
runs: [43527839765]
---
The coldplug filtered in `630fa2c` left out cpu, and a core attached before systemd starts is onlined by that replay reaching `40-spin-hotadd.rules` and by nothing else. spin gives a workspace its cores while its own init is PID 1, so with v20261113.03 every workspace came up with one core whatever it was sized to (spin's kvm run 43527839765, four tests "still waiting for the guest to have its floor's two cores"). `task boot:hotadd` attaches a core before systemd and reads `online` after: `0` on v20261113.03's image, `0-1` with cpu in the filter. What cpu costs the first login was not measured; it is one device per core.
