---
date: "2026-11-26"
order: 93
area: release
title: 'the release record back to 20 a side, for the 250 ms goal (#809, #694)'
issues: [809, 694]
runs: [45884961558, 45884965355]
---
*Why:* the goal is now under 250 ms to the first login on a machine with a TPM and shirudo, with stability, security and scalability before speed (#809). Earlier the same day the record went to 40 a side so that a run on a disturbed host would still read effects of 9 to 17 ms right (#694). The new goal does not weigh those. *Measured:* at 20 a side a release's cold Δ is ~13 ms wide on a quiet host, and the restored login's ~17 (A/As at 120 a side, labs 45884961558 and 45884965355). That settles the 250 ms line, and any regression worth acting on against it. *Done:* `releases measure` boots 20 a side again, saving the bench ~8 min a release. *Status:* in place. Another change moves the probes to the TPM and shirudo machine.
