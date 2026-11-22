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
| The toolchain | `image/Dockerfile` | Ubuntu 26.04 by digest, mkosi from its release tarball by SHA-256, both from `versions.yaml`. The digests of Task, git-lfs and BuildKit are passed in as environment for the tree. mkosi cannot run under BuildKit (it unshares namespaces), so `spin-machine-dev build image` runs this container with `--cap-add SYS_ADMIN`. |
| Two rebuilt packages | `image/systemd`, `image/openssh` | Ubuntu's own systemd and OpenSSH source packages, rebuilt and versioned `+spin1`. systemd is built with Debian's `stage1` profile (no BPF framework in PID 1, no SELinux, audit, TPM2, homed). OpenSSH carries `userauth-delay-failures.patch`. Their rows are under "Packages". |
| The tree | `image/mkosi.conf` | mkosi installs the `Packages=` list from the archive, with the two rebuilt packages offered from a local repository. `WithDocs=no` keeps documentation out (the per-package copyright files stay). `RemoveFiles=` removes identity and caches. `mkosi.extra` is staged under `/usr/local/lib/spin-base`, not at its final paths. |
| The scripts | `image/mkosi.postinst.chroot` | In the tree, in this order: `install-dev-tools.sh`, `configure-system.sh`, the licence texts collected, the rebuilt packages held, `optimize-systemd.sh`, then `rootfs/` copied over `/`, then the staging directory deleted. |
| The filesystem | `image/build.sh` | Installs the pressure sender, empties `/etc/machine-id` and removes the rest of the identity, writes `licenses.txt` and `packages.txt` beside the image, checks the rebuilt packages are held, checks the inventory (below), makes a partitionless ext4 with this repository's own `mkfs.ext4`, checks it, opens it and asserts what follows, and wraps it in a read-only qcow2. |

**Why `rootfs/` is copied last.** `containerd.io` ships `/etc/containerd/config.toml` as a
conffile and installs during `install-dev-tools.sh`. A file already at that path stops dpkg at a
conffile prompt with no tty (`end of file on stdin at conffile prompt`, 2026-09-07). So nothing in
`mkosi.extra` reaches its final path until the packages are in. The enable links
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
| `modified` | A conffile whose MD5 is not the one dpkg recorded, a file a package makes from a template that is not the template (`/etc/nsswitch.conf`, `/etc/ssh/sshd_config`), or the session class added to `/etc/pam.d/common-session`. |
| `deleted` | A conffile, or a directory of a package's files, that the image does not have. |
| `unwanted` | An enable link `deb-systemd-helper` recorded for a package that the image removed. |
| `linger` | A user whose manager logind starts at boot. |

On 2026-11-22 the list had 120 lines. A file a maintainer script makes on every Ubuntu (the
accounts, `ld.so.cache`, systemd's own three enables) is not a deviation and the script says which
it skips. A unit Ubuntu itself ships disabled or masked, such as `systemd-gpt-auto-generator`, is
not one either: the image's `optimize-systemd.sh` linked it again, and the inventory does not count
a change that changes nothing.

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
| `spin-machine-sshd-keygen.service`, `sysinit.target.wants/spin-machine-sshd-keygen.service`, `ssh.service.wants/spin-machine-sshd-keygen.service`, `sshd@.service.d/keygen.conf` | added, reordered | Host keys are identity, so the image ships none. Ubuntu's `sshd-keygen.service` carries `ConditionFirstBoot=yes`, which never holds in time here, so without this unit `ssh.service` fails. It makes an Ed25519 key only, from the boot's first dispatch (`DefaultDependencies=no`), so the key is made beside the rest of the boot and not inside the first login. Every sshd, including the per-connection `sshd@` for vsock, waits for it. | Ed25519 only: measured, −5.0 [−8.3, −1.8] (lab 44979726719). From the first dispatch: lane 489 → 468 (lab 43483372488); in spin's shape: see "Measured in this review". | No ECDSA or RSA host key. A client that accepts only those cannot connect. |

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
| The docker-ce packages, from Docker's apt repository | added | Docker and containerd in a workspace. | function | Their versions are what Docker's repository serves on the day of the build: not pinned in `versions.yaml`, and Docker's signing key is fetched without a hash (issue opened by this review). |
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
the boot path unless it enables a unit), left open in the issue this review opened.

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
| `sys-kernel-debug.mount`, `sys-kernel-tracing.mount` | removed | Not mounted at boot. | debugfs and tracefs are not mounted. `mount -t tracefs nodev /sys/kernel/tracing` works, and `boot:workspace` checks it. |
| `systemd-tmpfiles-setup-dev.service`, `systemd-tmpfiles-setup-dev-early.service` | removed | They create static device nodes for modules not yet loaded. With no modules, devtmpfs and udev already made every node. | Nothing. |
| `dev-hugepages.mount` | removed | No huge pages are configured. | No `/dev/hugepages`; a program that wants it mounts hugetlbfs by hand. |
| `dev-mqueue.mount` | removed | The kernel has POSIX message queues and the syscalls work without the mount. | No `/dev/mqueue` to list queues in. |
| `systemd-timedated.service` | configuration | Time is the host's. timedated would let a session set a clock it does not own. It is bus-activated, never part of the boot, so the mask costs and saves no boot time. | function | `timedatectl` fails ("Unit dbus-org.freedesktop.timedate1.service is masked"). A restored guest's clock is stepped by spin (spin-stack/spin#1498). |

The masks of the units of `ConditionNeedsUpdate` are load-bearing, not removed work:

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `ldconfig.service`, `systemd-sysusers.service`, `systemd-hwdb-update.service`, `systemd-journal-catalog-update.service`, `systemd-update-done.service` | removed | They run when `/etc` is newer than `/etc/.updated`, which the image does not have, so unmasked they rebuild `hwdb.bin`, the linker cache and the catalog in every boot. The build already ran `ldconfig`, `systemd-sysusers` and `systemd-hwdb update` (the package scripts do). | laptop, 2 boots: +260 ms and QEMU's RSS +90 MiB unmasked (docs/boot-time.md) | A package installed in a workspace that drops a hwdb file or a sysusers.d file has it applied by its own maintainer script, as on Ubuntu; at the next boot nothing reapplies it. Writing `/etc/.updated` at build time would let the units stay and skip themselves (issue opened by this review). |

The distribution's background maintenance timers:

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `apt-daily.timer`, `apt-daily-upgrade.timer` | removed | They take the apt and dpkg locks, so a workload's own `apt install` fails with `Could not get lock /var/lib/dpkg/lock-frontend` at a moment its logs do not explain, and they reach the archive unasked. All five timers carry `Persistent=true`, so a VM resumed after a week catches up at the moment it is handed a workload. | function | No unattended package lists or upgrades. |
| `motd-news.timer` | removed | Its script was deleted with the MOTD, so it could only fail. | function | Nothing. |
| `dpkg-db-backup.timer` | removed | It copies the dpkg database daily. The release carries `packages.txt`. | function | No daily copy of `/var/lib/dpkg/status` in `/var/backups`. |
| `fstrim.timer.d/spin.conf` | configuration | fstrim hourly, not weekly, with up to 10 minutes' random delay. What a guest deletes stays allocated in its overlay and in the host's page cache until a trim reaches it: 657 MB to 21 MB on disk, 657 to 20 MB in the cache (run 36815822140). | measured (memory, not boot) | Nothing; a trim with nothing to give back is cheap. |

## Generators and tmpfiles rules turned off

Group C-generators, taken back together: cold first login +16.7 [+11.6, +23.3] (lab 44818953987).

| Deviation | Category | Why | A workspace gives up |
|---|---|---|---|
| `systemd-fstab-generator` | removed | There is no `/etc/fstab`; the root comes from the kernel command line, other disks are attached and mounted by whoever attaches them. | An `/etc/fstab` written in a workspace is ignored at the next boot. Mount units in `/etc/systemd/system` still work. |
| `systemd-debug-generator` | removed | It implements `systemd.mask=`, `systemd.wants=` and the debug shell from the command line. | Those command-line switches do nothing. The lab masks with links for this reason. |
| `systemd-sysv-generator`, `systemd-rc-local-generator` | removed | No SysV init scripts and no `rc.local` in the image. | An init script or `/etc/rc.local` added in a workspace does not run. |
| `systemd-hibernate-resume-generator`, `systemd-system-update-generator`, `systemd-factory-reset-generator` | removed | No hibernation, no offline updates, no EFI. | Nothing. |
| `systemd-pstore.conf`, `systemd-network.conf`, `x11.conf` (tmpfiles) | removed | No pstore, no networkd, no X11. | Nothing. |
| `systemd-nologin.conf` (tmpfiles) | reordered | It writes `/run/nologin` at boot, and `systemd-user-sessions` removes it late in the boot. Until then `pam_nologin` refuses every user, and the first login arrives before that ("Access denied for user spin by PAM account configuration"). Shutting down still writes it. | lane: part of 478 → 402 (lab 43518915618) | Nothing: logins are refused at shutdown as before. |

## The first login's lane: units reordered

These change when units start, not what they do. All of it still runs before or beside the first
login, on the clock. `DefaultDependencies=no` takes a unit out of the wait for `sysinit.target`
and `basic.target`; each of these drop-ins keeps `Conflicts=shutdown.target` and
`Before=shutdown.target`, so the unit still stops at shutdown.

| Deviation | Category | Why | Evidence | A workspace gives up |
|---|---|---|---|---|
| `ssh.socket.d/10-lane.conf`, `ssh.service.d/10-lane.conf`, `sshd-keygen.service.d/10-lane.conf` | reordered | spin's login reaches `ssh.service` through 127.0.0.1:22. ssh.service is ordered after Ubuntu's `sshd-keygen.service`, whose job waits for its ordering before its condition fails, so all three leave the default dependencies. | measured with the two rows below: −59.4 [−65.9, −56.2], the image's half alone −17.4 (labs 44805051310, 44807457606) | Nothing. |
| `systemd-logind.service.d/30-privatetmp.conf`, `dbus-broker.service.d/30-privatetmp.conf` | reordered | `PrivateTmp=` orders a unit after `systemd-tmpfiles-setup.service`. | the same | logind and the bus share the machine's `/tmp` (read-only to logind under `ProtectSystem=strict`). |
| `systemd-udevd.service.d/20-startup.conf`, `systemd-udev-trigger.service.d/20-startup.conf` | reordered | udev and its coldplug at `StartupCPUWeight=1` while the boot runs, so the login started beside them gets the one vCPU first. | the same | The console prompt 51 ms later (the serial getty waits for `dev-ttyS0.device`, which the coldplug announces). |
| `systemd-udevd.service`, `systemd-udev-trigger.service` (copies without `Before=sysinit.target`) | reordered, deferred | A drop-in cannot remove an ordering, so the image carries the units without that line. udevd at the lowest weight then no longer holds `sysinit.target`, `basic.target` and spin's supervisor. udevd becomes ready after the first login (at ~342 ms on the guest's clock, against the login's ~290). | measured: cold −48.6 [−51.7, −42.6], restored −2.0 [−8.0, +4.7] (labs 44987412171, 44987412210) | Deferred: udev's coldplug finishes after the first login. A device that only udev's rules prepare (permissions, symlinks under `/dev/disk/by-*`) appears a few tens of ms after the first login, not before it. A checkpoint taken with udev still at work needs QEMU's patch 0005 (#452). |
| `systemd-udev-trigger.service.d/10-coldplug.conf` | removed | The coldplug replays only the subsystems something here uses: block, net, virtio, misc, input (logind's power button), rtc, ptp, cpu (a core attached before systemd starts), and ttyS0. | group C-coldplug: cold +14.9 [+8.2, +19.8] (lab 44818953987) | Devices of other subsystems present at boot get no udev add event: no rules run for them until a later event. A USB or PCI device passed through at boot is not set up by udev. |
| `systemd-logind.service.d/10-lane.conf`, `systemd-logind-varlink.socket.d/20-lane.conf`, `user-runtime-dir@.service.d/10-lane.conf`, `user@.service.d/10-lane.conf`, `dbus.socket` (a copy without `After=sysinit.target`) | reordered | A login waits for logind, its runtime directory and its manager; none of them need what `sysinit.target` and `basic.target` gather. | lane, two vCPUs: 478 → 402 with the rows above (lab 43518915618); in spin's shape: see "Measured in this review" | Nothing known: logind, the bus and the managers run the same units, earlier. |
| `systemd-logind-varlink.socket.d/10-seats.conf` | configuration | Makes `/run/systemd/seats` before logind is reached. Without it, a login that arrives before logind has run gets no session, `XDG_RUNTIME_DIR` unset, and no error (2026-09-12). | function; `boot:logind` | Nothing. |
| `sshd-vsock.socket.d/10-lane.conf`, `sshd@.service.d/10-lane.conf` | reordered | The image's own vsock login, `login=lane` in the probes. spin's path does not use it. | lane | Nothing. |
| `ssh.service.d/20-no-config-test.conf` | removed | Ubuntu's ssh.service runs `sshd -t` before it starts. The check stays on reload. | measured: −3.2 [−7.2, −0.8], 60 a side (lab 44977044826) | A broken `sshd_config` fails at start rather than in a pre-check; the error is sshd's own. |

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
| `docker.socket`, `containerd.socket`, `sockets.target.wants/containerd.socket`; `multi-user.target.wants/docker.service` and `multi-user.target.wants/containerd.service` removed | deferred | dockerd and containerd start at the first connection to their socket, not at boot. | see "Measured in this review" | The first `docker` command waits for dockerd and containerd to start (seconds on one vCPU). The image's `docker.socket` is mode 0666, group root, where Docker's is 0660, group docker: any local user reaches the daemon (issue opened by this review). |
| `/etc/docker/daemon.json` | configuration | Container logs rotate at 10 MB, 3 files, so a long-running container does not fill the overlay; default address pools of /24s. | function | Nothing. |
| `/etc/containerd/config.toml` | configuration | Replaces containerd.io's file, which disables the CRI plugin, with one that enables it (runc, systemd cgroups, a pause image from registry.k8s.io). | unmeasured, no recorded reason (issue opened by this review) | Nothing missing; containerd loads a plugin docker does not use. |
| `/etc/apt/sources.list.d/docker.sources`, `/etc/apt/keyrings/docker.asc` | added | Docker's repository, so `apt upgrade` updates docker in a workspace. | function | Nothing. |
| `/etc/apt/sources.list.d/resolute.sources` | added | mkosi writes the archive's sources: the release, `-updates` and `-security`, with `deb-src`. Ubuntu's installer writes `ubuntu.sources` with the same pockets. | function | Nothing. |
| `/etc/systemd/system.conf.d/fast-boot.conf` | configuration | Units time out at 15 s to start and 10 s to stop, not 90 s, and the watchdogs are off. | see "Measured in this review" | A service of a workspace that takes longer than 15 s to start is killed and fails, unless it sets its own `TimeoutStartSec=`. |
| `/etc/systemd/journald.conf.d/volatile.conf` | configuration | The journal lives in `/run` (RAM), capped at 4 MB, uncompressed, not forwarded to the console or syslog. | see "Measured in this review" | The journal does not survive a reboot of the workspace, and 4 MB holds little: a busy workspace's journal rotates in minutes. |
| `/etc/nsswitch.conf` | configuration | Ubuntu's file without `db` before `files` for protocols, services, ethers and rpc, and without `netgroup: nis`. `libnss-db` is not installed, so the `db` entries only fail to load. | see "Measured in this review" | Nothing. |
| `default.target` | configuration | `multi-user.target`, set explicitly. | function | Nothing: there is no display manager. |
| `/etc/systemd/network/99-default.link.d/traditional-naming.conf` | configuration | Interfaces keep the kernel's names (`eth0`), which is what `net.ifnames=0` on the command line also asks for. | function | Nothing. |
| `/etc/locale.conf`, `/etc/default/locale` | configuration | `LANG=en_US.UTF-8`, and that locale generated at build time. | function; `boot:workspace` checks it sets without a warning | Other locales must be generated with `locale-gen`. |
| `tmp.mount.d/10-size.conf` | configuration | `/tmp` is a tmpfs capped at 25% of memory. It is in RAM, so a workspace that fills it takes memory, and a cap keeps one runaway from taking all of it. | function | `/tmp` larger than a quarter of memory. |
| `lru-gen.conf` | configuration | MGLRU's `min_ttl_ms=1000`, so the kernel's OOM killer acts before a guest thrashes. | measured, memory (`47aa681`; docs/log.md, 09-28) | Nothing. |
| `/etc/udev/rules.d/40-spin-hotadd.rules` | added | A hot-plugged CPU comes online. Without it, a core attached to a running workspace stays offline. | function; `boot:hotadd`, `boot:cpu-plug` | Nothing. |
| `spin-machine-pressure.service`, `spin-machine-pressure.timer`, `timers.target.wants/spin-machine-pressure.timer` | added, deferred | The pressure probe's sender, started by a timer 10 s after the boot, at idle priority. | measured: adding it moved neither number (labs 44926664003, 44926917170) | Deferred: the host gets no pressure reports for the first 10 s (#401). |

## What a workspace keeps

`task boot:workspace` boots a release and checks what a workspace expects: no unit failed, the
journal takes a line, `fstrim.timer` and `systemd-tmpfiles-clean.timer` wait, dpkg and apt are
whole, `en_US.UTF-8` sets without a warning, localhost resolves, spin has sudo, `/tmp` is a tmpfs,
tracefs mounts by hand, and docker answers on its socket and runs a container. It also asserts what
the rows above say is given up (no persistent journal, no apt lists before `apt update`, no time
zones, no `timedatectl`, tracefs and `/dev/hugepages` not mounted at boot), so a row that stops
being true fails the check rather than going stale. `boot:logind` checks sessions, user services
and logout.

## Measured in this review

Six deviations had no measurement of their own in spin's shape on spin's login: the journal's
settings, docker and containerd by socket, the manager's timeouts, `nsswitch.conf`, the lane's
logind, bus and user units, and the host keys from the first dispatch. Each is taken back to what
Ubuntu ships by a `lab` edit (`carry-journal`, `carry-docker`, `carry-timeouts`, `carry-nsswitch`,
`carry-lane-logind`, `carry-keygen`), one arm each, interleaved against the release in one
`prestart` run (lab 45034963004), and kept or reverted by AGENTS.md's acceptance rule. Until that
run is read, these rows say *unmeasured*.
