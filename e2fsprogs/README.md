# e2fsprogs

`mkfs.ext4`, `e2fsck`, `dumpe2fs` and `debugfs`, **statically linked** like QEMU, from a
pinned upstream tarball, together with the `mke2fs.conf` from the same tarball. What an ext4
is made with, its features and its block and inode sizes, is what the `mke2fs` that made it and
its configuration file default to, so the same command line on two distributions makes two
filesystems. The base image is made and checked with these and not the build container's,
which carries no e2fsprogs. A release ships all four and the configuration: `mkfs.ext4` and
`mke2fs.conf` for anything else that makes a filesystem this kernel mounts, and the other three
because the probes edit a copy of the base image with this `debugfs`. Set `MKE2FS_CONFIG` to the shipped file:
the binary alone reads the host's `/etc/mke2fs.conf`.

The shipped file is upstream's, and upstream and the Debian family differ in one place worth
knowing: under 512 MiB mke2fs uses its "small" profile, which upstream gives 1024-byte blocks
and Debian and Ubuntu do not. A 64 MiB filesystem made with this `mkfs.ext4` has 1024-byte
blocks unless `-b 4096` says otherwise, and such a filesystem refuses fs-verity with 4096-byte
Merkle blocks.
