---
date: "2026-11-22"
order: 210
area: CI
title: the coverage floor counts what CI can run (#410, this change)
issues: [410]
---
`task test`'s floor counted every statement, and a function marked `mutate-lane:` runs only in a lane with root and KVM. Each probe the lab gained pulled the total down however well its testable half was tested: #406 passed at 79.96%, which `go tool cover -func` rounded to 80.0 before the comparison. `internal/coverfloor` reads coverage.out, leaves out each function whose doc comment carries the marker, as go-tools' mutate does, and compares the exact percentage. *Measured* locally on `140ebbb` with CI's conditions (no release tree, no root): 80.48% before it, 80.65% of 9298 statements with internal/coverfloor's own, and 93.36% of the 7697 left without the lane's 1601. The floor moves from 80 to 93, just under it, so it ratchets. *Status:* in place.
