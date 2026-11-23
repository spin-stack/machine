---
date: "2026-11-23"
order: 230
area: image
title: the update stamps written at build, the five units of `ConditionNeedsUpdate` unmasked (#513, labs 45120950183, 45120950314, 45120950141, this change)
issues: [509, 512, 513]
runs: [45120950141, 45120950183, 45120950314]
---
The image had no `/etc/.updated` or `/var/.updated`, so `ldconfig`, `systemd-sysusers`, `systemd-hwdb-update`, `systemd-journal-catalog-update` and `systemd-update-done` were masked: without the stamps they rebuild the caches in every boot (+260 ms, laptop, 11-21). *Changed:* `image/build.sh` writes both stamps last, with `/usr`'s mtime and `TIMESTAMP_NSEC=`, as `systemd-update-done` does, and fails an image whose stamps' mtime is not `/usr`'s; the five masks are gone from optimize-systemd.sh; `boot:workspace` asserts each unit checked its condition and skipped. `/usr`'s mtime in the release is 0 (`SOURCE_DATE_EPOCH`), which first failed the arm's setup through `expr`'s exit status on a result of 0. *Measured* (bench, nested, spin's shape and login, against v20261123.04 with the stamps and the unmasks in its overlay, branch `exp-batch3`): first login −0.3 [−6.8, +4.5], sshd accepting −1.3 [−7.7, +1.1], 20 a side (lab 45120950183); restored +0.2 [−2.1, +0.9] evicted, −1.3 [−4.5, −0.4] cached, cold login +1.2 [−2.6, +7.3] (lab 45120950314); `boot:workspace` on the overlay: the five units' conditions false and no unit failed (lab 45120950141, whose other failures are #509's and #512's checks, which v20261123.04 predates). The overlay measures the stamps and unmasks as files of the overlay, not of the base, which only changes where their few blocks are read from. *Status:* in place, a functional change: a `/usr` change made later is applied at the next boot.
