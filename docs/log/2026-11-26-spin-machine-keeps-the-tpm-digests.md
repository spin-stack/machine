---
date: "2026-11-26"
order: 90
area: machine
title: spin-machine boot --tpm keeps each file's digest instead of hashing the vmlinux at every boot (#776)
issues: [776]
prs: [799]
runs: [45884499622, 45838578696, 45843630680]
---
*Why:* the CLI hashed the 42 MB vmlinux, and any initrd, before every exec of QEMU, inside the probes' clock: +26.0 [+18.4, +37.2] (lab 45838578696) and +28.1 [+22.8, +37.2] (lab 45843630680) on every TPM machine the lab boots, a cost spin, which hashes once a file, never pays. *Done:* `cmd/spin-machine/digest.go` keeps each digest under the user's cache directory, keyed by the file's device and inode and checked against its size, modification and change times; a file changed within a second is hashed and not kept, as git's index and ccache's inode cache refuse a file too new for the kernel's coarse clock (#799). Hashing beside QEMU's start was not an option, since the digests are on QEMU's command line, and the release's `kernel_sha256` would have covered only a release's own kernel. *Measured* on the bench, spin's shape, both with a TPM, 40 a side: first login −28.7 [−35.1, −20.3], sshd accepting −28.6 [−36.5, −19.9], accept to login +0.7 [−1.0, +1.9] within noise (lab 45884499622). *Status:* carried in the CLI; a TPM tree through the CLI now compares with spin's cost.
