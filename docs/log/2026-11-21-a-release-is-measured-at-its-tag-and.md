---
date: "2026-11-21"
order: 70
area: CI
title: a release is measured at its tag and recorded by main (this change)
prs: [337, 350]
---
report.yml checked out the release's tag and ran `releases measure`, which wrote the record and the page with the tag's code, and `releases propose` put them on main (#337). v20261120.04's proposal (#350) failed main's TestTheCommittedPageIsTheRecords: #342 had changed `lab.Chart` after the tag, so the tag's page was not the one main writes from the same records, and it was fixed by hand with main's `releases doc` (75a3b24). Measuring stays at the tag, which boots each release with its own spin-machine and the tag's probes, and `releases measure` now only keeps the ladders. A second checkout of main builds its own spin-machine-dev, `releases record` (now taking every ladder.json) writes the records and the page, and `propose` runs there. Functional: no boot number moves.
