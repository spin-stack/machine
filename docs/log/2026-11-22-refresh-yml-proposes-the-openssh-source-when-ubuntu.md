---
date: "2026-11-22"
order: 150
area: CI
title: refresh.yml proposes the openssh source when Ubuntu moves it (#463, this change)
issues: [463]
prs: [1]
---
The archive serves only the openssh source resolute publishes now, so image/openssh stopped building the day Ubuntu moved it, and nothing moved the pin. `spin-machine-dev refresh openssh` is `versions refresh -propose -label openssh openssh-source`, and refresh.yml's `systemd` job is now `sources`, one matrix job each for systemd and openssh. The patches are checked where the build already checks them: the proposing pull request changes `openssh-source`, so parts.yml's `changed` builds the image, and `image/openssh/Dockerfile` applies every patch with `--fuzz=0`. *Checked.* With the pin set back to 10.2p1-2ubuntu3.7, `versions refresh openssh-source` committed 10.2p1-2ubuntu3.8 with the repository's pin, 5f83862e…. A moved `openssh-source` makes `changed` answer `build=true` for the image. The patch applies with `--fuzz=0` to the pinned source as `dpkg-source -x` lays it out. With one of its context lines in auth2.c changed, `--fuzz=0` refuses it (`Hunk #1 FAILED at 360`), where patch's default fuzz would have applied it. *Status:* in place.
