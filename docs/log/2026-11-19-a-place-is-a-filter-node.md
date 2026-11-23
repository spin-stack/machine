---
date: "2026-11-19"
order: 100
area: machine
title: a place is a filter node (this change, functional)
---
A place's node was raw over the bound disk's top image. spin's storage compacts a chain by rewriting a bound image's backing file (`change-backing-file`), which QEMU takes only on a root node whose chain, followed by backing and filter links, contains the image: on the root it answered "'slot0' and image file are not in the same chain", on the image "Need a root block node" (QEMU 11.2.0-rc0). A place is now a `throttle` node in a group with no limits: a filter, so the chain is seen through it, and pass-through. copy-on-read, compress and preallocate are filters too, and each changes what is written. Checked on QEMU 11.2.0-rc0: bind by `blockdev-reopen`, then `change-backing-file` through the place, then a seal under an overlay. The fingerprint does not move (`-object` is not in `Contract`). Its cost per I/O is the bench's to say.
