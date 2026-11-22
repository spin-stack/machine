# What a guest's memory and disk cost its host

A measurement record: what this machine does with a guest's page cache, its freed memory and
its disk, what was measured to decide each default, and what was tried and turned down. Unless
a section names another host, every number here was taken on construct between 2026-09-30 and
2026-10-01, through `.github/workflows/lab.yml` and the probes in `boot/`: `boot/pagecache_test.go` and
`boot/report_test.go`, both since deleted (c3c0fd1, 843c47e) and in git history. Construct is the KVM runner: Spin
OS on metal, with two WD Red SN700 NVMe disks. A number with no run beside it came from the run
named at the top of its section.

Decisions that span spin as well as this machine, such as the guest's reclaim service, the
qcow2 layer format and the host's dirty-page limit, are recorded in spin's
`docs/decisions/ADR-0028-sharing-a-hosts-memory-and-disk.md`. This file records what the
machine contributes and how it was measured.

## The defaults, and why

| Default | Why (measured) | Where |
|---|---|---|
| The overlay is opened writeback; the host keeps a copy of what the guest wrote | Without the host's copy, 16 384 small files read in random order take 1490 ms instead of 530 | Every image is opened through the host's page cache (`machine/disks.go`, `addImages`) |
| `fstrim.timer` hourly in the image, up to 10 minutes' random delay | A trim takes the overlay from 657 to 21 MB on disk and from 657 to 20 MB in the host's cache; the distribution's weekly timer returned it days later | #76, `image/mkosi.extra/usr/local/lib/spin-base/configure-system.sh` |
| QEMU counts virtio-blk discards | Without upstream's 331ce936, `query-blockstats` reports 0 unmap operations however much a guest trims | #79 carried it until QEMU 11.2.0-rc0 shipped it (3cafd28) |
| qboot programs the MTRRs, write-back by default | Without MTRRs the guest maps device memory uncached-minus: a virtio-pmem region read at 8.3 MB/s, 1.1 GB/s with them | #75, `qemu/qboot/mtrr.patch` |
| `page_reporting_order` stays at the kernel's 9 | A lower order gives ~100 MB more back per VM, briefly, and costs ~180 MB of the guest's RAM its huge pages for good | #83, below |
| The release's boot report boots each row 20 times, KVM only | At 3 boots a row the report flagged every KVM row 5-11% slower between two releases that 20 alternating boots put within 5 ms | #81; today `spin-machine-dev lab ladder`, 20 boots a row by default |

## What the host must set

**`/sys/kernel/mm/transparent_hugepage/defrag` = `defer`.** QEMU marks guest RAM `MADV_HUGEPAGE`,
and under the kernel's and Ubuntu's default, `madvise`, a fault that finds no free 2 MiB block
compacts and reclaims before it returns. One restore finds the blocks it needs; several at once, on
a host that has run a while, fight over them. `defer` lets the fault take 4 KiB pages and leaves
the compaction to kcompactd. On the bench (lab 44810128678, 2026-11-21, spin's shape, 30 bursts):
one restore alone to the first login 99.6 → 99.5 ms, Δ −0.1 [−2.0, +1.6]; eight at once from the
disk 283 → 239, Δ −44.1 [−53.2, −35.2]; eight from the page cache 268 → 193, Δ −74.9
[−83.2, −64.9]. spin restores workspaces concurrently and does not set the policy, so the host's
setup must set it (`echo defer > /sys/kernel/mm/transparent_hugepage/defrag` at boot, or a tmpfiles.d
`w` line). `docs/log.md`, 11-21, has the run.

## The host's copy of the guest's disk

With the overlay writeback, what the guest writes is also in the host's page cache. That copy
earns its memory: a small read that misses the guest's cache finds it there. A 2048 MB guest
and a 512 MB file, p50 of 5 boots (#57-#59):

| | writeback | overlay O_DIRECT |
|---|---|---|
| The overlay in the host's cache after the guest deletes the file | 657 MB | 2 MB |
| Writing 512 MB | 440 ms | 330 ms |
| Reading 512 MB again, sequentially | 90 ms | 90 ms |
| 16 384 files of 8 KiB, random order | 530 ms | 1490 ms |
| QEMU's RSS: idle, cache full, after `drop_caches` | 208 / 740 / 386 MB | 206 / 744 / 392 MB |

Firecracker, Lambda and Modal keep the host's cache on purpose for the same reason. What
bounds it is spin's `memory.max` per machine, the guest's ceiling plus 768 MiB (spin's
`internal/runner/qemu/bounds.go`). A
`memory.high` of 1280 MB held the overlay's share of the host's cache near 430 MB with small
reads unchanged (run 36803912357). `memory.max` already bounds it well enough, so no second
limit was added.

## How a guest's memory grows and shrinks

A machine with a ceiling above its boot memory gets a virtio-mem device over the difference,
with nothing plugged (`requested-size=0`, `appendDevices` in `machine/spec.go`). The machine
sets no block size, so QEMU uses its default, the host's THP size: 2 MiB on x86-64
(`virtio_mem_default_block_size` in QEMU's `hw/virtio/virtio-mem.c`). The guest onlines what
is plugged as it arrives (`memhp_default_state=online`, `machine/cmdline.go`). The balloon is
there for free page reporting and `deflate-on-oom`. spin reads the guest's memory statistics
from it every second and never inflates it.

spin decides the size. The numbers below are spin's main at d4f62bd (read 2026-11-21):

- A workspace boots with 128 MiB (`canonicalMemory`, `internal/runner/vmm/vmm.go`) and may
  grow to the runner's ceiling, 8 GiB unless the runner or the workspace sets a lower one.
- Every 10 s spin reads the machine's host cgroup (`memory.current` plus `memory.swap.current`)
  as a share of the memory the guest holds. That cgroup also holds QEMU's own memory and the
  host's cache of the overlay. The guest tells the host, too, when it stalls for
  memory, 150 ms in a 1 s window of PSI (`internal/guest/supervisor/memorypressure`), and spin
  answers that notice without waiting for the next reading (`internal/runner/hotplug/pressure.go`).
- It plugs when the share is 85% or more with less than 384 MiB free, or on a stall notice, at
  most once every 10 s. It plugs the whole deficit in one request: the larger of what is used
  plus 896 MiB, what is used over 85%, and one more 128 MiB step, rounded up to 128 MiB and held
  under the ceiling. What it plugs is taken from the host's memory budget, halved while the
  budget refuses it, down to one step. Six readings short at the ceiling report the workspace
  out of memory (`internal/runner/hotplug/memory.go`, `controller.go`).
- It unplugs after six readings in a row under 60%, at most once a minute, and only when 384 MiB
  would stay free one step down. The new size is one 128 MiB step down, or the size the plug
  rule above would ask for when that is smaller, and never under the boot memory. spin then
  asks for no more than the guest's `MemAvailable` beyond the larger of 128 MiB and a tenth of
  its memory, rounded to the block size (`shrinkFloor` in spin's
  `internal/runner/qemu/qmp/memory.go`). virtio-mem takes back only the blocks the guest can
  empty.

What the guest gives back inside the memory it keeps is free page reporting's, below.

## Giving the guest's cache back

A guest keeps in its page cache what it read once, and QEMU's resident memory with it: free
page reporting hands the host only what the guest has freed. Something in the guest has to
free it. `memory.reclaim` on the guest's root cgroup frees it and keeps the working set. The
probe read a working set (16 384 files of 8 KiB) every 5 s for 40 s beside a 512 MB file read
once, then ran the reclaimer (run 36809694419, p50 of 5, repeated in 36813445460 and
36817108384).

| Reclaimer | QEMU's RSS | The guest's cache | The working set read again |
|---|---|---|---|
| Nothing | 905 → 892 MB | 673 MB | 120 ms |
| `memory.reclaim` 512M at the end | 910 → 394 MB | 161 MB | 150 ms |
| DAMON_RECLAIM (idle 10 s, default quotas) | 904 → 748 MB | 296 MB | 260 ms (310 on repeat) |
| MGLRU `lru_gen`: age every memcg, evict the older generations | 906 → 394 MB | 34 MB | 440 ms |

DAMON is slower and takes part of the working set. MGLRU's eviction, as driven here, takes
everything older than the last aging, working set included. A first `lru_gen` attempt did
nothing at all. The kernel charges the cache to the memcg of the service that read it, and an
`lru_gen` command acts on one memcg, while `memory.reclaim` on the root walks the hierarchy.
spin's guest runs `memory.reclaim`, and not as the probe did. Its `spin-reclaim.service`
(spin's `deploy/guest/extension/supervisor/units/`) runs `spin-supervisor reclaim`, at nice 19
and on the idle scheduler. Every 10 s it reads the guest's `/proc/pressure/memory` and
`/proc/meminfo`. While `some avg10` stays under 0.1%, it asks the root cgroup's `memory.reclaim`
for the smaller of 2% of `MemTotal` and the file cache above a sixteenth of `MemTotal`. Under a
stall it asks for nothing until the stall ends (spin's `internal/guest/supervisor/reclaim/reclaim.go`,
spin's main at d4f62bd, read 2026-11-21). The table measured one request of 512M. The stepped reclaim has not
been measured here.

### What stays after the guest frees it

After the guest drops its cache, QEMU stays ~200 MB above where it booted. Outside the guest's
RAM mapping, QEMU has a constant ~49 MB resident, and most of it is not QEMU's own. 34.7 MB is
the kernel image QEMU maps, a file whose pages every machine on the host shares. 7.4 MB is
QEMU's binary, shared the same way. The other 6.9 MB is anonymous memory, and that is what one
more machine adds (run 43564203014, on the bench fleet, 2026-11-14, the probe's note at its idle
step). That run booted `vmlinux` through PVH, and QEMU's ELF loader maps the file
(`g_mapped_file_new_from_fd` in `include/hw/elf_ops.h.inc`). Since 2026-11-19 the machine boots
`kernel/bzImage` (`machine/release.go`). For a bzImage QEMU's `x86_load_linux`
(`hw/i386/x86-common.c`) reads the file into memory it allocates and keeps for fw_cfg, so the
kernel is now anonymous memory in every QEMU, the bzImage's size each, and no longer a file the
machines share. Measured on v20261120.04 in spin's shape after the second login, QEMU's mapped
files are 6.6 MiB resident (`RssFile`) against the 42 MB above, and its anonymous memory, guest RAM
included, is 225 MiB (`RssAnon`, `RssShmem` 0; `ssh-first`, lab 44847770977, 2026-11-21, 10
boots). The split of that anonymous memory between the guest's RAM and QEMU's own is not
measured.
The rest of the ~200 MB is guest RAM that the guest freed and free page reporting has not
handed back. Reporting returns free blocks of `page_reporting_order` and up (9, or 2 MiB, by
default), and what `memory.reclaim` frees is scattered (runs 36879800428, 36881456066,
36931619739, p50 of 3).

| `page_reporting_order` | Guest RAM on the host a minute after the drop (idle: 158 MB) | QEMU CPU that minute | RAM backed by huge pages after the guest uses 768 MB again | Using 768 MB again |
|---|---|---|---|---|
| 9 (default) | 346 MB | +100 ms | 968 of 968 MB | 250 ms |
| 3 | 243 MB | +130 ms | 794 of 960 MB | 290 ms |
| 0 | 238 MB | +120 ms | 778 of 961 MB | 290 ms |

A lower order gives about 100 MB more back per VM, and only until the guest uses that memory
again; order 3 already gets all of it. In exchange about 180 MB of the guest's RAM ends up on
4 KiB pages on the host and stays there, and every later access to it pays for that. The
order stays at 9. It is worth measuring again only if memory per host becomes the limit, and
then under a real workload. The `reclaim` probe in `boot/pagecache_test.go` had the columns,
and c3c0fd1 deleted it (2026-11-19).

## Trimming: what a deleted file costs the host

`discard=unmap` reaches qcow2 end to end: a trim after a delete takes the overlay from 657 to
21 MB allocated and from 657 to 20 MB in the host's cache, in every variant (run 36815822140).
Two things made it look broken first:

- `fstrim` run right after `rm` trims nothing of the deleted file. ext4 has not committed the
  freed extents yet, and it skips busy extents. The probe runs `sync` first.
- QEMU 11.1.1's virtio-blk never starts accounting for a DISCARD, so `query-blockstats`
  reported 0 unmap operations beside 1.1 GiB trimmed. Upstream fixed it in 331ce936
  (2026-09-11). This tree carried the fix (#79) until QEMU 11.2.0-rc0
  shipped it (3cafd28, 2026-11-19). With it, the caches probe counted 10-12 operations and
  1123 MB (run 36884702758), and it failed when a trim went uncounted.

A guest trims when its `fstrim.timer` fires, which the image makes hourly (#76).

## First writes onto the base

With 64 KiB clusters, a guest's first 4 KiB write onto data in the base copies a whole cluster
into the overlay. 2000 random 4 KiB `O_DIRECT` writes into a 106 MB file of the base, p50 of 5
(run 36813023345):

| Overlay | Overlay growth, and its share of the host's cache | First pass | Second pass |
|---|---|---|---|
| 64 KiB clusters | 82 MB | 3120 ms | 3040 ms |
| 128 KiB clusters, 4 KiB subclusters (`extended_l2=on`) | 13 MB | 3090 ms | 3030 ms |

The time is one `dd` process per write and hides the copy's latency. The measured gain is space
and cache. spin makes every layer this way (spin #220, `LayerOptions` in its
`internal/storage/qcow/chain.go`).

## Device memory and the MTRRs

A guest maps device memory it asks for as write-back only where the MTRRs allow it. qboot left
them disabled ("MTRRs disabled by BIOS"), so Linux mapped such memory uncached-minus through
PAT, and every access to it went to memory uncached. virtio-pmem found it: its region read at
8.3 MB/s whatever QEMU's mapping (runs 36811489671, 36814547075, 36816626039). The region was
RAM in a KVM memslot, and the guest's PAT entry for it was `uncached-minus` (run 36818224412).
Under SeaBIOS, which programs the MTRRs, it read at 1.1 GB/s (run 36818721809). The MTRRs
matter for any device memory a guest maps write-back (pmem, a virtio-fs DAX window), not for
RAM. qboot now programs them (#75): write-back by default, with the 32-bit PCI hole uncached.
No boot cost was measured (kernel and init 103.5 vs 103.1 ms over 20 boots).

## Writing a checkpoint's state

`QEMU.Checkpoint` writes the state through QEMU and syncs nothing. QEMU does not sync it either:
a trace of a save showed no `fsync` or `fdatasync` from any QEMU thread (QEMU 11.2.0-rc0). The
caller syncs it, and the question is where. spin synced it inside the seal, where the guest is
stopped, and also every 100 ms while QEMU wrote, so that less was left for the seal. Its reason
was that creating the seal's file waits for every dirty page of the filesystem.

Measured on a laptop (ext4 on NVMe, 99% full and shared with other work, 2026-11-21). The machine
was spin's shape, its guest plugged to 2 GiB with 1.5 GiB of tmpfs filled from `/dev/urandom`,
so each save wrote 1.6 GiB with mapped-ram and 8 channels. The seal made a 64 KiB file and synced
it. The arms were interleaved on one guest, 16 saves each. About a third of saves in every arm
stalled for seconds on the disk. The medians below are over the saves that did not stall:

| The state synced | Sync in the pause | Guest stopped, seal to `cont` | QEMU's total | Durable |
| --- | --- | --- | --- | --- |
| inside the seal | 275 ms | 683 ms | 728 ms | 963 ms |
| every 100 ms, then inside the seal | 167 | 566 | 668 | 877 |
| after `cont` | 0 | 506 | 492 | 738 |

Over every save, stalls included, a sync after `cont` took the pause's sync from the periodic
arm's down by 183 ms [−1676, −160] and QEMU's total by 232 [−2027, −124]. The guest's whole pause
does not resolve over every save, because the stalls are seconds long and hit every arm. The
sync after `cont` took 2-3 ms: the kernel had written most of the state while QEMU wrote it. The
seal's file took 2-7 ms with ~830 MiB dirty, so on ext4 it did not wait for the state's pages.

So the periodic sync buys a shorter sync inside the pause, and a sync outside the pause buys
more, with nothing to stop. Neither belongs in `Checkpoint`. The caller syncs after it has
continued the guest, before it records the checkpoint. A filesystem where creating a file does
wait on another file's dirty pages would need this measured on it.

QEMU's downtime (`Cost.Paused`) ends when the migration completes. In every arm the caller's
clock read 200-250 ms more from the seal to `cont`'s return, the sync aside. That was ext4
writing the state back on the file's last close, which was Checkpoint's own, before it returned:
QEMU truncates the file it writes, and ext4's `auto_da_alloc` flushes such a file when it is
closed. Checkpoint now leaves that close to a goroutine, and the gap is 6 ms p50
(docs/machine.md, "Phases", #399). The flush is then in the caller's sync after `Continue`.

### How long the guest is stopped

The guest is stopped from QEMU's stop to `cont`'s return. QEMU decides when to stop it. Every
iteration it compares what is still to write with `downtime-limit` times the rate it measured over
the last 100 ms (`migration_update_counters` and `migration_iteration_run` in QEMU 11.2's
`migration/migration.c`). Mapped-ram and multifd do not change that test. pause-before-switchover
puts the seal after the stop, and does not change it either. Whatever is left when QEMU stops the
guest is written with the guest stopped.

The machine never set `downtime-limit`, so QEMU used its default of 300 ms. A save into the page
cache measures 30-34 Gbit/s, so 300 ms let QEMU stop the guest with up to ~1.2 GB to go. An idle
guest stopped part-way through the first pass, before QEMU had read half its memory once. What it
wrote with the guest stopped was not memory the guest had dirtied. It was memory QEMU had not yet
reached. Two dirty syncs ran in every save, and QEMU's `expected-downtime` at the stop read
220-240 ms.

Measured on the bench fleet (nested KVM, v20261121.01, spin's shape plugged to 2 GiB, mapped-ram
with 8 channels, a seal that does nothing, 20 saves an arm interleaved on one guest, lab
44940051596, 2026-11-21). The idle guest had
1.5 GiB of tmpfs filled from `/dev/urandom`, so each save wrote 1.6 GiB. The writer case had
1 GiB filled and a loop rewriting 64 MiB of a 512 MiB tmpfs file every ~100 ms:

| Guest | `downtime-limit` | Left at the stop | QEMU's downtime | Stop to `cont` returned | QEMU's total | RAM written |
| --- | --- | --- | --- | --- | --- | --- |
| idle | 300 ms | 835 MiB | 192 ms | 199 ms | 460 ms | 1656 MiB |
| idle | 30 ms | 0 | 6 | 14 | 463 | 1656 |
| writer | 300 ms | 873 MiB | 221 ms | 231 ms | 490 ms | 1724 MiB |
| writer | 30 ms | 6 MiB | 8 | 18 | 528 | 1919 |

QEMU's downtime fell 186 ms [−189, −184] for the idle guest and 213 ms [−218, −208] with the
writer. The idle save's total did not move, +4 ms [−2, +6]. With the writer it rose 39 ms [+33, +45],
because QEMU ran one more pass and wrote 195 MiB more while the guest ran. A laptop showed the same
thing (ext4 on NVMe, 16-20 saves an arm): 238 ms to 12 for the idle guest and 209 ms to 11 with
the writer. A writer rewriting 256 MiB of a 1 GiB set with no pause still converged at 30 ms in
all 10 saves, after 4 dirty syncs, with a 23 ms downtime against 229 ms (laptop).

So `Checkpoint` sets `downtime-limit` to 30 ms (`checkpointDowntime`). The cost is a save that
runs longer while the guest runs, when the guest dirties memory. The state is the same file, and
`Resume` reads it as before.

`task boot:checkpoint-pause` (lab.yml's `check:checkpoint-pause`) takes the same two guests again
at the machine's 30 ms, 10 saves each, and holds the median pause under 50 ms. compat.yml runs it on
every change to QEMU and before every release, so a QEMU that stops the guest earlier fails there.
The pause it holds is the larger of QEMU's downtime and the guest's stop to `GuestRunning`, because
QEMU stops the guest before the host hears of it. Its first bench run (lab 44944984313,
v20261121.01, `lab dispatch check:checkpoint-pause`) read:

| Guest | Stop to `cont` returned, p50 | slowest | QEMU's downtime, p50 | Left at the stop, p50 | QEMU's total, p50 |
| --- | --- | --- | --- | --- | --- |
| idle | 13.3 ms | 14.8 ms | 6 ms | 0 | 465 ms |
| writer | 18.9 ms | 25.8 ms | 7 ms | 0 | 531 ms |

`Resume` has no bound of its own besides its context. A full load reads the state's written
blocks, not its length, since a mapped-ram state is sparse. How long that may take depends on the
host's slowest disk, which the caller knows. spin bounds it at 30 s plus the written blocks at
50 MB/s.

#### A busy host disk

On a laptop whose disk was busy, the same check stopped the guest for 0.1-8.7 s in 4-6 of the
writer's 10 saves, with at most 85 MiB left at the stop, so that time did not go to RAM (#426).
QEMU's downtime was longer than the guest's stop to `GuestRunning`, so the wait came before
`pre-switchover`, where the host hears of the stop.

QEMU's trace (`-trace` on `runstate_set`, `vm_stop_flush_all`, `migrate_set_state` and
`luring_co_submit`) named it. From `finish-migrate` to `pre-switchover` the time was the flush in
QEMU's `do_vm_stop`, after the drain: one io_uring fsync of the overlay. That was 527 ms in one
laptop save. On the bench it was 13-79 ms, beside four host writers on the instance store's ext4,
with the guest writing its disk past its page cache (lab 44958247487). One host writer, or a guest
that synced its own writes, left the stop nothing to flush or a disk that answered at once. Deferred
only to the end of the switchover, the same fsync came back inside `cont`. Activating a qcow2 node
closes it and opens it again, and the close flushes.

So QEMU carries `x-defer-disk-flush` (`qemu/patches/0003-migration-x-defer-disk-flush.patch`), and
`Checkpoint` sets it. The stop drains the disks first, so every flush the guest asked for is on the
disk. From then until the guest runs again, or QEMU closes the disks, QEMU's own flushes write what
qcow2 caches to the page cache and stop there: the stop's, a seal's `blockdev-snapshot` and `cont`'s
activation. The guest's next flush forces them. A caller that keeps the checkpoint's disk syncs it
with the state, after `Continue`.

`Checkpoint` asks for the capability only when `query-migrate-capabilities` lists it, one round trip
before the migration starts, with the guest running. A machine an earlier release started runs a
QEMU without the patch, v20261121.01's and every one before it, and that QEMU refuses a capability
it does not know. A runner whose machine library is upgraded checkpoints that machine as before:
the stop flushes to the host's disk, and on a busy disk the guest waits for it. `verify qemu` still
holds the pinned QEMU to offering it, so a bump that loses the patch fails there.

Measured on the bench (nested KVM, v20261121.01 with QEMU from parts run 44961949665, spin's shape
plugged to 2 GiB, the busy-disk guest, 20 pairs interleaved on one guest in a random order within
each pair, lab 44962720682, 2026-11-21):

| `x-defer-disk-flush` | Guest stopped at least, p50 / p90 / slowest | QEMU's downtime, p50 | Stop to `GuestRunning`, p50 |
| --- | --- | --- | --- |
| off | 40.0 / 72.0 / 93.0 ms | 40 ms | 18.9 ms |
| on | 18.0 / 22.3 / 28.8 ms | 7 ms | 18.0 ms |

The guest's stop fell 22.0 ms [−29.2, −15.8], and QEMU's downtime 33 ms [−40, −27]. The stop to
`GuestRunning` did not move, −0.9 [−1.6, +0.7], since it starts at `pre-switchover`, after the
flush. On QEMU's own clock, `finish-migrate` to `running` read 8-20 ms with it and 20-95 ms
without, and no flush was submitted between `completed` and `running` in either arm.
`boot:checkpoint-pause` now holds that guest too: 19.1 ms p50 (lab 44962720691). The patched QEMU
alone did not move restore-first, which a checkpoint's stop is not on: login −0.1 ms [−2.3, +2.4]
from an evicted state and −3.2 [−4.7, −0.5] from a cached one (lab 44963610565).

#### A host short of memory

The laptop's slowest saves had two more waits that the bench did not show: 1.6-1.9 s between
`finish-migrate` and the stop's flush, with nothing to flush, and 0.7-1.6 s writing the last RAM
after `pre-switchover`. Its swap was full, so they looked like QEMU's memory read back from swap
(#437). They were not. Traced again (laptop, v20261121.01 with the patched QEMU, the busy-disk
guest, the state on ext4 on NVMe, swap 8 of 8 GiB used, 2026-11-22), QEMU had no page in swap
(`VmSwap` 0) and the host swapped in 167 pages in 11 minutes. What the stop waited on was the disk,
which was 60-80% full in `/proc/pressure/io`:

- The drain waited for the guest's writes still in flight, whose io_uring workers slept in
  `jbd2_log_wait_commit` and `rq_qos_wait`, and for the vCPU and the migration thread, which slept
  in `folio_wait_bit_common` after major faults. Those faults were QEMU's own file pages: its
  binary, evicted by the host's reclaim (1.9 M file refaults in the run) and read back from the
  disk the state, the overlay, the swap and the busy writers all shared.
- The last RAM waited in the same `folio_wait_bit_common`, on the migration thread.
- Twice, 1.5 s and 10 s passed from `completed` to `running` with QEMU idle: the caller's own
  process waited on that disk before it sent `cont`.

A runner is not that laptop. It has no swap: spin installs a swap file only on the control plane,
and the control plane and a runner are separate machines (spin-stack/spin's
`internal/installation/install`). Each machine's group has `memory.swap.max` 0. QEMU's binary is
on the root volume and the workspaces' files on the instance store. The bench has that layout, and
under its memory taken but 4 GiB, with no swap and the busy disk, the host reclaimed 26 M pages in
16 saves and QEMU took no major fault. The guest stopped 26 ms p50 and 44 ms at the slowest, against
18.5 ms beside the busy disk alone (lab 44971787197 and 44971162018, 2026-11-22).

What a stop under pressure did wait on, in its two slowest saves of 40, was the kernel's dirty-page
throttling (`balance_dirty_pages`). Once a host is short of memory its dirty limit, a share of what
it can still give, is smaller, and the busy disk's writers keep it full:

- 120 ms in the drain, for a guest write that an io_uring worker had into the overlay's page cache.
- 173 ms of the last RAM, for one multifd channel's write into the state, while the other seven
  waited for the same file's `i_rwsem`.

(bench, lab 44972838681, `bpftrace` on `sched_switch` with kernel stacks.) `boot:checkpoint-pause`
measures that host as its fourth guest and does not hold it: on compat's kvm host, 118 GiB taken, it
stopped 56 ms p50 (compat 44976542939, #444).

Neither change that would keep the stop off the page cache is in place:

- `direct-io` on the state's channels never converged beside the busy disk. At the writer's
  640 MiB/s of dirtied memory each pass took ~2.5 s and left ~520 MiB, and the first save was
  cancelled after 10 minutes, where the page cache's save took ~4 s (lab 44973775066).
- `downtime-limit` at 15 ms instead of 30 left less RAM for the stop (11 MiB p50 against 30) but did
  not shorten it: −2.5 ms [−7.5, +3.0] short of memory, −1.5 [−6.0, +2.8] with the writer, −0.2
  [−0.5, +0.3] idle (25 pairs each, interleaved, lab 44975130836). A first run of 20 pairs had read
  −6.0 [−11.9, −1.1].

## Turned down

| | What was measured | Why not |
|---|---|---|
| The base on virtio-pmem with DAX, shared by every VM | ~155 MB less private memory per VM with /usr read; one copy of the base in the host's cache; re-reading slower (270 vs 70 ms) | Pages shared between VMs are a side channel (Flush+Reload; Firecracker advises against it), and this machine does not share pages where lateral movement is a risk. #63 and #65 closed |
| The overlay `O_DIRECT` | The host keeps 2 MB instead of 657; a writer beside it 1.7 GB/s with p99 20 ms for its neighbour | Small reads 3x slower (1490 vs 530 ms) |
| DAMON_RECLAIM, MGLRU eviction | Above | Slower, or not selective |
| A lower `page_reporting_order` | Above | Huge pages lost for good, for a transient gain |
| KSM | - | Rowhammer-style page-deduplication attacks (Flip Feng Shui). Firecracker asks for it off, and spin's runner writes 2 to `/sys/kernel/mm/ksm/run` (its `internal/installation/boot/runner.go`) |
| Free page hinting | - | QEMU uses it for migration only; Firecracker warns of corruption |
| virtio-fs DAX | - | SHMEM_MAP is not merged in QEMU; Cloud Hypervisor deprecated it |
| virtio-mem together with an inflated balloon | - | Incompatible in one VM |
| Readahead in GB | - | Modal saw production latency from it |
| A checkpoint's `downtime-limit` at 100 ms | QEMU's downtime 60 ms against 209 at 300 and 11 at 30, with the writer (laptop, 20 saves an arm, #415) | 30 ms stops the guest for less, at no total cost the idle guest shows |
| auto-converge for a checkpoint | The throttle never engaged: `cpu-throttle-percentage` read 0 in every save, the writer's too (laptop, #415) | QEMU throttles only when, two syncs running, the guest dirtied more than half of what QEMU wrote (`throttle-trigger-threshold`). Into the page cache QEMU outpaced the one vCPU's writer (inferred from the 0) |
| `avail-switchover-bandwidth` | Not run. QEMU's measured rate is already close to what it writes after the stop: 31.8 Gbit/s measured, and 835 MiB written in 192 ms, ~4.3 GB/s (bench) | It stands in for the measured rate in the same test, so `downtime-limit` is the one knob |

## What others do

From a survey of other sandboxes' code and documentation (VS: verified in code, D: documented,
B: blog), before measuring:

- **Reclaim in the guest, then free page reporting** is what everyone does. E2B freezes the
  user cgroup and runs `fstrim`, `sync`, `drop_caches` and `compact_memory` before a pause
  ([reclaim.go](https://github.com/e2b-dev/infra/blob/23f7a0f89dc3/packages/orchestrator/pkg/sandbox/reclaim.go), VS).
  Meta's Senpai/TMO paces reclaim by PSI ([TMO](https://engineering.fb.com/2022/06/20/data-infrastructure/transparent-memory-offloading-more-memory-at-a-fraction-of-the-cost-and-power/), D).
  ChromeOS inflates the balloon by `guest cache - target` under host pressure
  ([balloon_policy.cc](https://cos.googlesource.com/third_party/platform2/+/refs/heads/release-R113/vm_tools/concierge/balloon_policy.cc), VS).
- **Keep the host's cache.** Firecracker and Lambda do buffered I/O
  ([device.rs](https://github.com/firecracker-microvm/firecracker/blob/main/src/vmm/src/devices/virtio/block/virtio/device.rs), VS;
  [ATC23](https://www.usenix.org/system/files/atc23-brooker.pdf)), bounded by cgroups and rate
  limiters ([prod-host-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), D).
  Modal dropped FUSE direct I/O because it turns the page cache off
  ([talk](https://modal.com/blog/jono-containers-talk), B).
- **Discard to holes.** Firecracker turns TRIM into a hole punch
  ([block-discard.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/api_requests/block-discard.md), D).
- **Base on pmem with DAX.** Kata ([qemu_arch_base.go](https://raw.githubusercontent.com/kata-containers/kata-containers/main/src/runtime/virtcontainers/qemu_arch_base.go), VS),
  Firecracker 1.14 ([pmem.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/pmem.md), VS) and
  RunD ([ATC22](https://www.usenix.org/system/files/atc22-li-zijun-rund.pdf)). Firecracker's
  own warning about sharing the file between VMs is why it is turned down here.
- **The cold boot.** SnapStart, REAP and E2B record a boot's working set and prefetch it. Here a
  cold boot reads 47 MB of the 830 MB base and costs 32 ms over a warm one; prefetching those
  files with `WILLNEED` took 47 ms and won back 24 ms (run 36803912357). Not adopted, because a
  host keeps the base warm for every machine after the first.

## Measuring it again

No probe behind these tables is in the tree. The caches and reclaim probes were in
`boot/pagecache_test.go` until c3c0fd1 (2026-11-19). The cold-boot, `memory.high`, reclaimers
and first-write probes were deleted earlier, once answered:
`git log --diff-filter=D --name-only -- boot/` lists them. To measure one
again, restore it from git history as a `spin-machine-dev lab` probe (`docs/cli.md`,
"Measuring") and run it on the bench fleet. What the lab taught about itself:

- **Only a quiet host's numbers count**, and only against numbers from the same kind of host:
  the tables above are construct's, and the bench fleet's instance type is another host. A
  laptop is neither.
- **One run per instance.** Lab runs go at once, each on its own bench instance, so read results
  by run id, never by "the newest".
- **A probe that mounts must unmount in `t.Cleanup`.** A loop mount left behind broke every
  later checkout on that runner.
- **Unix socket paths are 108 bytes.** QMP sockets live under `/tmp`, not the lab's long
  `TMPDIR`.
- **A kernel without modules has nothing for `modprobe` to load.** Ask for the device
  (`/dev/nbd0`) or the feature, never the module.
- **Three boots a row is noise at the 5% scale**; compare with 20, alternated.
