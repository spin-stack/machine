# QEMU

One build, **statically linked**, against musl on Alpine. `spin-machine-dev build qemu` builds it from
source (tens of minutes). `spin-machine-dev fetch qemu` unpacks the published build of the pinned
version (seconds). `devices.mak` is the device allowlist, and `patches/` holds the changes this
tree makes to QEMU's source: 0001 builds the memory map once for device creation, 0002 gives
virtio-scsi a `max_target` property, and 0003 adds the migration capability `x-defer-disk-flush`,
which keeps a checkpoint's stop off the host's disk. 0005 has a machine made for an incoming
migration write its TSC only from the migration, so that KVM takes the checkpoint's TSC as given
(#452).
`docs/boot-time.md`, "What the machine carries", says what 0001 and 0002 earn, and
`docs/memory-and-disk.md`, "A busy host disk", what 0003 does.

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
