---
date: "2026-11-20"
order: 90
area: lab
title: the records' chart is an SVG, and the records a web page (this change)
---
The page's two Mermaid `xychart-beta` blocks drew one p50 line each and could not draw a gap: a release without a number was left off that chart's axis, so the cold and the restore charts had different x axes, and neither showed the p95. `spin-machine-dev releases doc` now writes `docs/releases.svg` beside the page, from the same code (`lab.Chart`, hand-written, no dependency): both goal numbers on one axis, each a p50 line over a band to its p95, broken where a release has not the number, the 100 ms goal dashed, a tooltip on every point. One SVG is shown on GitHub's light page and its dark one, and an image cannot follow the theme, so its two colours were chosen to clear 3:1 on both (#2a78d6, #d95926; dataviz's palette checks against #ffffff and #0d1117) with text in a mid grey (3.9:1 and 4.8:1). `releases site` writes the same page as one HTML file with the chart inline, so its tooltips show, and each record beside it. `pages.yml` publishes it at https://spin-stack.github.io/spin-machine/ from every merge that changes a record. The tables are one list of rows that the Markdown and the HTML both write, so the two cannot say different things. Each release in "Where each was measured" now links its record.
