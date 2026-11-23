---
date: "2026-11-20"
order: 60
area: CI
title: the builders are images, and the apk locks are gone (this change)
---
The daily apk-lock refresh (`c6c29b5`) kept the locks buildable, at a price: Alpine's mirror serves only the newest build of each package, so a lock moved every two or three days (11-09, two on 11-12, 11-19, #308 on 11-20), and the lock is the first layer of QEMU's build, so each move compiled QEMU cold on its pull request (17-21 min) and again on main (12 min), since only main exports the cache. #308 was libxml2 r2 → r3, which nothing links. QEMU's and e2fsprogs' builders are now images, Alpine with the part's apk.list installed (`builder/Dockerfile`), published to ghcr and pinned by digest (`qemu-builder`, `e2fsprogs-builder`). An image keeps its packages after the mirror drops them, so nothing forces a move: refresh.yml makes them again weekly and proposes the ones whose packages moved, with the list of what did. The tag is a hash of the Alpine, the list and every package installed, so it never names two images. A part's build stops at its first step when the tree's apk.list is not the builder's. In place.
