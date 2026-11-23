---
date: "2026-11-20"
order: 70
area: CI
title: qemu.yml, kernel.yml and image.yml are parts.yml (this change)
prs: [309]
---
Three workflows of the same shape, each a path-filtered `changed` job and build, and none of them a required check, since a path-filtered workflow does not report on a change outside its paths: a pull request could merge with its QEMU build red (#309's was not blocking). One workflow now, with no path filter: `changed` decides for each part, the builds run when it says so, and `parts` (`spin-machine-dev needs`) fails when one that ran did not pass, which gives a ruleset one check to require. A change that reaches all three runs one `changed` where it ran three (each a billed minute). Over the 200 runs before it (gha-optimize, 2026-11-20): build p50 1m01s cached, cold 10-22 min; `changed` 20 s. In place.
