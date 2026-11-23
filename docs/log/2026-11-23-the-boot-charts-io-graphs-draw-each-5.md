---
date: "2026-11-23"
order: 160
area: lab
title: 'the boot chart''s IO graphs draw each 5 ms on one scale (spin-stack/systemd-bootchart#11, labs 45110751133, 45113479570, #538, this change)'
runs: [45110751133, 45113479570]
---
The fork averaged 250 ms around each IO bar and scaled each graph to its own peak. A 5 ms read burst came out as a plateau over the whole chart, and the boot's one 164 KB write filled the write graph for 150 ms. The three boots of lab 45110751133 were replayed through `--proc` and drawn both ways. Unsmoothed on one scale, the ~8 MB burst at the chart's start, the later read bursts and the write each appear where they happened and at their sizes against one another. The other drawing shows none of that, so the fork now draws this one. Lab 45113479570 is the pinned fork's chart of v20261123.04, three boots. *Status:* in place.
