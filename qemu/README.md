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
| 0003 | The migration capability `x-defer-disk-flush`: from a checkpoint's stop until the guest runs, QEMU's own flushes stop at the page cache. The stop starts the deferral itself, after its drain, when its run state is `finish-migrate`; postcopy and CPR never defer (#488) | the busy-disk stop −22.0 [−29.2, −15.8] (lab 44962720682, `docs/memory-and-disk.md`, "A busy host disk") | `verify qemu` (the capability in the schema); `verify args`: a save's stop submits a flush to the host's disk without the capability and none with it |
| 0004 | `query-blockstats` `x-flat`: a device's counters without its backing chain | DiskUsage's main-loop time 293 → 96 µs, −197 [−202, −181] (lab 44973302743) | `verify qemu` (the member); `verify args`: no `parent` or `backing` under x-flat, both without |
| 0005 | A machine made for an incoming migration writes its TSC only from the migration, so KVM takes the checkpoint's TSC as given (#452) | correctness: without it a guest saved with a timer due sleeps until its old TSC deadline, the restored p95 539-548 ms (lab 44987412171) | `boot:patches`: a guest restored 650 ms ahead of its new vCPU's TSC goes no more than 200 ms without a line (#472) |

`docs/boot-time.md`, "What the machine carries", says what each earns among everything else the
machine carries. The qemu entry of `versions.yaml` says what a bump runs.

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

**A bzImage, through qboot.** QEMU puts the kernel's setup and payload in fw_cfg and qboot
loads and enters them itself. The bzImage's decompressor is where KASLR picks the kernel's
random base. Until 2026-11-19 the machine entered an ELF `vmlinux` through its Xen PVH notes
and `pvh.bin`, which skips that decompressor and left the kernel at the same address on every
boot. There is no bootloader and no UEFI: the same guest under UEFI + Secure Boot was measured
at +356 ms and rejected, before this repository existed (the number came with it, 2afac47,
2026-09-07, and no run of it is kept). The BIOS is qboot, patched and built here as
[qboot/README.md](qboot/README.md) describes, with SeaBIOS shipped as the fallback.
