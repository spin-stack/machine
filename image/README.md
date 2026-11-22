# Base image

An Ubuntu 26.04 userland with systemd as the guest's PID 1, built as a **qcow2 base, read-only,
with a fresh qcow2 overlay per VM**:

```
qemu-img create -f qcow2 -F qcow2 -b rootfs.qcow2 overlay.qcow2
```

[`docs/image.md`](../docs/image.md) says how it is built, every way it differs from what Ubuntu
installs, why, what measured it and what a workspace gives up. The scripts and drop-ins here point
to it rather than explain themselves. `spin-machine-dev build image` builds it.

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

Why one exchange is in docs/image.md, "Users and sessions".

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
It boots with no initrd: `root=/dev/vda rw rootfstype=ext4 init=/sbin/init`. There is no debug
initramfs: one that mounted the API filesystems, found the root disk and exec'd would be a second
init, doing what the init a consumer brings already does.

What the shell has found about the image (the serial login exists because udev does, sshd needs
host keys made at boot, systemd once spent two thirds of the boot asking the console questions) is
in docs/image.md, "udev", "The host keys" and "Users and sessions".
