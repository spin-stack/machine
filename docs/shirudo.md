# shirudo on a machine of the release

A release carries shirudo in two halves, both built from the commit the kernel's shirudo hooks come
from (versions.yaml, `shirudo`; kernel/README.md, "shirudo's hooks"):

- **The guest half**, `usr/share/spin-stack/guest/shirudo` in the main tarball. It is the static
  binary a shirudo machine runs, from its initrd and from its dm-verity runtime image.
- **The operator half**, `usr/share/spin-stack/shirudo-operator/` in the second tarball,
  `spin-machine-VERSION-linux-x86_64.shirudo-operator.tar.gz`. It makes what a shirudo machine boots
  with around the guest half: the operator's key, the signed config image, the dm-verity runtime
  image, signed upatches and the systemd initrd. With both halves, a shirudo machine needs nothing
  beyond the release: no Go, no Docker and no build of shirudo (#800).

## The operator half

| path under `shirudo-operator/` | |
|---|---|
| `install` | run as root in a guest of the release: installs `debs/` offline and links the paths below into `/usr/local` |
| `libexec/shirudo/shirudo-build` | static, linked as `guest/shirudo` is. It signs images and upatches, and compiles upatches |
| `libexec/shirudo/llvm/` | LLVM's own clang and `llvm-objcopy` (`llvm-strip`) of the release `guest/shirudo` is compiled with (versions.yaml, `llvm`), stripped, and clang's headers |
| `libexec/shirudo/gen-image/`, `libexec/shirudo/gen-initrd/` | shirudo's `shirudo-gen-image`, `-verity`, `-runtime` and `-initrd`, and its dracut module |
| `share/shirudo/` | the BPF headers, the `vmlinux.h` of the release's kernel, the systemd units (`*.in`) and shirudo's `upatches/`, `kernel-obs` among them |
| `debs/` | the Ubuntu packages the guest's image lacks for this, as a flat apt repository: dracut, tpm2-tools, cryptsetup's `veritysetup`, binutils' `readelf`, `zstd`, `cpio`, and python3 for `kernel-obs`'s generator |
| `licenses.txt` | the licences of what `shirudo-build` links, and LLVM's. Each package's own is inside its `.deb` |

The layout is shirudo's own `make install-operator` layout under `/usr/local`. That is where
`guest/shirudo` looks for `shirudo-build`, and where `shirudo-build` looks for its clang, headers and
`vmlinux.h`. `install` links those paths to the tree wherever it is unpacked.

It runs in a guest of the release, as root, for two reasons. shirudo locks its memory, which a
host's memlock limit (8 MiB) refuses a normal user. And the guest's image is the one system whose
packages the operator half is built against. `install` installs the Ubuntu packages from `debs/`
alone, with no network. It refuses any package that would replace one the image has, so the
initrd is made of the image's own systemd (#777). The tree is 303 MB unpacked, clang alone 246 MB
(measured 2026-11-26), which is more than the base image's root has free (116 MB). It goes on a disk of its own.

## Running it

Unpack both tarballs into one directory, the operator tarball second. Put
`usr/share/spin-stack/guest` and `usr/share/spin-stack/shirudo-operator` on a disk, for example
an ext4 made by the release's own `bin/mkfs.ext4 -d`. Boot a machine of the release with that disk
attached and mount it in the guest. Then, as root:

```sh
/mnt/spin-stack/shirudo-operator/install
shirudo genkey --keys-dir=keys
shirudo-gen-image ...; shirudo-gen-runtime --binary /mnt/spin-stack/guest/shirudo ...
shirudo build ...; shirudo bpm build ...
shirudo-gen-initrd --kver "$(uname -r)" --no-kernel --binary /mnt/spin-stack/guest/shirudo ...
```

`boot/testdata/shirudo-operator.sh` is the whole flow as `boot:shirudo` runs it, every argument
included. It follows shirudo's boot integration guide, and `boot/shirudo_test.go` then boots the
shirudo machine from what it made. A lab probe that boots a shirudo machine takes the same path,
with no person involved (#810).

## Size

The operator tarball is 127 MB, measured 2026-11-26. It holds clang, `llvm-objcopy` and clang's
headers (93 MB), `debs/` (28 MB), `shirudo-build` (4.8 MB) and `share/` (1.6 MB). The main tarball
does not change.

## Checked

Every release runs `boot:shirudo` on both tarballs before it publishes (release.yml, `shirudo`).
It runs the operator half in a guest of the release, as above, then boots a machine with
`--lockdown shirudo` and a TPM from what that made. compat runs the same check on a change that
reaches shirudo, its pins, the kernel it hooks or the QEMU it boots on (compat.yml, `shirudo`).
