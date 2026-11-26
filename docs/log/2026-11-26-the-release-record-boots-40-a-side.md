---
date: "2026-11-26"
order: 91
area: release
title: 'the release record boots 40 a side (labs 45884961558, 45884965355, #694)'
issues: [694]
runs: [45884961558, 45884965355, 45241645340, 45819169536, 45856619196]
---
*Why:* at 20 a side the record read v20261123.07's −17 ms cold gain as −4.1 [−13.1, +2.6], within noise (report run 45241645340, #694), and v20261126.01's cold first login as +8.9 [+0.8, +16.3], a regression, while the same run's restore-first cold boots read +3.9 and +6.4 within noise (report run 45819169536). *Measured:* two A/As of v20261126.02 against itself at 120 a side on the bench, spin's shape, cut into runs of k a side. ssh-first's interval excluded 0 in 0 of 6 disjoint runs of 20 and was 13.2 ms wide, 8.9 at 40 and 7.2 at 60 (lab 45884961558). restore-first's evicted login read 0 of 6, 17.0 ms wide at 20 and 9.3 at 40 (lab 45884965355). So on a quiet host 20 a side is calibrated, and the misses are runs on a disturbed one. More boots spread a run over more of the host's time. The six release runs since v20261123.06, with restore-first's 20 cold boots pooled with ssh-first's as 40 a side, read v20261123.07 −10.2 [−14.5, −3.9], a gain, and v20261126.01 +5.1 [−0.2, +10.5], within noise. *Done:* `releases measure` boots 40 a side by default, and report.yml uses it. *Cost:* in report run 45856619196, 465 s of the 9 min 14 s step were boots that scale with it, so a release costs the bench ~8 min more, ~17 min in all (an estimate from that run). *Status:* in place. In all four A/As the second tree read slower, +1.0 to +2.2 ms, each within noise: the trees boot in one fixed order each repetition, and a newer release always boots second.
