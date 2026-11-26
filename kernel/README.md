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

- **Lockdown** is forced to *integrity* from the first instruction, and the machine's command line
  raises every guest to *confidentiality* (`lockdown=confidentiality`, `machine.Cmdline`) unless
  it runs an enforcer that needs integrity. Lockdown only rises: no command line and no write to
  `/sys/kernel/security/lockdown` takes it below the level the kernel forces, and a write cannot
  take it below the one the command line asked for (security/lockdown/lockdown.c,
  `lock_kernel_down`). At integrity root cannot rewrite the running kernel (kexec, `/dev/port`,
  MSRs, ACPI table overrides); at confidentiality it cannot read it either (kprobes,
  `bpf_probe_read_kernel`, perf on the kernel, tracefs).
- **`/dev/mem`, `/dev/port` and `/proc/kcore`** are not built at all, rather than left to lockdown.
- **The BPF LSM** is there so a consumer can enforce policy in the guest with programs it loads
  before handing the machine to its tenant. Lockdown is what makes such policy hold: without
  it, root reads or rewrites the kernel those programs live in.

### The floor, the default and the choice

A guest runs at confidentiality: `machine.Cmdline`'s zero value puts `lockdown=confidentiality` on
the line, so a consumer that builds its own command line and has never heard of lockdown gets it
too, and a workspace without an enforcer keeps the protection every release before #734 forced in
the kernel. A shirudo machine (`Cmdline.Lockdown` `LockdownShirudo`, spin-machine's
`--lockdown=shirudo`) leaves `lockdown=` out and runs at the kernel's floor, integrity. That is
for a guest whose BPF LSM enforcer reads kernel memory: shirudo's programs read it through about
140 `BPF_CORE_READ`s, each a `bpf_probe_read_kernel()`, and at confidentiality the kernel refuses
the helper (`LOCKDOWN_BPF_READ_KERNEL`), so they do not load (#734). A signed-programs exception
was read in the source and ruled out before any patch (#734's last comment): shirudo loads the
objects that hold the reads without a signature, and every gen_loader loader calls the helper
itself.

What a shirudo machine trades: from the first instruction until shirudo's `init` seals, root can
read the kernel's memory through BPF and tracing, which confidentiality would refuse. That window
is the boot up to `init`, which runs from the measured initrd (shirudo's A6), before the workspace
is handed to anyone. From the seal on, shirudo refuses an unsigned BPF program load to every
caller, `init` included (shirudo Documentation/internals/enforcement.rst:253-254), and its
`lsm/locked_down` refuses what integrity refuses whatever the command line says. Root's other
kernel reads that confidentiality refuses, kprobes, perf on the kernel and tracefs, are shirudo's
policy's to refuse or not; this machine does not check what it does with them. The rewrites stay
refused by the kernel: integrity is the floor.

A shirudo machine also leaves the `.bpf` and `.dm-verity` keyrings unsealed
(`bpf.keyring_unsealed=1`, `dm_verity.keyring_unsealed=1`). Without them the kernel seals both
empty at boot (kernel/bpf/keys.c, drivers/md/dm-verity-verify-sig.c), shirudo's `init` cannot
enroll its certificates, and it fails (shirudo Documentation/operate/plan/system-requirements.rst).
Until `init` seals them, root can add keys to both, and an unsealed `.bpf` keyring has the
verifier refuse a signed program its caller's own keyring (`bpf_keyring_enforced`). Every other
machine keeps both sealed: unsealed with no enforcer to seal them, they would stay open to root.
Integrity and the keyrings are one choice, `machine.Lockdown`, so neither comes without the other.

`machine.Spec` validation refuses a `lockdown=` or either keyring parameter in `Cmdline.Extra`,
since the kernel keeps the higher of two levels and the last of two parameters, and one there would
silently undo the choice. The choice is the booted guest's, and not in the machine's record: a
checkpoint resumes the guest it saved, at the level and with the keyrings it booted with
(docs/machine.md, "The kernel command line").

What enforcing the policy is for, and how, is the consumer's. `kernel/Dockerfile` checks after
olddefconfig that this kernel does both and forces integrity, and `task boot:lockdown` asks a
booted guest of each kind: one at confidentiality and one at integrity.
`compat.yml` runs `boot:lockdown` on every kernel change on main and before every release.

Lockdown at confidentiality, every guest's but an enforcer's, also takes away the tenant's kernel tracing: kprobes,
`bpf_probe_read_kernel` and perf on the kernel. User-space tracing and networking eBPF are
untouched, and so is a BPF LSM program that reads no kernel memory.

It takes tracefs too. The boot probes read the ring buffer and systemd, which lockdown leaves
alone: `spin-machine-dev lab run initcalls` reads `initcall_debug` from the ring buffer. Tracing
the kernel itself (function_graph, scheduler events) needs a kernel built with
`CONFIG_LOCK_DOWN_KERNEL_FORCE_NONE=y`, and the lab's ledger needs one for each release (#536).

So each release has a traceable kernel beside it, for the lab and nothing else. `release.yml`'s
`traceable` job builds this config with `KERNEL_LOCKDOWN=none`, which `Dockerfile` swaps for the
integrity option before olddefconfig and checks in its place. `spin-machine-dev release
traceable-kernel` then holds the result's config to the release kernel's, which is this file to
the line: lockdown swapped, and every other option the same. `parts.yml`'s traceable job builds it
the same way on every change that reaches the kernel, so a change that breaks it fails there,
before a release. The job signs the bzImage as the release's files are signed and
publishes it on the release as `spin-machine-TAG-linux-x86_64.lab-only-traceable.bzImage`. It is
in no tarball and in nothing spin boots, and `lab tree --kernel traceable:TAG` checks its
signature before a tree boots it. Its boots pass `--lockdown=kernel` when the tree's
spin-machine would put `lockdown=` on the line, so the kernel stays at none, and the tree is
refused if its command line would carry one anyway (`internal/lab`, `keepUnlocked`). Every kernel
spin boots is built with `KERNEL_LOCKDOWN=integrity`.

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
and nothing here uses them. Confidentiality is every guest's on purpose. Integrity would give PID 1
its helper back, and with it root's reads of the kernel's memory, so only a machine whose enforcer
needs the helper runs at it. Logins are unaffected (`task boot:logind`).

Measured 2026-10-01 with the probe of the time, `task boot:initcalls`, and `SPIN_KERNEL_B` set to the kernel without these
options: no cost that can be told from noise. Over 20 interleaved boots each, kernel start to
`Freeing unused kernel image` had a p50 of 196.3 ms with them and 208.3 ms without, on a host at
load 6-10, where the p95 was 342 ms.

## Modules

A workspace can load more of the kernel than it boots with, but only what spin-stack signed (#736).
Everything a workspace boots with is built in. The modules are what it may ask for later:
wireguard, fuse, squashfs and erofs, and the crypto library wireguard brings. Each
loads when its feature is asked for, never at boot (docs/image.md, "Kernel modules").
`CONFIG_VIRTIO_FS` is off, built in until #736: the machine has no virtio-fs device, and as a
module systemd's PID 1 loads it, and fuse with it, in every boot of a guest with a virtio PCI
device (systemd 259's `src/core/kmod-setup.c`, `may_have_virtio`), on the first login's clock.

- **Signed only.** `CONFIG_MODULE_SIG_FORCE` refuses a module without a valid signature by a
  trusted key, at every lockdown level, the traceable kernel's none included. Lockdown at
  integrity would refuse an unsigned one anyway (`LOCKDOWN_MODULE_SIGNATURE`), but the kernel
  refuses first and lockdown is never asked. A tenant's root can build a module, and the kernel
  says `Loading of unsigned module is rejected` or `Loading of module with unavailable key is
  rejected` and loads nothing. `CONFIG_MODULE_UNLOAD` is off, so nothing loaded is taken out.
- **One trusted key.** The kernel builds in one certificate (`CONFIG_SYSTEM_TRUSTED_KEYS`):
  `module-signing.crt`, the public half of spin-stack/ci's KMS key
  `alias/spin-machine-module-signing` (module_signing.tf). That key is RSA-4096. A PKCS #1 v1.5
  signature is deterministic, so a release's signed modules are reproducible (#732). The kernel
  verifies a builtin certificate's self-signature before it trusts the key
  (`x509_check_for_self_signed`), so the certificate takes one signature by the key, which only
  this repository's `release` environment may ask KMS for: `module-certificate.yml` runs
  `spin-machine-dev certificate` there and proposes what it writes. `kernel/Dockerfile` refuses a
  tree without the certificate or a config that does not name it, and `spin-machine-dev verify
  kernel` refuses a vmlinux that does not carry it.
- **The build never signs.** `CONFIG_MODULE_SIG_ALL` is off and `CONFIG_MODULE_SIG_KEY` is empty,
  so the build makes no key and signs nothing, and `kernel/Dockerfile` reads that off what came
  out. A pull request's, a laptop's and a release's vmlinux are the same file, and a module built
  anywhere but a release loads nowhere.
- **A release signs.** `spin-machine-dev release` builds the kernel, has KMS sign each module's
  SHA-256 digest and appends the signature as `scripts/sign-file` does (`internal/modsign`), then
  builds the image, which installs them. A module is stripped of its debug info before that, by
  `modules_install`, since a strip after would cut the signature off. A third-party module spin-stack
  approves is signed the same way, from the same environment, when the first one comes.

What `CONFIG_MODULES` brings with it gives root nothing lockdown or this config takes away. The
reads and writes of the kernel's memory stay out (`DEVMEM`, `DEVPORT`, `PROC_KCORE`, `KEXEC`, which
`kernel/Dockerfile` checks), `CONFIG_MODULE_FORCE_LOAD` is off, and `CONFIG_STRICT_MODULE_RWX`
keeps a module's code read-only. shirudo's programs do not touch module loading: its
`lsm/kernel_load_data` and `lsm/kernel_read_file` refuse only kexec images, and its
`lsm/locked_down` refuses `LOCKDOWN_MODULE_SIGNATURE`, which this kernel never asks, as
`MODULE_SIG_FORCE` decides first. Its `module_signatures` pre-flight check passes on a kernel that
builds `mod_verify_sig`, and it does not check which keys the kernel trusts. A consumer's BPF LSM
program can still refuse a load (`kernel_module_request`, `kernel_read_file`), and signed modules
change nothing for it.

## shirudo's hooks

shirudo (shirudo-dev/shirudo) enforces policy in a guest with BPF LSM programs and keeps its
measurement register in a TPM. The kernel hooks it attaches to are not upstream yet, and shirudo
carries them in its `testing/kernel/patches`; 0026-0031 are those, each under its own Subject
with a note saying where it comes from. Its `kbuild` and `riscv` patches are not carried, each
for the reason `internal/build/shirudo.go` gives.

`versions.yaml`'s `shirudo` pins the commit they are read from. `spin-machine-dev bump shirudo`
moves it and compares upstream's patches with these by Subject, and fails naming each one a
person has to answer: a carried patch upstream rewrote or no longer has, and an upstream patch
neither carried nor declined. A carried patch is remade from upstream's as every patch here is,
below.

The kernel has the TPM driver 0027 hooks (`CONFIG_TCG_TPM`, with `CONFIG_TCG_CRB` and
`CONFIG_TCG_TIS` for QEMU's `tpm-crb` and `tpm-tis`, which #719 measures against each other), so
sparse and the effect check read the file the patch changes. Without a TPM device it binds to
nothing. 7.4 has the BPF signed loader's ABI shirudo loads its programs through.

The kernel also has every other option in shirudo's "Kernel configuration" table
(`Documentation/operate/plan/system-requirements.rst`), and `kernel/Dockerfile` checks each after
olddefconfig. Most were on already for BPF, ftrace and the base image; #720's five were not:
device-mapper and dm-verity, which map shirudo's runtime and config images, with the check of a
config image's signed root hash (`CONFIG_DM_VERITY_VERIFY_ROOTHASH_SIG`, which also makes the
`.dm-verity` keyring), and `CONFIG_SYSTEM_DATA_VERIFICATION` and `CONFIG_CRYPTO_MLDSA`, which verify
the PKCS#7 ML-DSA-87 signatures of its loaders and that root hash. shirudo's `CONFIG_MODULE_SIG` row
asks for module signatures of a kernel that loads modules, and this one has them, signed-only
("Modules"). A
shirudo machine runs at integrity (`LockdownShirudo`, #734), since confidentiality
withholds `bpf_probe_read_kernel`, which shirudo's programs call. Still missing for shirudo to run:
a TPM in the machine (#719), and with it a booted shirudo, whose `shirudo init --check-only` says
whether its programs load at integrity as the source reading predicts.

Booting shirudo on this kernel found four more that its flow needs beyond that table (#740), and
`kernel/Dockerfile` checks them too. `CONFIG_RD_ZSTD`: dracut's default initrd is zstd, and without
it the kernel cannot unpack it and boots the root without shirudo's initrd. `CONFIG_FPROBE`: the
`kprobe.multi` and `kretprobe.multi` upatches attach through it, and fail with `EOPNOTSUPP`
without it. `CONFIG_FUNCTION_ERROR_INJECTION`: `fmod_ret` on the functions marked
`ALLOW_ERROR_INJECTION`, rather than only on syscalls and `security_*`. And
`CONFIG_BLK_DEV_WRITE_MOUNTED` off, from shirudo's hardening checklist: its seal acts on file
operations, and a write to the block device under `/var/lib/shirudo` goes around it; the kernel
now refuses opening a mounted filesystem's device for writing. olddefconfig's new prompts stay off:
`CONFIG_FPROBE_EVENTS`, tracefs' fprobe events, which shirudo does not use, and
`CONFIG_BPF_KPROBE_OVERRIDE`, `bpf_override_return()` from a kprobe program, which shirudo's
table lists as optional and no flow here has needed.

Lockdown governs none of the BPF use these give, at either level, and they give root nothing the
kernel already gave it. A kprobe.multi program reads the registers at a function's entry or
return; an `fentry` program, there already, reads the same arguments and the kernel structures
they point to, and at confidentiality the kernel refuses neither (only the helper
`bpf_probe_read_kernel`, `LOCKDOWN_BPF_READ_KERNEL`). fprobe-based tracefs events would be refused
at confidentiality (`LOCKDOWN_KPROBES`), and they are not built. `fmod_ret` skips a function and
chooses its return; root could already do it on every syscall and every `security_*` hook, and
error injection moves the set to the functions the kernel marks safe to fail plus `security_*`.
The debugfs `fail_function` needs `CONFIG_FAULT_INJECTION`, which is off.

## Changing it

`kernel/patches` holds the changes this tree makes to the kernel's source, applied in name
order. The gaps in their numbers are patches deleted once a run read nothing for them
(0006, 0009 and 0011, #345). They share one row in `docs/boot-time.md`, "What the machine
carries", which says what they earn together.

A patch is made, and a changed one remade, as `git format-patch -N` writes it: from a git tree
of the pinned tarball with the series applied, keeping each file's name, less the mbox
`From <commit>` line. Subjects say `[PATCH]`, not a place in a series that deleting a patch
makes stale. `gates/patches_test.go` holds this directory and `qemu/patches` to it (#487).

Its code is written to the kernel's coding style,
[Documentation/process/coding-style.rst](https://raw.githubusercontent.com/torvalds/linux/refs/heads/master/Documentation/process/coding-style.rst), and its
changelog to `submitting-patches.rst`. `checkpatch.pl --strict`, below, checks the mechanical
part of the style; naming, function size, what a comment says and when a macro is worth it are
the reviewer's, against that document.

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
| 0026 | `cgroup_attach_task` and `cgroup_can_fork` call `security_task_cgroup_attach` |
| 0027 | `tpm_common_write` calls `security_tpm_command` |
| 0028 | the compiled `fs/bpf_fs_kfuncs.c` returns a `security.bpf.` label without the calling task's permission |
| 0029 | the compiled `kernel/bpf/bpf_lsm.c` lets a sleepable program attach to `inode_mkdir` |
| 0030 | the compiled `kernel/bpf/verifier.c` trusts `dentry->d_parent` |
| 0031 | `mm_access` calls `security_mm_read_access` |

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
