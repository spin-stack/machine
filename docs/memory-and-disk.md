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
| The overlay is opened writeback; the host keeps a copy of what the guest wrote | Without the host's copy, 16 384 small files read in random order take 1490 ms instead of 530 | Every image is opened through the host's page cache (`machine/qemu.go`, `addImages`) |
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
with nothing plugged (`requested-size=0`, `appendDevices` in `machine/machine.go`). The machine
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
- **One run at a time.** Two lab runs side by side measure each other; the bench fleet runs one.
  Read results by run id, never by "the newest".
- **A probe that mounts must unmount in `t.Cleanup`.** A loop mount left behind broke every
  later checkout on that runner.
- **Unix socket paths are 108 bytes.** QMP sockets live under `/tmp`, not the lab's long
  `TMPDIR`.
- **A kernel without modules has nothing for `modprobe` to load.** Ask for the device
  (`/dev/nbd0`) or the feature, never the module.
- **Three boots a row is noise at the 5% scale**; compare with 20, alternated.
