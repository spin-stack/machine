---
date: "2026-11-22"
order: 410
area: machine
title: what a guest sends on the pressure port is bounded in time (#559, spin-stack/spin#1597, this change)
issues: [559]
---
spin's security review, adopting the probe, found the guest's bytes bounded in size (128-byte lines) and count (one connection per CID) but not in time, which spin's I7 asks for: a guest's root writing `stall memory 1 1 1` in a loop kept a host goroutine busy on the runner's CPU, a dial-and-drop loop left one goroutine and timer per connection waiting `answerWait` with one `GuestLost` each, and `hello oom=0` then `oom 18446744073709551615`, again on each connection, reset the base and wrapped the reported kills. *Changed:* each watch paces its guest's lines (64 at once, 16 a second) and connections (4 at once, 1 a second) with a token bucket, sized from the sender: a Stall at most once a 1 s window for each of three resources, an OOM line only in an OOM storm, and a dial 1 s after an end. A guest over the line pace is cut off with `ErrGuestTooFast` rather than having lines dropped and counted: the counters are totals, so its next hello and Stall lose nothing, and the end needs no new field to say what happened. A connection over the accept pace is closed and the one held stays. One `GuestLost` waits per watch, and later ends give it their error. A count or `Total` below the last heard is ignored, and `internal/pressure` saturates microseconds past a `time.Duration`. *Measured:* nothing; no guest's path changes, since the sender starts 10 s after boot. *Status:* in place.
