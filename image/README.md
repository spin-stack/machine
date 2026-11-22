# Base image

A **qcow2 base, read-only, with a fresh qcow2 overlay per VM**:

```
qemu-img create -f qcow2 -F qcow2 -b rootfs.qcow2 overlay.qcow2
```

Copy-on-write happens in the block layer, where QEMU does it, rather than in a filesystem
stacked inside the guest.

The contents are an Ubuntu 26.04 userland with systemd as the guest's PID 1,
the tools a workspace expects, and the boot optimizations in
`mkosi.extra/usr/local/lib/spin-base/optimize-systemd.sh`.

Every package comes from the Ubuntu archive except systemd's and OpenSSH's. Those are Ubuntu's
own source packages rebuilt here (`systemd/Dockerfile`, which says what is built out and what it
measured, and `openssh/Dockerfile`, which applies the patches beside it), versioned `+spin1` and
held, so an `apt upgrade` in a workspace cannot bring back the archive's build. `build.sh` fails an
image whose systemd or sshd is not that build or whose packages are not held.
The rebuild has a cost. A package of the same source that the image does not carry, such as
`systemd-container` or `systemd-resolved`, asks for the archive's systemd at exactly its
version. apt therefore refuses to install it in a workspace rather than mix the two builds.

**ext4**, because the guest kernel has `EXT4_FS` and `OVERLAY_FS` and explicitly not
`XFS_FS`, `BTRFS_FS`, `SQUASHFS` or `EROFS_FS`. Another filesystem starts with a kernel config
change one directory over, in the config that `kernel/Dockerfile` asserts.

**Partitionless**, not a bootable disk with an ESP and a GPT, which nothing here would read.
mkosi produces the tree (`Format=directory`), and `build.sh` runs `mkfs.ext4 -d`, with no loop
device and no mount, and wraps the result with `qemu-img convert`. The `mkfs.ext4` is the
pinned one from [`e2fsprogs/`](../e2fsprogs/README.md), not the build container's. The base
image reserves no blocks for root (`-m 0`): the reservation is a proportion, so a filesystem
grown from the image onto a larger disk would keep 5% of the larger size.

## The two traps

- **The base must not be written to.** Every VM maps it read-only through a backing file and
  many VMs share one. A build step that opens it read-write invalidates every overlay in
  existence, and silently, because the overlays keep working until they read a cluster that
  moved. `build.sh` writes it `0444`, and `spin-machine-dev shell` checks its checksum across a boot.
- **No identity in the image.** No hostname, no machine-id, no `/etc/resolv.conf`, no SSH
  host keys, no random seed. A VM is handed its identity by whoever runs it. Anything
  baked in is shared by every VM that ever boots from it.
  `build.sh` asserts each of these on the finished filesystem: `/etc/machine-id` present
  and *empty*, everything else absent.

## Logging in: the SSH client has to speak ML-KEM

`sshd` offers one key exchange, `mlkem768x25519-sha256`, and refuses a client that has no
ML-KEM rather than falling back to a weaker one. The client sees:

```
Unable to negotiate with … port 22: no matching key exchange method found. Their offer: mlkem768x25519-sha256
```

A client that connects is one of:

- **OpenSSH 9.9 or later**, which `ssh -V` names. Earlier ones are refused, among them the 9.6
  Ubuntu 24.04 LTS ships (the bench fleet's runners have it, 2026-11-13): an operating system
  whose OpenSSH is older needs a newer one installed.
- **`golang.org/x/crypto/ssh` v0.38.0 or later, built with Go 1.24 or later.** It offers
  ML-KEM first by default, so a client that sets no `KeyExchanges` already connects.
- **Not** the `ssh2` npm package: no release up to 1.17.0 has ML-KEM.

Why one exchange: ML-KEM is post-quantum, and on the bench fleet a login with it cost what one
with curve25519 did (40 and 39 ms; `task boot:ssh-first`, lab run 43479854381, 2026-11-13).
sntrup761x25519, the other post-quantum exchange and the one OpenSSH 8.5-9.8 picks, took
104 ms, and its key generation alone was ~39 ms of the client's. The classical exchanges are
gone with it. The drop-in is `/etc/ssh/sshd_config.d/10-spin-kex.conf`, written by
`configure-system.sh` and asserted by `build.sh`. A drop-in that sets `KexAlgorithms` and is
read before it wins, because sshd keeps the first value it reads.

## The first login's lane

What a first SSH login goes through starts from the boot's first dispatch, ordered only by what
it needs: the vsock socket and the per-connection sshd (after the host keys), dbus, logind, and
the user's runtime directory and manager. None of them waits for `sysinit.target` or `basic.target`
(`etc/systemd/system/*.d/10-lane.conf`, and a copy of `dbus.socket` without its
`After=sysinit.target`, which a drop-in cannot take back). Two settings make that worth it:

- **No `/run/nologin` at boot.** Until `systemd-user-sessions` removes it late in the boot,
  `pam_nologin` refuses every user, and in the lane the first login arrives before that. Shutting
  down still writes it.
- **Sessions of class `user-early-light`.** logind holds a session of class `user` until the
  user's manager has started and the boot's `systemd-user-sessions` has run. A light, early
  session has its scope and `XDG_RUNTIME_DIR` and waits for neither. Nobody lingers: no user's
  manager starts in the boot.

udev's coldplug replays only the devices something here uses (`10-coldplug.conf`), so the
lane has the CPU. On the bench fleet the first login went 478 -> 402 ms with all of it (`task
boot:ssh-first`, lab run 43518915618, 2026-11-13). `configure-system.sh` and the drop-ins say what
each part did, and `build.sh` asserts each on the finished filesystem.

spin's own path to the first login is in the lane as well: ssh.socket and the one ssh.service
behind it (spin's supervisor forwards vsock to 127.0.0.1:22). Three kinds of drop-in put it
there:

- ssh.socket, ssh.service and Ubuntu's `sshd-keygen.service`, which ssh.service is ordered
  after, have no default dependencies (`10-lane.conf`).
- logind and dbus-broker have no private /tmp, which would order them after the filesystems
  (`30-privatetmp.conf`).
- udevd and its coldplug run at the lowest CPU weight while the boot runs (`20-startup.conf`),
  so the login started beside them gets the one vCPU first.

Together, on the bench fleet in spin's shape, the cold first login came 59.4 ms sooner and the
console prompt 51 ms later (lab 44805051310, 2026-11-21).

## Looking inside

```
spin-machine-dev shell                   # systemd, to a console login prompt
spin-machine-dev shell --init /bin/bash   # a bare shell, for when systemd is the broken thing
```

The default is `/sbin/init` because that is what this image is: a userland whose first
process is systemd. Booting a bare shell answers a different question. `systemd-analyze`
in it replies *"System has not been booted with systemd as init system (PID 1)"*, which is
true and useless.

It is `spin-machine boot` with no `--disk`: this QEMU and this kernel over `rootfs.qcow2`
under a throwaway qcow2 overlay in a temporary directory, with the serial console on stdio.

It boots with no initrd at all: `root=/dev/vda rw rootfstype=ext4 init=/sbin/init`, which the
kernel can serve because virtio-blk and ext4 are built in and `build.sh` writes a partitionless
filesystem. There is no debug initramfs: one that mounted the API filesystems, found the
root disk and exec'd would be a second init, doing what the init a consumer brings already
does. `systemd-udevd` being on removes the need for one.

Three things the shell has found, all of them true of the image and none of them visible
from outside:

- **The serial login exists because udev does.** `serial-getty@ttyS0` carries
  `BindsTo=dev-ttyS0.device`, and a `.device` unit exists only if udev announced it. This
  image once masked `systemd-udevd`, on the theory that a VM's hardware is fixed and
  discovering it is boot time spent. While it did, there was no login on the serial port at
  all, a drop-in clearing `BindsTo` did not bring it back, and the image had to carry an agetty
  unit of its own with the dependency removed. Masking bought nothing measurable and cost a
  ten-second `dev-ttyS0.device` timeout on every boot that had no initrd. The numbers are in
  `optimize-systemd.sh`, where the decision is.
- **`sshd` needs host keys generated at boot.** The image ships none, because they are
  identity. The distribution's `sshd-keygen.service` carries `ConditionFirstBoot=yes`, so it
  does not run before `sshd` is asked to validate a configuration with no keys, and
  `ssh.service` fails five times and gives up. `spin-machine-sshd-keygen.service` generates
  them instead. Every boot here *is* a first boot, since the root filesystem is a fresh
  overlay, so the keys live exactly as long as the VM does. Every sshd waits for it,
  including the per-connection `sshd@.service` that `systemd-ssh-generator` starts for vsock
  and `ssh .host`: without that, those connections found no host keys and were dropped. It
  starts from the boot's first dispatch, wanted by `sysinit.target` without default
  dependencies, so the keys are made while the rest of the boot runs and not inside the first
  login. The vsock socket listens from the first dispatch too (the lane, above), and a
  connection that arrives before the keys exist waits for them. A guest checkpointed after it has booted therefore carries its host keys, and
  whoever restores it hands each VM its own.
- **Two thirds of the boot was systemd asking the console questions.** The kernel execs
  `/sbin/init` at 59 ms and systemd's first log line arrived at 852, with nothing running in
  between. It was a terminfo query and a terminal reset, 334 ms of timeout each, waiting for
  a serial port with a file behind it to answer. `TERM=dumb` on the command line stops it
  asking: 855 ms to 163–207 ms for the whole boot. See `machine/cmdline.go`.

## Why mkosi does not run under BuildKit

mkosi assembles a root filesystem by unsharing a user and mount namespace, and BuildKit
forbids it (`mkosi was forbidden to unshare namespaces`, `PermissionError` on `unshare(2)`).
The alternatives were `RUN --security=insecure` or a container run with the privileges mkosi
needs. `RUN --security=insecure` needs a `buildkitd` started with an entitlement flag, so
`spin-machine-dev build image` would fail on any machine whose builder was created the ordinary
way. `Dockerfile` pins the
toolchain, `build.sh` is the build, and `spin-machine-dev build image` runs it in a container with
`--cap-add SYS_ADMIN` and seccomp and AppArmor unconfined (`internal/build/build.go`), not
`--privileged`.
