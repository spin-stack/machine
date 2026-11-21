# spin-machine

[![CI](https://github.com/spin-stack/spin-machine/actions/workflows/ci.yml/badge.svg)](https://github.com/spin-stack/spin-machine/actions/workflows/ci.yml)
[![Parts](https://github.com/spin-stack/spin-machine/actions/workflows/parts.yml/badge.svg)](https://github.com/spin-stack/spin-machine/actions/workflows/parts.yml)

A virtual machine: QEMU, the guest kernel, the base image, and the definition of the machine
they make. spin boots every workspace from it.

**The goal is a cold boot to the first SSH login in under 100 ms.** The second number is a
checkpoint restored to the same login (AGENTS.md). [docs/boot-time.md](docs/boot-time.md) says
where both stand and where their time goes, and [docs/releases.md](docs/releases.md) has each
release's numbers.

## Why one repository

A checkpoint resumes only on the same hardware it was saved on, and QEMU does not check that.
So the machine's identity is its contract, and `machine.CanResume` refuses a checkpoint saved on
another one. `machine.Spec.Fingerprint` hashes the versioned
machine type (`machine.MachineType`), the shape, every device as the command line gives it, and
the BIOS's size. Two machines with different fingerprints do not take each other's checkpoints.
Two with the same one do, where the QEMU is as new as the one that saved the checkpoint and the
host can show the guest every CPU feature it was shown (docs/migration.md). A new QEMU of the
same machine type, a new kernel, or new firmware of the same size is the same machine, and
`task boot:compat` holds every release to that. One repository and one version keep the four parts from drifting apart.

## What a release is

One tarball, laid out as `spin-machine-dev build` lays out `_output/`:

| path under `usr/share/spin-stack/` | |
|---|---|
| `bin/qemu-system-x86_64` | static and KVM only. It refuses to emulate, on purpose |
| `bin/qemu-system-x86_64-tcg` | for CI, which has no `/dev/kvm` |
| `bin/qemu-img` | |
| `bin/mkfs.ext4`, `bin/debugfs`, `bin/e2fsck`, `bin/dumpe2fs`, `e2fsprogs/mke2fs.conf` | static e2fsprogs, and the defaults an ext4 for this kernel is made with |
| `qemu/qboot.bin` and the other ROMs | qboot.bin is the BIOS a machine boots with |
| `qemu/patches/`, `qemu/qboot-*.patch`, `qemu/qboot-COPYING` | the changes this tree makes to QEMU and qboot, shipped with the binaries built from them |
| `kernel/bzImage`, `kernel/vmlinux`, `kernel/kernel-config` | the kernel a machine boots, and the same kernel as an ELF |
| `kernel/sources` | every source and header the kernel compiled, which `spin-machine-dev cves` judges CVEs against |
| `kernel/patches/` | the changes this tree makes to the kernel |
| `image/rootfs.qcow2` | the base image, read-only (0444) |
| `machine.env` | the version, the commit, the QEMU, kernel and e2fsprogs versions, the checksums of QEMU, `vmlinux`, `qboot.bin` and the base image, and `fingerprint_sha256` |
| `SOURCES` | every upstream source by version, URL and SHA-256, and the written offer |
| `packages.txt` | every package and exact version in the base image |
| `licenses.txt` | the copyright and licence text of each package in the base image |

`LICENSE` and `NOTICE` sit at the tarball's root. `machine.OpenRelease` reads a release and a
build directory alike. The two share one layout, so no translation between them can lose a file.
`spin-machine`, the tool that boots one by hand, is built into `_output/bin/` and not shipped.

## Boot a machine from Go or by hand

```go
rel, err := machine.OpenRelease("/usr/share/spin-stack")  // says which file is missing, if one is
spec := rel.Spec()                                        // QEMU, Kernel, Firmware
// … the caller's initrd, memory, CPUs, disks, monitors
args, err := spec.Args()          // the QEMU command line
fp, err := spec.Fingerprint()     // which checkpoints this machine may resume
```

By hand, `spin-machine` boots a machine, prints its command line or fingerprint, attaches,
detaches or saves on a running one, grows or shrinks its vCPUs and memory, and restores a saved
one (`spin-machine <command> -h`). To run one command in a throwaway guest:

```sh
printf '\n\n\n\n\n\n\n\n\n\nmount -t proc proc /proc\necho hello from $(uname -r)\npoweroff -f\n' |
  _output/bin/spin-machine boot --release _output --init /bin/sh --console stdio --memory 1024 --cpus 2
```

AGENTS.md, "Trying the machine", says why the blank lines matter and covers the rest of driving
a guest. A guest proves things about the guest, never about its host. The machine hides `vmx`
and `svm` from the CPU it shows, and the kernel has no KVM, so nothing inside a guest can nest.

## Build, test and release

```
go tool task bin                      # spin-machine-dev, into _output/bin/
spin-machine-dev build                # everything that is missing or stale, into _output/
spin-machine-dev shell                # boot the machine and look around inside it
go tool task lint                     # gofmt, vet, and go-tools' gates
go tool task test                     # the machine definition and the verbs
spin-machine-dev release              # one tarball, one version
```

`spin-machine-dev` builds, verifies, measures and releases the machine (`docs/cli.md`). Each
part's build is its directory's Dockerfile, so building needs a container engine. Booting needs
only read-write access to `/dev/kvm`. Without it, `--accel tcg` reaches the first login about 17 times
slower (7.6 s against 455 ms, lab 44848049631, 2026-11-21), and nothing chooses TCG silently. Every version, digest and commit taken from outside is pinned in
`versions.yaml`. `spin-machine-dev outdated` says what is behind, and `spin-machine-dev bump
<entry>` moves one entry.

## Where to read next

| | |
|---|---|
| [AGENTS.md](AGENTS.md) | how to work here: the goal, what must not break, and how a change is decided |
| [docs/boot-time.md](docs/boot-time.md) | where the two goal numbers stand, where their time goes, and what was kept, rejected and is open |
| [docs/measuring.md](docs/measuring.md) | how to measure a boot-time change: the steps, the rules a comparison follows, and the pitfalls |
| [docs/releases.md](docs/releases.md) | each release's numbers, measured on the bench |
| [docs/log.md](docs/log.md) | what was tried, when, why, and what it measured |
| [docs/cli.md](docs/cli.md) | `spin-machine-dev`: building, verifying, measuring, releasing |
| [docs/machine.md](docs/machine.md) | the definition: fixed slots, vmgenid, and what the fingerprint hashes |
| [docs/migration.md](docs/migration.md) | one VM moved to another host, and why the CPU model decides it |
| [docs/memory-and-disk.md](docs/memory-and-disk.md) | what a guest's page cache, freed memory and disk cost its host, and what the host must set |
| [docs/releasing.md](docs/releasing.md) | CI, versions, and what a release owes its upstreams |
| [qemu/](qemu/README.md), [kernel/](kernel/README.md), [image/](image/README.md), [e2fsprogs/](e2fsprogs/README.md) | why each part is built the way it is |

## Licence

Apache-2.0. The qboot patches carry context from qboot under GPL-2.0, and `NOTICE` says so. The
licence covers the recipes, not what they build. A release is mostly GPL and LGPL binaries, and
[docs/releasing.md](docs/releasing.md#what-a-release-owes) says how a release answers for them.
