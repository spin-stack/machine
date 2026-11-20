# Lab: where boot time stands, and how it is measured

This page is the one place that says how far each goal number is from 100 ms, where its time
goes, and how a number is taken before anything is decided on it. Update it with every change
that decides something. The story of each change, dated, is `docs/log.md`. Each release's own
numbers are `docs/releases.md`. What the guest's memory and disk cost is
`docs/memory-and-disk.md`. The lab's commands are `docs/cli.md`, "Measuring".

Every number here is from the bench fleet (spin-stack/ci, AWS c8id.4xlarge, Xeon 6975P-C, KVM
nested, scratch on local NVMe), p50 or p50/p95 in ms, unless a row says otherwise. "The review
of 2026-11-20" is the methodology review in `docs/log.md`'s entry of that date.

## The two numbers

AGENTS.md, "The goal", sets both at 100 ms.

| Number | What the probe times | p50/p95 | Run |
|---|---|---|---|
| Cold boot to the first SSH login | `ssh-first`: from the moment before QEMU is exec'd to `true` run as `spin` over SSH through vsock | 324/329 | lab 44727451080, v20261120.02 |
| The same, QEMU started ahead | `prestart`: from the request to a QEMU already started held | 296/309 | lab 44727451080 |
| A checkpoint restored to the first login | `restore-first`, `evicted` row: the state read from local NVMe, out of the page cache | 168/180 | lab 44727451080 |
| The same, QEMU started ahead | `prestart`, `restored into prestarted` | 135/140 | lab 44727451080 |

Measured 2026-11-20, 20 boots of each of five releases, interleaved. `docs/releases.md` shows
v20261120.02 at 328/346 and 158/184: another run, on another day, and the gap between the two
is the drift between runs (see "Measurement pitfalls").

### What the probes do not yet measure

The goal probes boot a machine, and log in by a path, that spin does not use (review of
2026-11-20):

- **The shape.** Every probe above ran with `flags: ""`: 2 vCPUs, 2 GiB, and spin-machine's
  defaults `--max-cpus 0` and `--max-memory 0`, so no CPU or memory hotplug. spin
  (spin-stack/spin, `internal/runner/vmm/vmm.go`) boots 1 CPU and 128 MiB, with ceilings of 16
  CPUs and 8 GiB, and 4 hotplug disks. In that shape the kernel takes 86.6 ms to "Freeing unused
  kernel image" against 62.6 (lab 44587603255: `virtio_scsi_init` 12.1, `virtio_mem` 4.7,
  `acpi_init` 17 against 10).
- **The login path.** spin boots `/sbin/vminitd` from its own initrd with `systemd.ssh_auto=0`
  (spin's `internal/runner/qemu/kernel_cmdline.go`), and its end-to-end test
  `TestAWorkspaceOpensNoSSHServerOfSystemdsOwn` requires `sshd-vsock.socket` inactive. spin
  reaches sshd through its supervisor's vsock forwarder to 127.0.0.1:22. The lane tuned here
  (`sshd-vsock.socket.d`, `sshd@.service.d`, the keygen ordering) is tuned for a socket spin
  turns off.

Branch `lab-measure` makes spin's shape the probes' default and names the shape on every table.
The login path is still the probes' own. Until both are spin's, a gain on these numbers is a
lead for spin, not a measurement of it.

- **The memory.** spin sizes every workspace to 2048 MiB or more through virtio-mem right after
  its start, before the guest's handoff. Left at 128 MiB, the guest's OOM killer runs through
  every boot and login of spin's shape (laptop, 2026-11-20, `docs/log.md`). `--opt plugged=2048`
  sizes it as spin does, and `--opt login=spin` logs in as spin does: root, through a forwarder to
  `ssh.service` on 127.0.0.1:22.

The cold boot became the main target when templates were removed on 2026-09-30 (#51). Before
that, `c2c4c6a` (2026-09-10) called it the path the product is designed never to take.

## Where the time goes

Each table splits one number into phases, with its source. A phase marked "approximate" is
taken from another run or another release, or found by difference. The last column is one way
to spend 100 ms. It is an allocation, not a measurement: it says how far each phase has to move.

### Cold boot, v20261120.02, ladder shape (2 vCPU, 2 GiB, no ceilings)

| Phase | p50 today | Source | How well known | For 100 ms |
|---|---|---|---|---|
| QEMU exec to ready for the guest | 29.4 | cold boot 325.3 less the boot into a QEMU started ahead 295.9 (`prestart`, lab 44727451080) | by difference, same run | 0, started ahead |
| qboot, loading the kernel | ~8.5 | `boot:firmware-stages`, lab 43494513791 (2026-11-13, PVH, before the bzImage) | approximate: one run, an older machine | 3 |
| The kernel before its clock starts: LZ4 decompression of the bzImage and early setup | ~25-35 | v20261119.01 → .02: sshd accepts +10.2 ms (95% CI [2.2, 16.9]) while "Freeing" went −12.3 (lab 44727451080), so ~22-25 ms moved where the kernel's clock does not see; firmware-stages read "kernel and init" 108.7 against a "Freeing" of 75.6 (lab 44535161371, PVH) | approximate: `boot:firmware-stages`' "to the kernel's clock" times it since 11-20, not yet on the bench | 5 |
| Kernel start to "Freeing unused kernel image" | 62.6 (86.6 in spin's shape) | `initcalls`, lab 44727451080; lab 44587603255 | measured | 25 |
| PID 1 to sshd accepting on vsock | ~75-85 | sshd accepts 210.0 less the four rows above | by difference | 30 |
| sshd's accept to the first login | 114.2 | `ssh-first` `accept_to_login`, lab 44727451080 | measured | 37 |
| … of it, what any login costs (a second login) | 42.9 | `ssh-first` `second_login`, lab 44727451080 | measured | |
| **Total** | **324.1** | lab 44727451080 | | **100** |

The first login's own time, on the guest's clock (v20261119.01, lab 44555905383): accept to
authenticated 55.5, PAM's session to logind's new session 29.0, to the scope 4.3, to the
command 8.4. Before authentication, PID 1 starting `sshd@` takes ~11 ms, OpenSSH 10's three
execs ~12, and PAM's modules loaded cold 8.3 against 0.9 at a second login (lab 44557162195).

**The cold number cannot reach 100 ms by userspace work alone.** QEMU, qboot and the kernel
take ~125-135 ms before PID 1 runs in the ladder's shape, and ~150-160 in spin's, where the
kernel is 24 ms longer. With QEMU started ahead they are still ~96-106. Every phase before PID 1
has to shrink as well.

### Restore, v20261120.02, ladder shape

| Phase | p50 today | Source | How well known | For 100 ms |
|---|---|---|---|---|
| QEMU start | 27.7 | `restored` running 115.6 less `restored into prestarted` running 87.9 (`prestart`, lab 44727451080) | by difference, same run | 0, started ahead |
| RAM load from the mapped-ram file (155 MiB of data) | ~65-70 | review of 2026-11-20 | approximate: no probe isolates it, and `read` times the file's holes (see "Measurement pitfalls") | 30 |
| Device state and `cont`: the rest of running | ~18-23 | 87.9 less the RAM load | by difference | 20 |
| Running to the first login | 47.1 | `restored into prestarted`, login 135.0 less running 87.9 | by difference, same run | 50 |
| **Total, QEMU started ahead** | **135.0** | lab 44727451080 | | **100** |
| Total, QEMU started by the restore (`restore-first`, evicted) | 168.1 | lab 44727451080 | | |

**The restore can reach 100 ms.** With QEMU started ahead it is 135 ms today, and ~65-70 of
that is one sequential load of guest RAM. If multifd channels make that load ~4× faster, the
restore is ~85-90 ms. The guest's ~47 ms from running to login is about one warm login (42.9),
so a cheaper login moves both numbers.

Measured (lab 44750713447, this shape, QEMU started ahead): 16 channels read 93.5/114.9 against
140.9/152.0 at one, Δp50 −47.4 [−54.4, −39.4], the first goal number under 100 ms. What is
left is ~42 ms to running and ~48 to the login. In spin's shape the p50 also falls (149 → 114),
but ~20% of restores with channels (56 of 280) stalled ~400 ms between running and the login.
That was a guest left at 128 MiB, which spin never runs past its first ~80 ms: sized to 2048 MiB
as spin sizes it (`plugged=2048`, `login=spin`), no row stalls (lab 44788801555), and restored
into a QEMU started ahead the login reads 150/156 at one channel, 103/108 at 8 and 92.7/111 at
16, Δp50 −57.4 [−59.4, −54.3] (lab 44789688221): the restore under 100 ms in spin's shape. The
default count waits on restores run side by side on one host.

### Nested, not metal

The bench fleet is nested. On a c8i.metal-48xl (the same CPU) the cold first login read 332 ms
against 378-392 nested (v20261114.02, lab 44537498507), so nesting costs ~50-60 ms. Most of it
is VM exits: the PCI walk takes 5.00 ms nested against 2.22 on metal, ~29 µs an exit against 13
(labs 44535161371, 44535156851). A production host that is not nested starts closer to the goal.

## How a number is measured

1. **Write the budget entry first.** Before an experiment runs, name the phase it attacks in the
   tables above, that phase's p50 today, its ceiling (what the goal number gains if the phase
   cost nothing), and the result that would decide it. An experiment whose ceiling is under the
   noise needs more boots before it needs a run.
2. **Measure spin's shape.** Boot the CPUs, memory, ceilings and disks spin boots a workspace
   with, and name the shape on every table. A gain in another shape is a lead.
3. **Compare only inside one interleaved run.** The arms of a comparison boot in turn on one
   host, 20 boots or more each, one lab run at a time on the bench fleet. Two runs are two
   machines: never subtract a number in one run from a number in another.
4. **One acceptance rule.** Report Δp50 with its bootstrap 95% confidence interval. A change is a
   gain only when that interval excludes 0. Two releases that boot the same guest, 20 boots each,
   read Δp50 of the first login in [−8.7, +4.8] ms (v20261119.02 against v20261120.02, lab
   44727451080), so an effect under ~7-9 ms needs more boots, not a second look. Halving the
   interval takes about four times the boots.
5. **One change per arm.** A comparison that decides a change carries that change and nothing
   else. The bzImage's comparison (lab 44583379559) also carried the logind socket, and neither
   can be read from it. A floor or a bound is different: it may combine every change its
   question needs, because it answers what is possible, not what one change is worth.
6. **An instrument must see the phase it claims.** The kernel's "Freeing" clock starts after the
   bzImage is decompressed, so it recorded the bzImage's cost as zero while sshd accepted ~10 ms
   later. The restore probe's `read` reads the whole mapped-ram file, whose length is the RAM
   ceiling, and times holes. Before a phase's number is trusted, check that something on the
   other side of it moved by the same amount.
7. **Measure the bounds each release.** The no-systemd floor (below) bounds what systemd costs.
   It was measured once, on an older kernel. A bound that is not re-measured stops being one.
8. **A laptop finds leads.** It does not decide anything. Its noise has read 61 against 105 ms
   minutes apart (`de9b7db`), and it is not nested.

The decision itself is AGENTS.md's: a change goes in when it moves one of the two numbers,
interleaved against the release, without making the other worse, or when it is functional.

## What has been kept

Each change that moved a number and stayed, with what it moved. Rows before 2026-10-24 were
measured on the lab runner or a laptop, not on the bench fleet. No row before 2026-11-20 had a
confidence interval. The two rows marked "inside the noise" would not pass rule 4.

| Date | Change | Moved | Run |
|---|---|---|---|
| 09-08 | ICH9 `sata=off`, `smbus=off` | kernel to init 62.3 → 60.6 (400 boots, laptop) | `fa1803f` |
| 09-10 | `TERM=dumb` on the console | console stall 687 → 19; boot to the console's login 855 → 163-207 (laptop) | `b5f361f` |
| 09-29 | Hotplug disks on virtio-scsi instead of PCIe root ports | boot −31; attach 135 → 1.5 | `2b19096` (#25) |
| 09-29 | qboot instead of SeaBIOS | 121.89 → 114.76 | run 36650049960 |
| 09-30 | Kernel patches 0001-0004 | kernel 109.4 → 73.7 | run 36651858261 |
| 11-12 | logind back in the boot, spin lingers | first login 570/626 → 510/525 (10 boots) | lab 43308825692 |
| 11-12 | MOTD and `pam_motd` off | −12 on the first login, −7 on each | #240 |
| 11-13 | sshd offers only `mlkem768x25519-sha256` | a second login 104 → 40 against sntrup761; first 489 → 461 | lab 43479854381 |
| 11-13 | SSH host keys made from the first dispatch | first login 489/506 → 468/494 | lab 43483372488 |
| 11-13 | Kernel 0005, 0006, `DEFERRED_STRUCT_PAGE_INIT` | kernel 96.3 → 87.1; first login 466 → 456 | labs 43505126131, 43501992969 |
| 11-13 | `driver_async_probe` for rng, balloon, vsock | 214 → 209 at the no-systemd floor; inside the noise | lab 43506390106 |
| 11-13 | The first login's lane (units without default dependencies, `user-early-light`, coldplug filtered) | first login 478 → 402 | lab 43518915618 |
| 11-19 | systemd from Ubuntu's source, stage1 | first login −47, −38, −47 | labs 44531842773, 44537549305, 44538877783 |
| 11-19 | logind's Varlink socket without default dependencies | accept to login 131 → 120; first login 332 → 328, inside the noise | labs 44558035927, 44558053261 |
| 11-19 | QEMU 11.2 | first login 332 → 317, with main's other changes in the same arm | lab 44581350077 |
| 11-19 | Kernel 0007-0011 and a config trim | kernel 96.4 → 86.6, spin's shape | lab 44587603255 |
| 11-19 | Every machine started held: QEMU can be started ahead | cold 325 → 296, restore 163 → 135 | lab 44727451080 (first 44573266140) |

Kept for a function, at a cost: the kernel entered as a bzImage (11-19), so that KASLR works.
Its cost is the ~22-25 ms before the kernel's clock in the cold table, which the comparison
that kept it could not see (rule 6). Making it cheaper is on the list below.

## Measured and rejected

An idea already here needs a new reason, not a new run.

| Idea | Result | Source | Date |
|---|---|---|---|
| `usb=off`, `vmport=off`, `smm=off`, `i8042=off` | 63.0/62.6/62.3/62.5 vs 62.9 ms | `fa1803f` | 09-08 |
| Mask udev | Saves 0, and boots never reach a login (10 s ttyS0 timeout) | `4cbd229`, `80f984b` | 09-10 |
| Trim the 8250 driver | 154 µs | `c03ed89`, kernel/Dockerfile | 09-10 |
| Modular kernel | 0.57 ms; breaks "a release is one machine" | `1a8e25c` | 09-10 |
| ACPI params (acpi=noirq, pnpacpi=off, …) | 69.9 vs 68.6 ms, slower | `64d2901` | 09-10 |
| initcall_blacklist ksm/kcompactd | 0; event_trace's 6 ms is the profiler | `64d2901` | 09-10 |
| CONFIG_XFRM=n | Moves BTF's 9 ms first parse elsewhere | `e285dec` | 09-10 |
| Generous vCPU ceiling | maxcpus=16 +5 ms, 64 +9 ms of kernel | `f32d527` | 09-10 |
| MICROVM | No ACPI CPU hotplug | `b151186` | 09-10 |
| agetty tweaks (TTYReset, --noissue, --delay 0, …) | Within noise of agetty's sleep(1) | `80f984b` | 09-10 |
| Less guest RAM (512 MiB) | 1241 vs 1256 ms, nothing | `80f984b` | 09-10 |
| chrony or another time daemon | 10 ms; cannot know it was restored | `c7af4d3` | 09-10 |
| systemd-timesyncd | SNTP only, no refclock | `c7af4d3` | 09-10 |
| `cache=none` on disks | Breaks shared base caching, fails on tmpfs | `8b7391a` | 09-07 |
| Overlay O_DIRECT | Small reads 1490 vs 530 ms | docs/memory-and-disk.md | 10-01 |
| CONFIG_VT=n for speed | 0 to login; 0.31 ms kernel (2.09 withdrawn) | `e2f6b8f`, `9b5f0dd` | 09-26 |
| 19 udev rules masked; loop/8250 count params | 0 | `e2f6b8f`, boot/harness_test.go | 09-26 |
| Console unit instead of serial-getty | +340 ms until TERM=dumb, then equal | `c6e48de`, `7b08e5a` | 09-26 |
| Fewer unit files | 300 extra: 236 vs 246 | `9fab3f1` | 09-26 |
| KSM + THP off | −0.50 ms | `cda04b1` | 09-26 |
| COMPACTION off | Impossible: virtio-mem needs CONTIG_ALLOC | `cda04b1` | 09-26 |
| erofs removal for speed | Noise (100.46 vs 102.22) | `5a3d509` | 09-26 |
| Smaller TCP hash tables | Moved nothing | `dae6a14`, #48 | 09-26 |
| ZRAM | Runaway killed later (20 vs 14 s), worst login 3.8 s | kernel/Dockerfile, `430d2b3` | 09-28 |
| systemd-oomd | The kernel always beat it | `337c08d` | 09-28 |
| CPUWeight/IOWeight/MemoryLow on ssh.service | No change | `f7cd1ab` | 09-28 |
| Wrapping board init in a memory transaction | Guest prints nothing | `f10383b`, lab 36662499370 | 09-30 |
| Lowering `memhp_default_state` (unplug policies) | Kept `online` | `2903d8c`, #48 | 09-30 |
| Base on virtio-pmem + DAX | ~155 MB less per VM, but a side channel | #63, #65, docs/memory-and-disk.md | 10-01 |
| DAMON_RECLAIM, MGLRU eviction | Slower, or not selective | docs/memory-and-disk.md | 10-01 |
| `page_reporting_order` 0/3 | ~100 MB transient vs ~180 MB huge pages lost | docs/memory-and-disk.md, #83 | 10-01 |
| Prefetch the boot's files | 47 ms to win 24 | docs/memory-and-disk.md, run 36803912357 | 10-01 |
| A second limit (`memory.high`) | `memory.max` suffices | docs/memory-and-disk.md | 10-01 |
| KSM, free page hinting, virtio-fs DAX | Security, correctness, unsupported | docs/memory-and-disk.md | 10-01 |
| Reusable release builder (SLSA L3 attempt) | Not L3, needed guards | #109-#111 | 10-13 |
| Getty without BindsTo dev-ttyS0 | 342 vs 345 | lab 43303190733 | 11-12 |
| Keygen wanted by multi-user.target | 518 vs 514 | lab 43312419007 | 11-12 |
| `pci=noearly` | Guest does not boot | #240 | 11-12 |
| Remove virtio-rng | −13.6 ms to the console prompt, never measured on a goal number. Set aside because the fingerprint would move, which AGENTS.md now allows (see "Open questions") | lab 43321809471 | 11-12 |
| Mask sshd-socket- and run-generators | Login 489 (no change) | lab 43483372488 | 11-13 |
| Mask 11 always-failing units | 484 (no change) | lab 43483372488 | 11-13 |
| Vsock socket + sshd@ without default deps | No sooner; dropped 7 of 22 first connections (laptop) | `boot/ssh_first_test.go` (deleted 11-19, `c3c0fd1`) | 11-13 |
| mapped-ram checkpoints (multifd ×4) | Restore login 385/400 vs 386/399. The run's own log calls the comparison not comparable (review of 2026-11-20); checkpoints have been mapped-ram since 11-19, and multifd channels were measured on 11-20 ("What is next", rank 1) | #246, lab 43483852339 | 11-13 |
| sntrup761 (OpenSSH default) | 104 vs 40 ms per login | #245, lab 43479854381 | 11-13 |
| Configuration-space snapshot over fw_cfg (QEMU + kernel) | 209 vs 211 with 0005 | lab 43514884598 | 11-13 |
| CPUWeight/IOWeight/Nice on the first login's lane | −2 ms over coldplug filtered; second login 63 vs 45 | lab 43518915618 | 11-13 |
| One sshd for every connection (vsock Accept=no, `sshd -D` early) | First login 382 vs 381; second 40 vs 48 ms | lab 43524721490 | 11-13 |
| Login without pam_systemd (diagnostic bound) | 380 vs 436, but no session, no XDG_RUNTIME_DIR | lab 43518044150 | 11-13 |
| QEMU copies the kernel's ELF segments on threads (patch `hw/core/loader`, #290). Laptop: QEMU start 30.9 → 21.6 ms | Bench `boot:ssh-first`, 20 each, against the release: 11.2 + copy 331 → 316 (sshd accepts 200 → 199), 11.2 alone 332 → 317 (201 → 200). The copy adds nothing nested; the 15 ms is 11.2 and main | labs 44580080420, 44581350077 | 11-19 |
| `scsi_mod.scan=manual` (no boot-time scan of 256 virtio-scsi targets) | Kernel −9.3 ms in spin's shape (95.0 → 85.7), but since Linux 5.10 (`beef6fd02b90`) a LUN-0 hotplug event goes through `scsi_scan_target(…, SCSI_SCAN_INITIAL)` and EVENTS_MISSED through `scsi_scan_host`, both no-ops under manual: every disk added at run time, present at boot, or added under `-S` or before `cont` never appears, silently. Removal still works. Keeping it needs a virtio_scsi patch (`SCSI_SCAN_MANUAL` in those two paths) plus a guest-side scan of known targets | lab 44570773103; source reading of 7.3.5 `drivers/scsi/virtio_scsi.c`, `scsi_scan.c` | 11-19 |
| Fast snapshot load, QEMU 11.2 (mapped-ram + postcopy-ram on a `file:` URI) as the restore | Running at 38 ms instead of 102, but the first login at 293/325 instead of 152/161: every page the guest touches before a login is a fault served by one read from the file, and nested faults are expensive. The full mapped-ram load is the default; `Load.Fast` stays for large states | `boot:restore-first` with `SPIN_RESTORE_FAST=1`, lab 44585840498 (140 MiB state from disk, 20 each) | 11-19 |
| Mask `modprobe@drm` (logind is ordered after it) | First login 327/345 vs 328/337 with logind's socket early | lab 44558035927 | 11-19 |
| Mask `systemd-udev-load-credentials` | First login −8 then −6 ms, p95 unchanged: not separable from noise at 20 boots | labs 44537549305, 44538877783 | 11-19 |
| systemd with `-Db_lto=true` | Ubuntu's dpkg-buildflags already pass `-flto=auto`: the same packages to within a few hundred bytes | image/systemd/Dockerfile | 11-19 |
| The base memory in a shared file, left out of a checkpoint (`memory-backend-file,share=on` as `pc.ram`, `x-ignore-shared`): the restore maps the pages, QEMU copies none (#276) | The state is 0.3 MiB, the save 15 ms instead of 165 and the restore running at 45-50 ms instead of 112-122, but neither goal number improves. With the file on the scratch disk the cold boot's first login is 1089 ms instead of 335 (each first touch a fault into a file) and the restore's 182-238 instead of 171; on tmpfs the cold boot is 709-719 instead of 331 and the restore's login 177 p50, 741-768 p95. Code not kept (spin-stack/spin-machine#306, closed) | `restore-first` with `shared=true`, labs 44663272360 (disk) and 44664989139 (`memory-dir=/dev/shm`), 20 each | 11-20 |
| Lower the vCPU ceiling, drop the balloon or ACPI for boot time | Not tried: hotplug and dynamic memory are an invariant | AGENTS.md | 11-13 |

## Measurement pitfalls

Each of these made a number say something it did not measure.

- **The instrument measured itself.** Now avoided by quiet consoles, `--profile`, and asserting
  the log is whole. Each case:
  - initcall_debug with a serial console (`c03ed89`);
  - a function_graph wide filter, 26× inflation (`a31d7b2`);
  - the `kvm:kvm_pio` tracepoint, 49.3 vs 27 ms (`c049a21`);
  - `trace_event=initcall` (`64d2901`);
  - systemd debug logging, 212 vs 26 ms (`ad05544`);
  - the `boot:systemd` probe "3× slower" (`7b08e5a`);
  - a shell harness throttling the console, 1424 vs 190 ms (`80f984b`);
  - a log_buf_len of 8M costing 4.9 ms (`5221690`).
- **The instrument could not see the phase.** Two cases found in the review of 2026-11-20:
  - the kernel's "Freeing" clock starts after the bzImage is decompressed. v20261119.01 → .02
    read sshd accepts 198.9 → 212.3 and 200.6 → 211.3 in two runs (44641240861, 44727451080)
    while "Freeing" went 76 → 62.5, and the comparison that kept the bzImage (lab 44583379559)
    read no cost. A laptop lead put exec to `/bin/sh` at 101.0 for the bzImage against 82.3
    for PVH;
  - `restore-first`'s `read` (`internal/lab/restore.go`) reads the whole mapped-ram file, whose
    length is the RAM size and mostly holes: 2 GiB read for 155 MiB of data. A local repro
    read 422-675 ms whole against 106-112 for the data extents alone. Its `cached` row is
    slower than `evicted` in all five cells, and `up` times the monitor socket opening, not
    QEMU being up (QEMU starts ~28 ms into a restore).
- **Blame, the critical chain and log gaps are not savings.** logind was 63 ms in blame and
  worth 24; chrony 43 and worth 10; udev 46 ms and worth 0 (`6aba37b`, `e2f6b8f`). Recorded in
  `boot/harness_test.go` comments.
- **Misattribution.** BTF's first parse looked like XFRM (`e285dec`); ksm/kcompactd 2.00 ms is
  HZ resolution (`cda04b1`); vCPU descheduling was counted inside initcalls (`64d2901`).
- **Checkpoints that lie.** The QMP greeting comes before the board is built (`8eef89d`);
  SeaBIOS's banner comes before hardware init (`6aba37b`, #45).
- **Variants that did nothing:**
  - `systemd.mask=` with the debug-generator masked (`80f984b`);
  - `unmask_udev` that had become a no-op (`b5f361f`);
  - `rm -f` on an `/etc` path that did not exist (`bb6bb89`);
  - a mask that matched no file (`e2f6b8f` now refuses it);
  - `debugfs -R` exiting 0 (AGENTS.md, "Verifying");
  - SPIN_GRAPH and SPIN_CPU_SHARE under lockdown (#240).
- **Host noise.** Now avoided by 20 interleaved boots on the bench fleet, one lab run at a time.
  - a cold page cache right after `task build`: 98 ms read as a 35 ms regression (`f32d527`);
  - a host building a kernel: CONFIG_VT's 2.09 ms (`9b5f0dd`);
  - three runs of one kernel reading 59.5/96.8/167.7 ms (`5221690`);
  - a laptop reading 61 vs 105 ms minutes apart, on an efficiency core (`de9b7db`);
  - 3 boots a row is noise at 5-11% (#81).
- **Two runs are two machines.** Absolute numbers drift between runs by as much as a release
  moves them:
  - one baseline, the "as built" first SSH login, read 570/626 in lab 43308825692 (11-12) and
    489/506 in lab 43483372488 (11-13);
  - v20261019.01 → v20261023.01, with only the image changed, read +6.7% to +14.6%;
  - v20261120.01's restore login read 158.9 in run 44641240861 and 167.0 in run 44727451080.
    `docs/releases.md` shows v20261120.02's restore +8.6 against it; interleaved in one run it
    is +1.1.

  Compare only within one interleaved run (rule 3).
- **Different hosts compared.** The v20261026.01 notes compare a Ryzen 9 5900X with a Xeon
  6975P-C: "the times below compare two machines, not two releases". The report's per-row
  fingerprint also changed then, because the host CPU was in it. Numbers from construct
  (docs/memory-and-disk.md, kernel patches 0001-0004, qboot) do not compare with the bench
  fleet's.
- **Nested KVM on the bench fleet.** A cold boot after a restored guest has run on the host is
  ~1.8× slower across all phases; a laptop shows nothing of the kind (#246). Restored VMs leaked
  between repetitions and contaminated lab 43364516874 (#242's numbers). Now avoided:
  `restore-first` runs every cold boot before every restore and checks with mincore that the
  state is out of the page cache.
- **Page cache.** `POSIX_FADV_DONTNEED` declines recently touched pages, 4.8 vs 251 ms
  (`ab4844c`). `mincore` lies for files the caller does not own (`21a830c`). `drop_caches` was
  forbidden on the shared runner (#57). fstrim right after rm trims nothing (`251638d`). QEMU
  11.1.1 never counted discards (`aaf2d86`).
- **Lab plumbing:**
  - green on a skip (`8d8e97b`);
  - concurrency groups cancelling queued runs (`a3ba5a6`);
  - two runners on nbd0 (`b21e100`);
  - 108-byte socket paths (`299777b`, `f2c47f7`);
  - `modprobe` on a module-less kernel (`5a70721`, `213cd07`);
  - udev resetting vhost-vsock permissions (`97a6de1`);
  - BuildKit comparing a file with itself (`b9d1985`).
- **Guest clock.** `1d5e7f5` says the guest's wall clock "stepped back … chrony setting the
  time" on the lab runner on 09-30, yet chrony was removed from the image on 09-10 (`c7af4d3`).
  The cause named there is not supported by the tree. The fix (time by `/proc/uptime`) stands
  either way.

## What is next

Ranked by ceiling: what the goal number would gain if the phase cost nothing, or what the
experiment can at most say.

| Rank | Experiment | Number | Ceiling | Evidence it is untried | Status |
|---|---|---|---|---|---|
| 1 | multifd channels 1, 2, 4 and 8 on the mapped-ram save and load, restore rows with and without QEMU started ahead | restore | the RAM load's ~65-70 ms; ~85-90 ms to the login with QEMU ahead if the load is ~4× faster | `grep multifd machine cmd` found nothing; the one rejection (#246) is a comparison its own run calls not comparable | **measured** (lab 44750713447): 16 channels 93.5/114.9 against 140.9/152.0, Δp50 −47.4 [−54.4, −39.4]; 8 channels 101.5; 2 channels within the noise. In spin's shape sized as spin sizes it, 16 channels 92.7/111 against 150/156, Δp50 −57.4 [−59.4, −54.3], no stalls (labs 44788801555, 44789688221); the default count waits on concurrent restores |
| 2 | A login without OpenSSH's exec per connection and PAM | both | a login costs 42.9 warm and is in both numbers; accept to login is 114.2 cold | `grep` for UsePAM, privsep, dropbear, tinyssh, qemu-ga finds nothing. It has to work on spin's path: its vsock forwarder to 127.0.0.1:22 | leads, 11-20 (`docs/log.md`): spin's path already runs one sshd; PAM and pam_systemd are what spin's sessions are made of. Next: spin not lingering, below |
| 2a | spin not lingering (`--edit nolinger`), on spin's path and memory (`plugged=2048`, `login=spin`) | both | user@1000, a systemd --user on the one vCPU, ~38 ms of it in the first login's window (11-19); a laptop lead read accept to login 40 against 70 ms | the linger was added 11-12 for sessions of class `user`, which the lane replaced by `user-early-light` on 11-13; spin logs in as root | **kept** (labs 44785179901, 44785565313): first cold login 532/551 → 496/514, Δp50 −36.1 [−45.8, −23.6]; restore rows within the noise |
| 3 | The bzImage's decompression made cheaper: other compression settings, KASLR without decompression, qboot doing the load | cold | ~22-25 ms | the cost was not seen when the bzImage was kept (pitfalls). KASLR stays. First give the phase an instrument (rule 6) | not started |
| 4 | Asynchronous SCSI scan | cold, spin's shape | ~12 ms: `virtio_scsi_init` is 12.1 in spin's shape (lab 44587603255) | `# CONFIG_SCSI_SCAN_ASYNC is not set` (kernel/config-7.3-x86_64). Not `scsi_mod.scan=manual`, which loses disks (rejected) | not started |
| 5 | The no-systemd floor on today's kernel and QEMU, in the ladder's shape and spin's | a bound | none: it says how much of the cold number is systemd's to give back. Last read 209 ms on 2026-11-13, on an 87 ms kernel (lab 43506390106) | measured once, on an older kernel | **measured** (lab 44750322384): the ladder's shape 322 → 219, Δ −103.5 [−108.6, −100.5]; spin's 445 → 240, Δ −204.8. Without systemd sshd accepts at 173 and 189 ms, so the cold number needs the phases before PID 1 as well |

The floor is a bound and never a candidate: systemd stays (AGENTS.md, "What must not break").

## Open questions

- **Is `10-seats.conf` still needed?** The control that should fail passes on systemd 259
  (#240, "Not settled here").
- **virtio-rng removal.** −13.6 ms to the console prompt (lab 43321809471), never measured on
  either goal number. It was set aside because the fingerprint would move, which AGENTS.md now
  allows. What seeds the guest's RNG without it is the question to answer before a run.
- **The restore under load.** Not measured: concurrent restores on one host, and RAM above
  2 GiB (#242's checklist). Channel counts are rank 1 above.
- **PID 1 time.** The ~25 ms in the BPF verifier for systemd's socket-bind program (#240) is
  gone with the image/systemd builds (11-19). The ~32 ms gap after the unit load is loading 192
  units, ~21 ms of CPU (11-19, image/systemd/experiments/pid1-marks.patch), not building the
  transaction (0.3 ms). It shrinks only with fewer units reached at boot.
- **musl under long-running load.** Never measured (`31a57e7`).
- **`page_reporting_order`.** Revisit "only if memory per host becomes the limit"
  (docs/memory-and-disk.md).
- **Moving `MachineType` off pc-q35-11.1.** QEMU is 11.2.0-rc0 (versions.yaml) and the machine
  is still pc-q35-11.1 (`machine/machine.go`, `internal/machinetype`). Its own change.
- **The kernel CVE judge.** 9 CVEs apply with a fix later in 7.3, and 1 has no fix (`0ce27ad`,
  against 7.3.1; the kernel is 7.3.5 on 2026-11-20).
- **Host keys in checkpoints.** A guest checkpointed after boot carries host keys, so the
  restorer must re-key (`image/README.md`, #248). This is a consumer contract to confirm in
  spin.
- **The min_ttl_ms evidence disagrees.** `47aa681` and the LRU_GEN comment in
  `kernel/Dockerfile` (docs/log.md, 09-28).
