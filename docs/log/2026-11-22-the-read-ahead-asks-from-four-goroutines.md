---
date: "2026-11-22"
order: 70
area: machine
title: the read-ahead asks from four goroutines (lab 44976924478, this change)
runs: [44976924478]
---
With the read-ahead before the dial, restore-first's evicted login was still 6 ms over the cached one. The read-ahead was one goroutine making a `posix_fadvise(WILLNEED)` per 128 KiB piece, 1432 of them for the state's 179 MiB. Each call allocates its pages and submits their read before it returns, on a nested host. Four goroutines now ask, each for every fourth piece of each extent. *Measured* (bench, nested, spin's shape and login, 30 a side interleaved, against the read-ahead before the dial, lab 44976924478): evicted login 102 → 96.2, −6.2 [−9.1, −4.3], `running` −6.1 [−7.2, −4.8]. The cached row read −0.5 [−1.6, +1.9]. The evicted row is now level with the cached one (96.2 against 96.3), so the one goroutine asking was the limit and not the disk, and the restore has nothing left to win from reading. A test now holds a 64 KiB hole between two extents: a goroutine's last stride can end past it, inside the next extent, and without the reset to the extent's end over a third of that extent went unread. *Status:* in place.
