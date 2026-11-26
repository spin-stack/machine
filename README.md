# spin-machine

[![CI](https://github.com/spin-stack/machine/actions/workflows/ci.yml/badge.svg)](https://github.com/spin-stack/machine/actions/workflows/ci.yml)
[![Parts](https://github.com/spin-stack/machine/actions/workflows/parts.yml/badge.svg)](https://github.com/spin-stack/machine/actions/workflows/parts.yml)

A virtual machine: QEMU, the guest kernel, the base image, and the definition of the machine
they make. spin boots every workspace from it.

**The goal is a cold boot to the first SSH login in under 100 ms.** The second number is a
checkpoint restored to the same login (AGENTS.md). [docs/boot-time.md](docs/boot-time.md) says
where both stand and where their time goes, and [docs/releases.md](docs/releases.md) has each
release's numbers.

## Why one repository

A checkpoint resumes only on the same hardware it was saved on, and QEMU does not check all of
it: it loads a checkpoint into a machine with an extra device, a larger BIOS or more base RAM.
So a checkpoint carries the record of the machine it was taken on, read from the running QEMU
(`machine.QEMU.Target`): its version, its CPU, every device and its properties, its options and
its RAM blocks. `machine.CanResume` refuses it on a machine whose record differs and names each
difference (docs/machine.md). A new kernel, or new firmware of the same size, is the same
machine, and so is a new QEMU of the same machine type that reports the same machine;
`task boot:compat` holds every release to that. One repository and one version keep the four
parts from drifting apart.

## Layout

`machine/` and `qmp/` are the public API. spin imports both, and nothing else here is meant to be
imported. Every other directory is this repository's own and changes without notice.

| directory | what it is |
|---|---|
| [`machine/`](machine/) | the machine as Go: the `Spec` and its QEMU command line, the record a checkpoint is held to, and the `VM` that boots, resumes and checkpoints one |
| [`qmp/`](qmp/) | QEMU's machine protocol: the `Client` with `Do`, `Expect` and `Last`, and the commands and block nodes spin sends |
| [`cmd/spin-machine/`](cmd/spin-machine/) | the tool that boots and drives one machine by hand |
| [`cmd/spin-machine-dev/`](cmd/spin-machine-dev/) | the tool that builds, verifies, measures and releases the machine ([docs/cli.md](docs/cli.md)) |
| [`internal/`](internal/) | what `spin-machine-dev` is made of: the builds, the release, its notes, the lab and the CVE and OSV checks |
| [`boot/`](boot/) | the probes that check and measure a boot from the host: tests run as Task targets, no package, and `probeinit`, their diagnostic PID 1 |
| [`qemu/`](qemu/README.md) | QEMU and qboot: the build, the device list and the patches |
| [`kernel/`](kernel/README.md) | the guest kernel: the build, the config and the patches |
| [`image/`](image/README.md) | the base image, and the systemd it installs |
| [`e2fsprogs/`](e2fsprogs/README.md) | the static `mkfs.ext4`, `e2fsck`, `dumpe2fs` and `debugfs` that make and check the image |
| [`builder/`](builder/) | the Alpine image the static parts are compiled on |
| [`gates/`](gates/) | the tests that hold the whole repository to its rules: every pin in `versions.yaml`, every published artefact attested, every probe offered by lab.yml |
| [`docs/`](docs/) | how the machine is measured, defined, migrated and released |

## What a release is

One tarball, laid out as `spin-machine-dev build` lays out `_output/`:

| path under `usr/share/spin-stack/` | |
|---|---|
| `bin/qemu-system-x86_64` | static and KVM only. It refuses to emulate, on purpose |
| `bin/qemu-system-x86_64-tcg` | for CI, which has no `/dev/kvm` |
| `bin/qemu-img` | |
| `bin/swtpm` | static. The TPM of a machine that asks for one (`machine.TPM`), one beside each QEMU |
| `bin/passt` | static. The network of a NIC on passt (`machine.NIC.Passt`, `spin-machine boot --net`), one beside each QEMU |
| `guest/shirudo` | static, for a guest. The shirudo the kernel's hooks come from (kernel/README.md, "shirudo's hooks"), which an operator builds a shirudo machine's initrd and runtime image around |
| `bin/spin-machine` | static. The tool that boots a machine of this release by hand (below), built from the release's commit |
| `bin/mkfs.ext4`, `bin/debugfs`, `bin/e2fsck`, `bin/dumpe2fs`, `e2fsprogs/mke2fs.conf` | static e2fsprogs, and the defaults an ext4 for this kernel is made with |
| `qemu/qboot.bin` and the other ROMs | qboot.bin is the BIOS a machine boots with |
| `qemu/patches/`, `qemu/qboot-*.patch`, `qemu/qboot-COPYING` | the changes this tree makes to QEMU and qboot, shipped with the binaries built from them |
| `kernel/vmlinux`, `kernel/kernel-config` | the kernel a machine boots, through PVH, and its configuration. No `kernel/bzImage` since #660 |
| `kernel/sources` | every source and header the kernel compiled, which `spin-machine-dev cves` judges CVEs against |
| `kernel/patches/` | the changes this tree makes to the kernel |
| `image/rootfs.qcow2` | the base image, read-only (0444) |
| `machine.env` | the version, the commit, the QEMU, kernel and e2fsprogs versions, and the checksums of QEMU, `vmlinux`, `qboot.bin` and the base image |
| `SOURCES` | every upstream source by version, URL and SHA-256, and the written offer |
| `packages.txt` | every package and exact version in the base image |
| `licenses.txt` | the copyright and licence text of each package in the base image |
| `go-licenses.txt` | the licence text of the Go code linked into `bin/spin-machine` |
| `shirudo-licenses.txt` | the licence text of everything linked into `guest/shirudo` |

`LICENSE` and `NOTICE` sit at the tarball's root. `machine.OpenRelease` reads a release and a
build directory alike. The two share one layout, so no translation between them can lose a file.

## Boot a machine from Go or by hand

From Go, [`machine/example_test.go`](machine/example_test.go) is the whole flow spin runs, as
code that compiles. It opens a release, fills a `Spec`, starts QEMU from `Spec.Args`, attaches,
boots or resumes, attaches a disk and takes a checkpoint. The package doc
(`go doc ./machine`) lists the same steps.

By hand, `spin-machine` boots a machine, prints its command line, attaches,
detaches or saves on a running one, grows or shrinks its vCPUs and memory, and restores a saved
one (`spin-machine <command> -h`). A release tarball carries it, so a machine boots from one with
nothing built. To run one command in a throwaway guest:

```sh
tar -xzf spin-machine-vYYYYMMDD.NN-linux-x86_64.tar.gz
cd spin-machine-vYYYYMMDD.NN-linux-x86_64/usr/share/spin-stack
printf '\n\n\n\n\n\n\n\n\n\nmount -t proc proc /proc\necho hello from $(uname -r)\npoweroff -f\n' |
  bin/spin-machine boot --release . --init /bin/sh --console stdio --memory 4096 --cpus 2
```

In a checkout, `spin-machine-dev build` lays the same tree out in `_output/`: `_output/bin/spin-machine
boot --release _output` boots this checkout's machine.

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
| [docs/log](docs/log) | what was tried, when, why, and what it measured: one file an entry, `spin-machine-dev docs log` |
| [docs/experiments](docs/experiments) | the rows of boot-time.md's tables, one record each, `spin-machine-dev docs tables` |
| [docs/cli.md](docs/cli.md) | `spin-machine-dev`: building, verifying, measuring, releasing |
| [docs/machine.md](docs/machine.md) | the definition: fixed slots, vmgenid, and the record a checkpoint is held to |
| [docs/migration.md](docs/migration.md) | one VM moved to another host, and why the CPU model decides it |
| [docs/memory-and-disk.md](docs/memory-and-disk.md) | what a guest's page cache, freed memory and disk cost its host, and what the host must set |
| [docs/releasing.md](docs/releasing.md) | CI, versions, and what a release owes its upstreams |
| [qemu/](qemu/README.md), [kernel/](kernel/README.md), [image/](image/README.md), [e2fsprogs/](e2fsprogs/README.md) | why each part is built the way it is |

## Licence

Apache-2.0. The qboot patches carry context from qboot under GPL-2.0, and `NOTICE` says so. The
licence covers the recipes, not what they build. A release is mostly GPL and LGPL binaries, and
[docs/releasing.md](docs/releasing.md#what-a-release-owes) says how a release answers for them.
