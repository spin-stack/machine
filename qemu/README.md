# QEMU

One build, **statically linked**, against musl on Alpine. `spin-machine-dev build qemu` builds it from
source (tens of minutes). `spin-machine-dev fetch qemu` unpacks the published build of the pinned
version (seconds). `devices.mak` is the device allowlist, and `patches/` holds the changes this
tree makes to QEMU's source, applied in name order with no fuzz (`source.sh`). They are made
as the kernel's are (`kernel/README.md`, "Changing it").

## The patches

Each patch has one test that fails when it is lost, or rebased into applying and doing nothing:
it still builds and starts every machine, so nothing else would. `verify args` asks the TCG
binary with no guest (`TestEachQEMUPatchDoesItsJob`, every push against the published QEMU in
ci.yml and every QEMU parts.yml builds). `boot:patches` asks a guest of spin's shape what only a
guest sees (`patches_test.go`, compat.yml on every change that reaches the machine, QEMU or the
kernel). None is in QEMU 11.2.0-rc0, the tree they apply to without fuzz, and none has been
posted upstream.

| Patch | What and why | Earns | Held by |
|---|---|---|---|
| 0001 | Device creation and the machine-done notifiers in one memory transaction: 283 of a start's 438 flat view rebuilds gone | restored +11.7 [+0.9, +19.3], cold within noise (lab 44993254745) | `verify args`: the flat views built from QEMU's start to its first answer, at spin's shape |
| 0002 | virtio-scsi `max_target`, which the machine sets to its hotplug disks less one, so the guest scans 16 targets and not 256; a disk beyond it is refused | cold −8.9 [−15.5, −0.9] (lab 44788216590) | the build (`-device virtio-scsi-pci,help`); `verify args`: a disk beyond max_target refused; `boot:patches`: the guest's SCSI host refuses a scan of target 16 |
| 0003 | The migration capability `x-defer-disk-flush`: from a checkpoint's stop until the guest runs, QEMU's own flushes stop at the page cache. The stop starts the deferral itself, after its drain, when its run state is `finish-migrate`; a CPR mode never defers (#488, #501) | the busy-disk stop −22.0 [−29.2, −15.8] (lab 44962720682, `docs/memory-and-disk.md`, "A busy host disk") | `verify qemu` (the capability in the schema); `verify args`: a save's stop submits a flush to the host's disk without the capability and none with it |
| 0004 | `query-blockstats` `x-flat`: a device's counters without its backing chain | DiskUsage's main-loop time 293 → 96 µs, −197 [−202, −181] (lab 44973302743) | `verify qemu` (the member); `verify args`: no `parent` or `backing` under x-flat, both without |
| 0005 | A machine made for an incoming migration writes its TSC only from the migration, so KVM takes the checkpoint's TSC as given (#452) | correctness: without it a guest saved with a timer due sleeps until its old TSC deadline, the restored p95 539-548 ms (lab 44987412171) | `boot:patches`: a guest restored 650 ms ahead of its new vCPU's TSC goes no more than 200 ms without a line (#472) |
| 0006 | x86 CPUs get `x-legacy-cpuid2-cache`. Off, a legacy cache model under `x-consistent-cache=off` (host's and max's) describes in leaf 2 the caches leaf 4 does, and not `legacy_intel_cpuid2_cache_info`. Default on, QEMU's behaviour (#586) | correctness: glibc read a 2 MiB 8-way L2 from leaf 2 where leaf 4 and the guest's kernel said 4 MiB 16-way (lab 45100482399) | the build (`-device max-x86_64-cpu,help`); `boot:patches`, once the machine sets it off: glibc's L2 is the kernel's |
| 0008 | A 64-bit ELF kernel with `XEN_ELFNOTE_PHYS32_RELOC`, which Linux emits under `CONFIG_PVH` since 6.12 (Jason Andryuk), is loaded at a random multiple of the note's alignment below the initrd and the ACPI data, its entry moved with it, from `qemu_guest_getrandom`: the physical base a bzImage's decompressor chose before (#645). Xen's toolstack honours the note. QEMU's tree and history name it nowhere (the qemu/qemu mirror's code and commit search, 2026-11-23); qemu-devel's archives could not be searched that day (lore and lists.gnu.org refused, patchew timed out) | with kernel 0014-0020, cold −16.8 [−20.9, −13.0] (lab 45219785493, `docs/boot-time.md`) | `boot:kaslr`: the physical "Kernel code" takes more than one value across ten boots |
| 0009 | A machine's first reset copies an ELF kernel's segments (the PVH vmlinux, 37.4 MB of data) into guest RAM in a thread beside the main loop, waited for before the vCPUs start, a migration's setup, the next reset and QEMU's exit. A machine made for an incoming migration copies nothing at reset, as before (#698) | cold −8.6 [−11.0, −4.0], restored −0.1 [−1.1, +0.7] (labs 45260098299, 45260694485) | the build: a held TCG machine with its threads named shows `rom-copy` while it copies a 256 MiB stub ELF |
| 0007 | x86's `-kernel` maps a bzImage read-only instead of reading the whole file into a heap buffer that only fw_cfg reads; the pages are touched when qboot copies them, from the page cache. A kernel with a dtb appended still gets a buffer (#646) | cold −13.4 [−16.9, −7.3], restored −9.3 [−11.2, −6.1] evicted (labs 45182382388, 45182382864) | the build: a held TCG machine's `/proc/PID/maps` names a stub bzImage given to it |

`docs/boot-time.md`, "What the machine carries", says what each earns among everything else the
machine carries. The qemu entry of `versions.yaml` says what a bump runs.

## Under the sanitizers

`sanitize.yml` runs the patches under AddressSanitizer and UndefinedBehaviorSanitizer (#653). It
runs on a change to anything QEMU is built from (`qemu/` and its pins, `--part qemu`) or to what
builds and reads the sanitized QEMU, and weekly. `spin-machine-dev patched sanitized` builds the patched tree with the Dockerfile's own
`QEMU_FLAGS`, less what the sanitizers cannot take (`--static`, LTO, strip, fortify; each is
named with its reason in `internal/patched/sanitized.go`). It is linked dynamically against the
runner's libraries, since neither runtime links into a static musl binary. Nothing ships it.
The `boots` job, on the KVM fleet, makes it the QEMU of the newest release's tree, with the
release's firmware, and runs `boot:record`, `boot:patches`, `boot:checkpoint-pause` (three saves
each), `lab compat` and three boots and restores of `ssh-first` and `restore-first`. Between them
these reach every patch: 0002's scan past `max_target`, 0003's deferred flush, 0005's TSC on
every restore, and 0001, 0006 and 0007 on every start. The `fuzz` job runs QEMU's own
`generic-fuzz` (`docs/devel/testing/fuzzing.rst`) over a virtio-scsi controller with 0002's
`max_target=15` and a disk at target 3. It runs five minutes on a change and thirty on the
weekly run, built with clang under both sanitizers (`task qemu-fuzz`).

Every sanitized process logs its start and its reports under `_output/patched/qemu/sanitizer`.
`spin-machine-dev patched reports` fails on any report. It names the patches whose touched file
holds the report's innermost frame in one, and whether the line is one they added; a report with
no such frame is upstream's. It also fails on logs that record no process. The weekly run opens
an issue for each report, once a kind and place. No fuzzer reaches 0005. Its code runs only under
KVM, which libFuzzer's qtest accelerator does not use, and upstream has no migration-stream
fuzzer. That is #677.

## Static

A dynamically linked QEMU is not one artefact. It is an artefact plus the libraries it was
compiled against. Extracted onto a host with different ones it dies at start-up
(`liburing.so.2: cannot open shared object file`), so a release had to ship either a
container to run it in or the loader and the libraries beside it, and a consumer had to know
which. Static ends the question: the binary runs wherever the kernel does. musl rather than
glibc, because glibc links statically but badly: anything that resolves a user or host name
still wants to `dlopen` an NSS module, and a static binary cannot.

Measured before adopting it, same host and machine line: process start to a QMP `quit`,
**44 ms glibc against 43 ms musl** over five runs. Not measured: a long-running guest under
load, where musl's allocator and its smaller default thread stack differ from glibc's.

The build makes two binaries: one for KVM alone, and a TCG one for runners with no `/dev/kvm`.
A host serving tenants must fail at start-up rather than run a tenant's VM ~17 times slower
(TCG's first login, 7.6 s against 455 ms, lab 44848049631, 2026-11-21). The build asserts both halves, since neither is visible in a file listing.

`--enable-tools` ships `qemu-img`, which is not a convenience: a container's writable layer
is a qcow2 overlay created by running it, so a release with the emulator and without it
cannot give a container anywhere to write. `qemu-nbd` comes out of the same flag and is
deliberately not shipped. It exports a disk over NBD, which nothing here does.

**A vmlinux through PVH, through qboot.** QEMU loads the ELF kernel's segments itself, at a
random physical base (0008), and qboot enters it through `pvh.bin`; the kernel moves its own
virtual base (`kernel/README.md`, "KASLR with no decompressor", #645). From 2026-11-19 to #645 the
machine booted a bzImage, through `linuxboot_dma.bin`, because its decompressor was the only thing
that randomized the kernel; it still boots one for a release from then. Before 2026-11-19 it
entered a vmlinux through PVH that could not move, at the same address every boot. There is no bootloader and no UEFI: the same guest under UEFI + Secure Boot was measured
at +356 ms and rejected, before this repository existed (the number came with it, 2afac47,
2026-09-07, and no run of it is kept). The BIOS is qboot, patched and built here as
[qboot/README.md](qboot/README.md) describes, with SeaBIOS shipped as the fallback.
