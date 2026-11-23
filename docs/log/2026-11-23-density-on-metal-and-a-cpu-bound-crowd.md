---
date: "2026-11-23"
order: 310
area: lab
title: density on metal, and a CPU-bound crowd on the bench (#563 section 2, labs 45110655407, 45110663095, 45110670764, 45110678888, 45110688511, this change)
issues: [563]
runs: [45110655407, 45110663095, 45110670764, 45110678888, 45110688511]
---
#273 measured an idle crowd on the bench up to 16. #563 asks what a metal host carries, for spin's metal-or-nested cost model, and needs no new code: `density` as it stands, spin's shape and login, v20261123.04. *Bench, nested, 16 CPUs, `load=cpu`, 10 a row:* the first login read 387/417 empty, 378/431 beside 4 busy workspaces, 441/450 beside 8, 1015/1215 beside 16, 941/1196 beside 24. *Metal, c8i.metal-48xl, 192 CPUs:* `load=cpu`, 337/447 empty, 331/488 at 16, 335/344 at 32, 336/341 at 48 (10 a row), and in a second run 342/363 empty, 327/366 at 64, 466/486 at 96 (8 a row; a crowd of 96 took 83 s to boot, past what 10 rows fit in bench-metal's 60 minutes). `load=idle`: 345/368, 336/343, 329/341, 334/352 at 0-48, and 342/356, 328/350, 331/340 at 0, 64 and 96. *Read* by #563's line, a p95 within 25% of the empty host's in the same run: nested, up to 8 busy workspaces of 16 CPUs; metal, up to 64 busy of 192, not 96, and every idle crowd measured. The rows of one host are two runs, so a row compares only with the empty row of its own run. The empty metal row's 447 p95 in the first run is one slow boot of ten. *Not measured:* busy crowds between 64 and 96, and workspaces of more than one vCPU. *Status:* rows in docs/boot-time.md ("The bench fleet is nested, not metal"). Section 1 of #563 (the I/O tax) needs new probes and equal storage on both hosts, and stays open in #563.
