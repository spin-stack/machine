---
date: "2026-11-22"
order: 250
area: CI
title: 'the coverage floor is go-tools'' coverfloor (go-tools #131, v20261122.02, #496, this change)'
prs: [490]
---
`internal/coverfloor` (#490) copied go-tools mutate's rule for lane code, which every spin-stack repository's `task test` should read the same way. go-tools' `coverfloor` reads it through mutate's own `LaneFunctions`, and it refuses a marker that names no test, as mutate does. `task test` runs it by name from go-tools' release, and the copy is deleted. *Checked:* both tools printed the same line on main's profile (93.38% of what CI runs, 80.66% with the 1606 lane statements). The same release copies no ignored directory into mutate's copies (go-tools #132, #129) and stops a run whose importer test fails unmutated (go-tools #133, #130): the `_output` guard in `task test` stays, for a plain `go test`, with its comment saying so. *Status:* in place.
