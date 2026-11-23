---
date: "2026-11-19"
order: 60
area: image/machine
title: the root mount's storage path (branch `ext4-mount`, not yet on the fleet)
prs: [286]
---
The kernel gap `security_initcall_late_sync` → `EXT4-fs (vda): mounted` was 6.8 ms nested and 5.0 metal (`boot:initcalls`). Three changes, from reading Linux 7.3 and the image:
- the image without `orphan_file`, a mke2fs.conf default since 1.47.0: every mount, read-only included, reads it one block at a time (`ext4_init_orphan_info`, ~73 single-block reads with no readahead). Crash correctness is the classic orphan list's;
- `lazy_itable_init=0`: `mke2fs -d` left no group `ITABLE_ZEROED`, so each overlay's first read-write mount had `ext4lazyinit` zero every inode table in the first second of the boot. After one `--init /bin/sh` boot the overlay held 16.06 MiB of zero clusters, and none with the tables zeroed at build. The thread still starts, to prefetch block bitmaps (it does not with `no_prefetch_block_bitmaps`). The image is 256 KiB smaller;
- `rootfstype=ext4`: without it the root mount tries ext3, then ext2 (`EXT4_USE_FOR_EXT2`), on the superblock before ext4.

Laptop lead only, on a host at load ~30: the gap's p50 over three alternated rounds of `TestKernelInitcalls` (8-10 boots each) was 8.8/2.8/4.3 ms with the release, 4.1/3.2/2.9 with `rootfstype` alone, and under 2.4 (below the rows printed)/1.3/1.2 with all three. Not decided until `boot:ssh-first` and `boot:restore-first` run interleaved on the bench fleet. *Status:* merged 11-19 (#286) and in place; `rootfstype` and `^orphan_file` are still leads, not measured on the bench (docs/boot-time.md, "What the machine carries").
