---
date: "2026-11-23"
order: 200
area: machine
title: 'what SetMemory down and one vCPU cost (labs 45113957566, 45117940800, #549, this change)'
runs: [45113957566, 45117940800]
---
`boot:actuators` boots spin's shape to systemd at 2048 MiB and times each actuator until the guest shows it, 10 times each. Its first reading counted the 128 MiB memory blocks online, and 19 of 20 unplugs to 1024 MiB stood at 1536 MiB for the whole 2 minutes. virtio-mem's own log said QEMU had all 1024 MiB back. The driver takes back its smaller blocks from inside a memory block, and the block stays online until all of it is gone. So the check now reads MemTotal. *Bench, spin's shape:* SetMemory 2048 → 1024 MiB 21.2 ms idle and 53.7 with 1180 MiB of page cache; back up 9.1; a vCPU added 13.2 and removed 14.8. docs/machine.md's actuators table has them. *Status:* in place.
