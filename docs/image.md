# The base image: how it is made, and every way it differs from Ubuntu

The base image is an Ubuntu 26.04 (resolute) userland that a workspace boots with systemd as
PID 1. It is fast to its first login because of a long list of changes to what Ubuntu installs.
This document names every one of those changes, says why each is there, what measured it, and
what a workspace gives up for it.

Two readers use it. The person changing the image, who needs to know which change is load-bearing
before touching it. And the person asking whether the boot time is honest: whether a cost was
moved past the moment `task boot:ssh-first` stops its clock and hidden there. The rule for the
second reader is simple. Every change below has a category, and only the **deferred** ones move
work past the first login. Each deferred row says when that work happens and what a user notices.
Nothing else is deferred.

Explanations live here, not in the scripts and drop-ins under `image/`. A script carries at most a
one-line pointer to a section of this document.

## How the image is assembled

| Stage | Where | What it does |
|---|---|---|
| The toolchain | `image/Dockerfile` | Ubuntu 26.04 by digest, mkosi from its release tarball by SHA-256, both from `versions.yaml`. The digests of Task, git-lfs, BuildKit and Docker's packages are passed in as environment for the tree. mkosi cannot run under BuildKit (it unshares namespaces), so `spin-machine-dev build image` runs this container with `--cap-add SYS_ADMIN`. |
| Two rebuilt packages | `image/systemd`, `image/openssh` | Ubuntu's own systemd and OpenSSH source packages, rebuilt and versioned `+spin1`. systemd is built with Debian's `stage1` profile (no BPF framework in PID 1, no SELinux, audit, TPM2, homed) and carries upstream's drop-in lookup change (#42858). OpenSSH carries `userauth-delay-failures.patch`. Their rows are under "Packages". |
| The tree | `image/mkosi.conf` | mkosi installs the `Packages=` list from the archive, with the two rebuilt packages offered from a local repository. `WithDocs=no` keeps documentation out (the per-package copyright files stay). `RemoveFiles=` removes identity and caches. `mkosi.extra` is staged under `/usr/local/lib/spin-base`, not at its final paths. |
| The scripts | `image/mkosi.postinst.chroot` | In the tree, in this order: `install-dev-tools.sh`, `configure-system.sh`, the licence texts collected, the rebuilt packages held, `optimize-systemd.sh`, then `rootfs/` copied over `/`, then the staging directory deleted. |
| The filesystem | `image/build.sh` | Installs the pressure sender, empties `/etc/machine-id` and removes the rest of the identity, writes `licenses.txt` and `packages.txt` beside the image, checks the rebuilt packages are held, checks the inventory (below), makes a partitionless ext4 with this repository's own `mkfs.ext4`, checks it, opens it and asserts what follows, and wraps it in a read-only qcow2. |

**Why `rootfs/` is copied last.** A file already at the path of a conffile a package installs
during `install-dev-tools.sh` stops dpkg at a conffile prompt with no tty (`end of file on stdin at
conffile prompt`, 2026-09-07, when `rootfs/` replaced containerd.io's
`/etc/containerd/config.toml`, until #510). So nothing in `mkosi.extra` reaches its final path
until the packages are in. The enable links
`optimize-systemd.sh` makes for `docker.socket` and `containerd.socket` point at files from
`rootfs/` and dangle until the copy.

**Why the licence texts are collected in the middle.** `optimize-systemd.sh` deletes
`/usr/share/doc`. The copyright notices must travel with a redistributed copy, so they are
collected into `/var/lib/spin-machine-licenses.txt` first, and `build.sh` moves that file out of
the image into the release's `licenses.txt`.

**Why the rebuilt packages are held.** An `apt upgrade` in a workspace would otherwise replace
them with the archive's build of the same source, bringing back what was built out, or replace half
of systemd (`libsystemd-shared` and `systemd` must come from one build or the boot fails).
`build.sh` fails an image whose systemd or `sshd-auth` is not the rebuilt one, or whose rebuilt
packages are not held. The cost: a package of the same source the image does not carry, such as
`systemd-container` or `systemd-resolved`, asks for the archive's systemd at its exact version,
and apt refuses to install it.

**What `build.sh` asserts on the finished filesystem.** It reads the ext4 it made with `debugfs`
and fails the build when any of these is untrue: the size, no reserved blocks, a clean 64 MiB
journal, no `orphan_file`, every inode table zeroed; `/sbin/init`, a shell, docker, Task, bwrap,
socat, rg, hwclock present; the identity files absent and `/etc/machine-id` empty; udev not
masked; `tmp.mount` and `systemd-tmpfiles-setup.service` not masked; each mask shadows a file the
image has; logind wanted by `multi-user.target` and not masked; root lingers and spin does not;
each drop-in below that carries a measured gain still says what it says; the timers masked and
fstrim hourly; no `pam_motd`. Each check names the row of this document it protects.

### The toolchain

- **Why mkosi and not a container image build.** What is wanted is one filesystem, not a stack of
  tar layers. The image is a read-only qcow2 base with a fresh qcow2 overlay per VM
  (`qemu-img create -f qcow2 -F qcow2 -b rootfs.qcow2 overlay.qcow2`), so copy-on-write happens in
  the block layer where QEMU does it, not in a union filesystem inside the guest. A build that
  produces the filesystem directly skips the round trip through layers.
- **Why mkosi does not run under BuildKit.** mkosi unshares a user and mount namespace and BuildKit
  forbids it (`mkosi was forbidden to unshare namespaces`, `PermissionError` on `unshare(2)`,
  2026-09-07). `RUN --security=insecure` needs a `buildkitd` started with an entitlement flag, so
  `spin-machine-dev build image` would fail on any machine whose builder was made the ordinary way.
  So `image/Dockerfile` pins the tools, `build.sh` is the build, and `spin-machine-dev build image`
  runs it in a container with `--cap-add SYS_ADMIN` and seccomp and AppArmor unconfined
  (`internal/build/build.go`), not `--privileged`.
- **Why an Ubuntu 26.04 container, not Debian.** The image is Ubuntu 26.04, and mkosi's keyring and
  apt come from the same distribution. Debian's mkosi can build Ubuntu with `ubuntu-keyring` added
  by hand; the same distribution removes the question.
- **What the container installs.** `apt` and `dpkg` to assemble the tree, `ubuntu-keyring` for the
  archive (gpgv arrives with apt), `curl` to fetch mkosi, `qemu-utils` to wrap the qcow2,
  `apt-utils` for `apt-ftparchive`, with which mkosi makes the rebuilt packages a local repository
  (`--package-directory`; without it mkosi stops at "apt-ftparchive not found"), `python3` (mkosi,
  and `deviations.py`), `xz-utils` and `zstd`.
- **No e2fsprogs in the container.** The filesystem is made and read by the static e2fsprogs this
  repository builds (`e2fsprogs/`), from the same `_output` the image is written into. What an ext4
  is made with (its features, block and inode sizes) is what mke2fs's version and `mke2fs.conf`
  default to, and a distribution's copy is whatever it shipped that day: 1.47.0 made `orphan_file`
  a default. Left installed, a build missing the pinned tools would quietly use the container's;
  `build.sh` stops instead.
- **qemu-img is the container's, not this repository's.** The image build would otherwise depend on
  the QEMU build, tens of minutes of compiling to write one qcow2 header. The format is not
  version-sensitive in the direction that matters: an image written by Ubuntu's qemu-img is read by
  ours.
- **mkosi from its release tarball, by SHA-256, not Ubuntu's `mkosi` package.** Which version
  assembles the tree decides what ends up in it, and the base image is hashed into `machine.env`,
  so "apt gave us a newer mkosi" would be a change to the release nobody wrote down (resolute
  carries 26-3). mkosi is pure Python and runs from its source tree (`bin/mkosi` puts the checkout on
  `PYTHONPATH`), so nothing goes into the system Python, and apt's 30-package closure (systemd-boot,
  tpm2-tools, pesign, cryptsetup, for outputs this build does not make) stays out. The checksum is
  checked on every build, not only after a download, because the cache mount outlives the build that
  filled it, and the build asks the installed binary its version. `mkosi.conf`'s `MinimumVersion=`
  is the same version, not a floor with room above it; `gates/pins_test.go` holds the two together.
- **What the scripts download, by content.** `install-dev-tools.sh` runs inside mkosi's tree,
  which sees only what `mkosi.conf`'s `Environment=` passes, so each pin is an `ENV` of the
  container. Task is pinned for every platform in `versions.yaml`; the guest's is linux/amd64's.
  GitHub sheds anonymous release downloads under load (a 504 failed the build on 2026-11-09 and
  passed when run again), so downloads retry; the SHA-256 still decides the bytes. `mkosi.postinst.chroot`
  sets `PATH` itself, because mkosi's omits `/usr/local/bin`, where the tools are installed and then
  run to check them (`task: command not found`, exit 127).
- **mkosi's settings.** `Incremental=yes` and a cache directory keep the package cache between
  builds (the install is ~1.5 GB and the archive is the slow part). `Format=directory` because mkosi
  has no "one filesystem, no partition table" output, and a bootable disk with an ESP and a GPT is
  what nothing here would read: there is no bootloader or UEFI, and QEMU hands the bzImage to the
  BIOS through fw_cfg. `CleanPackageMetadata=no` keeps dpkg's database (~25 MB), because a workload
  runs apt. `WithDocs=no` excludes `/usr/share/doc/*` and keeps `/usr/share/doc/*/copyright`.
- **How the image is reached is the consumer's business.** This repository's boots use
  `root=/dev/vda init=/sbin/init` with no initrd, which the kernel serves because virtio-blk and
  ext4 are built in and the filesystem is partitionless. A consumer that brings an initrd enters the
  image from there. Nothing here depends on an OCI runtime's bundle semantics.

### The rebuilt packages

**systemd** (`image/systemd/Dockerfile`) is Ubuntu's source package of the version the image would
otherwise install, by its `.dsc` (`versions.yaml`'s `systemd-source`), so its patches and security
fixes are Ubuntu's. What changes is the build:

- `stage1`, Debian's reduced build profile, maintained in `debian/rules` and `debian/control`: no
  audit, cryptsetup, coredump, AppArmor, OpenSSL, TPM2, p11-kit, FIDO2, PCRE2, homed, userdb,
  importd, curl, sysupdate, boot loader or ukify, and no BPF framework. The BPF framework is where
  the time was: PID 1's setup before the generators went from 25.8 to 5.6 ms (boot:waits), with the
  verifier loading socket-bind at every boot and restrict-fs failing to load under lockdown (#261).
  So no unit can use `RestrictFileSystems=`, `SocketBind*=` or `RestrictNetworkInterfaces=`; none
  does, and spin's guard loads its own programs. None of the packages the image installs is left out
  by the profile.
- SELinux off as well: the guest kernel's LSMs are lockdown, capability and bpf.
- `pcre2` back on, which stage1 turns off: without it `journalctl --grep` says "compiled without
  pattern matching support". journalctl dlopens libpcre2 only when asked to grep, so a boot never
  loads it.
- `systemd-executor` (exec'd for every unit PID 1 starts), udevd and its helpers, systemctl and
  journalctl link libsystemd-shared in statically. PID 1, journald and logind have no such option.
- One patch, `image/systemd/0001-dropin-derive-candidate-names-once.patch`, applied after
  Ubuntu's own without fuzz: upstream's #42858 (v262), backported. PID 1 looked for a unit's
  drop-in directories by working out its candidate names (the name, its template, its "-"
  prefixes) again for each of ~12 lookup directories and 4 suffixes, for every unit it loads. It
  now works them out once per name. First login −6.5 ms [−9.9, −1.3], 100 boots a side against
  systemd rebuilt without it, and PID 1's CPU before the login 56.0 → 52.4 ms, p50 of 5 ledgers
  (labs 45090299768, 45090326943, 2026-11-23). The Dockerfile fails when the patch no longer
  applies, and when its function is not in the source it built. A release ships the patch as
  `usr/share/spin-stack/systemd/`.

Measured on the bench fleet, 20 boots of each interleaved, the first SSH login: stage1 −47, −38
and −47 ms against the archive's systemd (labs 44531842773, 44537549305, 44538877783); static over
stage1 −10 and −1, within noise, accept-to-login −7 in both. A checkpoint restored to the first
login: unchanged, ~50 ms of guest time either way (44533227886). Not taken: `-Db_lto=true`
(Ubuntu's flags already have `-flto=auto`; the packages came out the same to a few hundred bytes).

The build fetches the source from the archive's deb-src, which apt checks against the signed
Sources index, then the `.dsc` against the pin; `dpkg-source` checks the tarballs against the
checksums in the `.dsc`, so the pin holds the whole source. The archive keeps only the version
resolute publishes now, so the build fails at the download the day Ubuntu moves systemd, which is
when the pin moves with it (`refresh.yml` proposes it). Launchpad keeps every upload but answered
502 to a whole build (2026-11-19). `acl` is installed because the journal's install step runs
`setfacl`. The extra meson options go last (`CONFFLAGS_UPSTREAM`, which `debian/rules` hands last
and never sets), and `meson-took.py` asks the configured build whether it took each, so a misspelt
option fails the build. A patch from `image/systemd/experiments` is applied only for a lab build
(`spin-machine-dev lab tree --systemd PATCH`), never the image's. The build then checks what came
out: every package at the new version, libsystemd-shared without SELinux, AppArmor, audit or
libcrypto, an executor that loads no libsystemd, and a journalctl that greps.

**OpenSSH** (`image/openssh/Dockerfile`) is Ubuntu's source package of the version the image would
otherwise install (`openssh-source`, without its epoch, as the `.dsc`'s name has it), built as
Ubuntu builds it, with the patches beside the Dockerfile applied after Ubuntu's, in name order and
with `--fuzz=0` (a hunk landing elsewhere in a moved `auth2.c` is a different change).
`userauth-delay-failures.patch` keeps OpenSSH's userauth minimum time (5 ms plus 0-4.2 ms a user)
for a request that fails, and skips it for one that authenticated or for a public key query it
answered with `PK_OK`, which had told the client its outcome already (#457). Bench, spin's shape,
30 a side against v20261122.01: restored login −13.6 [−14.9, −9.8], cold −9.4 [−15.8, −2.6] (labs
44983986606, 44983986575, 2026-11-22). No udebs and no `ssh-askpass-gnome` (it would pull GTK
into the build), and `nocheck`, because the regression suite starts sshds on the build host's
ports. The source moves as systemd's does (`spin-machine-dev refresh openssh`). The build checks
that `sshd-auth`, the process that runs userauth, says it is this build in the version it sends
every client.

Both are versioned `+spin1`, so apt prefers them over the archive's, a reader of `packages.txt` can
tell them apart, and `mkosi.postinst.chroot` holds them. `build.sh` reads the held state from dpkg's
own status, not apt-mark's word. An image that took the archive's systemd boots and pays again for
everything built out; one that took the archive's sshd logs in and every login sleeps twice.

### The filesystem

`build.sh` turns the tree into a partitionless ext4 wrapped in a read-only qcow2.

- **ext4 and not something denser.** The guest kernel has `EXT4_FS` and `OVERLAY_FS` and explicitly
  not `XFS_FS`, `BTRFS_FS`, `SQUASHFS` or `EROFS_FS` (`kernel/config-7.3-x86_64`). Another
  filesystem starts with a kernel config change.
- **`SOURCE_DATE_EPOCH=0`** normalises timestamps, so the build's wall clock is not in every file.
  It is not a claim that the image is bit-reproducible: the userland comes from a live archive and
  the filesystem is sized from what came out, so two builds days apart differ. What a release
  contains is the checksum in `machine.env`.
- **`licenses.txt` and `packages.txt` beside the image, at the root of the tree.** That is where a
  release carries them: one each for the whole machine. `packages.txt` is the only record of what
  the image is made of without booting it (the tree is thrown away, the qcow2 is opaque, dpkg's
  database is inside), and it is what a release's `SOURCES` points at for the userland: an Ubuntu
  source package is fetched by name and version. The licence texts go beside the image, not in it:
  the notices must accompany a copy, a release is what is copied, and a few hundred licence texts
  in every VM are bytes nobody reads.
- **Sized to what the tree needs, plus 128 MiB.** Free space in the base is free space in every
  overlay built on it: a disk larger than the image, with the filesystem grown to fill it, has the
  space it was given plus whatever the image carried (317 MiB, 2026-09-14). Two passes, because ext4's
  own overhead (inode tables, directories) scales with the filesystem's size: the first is generous
  and only measured, the second is made at what the first used plus the margin. `resize2fs -M` cannot
  shrink it instead: it estimates its minimum from the inode tables laid out for the larger size,
  which on this image was the size it already had. The margin is for a machine booted from the image
  with nothing grown (`spin-machine-dev shell`, the probes), whose boot writes land in the image's free
  space. The size is rounded up to a whole MiB, and given in KiB with its unit spelled: a bare number
  is KiB only until `-b` is on the command line, after which mke2fs reads it as blocks, and
  v20260914.01 shipped a 4.08 GiB filesystem instead of a 974 MiB one that way (the build now compares
  the size made with the size asked).
- **`-b 4096`**, stated: mke2fs picks a "small" profile under 512 MiB, and upstream's `mke2fs.conf`,
  which this build uses, gives it 1024-byte blocks where Debian's and Ubuntu's give 4096. A tree that
  shrank under 512 MiB would quietly get the other (measured on a 64 MiB filesystem, 2026-09-14).
- **`-m 0`**: no blocks reserved for root. The reservation is a proportion, so a filesystem grown
  from the image keeps 5% of the larger size: 0.55 GiB of an 11 GiB disk (2026-09-14) that a user who
  is not root cannot write to.
- **`mkfs.ext4 -d`** writes the tree in directly: no loop device and no mount, no privilege beyond
  what mkosi needed.
- **`-U clear`**: a null UUID, for a deterministic build. Nothing mounts by UUID; the guest finds
  disks by virtio-blk serial.
- **A 64 MiB journal.** The base file is never written, but the ext4 in it is, in every overlay,
  mounted read-write and grown. A copy of an overlay taken while the guest writes, or the overlay of
  a machine that died, is a filesystem interrupted mid-update, and without a journal nothing repairs
  its metadata: built without one, a copy taken seconds after a grow did not mount ("structure needs
  cleaning", 2026-09-14). Each overlay replays and writes its own journal, so the image stays clean,
  and the build asserts it is clean: a journal needing recovery could not be replayed from a
  read-only drive. 64 MiB is stated rather than derived, because mke2fs sizes a journal from the
  filesystem (16 MiB for this image, and a different number in each sizing pass); 64 MiB is what it
  gives the 3, 4 and 11 GiB disks a VM is grown to (16 GiB gets 128), and a grow does not resize a
  journal. It costs the image almost nothing because mke2fs does not zero it: an empty 974 MiB
  filesystem converts to 1540 KiB of qcow2 with it and 1220 without, and an overlay at most 64 MiB
  once the guest has cycled through it (2026-09-14).
- **No `orphan_file`**, which `mke2fs.conf` turns on since 1.47.0. Every mount, read-only included,
  loads the orphan file one block at a time (`ext4_init_orphan_info`, `fs/ext4/orphan.c` in 7.3):
  about 73 sequential single-block reads, each a virtio round trip, inside the root mount the boot
  waits for. The feature buys scale for unlinking many open files at once from many CPUs; a
  workspace is not that. Crash correctness is the same: an inode unlinked while open is in the
  classic orphan list and the journal.
- **`lazy_itable_init=0`**: the inode tables are zeroed here and every group is marked
  `ITABLE_ZEROED`. Without it (`mke2fs -d` left every group unmarked) the first read-write mount of
  each overlay ran ext4lazyinit to zero every inode table in the first second of the boot: 16.06 MiB
  of zero clusters in the overlay after one boot, none with the tables zeroed here (`--init /bin/sh`,
  laptop, 2026-11-19). Zeros cost the image nothing, since qemu-img allocates no cluster for them.
- **qcow2, uncompressed, mode 0444, written beside the target and renamed over it.** Every VM reads
  the base on every boot, and a compressed cluster is decompressed on each read. `qemu-img convert`
  straight to the target opens the existing base for writing; a running VM's image lock makes that
  fail (`Failed to get "write" lock`), which is the right refusal for the wrong reason: a base is
  never edited, because writing into it invalidates every overlay, silently. A rename replaces the
  directory entry and leaves the old inode, so a running VM keeps the image it booted from.

### What build.sh checks, and why

The checks read the finished filesystem, not the build: a tree that failed to copy in makes a
perfectly valid empty ext4, and the failure would be a VM that boots and finds nothing to run.

- **debugfs's output is read, never its exit status.** `debugfs -R` exits 0 whether or not the
  request succeeded, printing "File not found by ext2_lookup" to stderr. A check written the obvious
  way passes for every path, which is how the script once reported a missing `/bin/sh` as fine and a
  deleted `/etc/hostname` as baked in (2026-09-07). A successful stat starts with "Inode:". Binaries
  are dumped to a file and not piped: `grep -q` stops at the match, and under `pipefail` debugfs's
  SIGPIPE failed a binary that matched.
- **The filesystem is made as asked.** mke2fs never says how it read a size, `-m` or `-J`; each is
  only visible on the filesystem. The two choices against `mke2fs.conf`'s defaults (`orphan_file`,
  zeroed inode tables) are read back, because an e2fsprogs bump that ignored one would build a
  filesystem that boots, only slower. The zeroed groups are counted against the header's group
  count, so output dumpe2fs did not print fails rather than passes.
- **`/sbin/init`, `/bin/sh` and the tools.** `/sbin/init` is what the image is; `/bin/sh` is what
  `spin-machine boot --init /bin/sh` runs. `hwclock` comes from `util-linux-extra` (Ubuntu split it
  from util-linux, and `hwclock` as a package name fails with `E: Unable to locate package` after the
  whole install); it is asked of the image because the package that provides it has moved once.
- **The directories the guest mounts over** (`/etc /tmp /proc /sys /dev /run`) exist and are empty;
  a mount over a missing directory fails.
- **Identity.** Each identity file is asserted absent, and `/etc/machine-id` present and empty.
  openssh-server's postinst generates host keys at install time, so they are removed and asserted
  gone. mkosi writes a machine-id of its own after `RemoveFiles` runs, which is how a build that
  looked clean shipped one (2026-09-07), so `build.sh` empties it after mkosi.
- **udev is not masked.** It was masked for six months to save boot time it does not cost: 15 boots
  of each with `task boot:bench` on 2026-09-10, and with its four units masked fifteen of fifteen
  never reached a login, waiting out the ten-second `dev-ttyS0.device` timeout. What udev is on for,
  other than the getty, is an added CPU: without `40-spin-hotadd.rules` its event reaches no one.
- **Memory.** `lru-gen.conf` (nothing fails without it; the machine stops answering under memory
  pressure, weeks later) and `/tmp`'s cap, its other half.
- **The getty's TERM.** Both drop-ins, since they only work together.
- **Masks.** A masked unit comes back when a package upgrade replaces the link, so the masks with a
  measured reason are asserted. Every mask must shadow a file the image has: a mask of something no
  package installed is dead code that reads as a decision, and `optimize-systemd.sh` carried 16
  (avahi, cups, multipathd, the tpm2 units of a systemd without TPM2) until 2026-11-21. An instance's
  mask is checked against its template.
- **Never masked: `tmp.mount` and `systemd-tmpfiles-setup.service`.** `/tmp` is where services and
  users leave what one boot needs, and on the disk it outlives the boot in every copy of the disk.
  `tmp.mount` makes it a tmpfs; tmpfiles-setup removes and recreates what does not carry over.
- **logind's want is the package's**, in `/usr`; an override would be a `/dev/null` link at the
  `/etc` path, which is how logind was taken out of the boot before (2026-09-12).
- **The lane, the login and the services**, each row below that has a measured gain, read back:
  each piece alone gains little and leaves the login waiting on the next, so a missing one costs the
  login without failing anything. The lane's units without default dependencies must still stop at
  shutdown. The ML-KEM drop-in is read back because without it OpenSSH's own list, with three
  classical exchanges, still logs in. The keygen unit needs both its link and
  `DefaultDependencies=no`, or it waits for the `sysinit.target` that wants it, and its condition on
  spin's key, or spin's path pays for a key nothing loads.
- **The timers** are asserted separately from the unit masks, because a package upgrade re-running
  a unit's `[Install]` brings them back, and what they cost is an apt lock under a workload's own
  install. fstrim must be enabled and hourly.
- **No `pam_motd`** in any stack the distribution enables it in, so whatever an upgrade restores into
  `/etc/update-motd.d` (`50-motd-news` reaches the network) never runs. Each file is first checked to
  be a PAM stack: debugfs prints nothing for a missing file, and nothing has no `pam_motd` line.

## The inventory

`image/deviations.txt` is every way the image differs from what its packages installed, one
`KIND PATH` a line. `image/deviations.py` derives it from the tree mkosi and the scripts produced,
and `build.sh` fails when the derived list is not the file. `gates/image_doc_test.go` fails when a
line of the file has no row here. So a change to the image that adds a mask, a drop-in, an edit of
a package's file or a deletion fails the image build until the line is added, and fails the gate
until this document says why.

The kinds, each read from what came out, not from the scripts:

| Kind | How it is found |
|---|---|
| `masked` | A link to `/dev/null` under `/etc` that no package owns: a unit, a generator or a tmpfiles rule turned off. |
| `added` | A file under `/etc`'s configuration directories, `/usr/lib/systemd`, `/usr/libexec` or `/usr/local` that no package owns and no maintainer script recorded. |
| `modified` | A conffile whose MD5 is not the one dpkg recorded, a file a package makes from a template that is not the template (`/etc/ssh/sshd_config`; `deviations.py` compares `/etc/nsswitch.conf` too, which the image leaves as libc-bin makes it), or the session class added to `/etc/pam.d/common-session`. |
| `deleted` | A conffile, or a directory of a package's files, that the image does not have. |
| `unwanted` | An enable link `deb-systemd-helper` recorded for a package that the image removed. |
| `linger` | A user whose manager logind starts at boot. |

On 2026-11-22 the list had 120 lines, and 118 once `nsswitch.conf` and the manager's timeouts
went back to Ubuntu's ("Measured in this review"). A file a maintainer script makes on every
Ubuntu (the accounts, `ld.so.cache`, systemd's own three enables) is not a deviation and the
script says which it skips. A unit Ubuntu itself ships disabled or masked, such as `systemd-gpt-auto-generator`, is
not one either, and the inventory sees that mask as Ubuntu's because it is. Ubuntu's `debian/rules`
links `/etc/systemd/system-generators/systemd-gpt-auto-generator` to `/dev/null` with `dh_link`,
and the archive's `systemd_259.9-0ubuntu0.1_amd64.deb` carries the link in its data, which is why
dpkg lists it in `systemd.list` (`dpkg-deb -c`, read 2026-11-23). mkosi then deletes it: 27.1's
`mkosi/installer/apt.py` unlinks that path after every install, "to make sure it is enabled by
default in our images". An image built without the image's own link
had none, and the derived list gained `deleted /etc/systemd/system-generators/` (image build on
#517, 2026-11-22): the inventory saw the deletion, as the directory it was taken from. The image's
link puts back what Ubuntu ships, so it is no line of the list. A `.list` file is the package's
data member, so a path dpkg lists is a path the package shipped; telling the two apart by md5sums
or the `.deb`'s contents (#518) would find nothing more.
`optimize-systemd.sh` also re-made Ubuntu's own `sockets.target.wants/ssh.socket` and removed an
`ssh.service` enable nothing makes; those two changed nothing and were deleted (2026-11-22).

## Categories

| Category | Means | On the goal clock? |
|---|---|---|
| removed | The work never happens in a workspace, because this machine has nothing for it to do, or a workspace does not need it. | It is gone, not moved. |
| deferred | The work still happens, after the first login. The row says when and what a user notices. | Moved off the clock, on purpose, and said so. |
| reordered | The same work, started earlier or ordered differently. It still runs before or beside the first login. | On the clock. |
| configuration | A setting that changes behaviour, not boot work. | No boot work moves. |
| added | Something the machine needs that Ubuntu does not ship. | Whatever it costs is measured. |
| identity | Removed because every VM booted from the image would share it (AGENTS.md, "No identity in the base image"). | Correctness. |

Evidence uses `docs/boot-time.md`'s words: *measured* (its own arm in spin's shape), *group* (an
arm that took back its group), *lane* or *ladder* (another shape or login), *laptop*,
*mechanism* (what it removes is visible, no goal number timed it), *function* (a correctness or
security reason, not boot time), *unmeasured*. A Δ is the Δp50 of taking the change back, with its
95% interval; positive means the change earns that much.

## Identity

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `/etc/machine-id` present and empty | identity | systemd makes a machine ID at boot when the file is empty. A value baked in would be every VM's. | function; asserted by `build.sh` | Nothing. Every boot is a first boot. |
| no `/etc/hostname`, `/etc/resolv.conf`, `/var/lib/dbus/machine-id`, `/var/lib/systemd/random-seed`, SSH host keys | identity | The same reason. The guest bind-mounts a `resolv.conf` and refuses to mount over a file that is not there. | function; asserted by `build.sh` | Nothing; whoever runs the VM hands it these. |
| `systemd-firstboot.service` masked | identity | Every boot is a first boot (empty machine-id), so it would run every boot. Identity comes from whoever runs the VM. | function | No first-boot prompt for locale, time zone or root password. |
| `systemd-machine-id-commit.service` masked | identity | It writes the transient machine ID to disk, which would make a VM's ID survive into its overlay's next boot. | function | Nothing. |
| `systemd-random-seed.service` masked | identity, removed | A saved seed in the base would be shared. virtio-rng seeds the kernel's RNG, and a restored guest reseeds through vmgenid. | function; one of group C-masks | Nothing. |
| `spin-machine-sshd-keygen.service`, `sysinit.target.wants/spin-machine-sshd-keygen.service`, `sshd@.service.d/keygen.conf` | added, reordered | Host keys are identity, so the image ships none. Ubuntu's `sshd-keygen.service` carries `ConditionFirstBoot=yes`, which never holds in time here, so without this unit `ssh.service` fails. It makes an Ed25519 key only, from the boot's first dispatch (`DefaultDependencies=no`), so the key is made beside the rest of the boot and not inside the first login. Every sshd, including the per-connection `sshd@` for vsock, waits for it. It does not run when spin's handoff has placed spin's key at `/run/spin/ssh/ssh_host_ed25519_key` (#515). | Ed25519 only: measured, −5.0 [−8.3, −1.8] (lab 44979726719). From the first dispatch: lane 489 → 468 (lab 43483372488); in spin's shape, taken back: −0.8 [−10.5, +3.5], not a gain (lab 45034963004). Skipped on spin's path: −6.9 [−9.6, −2.4] (lab 45086250796, #515). | No ECDSA or RSA host key. A client that accepts only those cannot connect. |

## Packages

The `Packages=` list in `image/mkosi.conf` is what a workspace finds. mkosi installs what is
listed and its dependencies, with no recommends and no Ubuntu metapackage. Against Ubuntu 26.04's
minimal cloud image (`ubuntu-26.04-minimal-cloudimg-amd64-root.manifest`, 2026-11-22), the image
has 217 packages against 293.

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| systemd rebuilt with `stage1`, its tools linked static | configuration | No BPF framework means no BPF verifier run in PID 1 at every boot, and no restrict-fs program that failed to load under lockdown. | ladder: first login −47, −38, −47 (labs 44531842773, 44537549305, 44538877783) | systemd's BPF features (`RestrictFileSystems=`, `SocketBind*=`, `IPAddress*=` filters by BPF), SELinux, audit, TPM2, homed, cryptsetup. A same-source package such as `systemd-container` or `systemd-resolved` cannot be installed. |
| OpenSSH rebuilt with `userauth-delay-failures.patch` | configuration | OpenSSH waits a minimum time on every authentication, a success included. The patch keeps the wait for failures only. | measured: restored −13.6 [−14.9, −9.8], cold −9.4 [−15.8, −2.6] (labs 44983986606, 44983986575) | Nothing: a failure still waits, so a timing oracle on failures is unchanged. |
| GNU coreutils, not uutils | configuration | On 2026-10-19 OSV listed 21 findings against `rust-coreutils` 0.10.0 with no fix in the archive, none against GNU's. | function | Nothing. |
| `sudo`, not `sudo-rs` | configuration | Named in the list; `sudo-rs` is the minimal image's default. | unmeasured, no recorded reason | Nothing known. |
| `dbus-broker`, not `dbus-daemon` | configuration | The broker is what Ubuntu's systemd prefers; it is smaller and starts faster. | unmeasured | Nothing known: the bus and its activation are the same. |
| `bubblewrap`, `socat`, `ripgrep` | added | What Codex's and Claude Code's sandboxes need to run a command: bwrap, socat as the proxy, rg to find the files to protect (#221). | function | Nothing. |
| The docker-ce packages (`docker-ce`, `docker-ce-cli`, `docker-ce-rootless-extras`, `containerd.io`, `docker-buildx-plugin`, `docker-compose-plugin`), from Docker's pool for resolute | added | Docker and containerd in a workspace. Each `.deb` is pinned by version and SHA-256 in `versions.yaml` and checked before it is installed, so a build installs those bytes or fails. | function | Nothing; `apt upgrade` in a workspace moves them to what Docker's repository serves then. |
| Task, git-lfs, BuildKit in `/usr/local/bin` (`/usr/local/bin/task`, `/usr/local/bin/git-lfs`, `/usr/local/bin/buildctl`, `/usr/local/bin/buildkitd`, `/usr/local/bin/buildkit-runc`, `/usr/local/bin/buildkit-cni-bridge`, `/usr/local/bin/buildkit-cni-firewall`, `/usr/local/bin/buildkit-cni-host-local`, `/usr/local/bin/buildkit-cni-loopback`) | added | Tools a workspace expects, at versions pinned by SHA-256 in `versions.yaml`. | function | Nothing; `apt` does not know them, so `apt upgrade` leaves them. |
| `/usr/libexec/spin-machine-pressure` | added | The pressure probe's guest half (docs/machine.md, "Pressure"). | see `spin-machine-pressure.timer` | Nothing. |
| Not installed, compared with the minimal cloud image | removed | A workspace boots in a VM spin provides: no cloud-init, netplan, snapd, apport, unattended-upgrades, ubuntu-pro-client, kbd and console-setup, microcode (the host's), lxd agents, or time daemon (chrony; time is the host's). | function | See the next table. |

What a workspace notices among the packages the minimal cloud image has and this one does not,
checked in a booted release by `task boot:workspace`:

| Not installed | What a workspace notices |
|---|---|
| `tzdata` | No `/usr/share/zoneinfo` and no `/etc/localtime`: the clock reads UTC, `TZ=Europe/Madrid` is ignored, and a program that loads a zone by name (Python's `zoneinfo`, Java, a database) fails. `apt install tzdata` restores it. |
| `python3` | No Python. Ubuntu's minimal image has it for cloud-init. |
| `e2fsprogs` | No `mkfs.ext4` or `fsck.ext4` in the guest, so a disk attached to a running workspace cannot be formatted from inside it. |
| `lsb-release` | No `lsb_release`, which some install scripts call; `/etc/os-release` is there. |
| `xxd`, `screen`, `fdisk`, `parted`, `usbutils`, `fuse3` | The tools are absent; the kernel has FUSE, `fusermount3` is not there. |
| `systemd-resolved` | Name resolution is `files dns` from a `resolv.conf` the guest is handed; no `resolvectl`, and it cannot be installed (see systemd above). |

Restoring any of these is a decision with no boot cost to measure (a package's files are not on
the boot path unless it enables a unit), left open in #512.

## Files removed

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `/usr/share/doc/` (copyright files kept until collected), `/usr/share/man/`, `/usr/share/info/` | removed | Size: every overlay and every host page cache reads the same base, and nobody reads documentation in a workspace. | mechanism (size) | `man` pages. `man-db` is not installed either. |
| `/var/lib/apt/lists/` | removed | The package lists are ~50 MB and stale the day after the build. | mechanism (size) | `apt install` fails with "Unable to locate package" until `apt update` has run once. |
| `/var/cache/apt/`, `/var/cache/apt/archives/` | removed | The build's downloaded `.deb`s. | mechanism (size) | Nothing. |
| `/etc/update-motd.d/00-header`, `/etc/update-motd.d/10-help-text`, `/etc/update-motd.d/50-motd-news` | removed | See the MOTD row under "Login". | | |

## Units masked

Masked means linked to `/dev/null` in `/etc/systemd/system`: the unit never starts, whatever wants
it. A mask is not free. A masked unit is still loaded, as masked, when another names it, which is
why masking units that were never started measured nothing (docs/boot-time.md, "Measured and
rejected").

The sixteen masks of group C-masks were taken back together in spin's shape: cold first login
+57.5 [+49.0, +63.0] (lab 44818953987). That is the group's worth; which of the sixteen earns it
was not separated. Each is also something this machine has no use for, which the row says.

| Deviation | Category | Why | A workspace gives up |
|---|---|---|---|
| `systemd-journal-flush.service` | removed | The journal is volatile (see journald below), so there is nothing to flush to. | Nothing beyond the volatile journal. |
| `systemd-fsck-root.service` | removed | The root is a fresh qcow2 overlay over a base checked at build time. ext4's journal still replays at mount. | No `fsck` of the root at boot after an unclean shutdown; journal recovery still runs. |
| `systemd-modules-load.service`, `modprobe@configfs.service`, `modprobe@drm.service`, `modprobe@fuse.service`, `modprobe@efi_pstore.service` | removed | The kernel has `CONFIG_MODULES` off. What it has is built in, so these can only fail. | Nothing: configfs and FUSE are built in. |
| `systemd-hibernate-clear.service` | removed | No hibernation in a VM. | Nothing. |
| `systemd-pstore.service` | removed | No pstore backend: no EFI, and `efi_pstore` is not built. | Nothing. |
| `systemd-random-seed.service` | identity | Under "Identity". | Nothing. |
| `sys-kernel-debug.mount`, `sys-kernel-tracing.mount` | removed | Not mounted at boot. | debugfs and tracefs are not mounted. debugfs mounts by hand (`mount -t debugfs nodev /sys/kernel/debug`). tracefs cannot be used: the kernel runs `lockdown=confidentiality`, which keeps every file out of it, so `/sys/kernel/tracing` has no `trace` whether or not the unit is masked or tracefs is mounted. `boot:workspace` checks both (lab 45036254377). |
| `systemd-tmpfiles-setup-dev.service`, `systemd-tmpfiles-setup-dev-early.service` | removed | They create static device nodes for modules not yet loaded. With no modules, devtmpfs and udev already made every node. | Nothing. |
| `dev-hugepages.mount` | removed | No huge pages are configured. | No `/dev/hugepages`; a program that wants it mounts hugetlbfs by hand. |
| `dev-mqueue.mount` | removed | The kernel has POSIX message queues and the syscalls work without the mount. | No `/dev/mqueue` to list queues in. |
| `systemd-timedated.service` | configuration | Time is the host's. timedated would let a session set a clock it does not own. It is bus-activated, never part of the boot, so the mask costs and saves no boot time. | function | `timedatectl` fails ("Unit dbus-org.freedesktop.timedate1.service is masked"). A restored guest's clock is stepped by spin (spin-stack/spin#1498). |

The masks of the units of `ConditionNeedsUpdate` are load-bearing, not removed work:

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `ldconfig.service`, `systemd-sysusers.service`, `systemd-hwdb-update.service`, `systemd-journal-catalog-update.service`, `systemd-update-done.service` | removed | They run when `/etc` is newer than `/etc/.updated`, which the image does not have, so unmasked they rebuild `hwdb.bin`, the linker cache and the catalog in every boot. The build already ran `ldconfig`, `systemd-sysusers` and `systemd-hwdb update` (the package scripts do). | laptop, 2 boots: +260 ms and QEMU's RSS +90 MiB unmasked (docs/boot-time.md) | A package installed in a workspace that drops a hwdb file or a sysusers.d file has it applied by its own maintainer script, as on Ubuntu; at the next boot nothing reapplies it. Writing `/etc/.updated` at build time would let the units stay and skip themselves (#513). |

The distribution's background maintenance timers:

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `apt-daily.timer`, `apt-daily-upgrade.timer` | removed | They take the apt and dpkg locks, so a workload's own `apt install` fails with `Could not get lock /var/lib/dpkg/lock-frontend` at a moment its logs do not explain, and they reach the archive unasked. All five timers carry `Persistent=true`, so a VM resumed after a week catches up at the moment it is handed a workload. | function | No unattended package lists or upgrades. |
| `motd-news.timer` | removed | Its script was deleted with the MOTD, so it could only fail. | function | Nothing. |
| `dpkg-db-backup.timer` | removed | It copies the dpkg database daily. The release carries `packages.txt`. | function | No daily copy of `/var/lib/dpkg/status` in `/var/backups`. |
| `fstrim.timer.d/spin.conf` | configuration | fstrim hourly, not weekly, with up to 10 minutes' random delay. What a guest deletes stays allocated in its overlay and in the host's page cache until a trim reaches it: 657 MB to 21 MB on disk, 657 to 20 MB in the cache (run 36815822140). | measured (memory, not boot) | Nothing; a trim with nothing to give back is cheap. |

## Generators and tmpfiles rules turned off

Group C-generators, taken back together: cold first login +16.7 [+11.6, +23.3] (lab 44818953987).
The group counted the 8 generators below.

| Deviation | Category | Why | A workspace gives up |
|---|---|---|---|
| `systemd-fstab-generator` | removed | There is no `/etc/fstab`; the root comes from the kernel command line, other disks are attached and mounted by whoever attaches them. | An `/etc/fstab` written in a workspace is ignored at the next boot. Mount units in `/etc/systemd/system` still work. |
| `systemd-debug-generator` | removed | It implements `systemd.mask=`, `systemd.wants=` and the debug shell from the command line. | Those command-line switches do nothing. The lab masks with links for this reason. |
| `systemd-sysv-generator`, `systemd-rc-local-generator` | removed | No SysV init scripts and no `rc.local` in the image. | An init script or `/etc/rc.local` added in a workspace does not run. |
| `systemd-hibernate-resume-generator`, `systemd-system-update-generator`, `systemd-factory-reset-generator` | removed | No hibernation, no offline updates, no EFI. | Nothing. |
| `systemd-gpt-auto-generator` | removed | Ubuntu masks it too, and mkosi deletes Ubuntu's mask ("The inventory"), so the image makes the link again. The root is a partitionless filesystem named on the command line; there is no GPT and no EFI to discover partitions from. Not a line of `image/deviations.txt`. Group C-generators took the image's link away, which runs the generator Ubuntu does not, so the group's number includes a cost Ubuntu's image does not pay. | Nothing. |
| `/etc/systemd/user-generators/systemd-xdg-autostart-generator` | removed | It turns the XDG autostart `.desktop` files of a graphical session into user units, in every user manager's start. Root's manager starts in the boot and the first login waits for it, so the generator was on the login's clock: 1.6 ms of CPU (lab 45062836688). Not in group C-generators: measured alone, first login −3.8 [−6.2, −0.7], accept to login −6.1 [−9.0, −0.3], 100 a side (bench, spin's shape and login, against v20261122.06, lab 45070150688, #527). | A `.desktop` file in `/etc/xdg/autostart` or `~/.config/autostart` starts nothing. A workspace has no graphical session to start one in. |
| `systemd-pstore.conf`, `systemd-network.conf`, `x11.conf` (tmpfiles) | removed | No pstore, no networkd, no X11. | Nothing. |
| `systemd-nologin.conf` (tmpfiles) | reordered | It writes `/run/nologin` at boot, and `systemd-user-sessions` removes it late in the boot. Until then `pam_nologin` refuses every user, and the first login arrives before that ("Access denied for user spin by PAM account configuration"). Shutting down still writes it. | lane: part of 478 → 402 (lab 43518915618) | Nothing: logins are refused at shutdown as before. |

## The first login's lane: units reordered

These change when units start, not what they do. All of it still runs before or beside the first
login, on the clock. `DefaultDependencies=no` takes a unit out of the wait for `sysinit.target`
and `basic.target`; each of these drop-ins keeps `Conflicts=shutdown.target` and
`Before=shutdown.target`, so the unit still stops at shutdown.

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `ssh.socket.d/10-lane.conf`, `ssh.service.d/10-lane.conf`, `sshd-keygen.service.d/10-lane.conf` | reordered | spin's login reaches `ssh.service` through 127.0.0.1:22. ssh.service is ordered after Ubuntu's `sshd-keygen.service`, whose job waits for its ordering before its condition fails, so all three leave the default dependencies. | measured with the two rows below: −59.4 [−65.9, −56.2] with the probe's forwarder early as well (lab 44805051310); in a second run, the image's half alone −17.4 [−25.0, −8.5] and with the forwarder early −68.5 [−78.0, −61.8] (lab 44807457606), each against its own run's release | Nothing. |
| `systemd-logind.service.d/30-privatetmp.conf`, `dbus-broker.service.d/30-privatetmp.conf` | reordered | `PrivateTmp=` orders a unit after `systemd-tmpfiles-setup.service`. | the same | logind and the bus share the machine's `/tmp` (read-only to logind under `ProtectSystem=strict`). |
| `systemd-udevd.service.d/20-startup.conf`, `systemd-udev-trigger.service.d/20-startup.conf` | reordered | udev and its coldplug at `StartupCPUWeight=1` while the boot runs, so the login started beside them gets the one vCPU first. | the same | The console prompt 51 ms later (the serial getty waits for `dev-ttyS0.device`, which the coldplug announces). |
| `systemd-udevd.service`, `systemd-udev-trigger.service` (copies without `Before=sysinit.target`) | reordered, deferred | A drop-in cannot remove an ordering, so the image carries the units without that line. udevd at the lowest weight then no longer holds `sysinit.target`, `basic.target` and spin's supervisor. udevd becomes ready after the first login (at ~342 ms on the guest's clock, against the login's ~290). | measured: cold −48.6 [−51.7, −42.6], restored −2.0 [−8.0, +4.7] (labs 44987412171, 44987412210) | Deferred: udev's coldplug finishes after the first login. A device that only udev's rules prepare (permissions, symlinks under `/dev/disk/by-*`) appears a few tens of ms after the first login, not before it. A checkpoint taken with udev still at work needs QEMU's patch 0005 (#452). |
| `systemd-udev-trigger.service.d/10-coldplug.conf` | removed | The coldplug replays only the subsystems something here uses: block, net, virtio, misc, input (logind's power button), rtc, ptp, cpu (a core attached before systemd starts), and ttyS0. | group C-coldplug: cold +14.9 [+8.2, +19.8] (lab 44818953987) | Devices of other subsystems present at boot get no udev add event: no rules run for them until a later event. A USB or PCI device passed through at boot is not set up by udev. |
| `systemd-logind.service.d/10-lane.conf`, `systemd-logind-varlink.socket.d/20-lane.conf`, `user-runtime-dir@.service.d/10-lane.conf`, `user@.service.d/10-lane.conf`, `dbus.socket` (a copy without `After=sysinit.target`) | reordered | A login waits for logind, its runtime directory and its manager; none of them need what `sysinit.target` and `basic.target` gather. | measured: +16.5 [+9.8, +20.4] cold, +11.0 [+6.2, +18.8] prestarted, restored in noise (lab 45034963004); lane, two vCPUs: 478 → 402 with the rows above (lab 43518915618) | Nothing known: logind, the bus and the managers run the same units, earlier. |
| `systemd-logind-varlink.socket.d/10-seats.conf` | configuration | Makes `/run/systemd/seats` before logind is reached. Without it, a login that arrives before logind has run gets no session, `XDG_RUNTIME_DIR` unset, and no error (2026-09-12). | function; `boot:logind` | Nothing. |
| `sshd-vsock.socket.d/10-lane.conf`, `sshd@.service.d/10-lane.conf` | reordered | The image's own vsock login, `login=lane` in the probes. spin's path does not use it. | lane | Nothing. |
| `ssh.service.d/20-no-config-test.conf` | removed | Ubuntu's ssh.service runs `sshd -t` before it starts. The check stays on reload. | measured: −3.2 [−7.2, −0.8], 60 a side (lab 44977044826) | A broken `sshd_config` fails at start rather than in a pre-check; the error is sshd's own. |
| `default.target` → `spin-machine-login.target`, `spin-machine-boot-rest.service` | reordered, deferred | The boot's default target holds only what the first login needs. `multi-user.target`, and with it `sysinit.target`, `basic.target`, udev, tmpfiles, sysctl, the sockets, the timers and the getty, starts once those units are up. See "The first login's target". | measured on v20261123.02, 40 a side: first login −21.5 [−26.4, −15.5] (lab 45098372930); restored +1.1 [−6.0, +5.2] evicted, +2.2 [−10.6, +12.9] cached (lab 45098372897) | Deferred: everything outside the target runs right after its units are up, not before them. A device udev prepares, `/run/utmp`, sysctls, docker's socket and the serial getty come tens of ms later (console prompt +15.5). |

## Login

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `/etc/pam.d/common-session` (sessions of class `user-early`) | reordered | logind answers a session of class `user` only after `systemd-user-sessions` ran, at the end of the boot. `user-early` waits for the user's manager and not for the boot, so the first login's own `systemctl --user` finds its manager (#467). | measured: first login +4.7 [−0.2, +12.2] at 40 a side, restored −10.8 [−15.9, −6.6]; the lane's login as spin +29.7 [+22.8, +42.1] (labs 45031644179, 45031644114, 45031687433) | Nothing. |
| `/var/lib/systemd/linger/root` | reordered | spin logs in as root, and root's manager starts in the boot, so the first login does not wait for a whole `systemd --user` start. spin (uid 1000) does not linger: its manager starts at its own first login. | the row above | spin's own first login waits for its manager (+29.7 ms on the lane, above). root's manager runs from boot and holds ~a few MiB. |
| `/etc/environment.d/10-user-sockets.conf`, `/etc/systemd/user/dbus.socket.d/10-env.conf`, `/etc/systemd/user/ssh-agent.socket.d/10-env.conf` | reordered | The manager reads its sockets' environment once, instead of each socket running `systemctl --user set-environment` while a login waits. | measured: first login +19.6 [+15.9, +25.6] without it (lab 45031644179) | Nothing: `DBUS_SESSION_BUS_ADDRESS` and `SSH_AUTH_SOCK` are set as before. |
| `/etc/pam.d/sshd`, `/etc/pam.d/login` (`pam_motd` commented out), `/etc/motd` (empty) | removed | The distribution's MOTD scripts check for updates, count reboots and advertise a subscription: network calls and package queries in every login. | ladder: −12 on the first login (#240) | No MOTD. |
| `/etc/ssh/sshd_config`, `/etc/ssh/sshd_config.d/10-spin-user-ca.conf` | configuration | Certificates are the only way in: `PasswordAuthentication no`, `TrustedUserCAKeys` naming the file spin's supervisor writes. `PermitRootLogin yes` because spin logs in as root, by certificate. | function (docs/machine.md) | No password logins. |
| `/etc/ssh/sshd_config.d/10-spin-kex.conf` | configuration | One key exchange, `mlkem768x25519-sha256`: post-quantum, and as cheap as curve25519 (40 against 39 ms a login), where sntrup761 took 104 (lab 43479854381). | measured (login cost) | A client older than OpenSSH 9.9 or golang.org/x/crypto/ssh v0.38.0 is refused (`image/README.md`). |
| `/etc/sudoers.d/spin` | configuration | spin has no password, so sudo asks for none. The boundary is the VM. | function | Nothing. |
| `getty@.service.d/term.conf`, `serial-getty@.service.d/term.conf` | configuration | The kernel command line sets `TERM=dumb` so that systemd does not query the console. A login on the console would inherit it; these give the getty `linux` and `vt220`. | function | Nothing. |

## Services, sockets and configuration

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `docker.socket`, `containerd.socket`, `sockets.target.wants/containerd.socket`; `multi-user.target.wants/docker.service` and `multi-user.target.wants/containerd.service` removed | deferred | dockerd and containerd start at the first connection to their socket, not at boot: when a workspace first runs `docker` (or anything that dials `/run/docker.sock` or containerd's socket), never before, and never if it does not. A checkpoint taken before that holds neither daemon. | measured: +13.9 [+11.4, +18.2] cold, +55.7 [+12.0, +73.9] restored, +40.7 [+33.5, +59.0] restored into prestarted (lab 45034963004) | Deferred: the first `docker` command waits for dockerd and containerd to start, 0.47 s from the command to `docker info` answering on two vCPUs (`boot:workspace`, lab 45036254377; one vCPU not timed). Later commands do not wait. The image's `docker.socket` is mode 0666, group root, where Docker's is 0660, group docker: any local user reaches the daemon (#509). |
| `/etc/docker/daemon.json` | configuration | Container logs rotate at 10 MB, 3 files, so a long-running container does not fill the overlay; default address pools of /24s. | function | Nothing. |
| `/etc/apt/sources.list.d/docker.sources`, `/etc/apt/keyrings/docker.asc` | added | Docker's repository, so `apt upgrade` updates docker in a workspace. The key is a file in `rootfs/` (fingerprint `9DC858229FC7DD38854AE2D88D81803C0EBFCD88`, no expiry), not fetched at build: the build installs the pinned packages by their SHA-256 and never reads Docker's repository. | function | Nothing. |
| `/etc/apt/sources.list.d/resolute.sources` | added | mkosi writes the archive's sources: the release, `-updates` and `-security`, with `deb-src`. Ubuntu's installer writes `ubuntu.sources` with the same pockets. | function | Nothing. |
| `/etc/systemd/journald.conf.d/volatile.conf` | configuration | The journal lives in `/run` (RAM), capped at 4 MB, uncompressed, not forwarded to the console or syslog. | measured, with the flush's mask: +6.1 [+2.3, +15.8] cold, +6.5 [+3.1, +13.1] prestarted, restored in noise (lab 45034963004) | The journal does not survive a reboot of the workspace, and 4 MB holds little: a busy workspace's journal rotates in minutes (#514). |
| `default.target` | configuration | `multi-user.target`, set explicitly. | function | Nothing: there is no display manager. |
| `/etc/systemd/network/99-default.link.d/traditional-naming.conf` | configuration | Interfaces keep the kernel's names (`eth0`), which is what `net.ifnames=0` on the command line also asks for. | function | Nothing. |
| `/etc/locale.conf`, `/etc/default/locale` | configuration | `LANG=en_US.UTF-8`, and that locale generated at build time. | function; `boot:workspace` checks it sets without a warning | Other locales must be generated with `locale-gen`. |
| `tmp.mount.d/10-size.conf` | configuration | `/tmp` is a tmpfs capped at 25% of memory. It is in RAM, so a workspace that fills it takes memory, and a cap keeps one runaway from taking all of it. | function | `/tmp` larger than a quarter of memory. |
| `lru-gen.conf` | configuration | MGLRU's `min_ttl_ms=1000`, so the kernel's OOM killer acts before a guest thrashes. | measured, memory (`47aa681`; docs/log.md, 09-28) | Nothing. |
| `/etc/udev/rules.d/40-spin-hotadd.rules` | added | A hot-plugged CPU comes online. Without it, a core attached to a running workspace stays offline. | function; `boot:hotadd`, `boot:cpu-plug` | Nothing. |
| `spin-machine-pressure.service`, `spin-machine-pressure.timer`, `timers.target.wants/spin-machine-pressure.timer` | added, deferred | The pressure probe's sender, started by a timer 10 s after the boot, at idle priority. | measured: adding it moved neither number (labs 44926664003, 44926917170) | Deferred: the host gets no pressure reports for the first 10 s (#401). |

## Details of the deviations

The tables above say what each deviation is. These notes keep what the scripts and drop-ins used to
say about how each was found and measured. A script or drop-in points to the subsection that
explains it.

### The package list

- **libpam-systemd** is named, not left to arrive. It came in with docker's `dbus-user-session`,
  asking for a logind, when the scripts run apt in the chroot, where the rebuilt systemd's local
  repository is not, and the archive's `libpam-systemd` wants the archive's systemd exactly. Without
  it nothing registers a login with logind.
- **libpcre2-8-0** is named for the same reason: journalctl dlopens it for `--grep`.
- **GNU coreutils**: mkosi installs the essential set, uutils' included, before the list, so the
  list swaps them with apt's trailing `-` (remove). `rust-coreutils` is named too, since the
  essential set installed it as asked for and autoremove would leave it.
- **bubblewrap, socat, ripgrep**: Codex's Linux sandbox and Claude Code's sandbox-runtime are both
  bwrap plus seccomp, neither uses Landlock (#221, checked in their source 2026-11-12). Codex
  carries its own bwrap; sandbox-runtime refuses to start without bwrap and socat on `PATH`, socat
  being its network proxy. It finds the `.git/hooks` and editor configs below the working directory
  to mount read-only with rg: without rg the scan fails, logs at debug level, and the command runs
  without those denies.
- **file**, because scripts branch on what it says, and a `command not found` there reads as the
  archive being wrong. **patch** beside diffutils: only `diff` arrived with what brings it.
  **unzip**, because a release asset downloaded with curl is usually a `.zip`; there is no zip.
- **locales**, because the cache pre-warm calls `locale-gen` and `iconvconfig`.
- **RemoveFiles** removes the identity files and the caches. `/etc/resolv.conf` is the sharpest: the
  guest bind-mounts one in and refuses to mount over a file that is not there, so a stale one baked
  in would be the one every container reads. `/etc/machine-id` is handled in `build.sh` instead,
  because it must exist and be empty.
- **Docker's packages** come from Docker's apt repository, added by `install-dev-tools.sh`;
  `buildkit-qemu-*` emulators are removed from BuildKit's tarball.

### Users and sessions

- **The workspace user.** `spin`, uid 1000, in `sudo` and `docker` (the docker group exists because
  `install-dev-tools.sh` ran first: a user who must sudo for every container runs everything as
  root). Both root and spin are locked: a password in a filesystem every VM boots read-only would be
  the same credential on every machine. The way in is a certificate placed by whoever provisions
  the VM. `spin-machine-dev shell` logs its console in as root with an `agetty.autologin`
  credential on its own kernel command line (`boot:shell`), and the image carries no autologin.
- **The user CA.** `TrustedUserCAKeys /etc/ssh/spin_user_ca.pub` names a file that arrives later:
  at build time there is no host to ask, and spin's supervisor writes it from the registration
  response. sshd re-reads it per authentication, so a workspace on a host with no CA has no file and
  accepts no certificates. At every launch spin's handoff writes a drop-in read before this one
  (`00-spin-owner.conf`): no `authorized_keys`, no password, and the principal a certificate may log
  in as asked of the supervisor, which answers only for a key the owner's signed word lists. Without
  this line nothing logs in at all. The script asserts `sshd_config` includes `sshd_config.d`,
  because the failure is silent: sshd starts, key logins work, and certificates are dead.
- **One key exchange.** sshd takes the first value it reads for a keyword, and spin's handoff writes
  drop-ins named `00-*` read before `10-spin-kex.conf`: none of them may set `KexAlgorithms`. A login
  with `mlkem768x25519-sha256` took 40 ms and with curve25519 39 on the bench fleet; sntrup761x25519,
  what OpenSSH 8.5-9.8 picks, took 104, its key generation alone ~39 ms of the client's (lab
  43479854381, 2026-11-13). docs/machine.md says which clients that leaves out.
- **root lingers, spin does not.** spin logs in as root, and a login waits for its manager, so
  root's starts in the boot. Started inside the login, the manager cost the first login +44.4
  [+37.7, +52.3] (lab 45030778365); started in the boot, with the sockets' environment read once,
  the first login moved +4.7 [−0.2, +12.2], sshd accepting +5.6 [+1.1, +10.1], and the restored login
  −10.8 [−15.9, −6.6] (bench, spin's shape and login, against v20261122.03, 40 and 20 a side, labs
  45031644179, 45031644114, 2026-11-22). spin lingered from 2026-11-12 to 2026-11-20, when sessions
  did not wait for a manager and its `user@1000` was a `systemd --user` in every boot for a user
  nobody logged in as (the first login 294 against 316 ms without it, 6 boots each). A login as spin,
  as the lane's is, waits for its own manager: +29.7 [+22.8, +42.1] (lab 45031687433).
- **Sessions of class user-early.** logind answers a session request once its scope, the runtime
  directory and, for a class that wants one, the user's manager are started, and orders the scope
  after `systemd-user-sessions.service` unless the class is early (systemd v259.9,
  `src/login/logind-session.c`: `session_job_pending`, `session_start_scope`). A login of class user
  waited for the end of the boot: 472 against 436 ms (lab 43518044150). From 2026-11-13 sessions were
  `user-early-light`, which wait for no manager, and the first login's own `systemctl --user` failed
  when it came before the manager (#467). From 2026-11-20 to 11-22 a drop-in
  (`user-runtime-dir@.service.d/20-manager.conf`) started every login's manager beside its session
  (#462); class user-early replaced it.
- **The sockets' environment.** dbus.socket and ssh-agent.socket each ran `systemctl --user
  set-environment` when they started, ~15 ms of a login's ~48 on the one vCPU while it waits for its
  manager. Their `10-env.conf` drop-ins empty those `ExecStartPost=`/`ExecStopPre=`, and
  `/etc/environment.d/10-user-sockets.conf` sets the same two variables once: −17.1 [−27.0, −10.7]
  against class user-early alone (lab 45030778365).
- **logind's seats directory** (`10-seats.conf`). pam_systemd decides whether to register a session
  by `access("/run/systemd/seats/")`, a test for "is this a system with logind", not "is logind up".
  On a false answer it logs "Skipping logind registration as logind is not running" and returns
  success: the login lands with no `XDG_RUNTIME_DIR`, no session and no user manager, and nothing
  reports an error. logind creates the directory itself, so the test is false exactly while logind
  has never run. Making it in the Varlink socket's `ExecStartPre=` makes the test true before
  logind runs, and the socket's `Service=systemd-logind.service` then starts logind for the login,
  on every login path (console getty and su too). `ExecStartPre` and not `RuntimeDirectory=`: a unit
  with no `Exec*` line never applies its execution context, so `RuntimeDirectory=` alone creates
  nothing, and then benchmarks better, measuring logins with no session. Found when logind was
  deferred to the first login (2026-09-12, `task boot:bench`, 25 boots each: 248/275 against 272/294
  with logind wanted by multi-user.target; the fork is 4 ms of it; masking logind would be 7 ms
  better again and leave every login without a session). logind is in the boot again, but a login
  can still arrive before it starts.
- **logind in the boot.** It stays in the boot transaction, as the package wants it, so the first
  login does not start it. Taking it out was worth 24 ms to a console prompt (2026-09-12) and moved
  logind, dbus-broker and the user manager into the first login: with logind in the boot the first
  SSH login over vsock came 60 ms sooner, 510/525 against 570/626 ms p50/p95, sshd accepting no later
  (321 against 324), 10 boots each on the bench fleet (lab 43308825692, 2026-11-12).
- **No /run/nologin at boot.** In the lane the first login's sshd ran first and was refused: "Access
  denied for user spin by PAM account configuration [preauth]". Shutting down still writes it,
  through `systemd-user-sessions.service`'s ExecStop.
- **sudo without a password**, because there is none. The boundary is the VM, but a workload that
  expects not to be root by default gets what it expects.
- **The MOTD.** The image's own replacement, a line saying how long the boot took, ran
  `systemd-analyze` inside every login; a boot time belongs to `task boot:ssh-first` and the
  release's report (removed 2026-11-12). `pam_motd` goes from every stack that has it, not only
  ssh's: with it enabled, whatever an upgrade restores into `/etc/update-motd.d` runs at the next
  login, and `build.sh` asserts the lines are off rather than that the directory is empty.
- **The getty's TERM.** agetty is invoked as `- ${TERM}` and the unit sets none, so it takes PID 1's
  `dumb` (set on the command line to stop systemd waiting 334 ms twice for a serial port to answer a
  terminfo query, `machine/cmdline.go`): no colours, no clear, no cursor addressing. Before
  `TERM=dumb`, the kernel exec'd `/sbin/init` at 59 ms and systemd's first log line came at 852,
  two 334 ms timeouts (a terminfo query and a terminal reset) to a serial port with a file behind it;
  with it, 855 ms became 163-207 for the whole boot (`b5f361f`). `linux` and
  `vt220` are what systemd picks for a virtual console and a serial line when nothing says otherwise.

### The first login's target

`spin-machine-login.target` is the boot's default target. It wants the units a workspace's first
login needs: `ssh.socket`, `ssh.service` and its keygen, the image's vsock socket, journald,
logind with its Varlink socket, the system bus, `/tmp`, and spin's supervisor by its name
(`spin-supervisor.service`, spin-stack/spin's `deploy/guest/extension/supervisor`). Each of those
starts without default dependencies, so the target pulls in neither `sysinit.target` nor
`basic.target`, and PID 1 loads only what the target reaches before the login.
`spin-machine-boot-rest.service`, wanted by the target, starts `multi-user.target` once the boot's
first jobs are done (`Type=idle`, at most 5 s later): every unit still runs, after the login's.

- **Why.** On one vCPU, every unit started before the first login is on its clock (#553, #564).
  A bound with the rest started 3 s later read −55.5 [−63.0, −51.6] (lab 45089584596, 20 a side),
  and PID 1's CPU before the login 55 → 38 ms (lab 45089584735). Prior art: Kata's
  `kata-containers.target`, Sony's critical target (LPC 2024), Axis's targets (ASG 2025), none
  timed against a login (#574).
- **Why after the boot's first jobs.** Three triggers were measured with spin's supervisor off
  `basic.target` (lab 45090246267, 20 a side): `Type=idle` −59.2 [−69.1, −47.4], a timer 2 s after
  the boot −66.5 [−75.2, −56.1], sshd's PAM stack starting it at the first session −52.0
  [−61.6, −37.4]. The timer starts the rest at an arbitrary time whatever the login, and docker's
  socket waits for it; the PAM trigger ran beside the login's last steps (accept to login +15.3).
- **spin's supervisor.** spin's unit has default dependencies and `WantedBy=multi-user.target`. The
  target wants it by name, so spin's login does not wait for the rest; its default dependencies
  still pull `sysinit.target` and `basic.target` in before the login. Off them (the
  probe's forwarder with `forwarder-early`'s drop-in, which `--edit forwarder-early` makes on a
  release that has this target), the first login read −45.3 [−50.2, −39.4] against the release, but
  the second login +43.7 [+41.1, +46.1]: the first login then comes before the boot's first jobs
  are done, and the rest of the boot runs when the second login comes (lab 45098372930,
  v20261123.02, 40 a side; spin-stack/spin#1653).
- **logind's Varlink socket.** systemd refuses to start a socket whose service already runs, so it
  starts with logind or not at all. Without it in the target, `boot:logind` failed (lab 45090950033).
- **The restore.** A probe's checkpoint is taken once the boot has finished (#578), so the rest of
  the boot is done in every state it saves. The restored login read +1.1 [−6.0, +5.2] evicted and
  +2.2 [−10.6, +12.9] cached, both within noise (lab 45098372897, v20261123.02, 40 a side). The
  guest's boot finishes 35.2 ms later than the release's, since the rest of it runs after the login.
- **The console prompt.** The serial getty is part of `multi-user.target`, so its prompt comes
  +15.5 [+11.5, +19.0] ms later (lab 45098372930). Nothing a workspace does waits for the getty:
  spin reaches it over SSH, and the console is for a person debugging a guest.

### The host keys

`spin-machine-sshd-keygen.service` replaces Ubuntu's `sshd-keygen.service`, whose
`ConditionFirstBoot=yes` has not fired by the time ssh.service asks sshd to validate its
configuration, so sshd found no keys, exited 1, and systemd gave up after five tries ("Start request
repeated too quickly"). Every boot here is a first boot, so keys live exactly as long as the VM.

- **From the boot's first dispatch.** Wanted by `sysinit.target`, `DefaultDependencies=no`, and not
  ordered before sysinit (nothing there waits for host keys, and ordering it there would hold the
  vsock socket behind the generation). Started by the first sshd instead, the generation sat inside
  the first login: from the boot, accept to first login 177 → 155 ms and the first login 486 → 468,
  20 boots each on the bench fleet (lab 43463472762, 2026-11-13). The vsock socket listens from the
  first dispatch too, and a connection that arrives first waits for the keys.
- **Not pulled in by ssh.socket.** Hanging it off the socket put it inside `sockets.target`, and with
  default dependencies systemd broke the cycle by dropping an edge at random ("basic.target: Found
  ordering cycle: sockets.target/start after ssh.socket/start after
  spin-machine-sshd-keygen.service/start after basic.target/start").
- **sshd@ waits too** (`sshd@.service.d/keygen.conf`). systemd-ssh-generator listens on AF_VSOCK port
  22 when the machine has a vsock device, and on `/run/ssh-unix-local/socket` always, each starting
  `sshd@.service` per connection. With the keygen wanted by ssh.service alone, until something had
  connected over TCP there were no keys and the per-connection sshd dropped every connection: over
  vsock the image could not be logged into at all (2026-11-12). A drop-in on the template, because
  `Before=sshd@.service` in the keygen unit orders against no instance.
- **Ed25519 only**, skipped when it exists, so a VM whose provisioner placed its own key keeps it.
  Not `ssh-keygen -A`: it also makes RSA, which was the whole cost of the unit (309 ms of
  `systemd-analyze blame` in a guest, 2026-09-24; on the host RSA 141 ms against 6 for ECDSA and 5
  for Ed25519). sshd skips a default host key that is not there, and every client since OpenSSH 6.5
  (2014) takes Ed25519. ECDSA went too: −5.0 [−8.3, −1.8] ms, sshd accepting −4.4 [−6.9, −1.9] (60
  boots each, lab 44979726719, 2026-11-22).
- **Not when spin hands over its key** (`ConditionPathExists=!/run/spin/ssh/ssh_host_ed25519_key`,
  #515). spin makes the workspace's key itself and pins it: its vminitd makes an Ed25519 key, the
  launch claim carries the public half, and spin connects with that key fixed. Before it execs
  systemd, vminitd writes the key to that path, with `sshd_config.d/00-spin-host-key.conf` naming
  it as sshd's only `HostKey` (spin-stack/spin at 1bd3414, `internal/guest/vminit/handoff`). A key
  made here would never be loaded, and ssh.service would wait for it. The path is spin's contract
  with the image (spin-stack/spin#1650). The cold first login came −6.9 [−9.6, −2.4] ms, sshd
  accepting −6.8 [−10.4, −1.8], 120 boots a side on the bench, spin's shape and login, both arms with
  spin's handoff standing in (lab 45086250796, 2026-11-22). Restored, in noise (lab 45085877238).
  The lab's `login=spin` does not hand over a key unless asked (#575).
- **Wanted by sysinit.target alone.** `ssh.service.wants/spin-machine-sshd-keygen.service`, a
  symlink checked in with `2df3dbf`, wanted the unit a second time in the same transaction, and
  was removed (#515): taken away, the first login read −1.6 [−3.7, +4.3], 120 a side (lab
  45086250796).
- **Ubuntu's sshd-keygen without default dependencies** (`sshd-keygen.service.d/10-lane.conf`). Its
  condition never holds here, but a job waits for its ordering before its condition is checked: with
  its default dependencies it held ssh.service until basic.target (laptop, v20261120.03,
  2026-11-21).

### The lane

- **The image's vsock lane** (`sshd-vsock.socket.d/10-lane.conf`, `sshd@.service.d/10-lane.conf`,
  logind's, the runtime directory's and the manager's `10-lane.conf`, the dbus.socket copy). Without
  default dependencies none waits for `sysinit.target` (journald, udev) or `basic.target`. Together
  with no `/run/nologin` at boot and sessions of an early class, the first login over vsock came
  478 → 402 ms on the bench fleet (lab 43518915618, 2026-11-13). Each part alone gained little: the
  socket early accepted 60 ms sooner and the login then waited for logind and the manager. `dbus.socket`
  orders itself after `sysinit.target` in its own `[Unit]`, which a drop-in cannot take back, so the
  image has a copy without `Wants=sysinit.target` and `After=sysinit.target`.
- **logind's Varlink socket** kept `After=sysinit.target` while the service did not: logind was ready
  ~230 ms into the boot, behind udevd (lab 44557605832), and pam_systemd waited ~32 ms from PAM's
  session open to logind's new session against 1 ms for a second login (lab 44557162195). Without
  them, 20 boots each (labs 44558035927, 44558053261, 2026-11-19): that hop 24.5/41.8 → 15.8/22.7 ms,
  accept to login 131/137 → 120/128, the first login 332/355 → 328/337: logind starting sooner takes
  the CPU sshd was starting on. Ordering logind after `modprobe@drm` cost nothing either way.
- **spin's path** (`ssh.socket`, `ssh.service`, `sshd-keygen.service`, each `10-lane.conf`). spin's
  supervisor forwards vsock port 22 to 127.0.0.1:22. ssh.service's own `After=` names
  `network.target`, `nss-user-lookup.target` and `auditd.service`, which nothing in this boot starts.
  With logind and the bus without a private `/tmp` and udev at the lowest weight, the cold first
  login came −59.4 [−65.9, −56.2] ms, sshd accepting 56 ms sooner (lab 44805051310, 20 boots each,
  2026-11-21, the probe's forwarder ordered as early). The order alone was −10.8 [−16.3, −5.2]: the
  login then ran beside udev's coldplug on the one vCPU, which the weight lets it pass.
- **No private /tmp for logind and the bus.** `PrivateTmp=` orders a unit after
  `systemd-tmpfiles-setup.service`, which waits for `local-fs.target`, and `DefaultDependencies=no`
  does not take that back: without it logind started ~50 ms sooner (laptop, 2026-11-21). logind,
  under `ProtectSystem=strict`, sees the machine's `/tmp` read-only; the broker, root under
  `ProtectSystem=full`, can read and write it. Neither keeps anything in `/tmp`.
- **ssh.service without `sshd -t`.** `sshd -D` parses the same configuration and refuses to start on
  an error, and `Restart=on-failure` with `RestartPreventExitStatus=255` treats both the same. The
  check stays on reload (`ExecReload=`), which spin's supervisor runs when it installs the user CA: a
  bad configuration then leaves the running sshd alone rather than sending it a SIGHUP it would die
  on. An empty `ExecStartPre=` is what resets the list.

### udev

- **udev is on.** Masked from 2026-09 for six months, it saved nothing (826 against 829 ms) and cost
  a login: `serial-getty@ttyS0` carries `BindsTo=dev-ttyS0.device`, a `.device` unit exists only
  if udev announced it, and a drop-in clearing `BindsTo` did not bring the login back. A hot-plugged
  CPU never produced an add event either.
- **The coldplug filter** (`10-coldplug.conf`). The trigger replays an add for each of ~170 devices
  at boot (ACPI's 39, sixteen memory blocks, bdi, mem, pci, platform). Filtered, the lane's login came
  436 → 402 ms on two vCPUs (lab 43518915618, 2026-11-13). It keeps block and net; virtio; misc (vsock,
  fuse, tun); input (the power button's `power-switch` tag, which logind's shutdown on
  `system_powerdown` needs); the clock devices; the serial console; and cpu, because a core a consumer
  attached while its own init was PID 1 is onlined by this replay reaching `40-spin-hotadd.rules` and
  by nothing else (`task boot:hotadd`). Compared device by device with the full replay, what differs
  is the group of `/dev/ptmx` and `/dev/tty` (mode 666 either way), dialout on ttyS1-3 and kmem on
  `/dev/port`. A device that arrives later is udev's as ever: hotplug is a new event.
- **The lowest weight while the boot runs** (`20-startup.conf`). `StartupCPUWeight=` holds only until
  the boot's jobs are done; a device plugged later is handled at the normal weight. Alone it did
  nothing on spin's path, where the login waited for basic.target, which waited for udev (#329); with
  the login early it is most of the −59.4 ms. It costs the console prompt +51 ms and the second login
  +6.1 ms (udev's deferred work runs after the first login), same run (lab 44805051310).
- **Not before sysinit.target.** At the lowest weight udevd's start came after every early unit, and
  `sysinit.target`, `basic.target` and spin's supervisor waited for it. Without the order: cold first
  login −48.6 [−51.7, −42.6], restored −2.0 [−8.0, +4.7], p95 114 against 114 (30 a side, lab
  44987412171, 2026-11-22); basic.target 286 → 242 ms on the guest's clock, udevd ready after the
  login (lab 44977226981). A checkpoint saved with udev at work has the guest's tick running, and
  QEMU's patch 0005 keeps its restore from sleeping ~450 ms; without it the restored p95 is 539
  (#452).
- **The hot-add rule** (`40-spin-hotadd.rules`). Ubuntu's `40-vm-hotadd.rules` does nothing here: it
  checks the DMI vendor for Hyper-V or Xen and jumps past itself otherwise, and this machine reports
  QEMU. The rule is ungated, since a hypervisor test is the mistake being fixed: a CPU added and left
  offline is a core somebody paid for. Without it the host would have to reach into the guest to
  online each core, a second mechanism that stops being reachable when the guest's init hands over.
  Memory has no rule: the kernel onlines each block (`memhp_default_state=online`,
  `machine/cmdline.go`), and a rule would read the block offline, write after the kernel has, and log
  `EINVAL` for every block.

### Units, generators and the manager

- **The masks' measurement.** The generator group includes `systemd-factory-reset-generator`, which
  cannot do anything on a machine systemd logs as not booted in EFI mode: with a tpm2 generator that
  was there too, worth 1.0 ms of the 26 ms systemd spends loading units and building the initial
  transaction (2026-09-26, the unitload probe, `ad05544`, asking `systemd --test`; the same number from
  a debug-logged boot is 212 ms, the difference being the logging).
- **The manager's logging.** `LogLevel=err`, `LogTarget=journal`, `ShowStatus=no`,
  `StatusUnitFormat=name` and `kernel.printk = 3 3 3 3` were set and taken out together: +2.4 [−2.3,
  +7.8] at 20 boots and −1.8 [−7.7, +3.2] at 40 (labs 44818953987, 44822157367, 2026-11-21). The
  console stays quiet through the command line's `quiet loglevel=3`.
- **Masked, not merely disabled**, for the timers: a link to `/dev/null` survives a package upgrade
  putting the unit's `[Install]` link back.
- **The cache pre-warm.** `optimize-systemd.sh` runs `ldconfig`, `locale-gen en_US.UTF-8`,
  `update-locale`, `iconvconfig` and `fc-cache` at build time, then deletes documentation, man pages,
  apt's lists and any journal.
- **docker and containerd by socket.** The two services' enable links are removed and the two sockets
  enabled; the links dangle until `rootfs/` is copied.

### Memory

- **MGLRU's `min_ttl_ms=1000`** (`lru-gen.conf`). When the pages used in the last second cannot all
  be kept, the kernel calls the OOM killer instead of evicting and re-reading them. Without it a guest
  whose memory fills slowly evicts its programs' code and faults it back in, page cache still counts
  as reclaimable, and the OOM killer never comes: the VM is up and answers nothing. Measured
  2026-09-28 with the memory-policy probe (`337c08d`), 1 GiB, 2 vCPUs, memory taken at ~80 MB/s while a
  session re-read its libraries, 3 boots: the worst SSH login 611-1785 ms without and 105-106 ms with,
  the process killed after 11.2 s instead of 13.8. 1000 ms is the kernel documentation's suggestion;
  `kernel/Dockerfile` asserts `CONFIG_LRU_GEN`.
- **`/tmp` at a quarter of memory**, not systemd's half. A tmpfs belongs to no process, so the OOM
  killer cannot take it back and kills whatever it finds instead, which `min_ttl_ms` makes it do
  sooner: with tmpfs holding all but 16 MiB of a 1 GiB guest it killed the console's getty and the
  session that filled it (2026-09-28). `Options=` replaces the whole list, so it is the shipped unit's
  with the size changed; `%%` is a literal `%` in a unit file.

### The pressure sender

The guest's half of the pressure probe: PSI triggers on CPU, memory and IO, and OOM kills, written
to the host over vsock as they happen (docs/machine.md, "Pressure"). At the lowest priority (`Nice=19`,
idle CPU and IO classes), since it reports on a guest whose CPU may be what is short. As root,
because a trigger with a window under 2 s is root's alone. Out of the OOM killer's way
(`OOMScoreAdjust=-900`), which otherwise took it in the OOM it was reporting (`task boot:pressure`,
2026-11-21); it holds a few MiB. Started 10 s after the boot: ordered after `multi-user.target` alone
it started beside the first login, 2.6-3.3 ms more between sshd accepting and the login in three runs
([+1.1, +4.7] at 20 boots, [+2.1, +4.9] and [+1.5, +4.2] at 40; labs 44921602512, 44924252325,
44924755505), the last with `Type=idle`, which did not move it. On a restore it is already running
with its triggers armed and dials the host again by itself.

### Timers

All five of the distribution's timers carry `Persistent=true`: a VM resumed from a checkpoint that
sat for a week catches up at the moment it is handed a workload. `motd-news.timer`'s `ExecStart` is
`/etc/update-motd.d/50-motd-news`, deleted with the MOTD, so it could only fail 203/EXEC twice a day
and stay in `systemctl --failed`. `fstrim.timer` is kept because the drives are opened
`discard=unmap`, so a guest's TRIM reaches the overlay and returns blocks to the host, and made hourly
because ext4 only trims block groups that freed something since their last trim, so a run with
nothing to give back is cheap.

## What a workspace keeps

`task boot:workspace` boots a release and checks what a workspace expects. On v20261122.03 (lab
45036254377, 2026-11-22, two vCPUs, 1 GiB) every one held: no unit failed and the system reached
`running`; the journal takes a line; `fstrim.timer` and `systemd-tmpfiles-clean.timer` wait; dpkg's
database is whole and `apt-get check` passes; `en_US.UTF-8` sets without a warning; localhost
resolves; spin has sudo; `/tmp` is a tmpfs; debugfs mounts by hand; docker answers on its socket and
runs a container. `boot:logind` checks sessions, user services and logout. The release workflow runs
both on the release's own image (`release.yml`, the bench job), so a release that loses one fails
there.

## What a workspace gives up

The same check asserts what the rows above say is given up, so a row that stops being true fails
it rather than going stale. On v20261122.03 (lab 45036254377):

| Given up | What a workspace sees | Row |
|---|---|---|
| A journal kept across a reboot | The journal is `/run/log/journal/…/system.journal`, in RAM, 4 MB. | `volatile.conf` (#514) |
| Unattended apt | No `apt-daily.timer` or `apt-daily-upgrade.timer`. | the timers |
| apt's package lists | `apt install` says "Unable to locate package" until `apt update` has run once. | `/var/lib/apt/lists/` |
| Time zones | No tzdata: the clock reads UTC and `TZ` is ignored. | `tzdata` (#512) |
| `timedatectl` | It fails: timedated is masked. | `systemd-timedated.service` |
| tracefs | `/sys/kernel/tracing` is empty: `lockdown=confidentiality` keeps tracefs's files out, mounted or not. | `sys-kernel-tracing.mount` |
| debugfs and `/dev/hugepages` at boot | Neither is mounted. debugfs mounts by hand; hugetlbfs is mounted by whoever wants it. | `sys-kernel-debug.mount`, `dev-hugepages.mount` |
| Some tools | No `python3`, `lsb_release`, `mkfs.ext4`, `man` or `xxd`. | the packages table (#512) |
| A daemon ready at login | The first `docker` command waits 0.47 s for dockerd and containerd (two vCPUs). | `docker.socket` (deferred) |

Issues this review opened, each a decision still to make: the docker socket's mode (#509),
the packages a workspace gets
(#512), `/etc/.updated` instead of five masks (#513), the journal's size and persistence (#514),
and the host keys from the first dispatch (#515).

## Measured in this review

Six deviations had no measurement of their own in spin's shape on spin's login. Each was taken
back to what Ubuntu ships by a `lab` edit, one arm each, interleaved against the release in one
`prestart` run: lab 45034963004, 2026-11-22, the bench fleet (c8id.4xlarge, nested), spin's shape
(1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login), 20 boots a
side, against v20261122.03 with `--edit lane`. A Δ is what taking the change back costs, so
positive means the change earns it. The rule is AGENTS.md's: a gain only when the interval excludes
0.

| Deviation | Edit | Cold first login | Restored | Decision |
|---|---|---|---|---|
| `nsswitch.conf` without the `db` entries | `carry-nsswitch` | +0.8 [−8.4, +5.2] | in noise | reverted: Ubuntu's file |
| The volatile journal, its flush masked | `carry-journal` | +6.1 [+2.3, +15.8] (prestarted +6.5 [+3.1, +13.1]) | in noise | kept |
| docker and containerd by socket | `carry-docker` | +13.9 [+11.4, +18.2] | +55.7 [+12.0, +73.9]; into prestarted +40.7 [+33.5, +59.0] | kept, deferred |
| The manager's timeouts and watchdogs (`fast-boot.conf`) | `carry-timeouts` | +0.3 [−5.2, +3.3] | in noise | reverted: systemd's defaults |
| logind, its socket, the user units and the bus without default dependencies | `carry-lane-logind` | +16.5 [+9.8, +20.4] (prestarted +11.0 [+6.2, +18.8]) | in noise | kept, now measured in spin's shape |
| Host keys from the boot's first dispatch | `carry-keygen` | −0.8 [−10.5, +3.5] | in noise | kept for the lane; on spin's path no key is made since #515 |

Reverted means gone from the image: what earns nothing is a difference to explain for nothing. A
workspace now gets Ubuntu's `nsswitch.conf` and systemd's 90 s start and stop timeouts, so a
service that takes longer than 15 s to start is no longer killed. docker's row is deferred: a
checkpoint taken before the first `docker` command holds neither daemon, which is the likely
reason the restore gains more than the cold boot (inferred: the run did not separate it). The edits for the four kept rows stay, so a release can measure
them again; the two reverted ones were deleted with what they took back.
