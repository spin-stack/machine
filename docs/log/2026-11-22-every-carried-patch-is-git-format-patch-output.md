---
date: "2026-11-22"
order: 230
area: kernel, qemu
title: every carried patch is git format-patch output (#487, this change)
issues: [487, 488]
prs: [478]
---
Three hygiene items found while writing #478, none of which changes what a patch does. Kernel 0003 left `eval_map_work` and its `INIT_WORK` behind after `trace_eval_init()` came to call the copy directly, and its allocation-failure branch did what the success path did: the copy (now `trace_eval_defer()`) runs once, first, and `trace_init_wq` stays for `tracerfs_init_work`. Subjects said `[PATCH 7/11]` and the like after 0006, 0009 and 0011 were deleted; they say `[PATCH]`. Kernel 0007, 0008, 0010 and QEMU 0002, 0004 and 0005 had no `diff --git` lines (#488 had just remade QEMU 0003); all thirteen were remade with `git format-patch -N` from git trees of the pinned tarballs. *Checked* on the laptop, no build: each set, applied with `patch -p1 --forward --fuzz=0` as the builds do, gives the tree the series' commits do; the old and new kernel sets differ only in 0003's trace.c; every changelog is unchanged; checkpatch --strict reads the kernel patches clean but for 0002's citation of a commit the tarball has no history for. `gates/patches_test.go` holds both directories to the format. CI rebuilt the kernel and QEMU, and the vmlinux check still finds each patch. *Status:* in place.
