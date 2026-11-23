# Guest kernel

This directory holds a config file for the kernel's series (`config-<major>.<minor>-<arch>`) and
the patches this tree carries. The version is `versions.yaml`'s `kernel` entry, and a stable release
within the series builds from the same config. The kernel is here because a kernel and the
machine that boots it are released together. A new kernel does not strand checkpoints: a
resumed guest runs the kernel in its saved memory until it reboots, and boots this one then
(the machine's record, `machine.QEMU.Target`, leaves the kernel out). `spin-machine-dev build kernel`
builds it. `spin-machine-dev verify kernel` asserts that the stripped vmlinux, which the machine
enters through PVH, carries the two Xen notes QEMU enters and places it by and a filled relocation
table.

The build makes `vmlinux` and nothing else: no bzImage since #660. Nothing booted the bzImage
after #645, so it went, and `CONFIG_KERNEL_LZ4`, which compressed only it, went with it. The
kernel compression mode is Kconfig's default and compresses nothing that ships. A release from
then on has no `kernel/bzImage`, and spin's `hack/machine/place.go` must stop requiring it
(spin-stack/spin#1689). A release from v20261119.02 to v20261123.06 still boots its own bzImage
(`machine.OpenRelease`). The RFC's patch to the decompressor, which read the relocations from
`.data.reloc` (its 0018 here), went too: nothing compiles the decompressor.

## KASLR with no decompressor

The machine enters the vmlinux through PVH (#645). A bzImage's KASLR happens in its decompressor,
which PVH skips, and until 2026-11-19 the kernel entered that way ran where it was linked at every
boot. Now each half is chosen where the decompressor chose it before:

- **The virtual base**, by the kernel at `startup_64` (0017), from a relocation table the relocs
  tool writes into `.data.reloc` at link time (0014-0016). The slot rule is the decompressor's
  (2 MiB slots below 1 GiB, ~480 of them), and so are the seed's sources once 0020 reads RDRAND
  and the TSC from the CPUID registers that hold them: the RFC reads them swapped, so it drew the
  base from the i8254 and a hash of `boot_params` alone. The decompressor also hashes the build
  string, which is public.
- **The physical base**, by QEMU (`qemu/patches/0008`), from `XEN_ELFNOTE_PHYS32_RELOC`, among the
  2 MiB slots of low memory below the initrd and the ACPI data, drawn from the host's `getrandom`.
- **The direct map, vmalloc and vmemmap** (`RANDOMIZE_MEMORY`), by the kernel as before, once 0019
  sets `KASLR_FLAG`, which only the decompressor set.

`CONFIG_RELOCATION_TABLE_SIZE` reserves the table, 1 MiB here (the RFC's Kconfig defaults to 2 MiB
and its help says 1 MB "should be ok in most cases"). A table
that does not fit fails the link: the relocs tool dies naming the size it needs, and the build
prints what this kernel uses of it. At boot the table cannot overflow: it is part of the image.
`boot:kaslr` boots the kernel ten times and requires each of the four to move.


## Memory that moves

A VM given a ceiling gets a `virtio-mem` device covering the gap between its boot size and
that ceiling, with nothing plugged. The host grows and shrinks it over QMP. The machine has no
ACPI DIMM slots, because a DIMM can be added but, in practice, not removed. Unplugging one
needs the guest to offline a whole memory block, and a single unmovable page in the block makes
that fail. Measured on a 1 GiB VM with a 4 GiB ceiling: 0 → 3072 MiB and back to 2 MiB.

The kernel command line carries `memhp_default_state=online` for it. Without that option the
growth stops at the boot size, and nothing says so. Memory added and never onlined is memory
the guest cannot use but must still describe, so the driver declines to take more. Asked for
2048 and then 3072 MiB, it plugged 1024 and stayed there.

Separately, every VM gets a `virtio-balloon-pci` with `free-page-reporting=on`, which is
how a guest hands back memory it merely stopped using. The two answer different questions:
the balloon returns what is free, virtio-mem changes how much there is.

## BPF

The kernel carries what modern BPF development needs, and it did not before: `BPF_JIT`
(every program ran interpreted), `DEBUG_INFO_BTF` (without it there is no
`/sys/kernel/btf/vmlinux`, so no `vmlinux.h`, no `bpftool btf dump` and no CO-RE at all),
`FUNCTION_TRACER` for fentry/fexit attachment, `FTRACE_SYSCALLS`, and the networking program
types: `NET_CLS_BPF`, `NET_ACT_BPF`, `NET_SCH_INGRESS`, `XDP_SOCKETS`, `BPF_STREAM_PARSER`.
Verified in a guest: `/sys/kernel/btf/vmlinux` is 4,552,359 bytes and
`net.core.bpf_jit_enable` is 1.

It costs **+21.4 ms** of kernel boot, measured over five runs each against the same config
without those symbols (137.4 ms to 158.8 ms, kernel start to `Freeing unused kernel image`),
and 5.0 MB of image. 4.55 MB of that is the `.BTF` section, which `strip -s` keeps because
it is allocated and the guest reads it back out of its own image. `ftrace: allocating 41727
entries` is the new work that shows up in the log.

## Lockdown and the BPF LSM

The guest's root is not the kernel's. The kernel runs two LSMs, `lockdown` and `bpf`
(`CONFIG_LSM="lockdown,bpf"`):

- **Lockdown** is forced to *confidentiality* from the first instruction, and no write to
  `/sys/kernel/security/lockdown` lowers it. Root can then neither rewrite the running kernel
  (kexec, `/dev/port`, MSRs, ACPI table overrides) nor read it (kprobes, `bpf_probe_read_kernel`,
  perf on the kernel).
- **`/dev/mem` and `/proc/kcore`** are not built at all, rather than left to lockdown.
- **The BPF LSM** is there so a consumer can enforce policy in the guest with programs it loads
  before handing the machine to its tenant. Lockdown is what makes such policy hold: without
  it, root reads or rewrites the kernel those programs live in.

What enforcing the policy is for, and how, is the consumer's. `kernel/Dockerfile` checks after
olddefconfig that this kernel does both, and `task boot:lockdown` asks a booted guest.
`compat.yml` runs `boot:lockdown` on every kernel change on main and before every release.

Lockdown at confidentiality also takes away the tenant's kernel tracing: kprobes,
`bpf_probe_read_kernel` and perf on the kernel. User-space tracing and networking eBPF are
untouched, and so is a BPF LSM program that reads no kernel memory.

It takes tracefs too. The boot probes read the ring buffer and systemd, which lockdown leaves
alone: `spin-machine-dev lab run initcalls` reads `initcall_debug` from the ring buffer. Tracing
the kernel itself (function_graph, scheduler events) needs a kernel built with
`CONFIG_LOCK_DOWN_KERNEL_FORCE_NONE=y`, and the lab's ledger needs one for each release (#536).

So each release has a traceable kernel beside it, for the lab and nothing else. `release.yml`'s
`traceable` job builds this config with `KERNEL_LOCKDOWN=none`, which `Dockerfile` swaps for the
confidentiality option before olddefconfig and checks in its place. `spin-machine-dev release
traceable-kernel` then holds the result's config to the release kernel's: lockdown swapped, and
every other option the same. `parts.yml`'s kernel job builds it the same way on every change that
reaches the kernel, so a change that breaks it fails there, before a release. The job signs the bzImage as the release's files are signed and
publishes it on the release as `spin-machine-TAG-linux-x86_64.lab-only-traceable.bzImage`. It is
in no tarball and in nothing spin boots, and `lab tree --kernel traceable:TAG` checks its
signature before a tree boots it. Every kernel spin boots is built with
`KERNEL_LOCKDOWN=confidentiality`.

Under the archive's systemd, the console showed two lines at boot that read like missing
protection and are lockdown working:

```
bpf-restrict-fs: Failed to load BPF object: Invalid argument
Lockdown: init: use of bpf to read kernel RAM is restricted; see man kernel_lockdown.7
```

PID 1 loads its `RestrictFileSystems=` program at every start, whatever the units ask, and the
program reads each file's filesystem type with `bpf_probe_read_kernel`, which lockdown refuses
it. The image's systemd (`image/systemd/Dockerfile`) is built without the BPF framework, so it
asks for nothing lockdown refuses and neither line appears. `task boot:lockdown` holds that for an
image built with it (#261). What that costs is the directives the framework implements:
`RestrictFileSystems=`, `SocketBind*=` and `RestrictNetworkInterfaces=` do nothing in the guest,
and nothing here uses them. Confidentiality is kept on purpose. Integrity would give PID 1 its
helper back, and with it root's reads of the kernel's memory. Logins are unaffected (`task boot:logind`).

Measured 2026-10-01 with the probe of the time, `task boot:initcalls`, and `SPIN_KERNEL_B` set to the kernel without these
options: no cost that can be told from noise. Over 20 interleaved boots each, kernel start to
`Freeing unused kernel image` had a p50 of 196.3 ms with them and 208.3 ms without, on a host at
load 6-10, where the p95 was 342 ms.

## Changing it

`kernel/patches` holds the changes this tree makes to the kernel's source, applied in name
order. The gaps in their numbers are patches deleted once a run read nothing for them
(0006, 0009 and 0011, #345). They share one row in `docs/boot-time.md`, "What the machine
carries", which says what they earn together.

A patch is made, and a changed one remade, as `git format-patch -N` writes it: from a git tree
of the pinned tarball with the series applied, keeping each file's name, less the mbox
`From <commit>` line. Subjects say `[PATCH]`, not a place in a series that deleting a patch
makes stale. `gates/patches_test.go` holds this directory and `qemu/patches` to it (#487).

Before the patches are applied, `Dockerfile` runs the pinned tree's `checkpatch.pl --strict` over each, and after the build it runs sparse (`make C=2`, versions.yaml's `sparse`)
over every C file they touch, with and without them; a style problem or a sparse warning the
patches add fails the build (#653).

`patch --fuzz=0` refuses a patch that no longer applies. What it cannot see is a patch deleted
from the directory, or one whose effect a kernel bump took away while its text still applied:
that kernel builds, boots and is a few milliseconds slower. So `Dockerfile` reads each patch's
effect out of the vmlinux it built, before the symbol table is stripped, and fails the build
naming every patch it cannot find. It runs wherever the kernel is built: `spin-machine-dev
build kernel`, `parts.yml` and `compat.yml` on every change that reaches the kernel, and so on
every kernel bump.

| Patch | What the build asserts |
| --- | --- |
| 0001 | `vfs_read` has no call to `__fentry__`: objtool wrote the site as a NOP |
| 0002 | `ftrace_check_for_weak_functions` calls nothing: no workqueue, no scan queued |
| 0003 | `trace_eval_init` queues no work, and `trace_format_open` calls `trace_eval_ensure` |
| 0004 | `raw_pci_read` and `raw_pci_write` read `raw_pci_ext_ops` before `raw_pci_ops` |
| 0005 | `pci_setup_device` calls `pci_cache_capabilities` |
| 0007 | the image no longer holds `unwind_init`'s "Corrupt .orc_unwind table" message |
| 0008 | the compiled `kernel/trace/ftrace.c` takes a range out with `memmove(first, stop, ...)` |
| 0010 | `early_quirks` calls `kvm_para_available` before its scan |
| 0012 | `btf_vmlinux_defer` is in the vmlinux, and the compiled `kernel/bpf/btf.c` defers registrations through it |
| 0013 | the compiled `kernel/bpf/verifier.c` asks for vmlinux BTF through `need_btf_vmlinux(env)` |
| 0014-0016 | `.data.reloc` holds a table the relocs tool filled; how much of the reserve it uses is printed |
| 0017 | `startup_64` calls `__pi___relocate_kernel`, and the object it is built in (`arch/x86/boot/startup/kaslr.o`, with `lib/kaslr.c` and `lib/cmdline.c` included) has no `__fentry__`, KASAN, KCOV, UBSAN or stack-protector call and no GOT relocation: it runs before the kernel has moved (virt-pvm/linux#11 is the failure a `__fentry__` call through the GOT made) |
| 0019 | the compiled `arch/x86/boot/startup/kaslr.c` sets `KASLR_FLAG` |
| 0020 | the compiled `arch/x86/boot/startup/kaslr.c` reads RDRAND from CPUID leaf 1's ECX |
| 0021 | `enable_IO_APIC` does not call `clear_IO_APIC`: each pin is cleared from the read its ExtINT search made |
| 0022 | `pci_bus_add_device` calls `pci_find_host_bridge`, the gate on its state save |
| 0023 | `vp_modern_probe` calls `pci_find_capability` once |
| 0024 | the compiled `drivers/virtio/virtio_pci_modern_dev.c` writes `queue_select` only when `selected_queue` differs |
| 0025 | the compiled `arch/x86/kernel/apic/msi.c` compares the vector and the destination before the message write |

None of them is a guest check. What the patches save is CPU and VM exits, which a guest cannot
count, and the format files 0003 defers are behind lockdown. What 0012 and 0013 save is a parse of
vmlinux BTF that, once made, gives an object id a guest's `bpftool btf list` shows; the image
carries no bpftool, so that check is a person's. The host can count exits: 0004 and
0010 also depend on the running machine (0004 on `raw_pci_ext_ops`, set from the ACPI MCFG
table, 0010 on KVM's CPUID leaf), so `boot:patches` reads KVM's `io_exits` through QEMU's
`query-stats` after a cold boot to userspace and holds them under a budget that fails without
either (`TestPCIConfigurationCostsNoPortExits`, #486). A new patch adds its row here and
its assertion in that step, and shows it red on a branch without the patch.

A change to the config is a new kernel for every guest's next boot, and the release notes say
what changed (`internal/release` lists the options turned on, off or changed). It does not
move the machine's record, and checkpoints keep resuming: each resumed guest goes on running the
kernel it was saved with until it reboots.

`Dockerfile` builds with a pinned toolchain, because a different compiler is a different
`vmlinux`: Debian trixie by image digest, and its packages from snapshot.debian.org at a fixed
date.
Both are `versions.yaml` entries (`debian`, `debian-snapshot`), and they move on purpose, not
when Debian publishes a point release.

The build is reproducible: two builds from the same inputs produce the same `vmlinux`,
byte for byte. That needs trixie's pahole 1.30, which is deterministic with `-j`. Bookworm's
1.24 encodes `.BTF` in whatever order make's `-j` threads finish, which gives every release a
different kernel whether or not its source changed.
