# qboot, with WRITE_POINTER

qboot at `8ca302e86d685fa05b16e2b208888243da319941` stops in its ACPI loader when
q35 supplies the WRITE_POINTER command needed by vmgenid. `write-pointer.patch`
adds that command and fw_cfg DMA writes. The pointer payload is little-endian, and
the DMA descriptor is big-endian. The patch applies a destination offset with
SELECT+SKIP before WRITE. Invalid pointer widths, missing or unallocated sources,
out-of-bounds writes, missing DMA and DMA errors fail rather than silently lose the
vmgenid address.
The destination is a writable fw_cfg file, not a guest allocation.

Protocol references:
[QEMU fw_cfg](https://www.qemu.org/docs/master/specs/fw_cfg.html) and
[the ACPI linker loader](https://github.com/qemu/qemu/blob/master/hw/acpi/bios-linker-loader.c).
The patches are GPL-2.0, like upstream. See COPYING and the repository NOTICE.

## PAM in three writes

qboot makes 0xc0000-0x100000 ram with seven one-byte configuration writes, one per PAM
register, and QEMU's q35 host bridge rebuilds the guest's memory map for each write that
touches one (`mch_update_pam`): every address space's flat view and KVM's memory slots.
`pam.patch` sets q35's registers, which start 4-aligned at 0x90, with a long, a word and a
byte. The values are immediates: once PAM0 is ram, 0xf0000-0x100000 reads zeroes until
`setup_hw` has shadowed the BIOS, so a table in `.rodata` would be read back as zeroes.
i440fx keeps the loop. This machine is q35 and nothing here boots the other.

## MTRRs

Stock qboot never touches the MTRRs, so the guest boots with them disabled ("MTRRs disabled by
BIOS"). Linux then maps uncached-minus every range it is asked to map write-back but does not
know as RAM. A virtio-pmem region read at 8.3 MB/s, against 1.1 GB/s under SeaBIOS, which
programs them (lab runs 36818224412 and 36818721809, 2026-10-01). `mtrr.patch` does what SeaBIOS
does, on the boot CPU only, because Linux gives its own MTRR state to the CPUs it starts. It
enables the MTRRs, write-back by default, and makes the 32-bit PCI hole, from the top of low RAM
to 4 GiB, uncacheable in naturally aligned power-of-two ranges. Fixed-range MTRRs stay off. If the hole does not fit the
variable ranges the CPU has, they are left disabled rather than caching MMIO.

## No walk of the PCI bus

Stock qboot walks the bus at every boot: it reads each function's interrupt pin and class,
writes an INTx line into its `PCI_INTERRUPT_LINE`, and numbers bridges. It does not assign BARs.
Linux does. Nothing on this machine reads what the walk leaves: Linux routes interrupts from
ACPI's `_PRT`, every device it drives uses MSI-X, and bus 0 carries no bridge. The walk was 173
configuration-space exits, 5.0 ms on nested KVM against 2.2 on bare metal (boot:firmware-stages,
lab runs 44535161371 and 44535156851, 2026-11-19). `no-pci-walk.patch` keeps the chipset check,
MMCONFIG and the BIOS32 entry, and drops the walk.

## Measured boot

A machine with a TPM (`machine.Spec.TPM`) is handed the events of its boot in the fw_cfg file
`opt/org.spin-stack/tpm-events`, and `tpm.patch` makes qboot measure them before it enters the
kernel (#742). It requests locality 0 of the tpm-crb, sends `TPM2_Startup(SU_CLEAR)`, reads the
active PCR banks (`TPM2_GetCapability`, `TPM_CAP_PCRS`), extends each event into all of them with
`TPM2_PCR_Extend`, and writes a TCG2 crypto-agile log (a `Spec ID Event03` naming the banks, then
one `TCG_PCR_EVENT2` per event) into `etc/tpm/log`, the area QEMU's TPM2 table points at (LASA,
LAML 64 KiB) and that the ACPI loader allocated. Linux exports it at
`/sys/kernel/security/tpm0/binary_bios_measurements`. A machine without a TPM has no such file,
and qboot returns after one lookup of its name.

The machine says what is measured (`machine.Spec.TPMEvents`, `docs/machine.md`, "A TPM"), and
qboot hashes nothing. The vmlinux QEMU loads for PVH never passes through qboot, so it could not
hash the kernel at all, and an initrd hashed here is on the first login's clock: a portable
SHA-256 read 60 MB in 400 ms on a laptop (-Os, 2026-11-26), where OpenSSL with SHA-NI read
1.5 GB/s. The caller hashes each file once (`machine.FileSHA256`). Stefan Berger's 2011 SeaBIOS series did the
same split, QEMU hashing the kernel, initrd and command line and the firmware extending, and was
never merged ([seabios list, 2011-08-31](https://mail.coreboot.org/hyperkitty/list/seabios@seabios.org/message/XMD5XKVUENWGDHUBKKUF7MUBLGD2ZS4R)).

Each event carries one SHA-256 digest. swtpm makes a TPM with SHA-1, SHA-256, SHA-384 and SHA-512
banks active (the release's swtpm 0.10.2 on an empty state; boot:tpm replayed all four, 2026-11-26),
and every active bank is extended, with the digest cut to SHA-1's 20 bytes or zero-padded to 48 and 64: no bank is left at zero for the guest to
extend to the values of a log it wrote. Only the SHA-256 bank holds the algorithm's digests of what
was measured; SeaBIOS filled its banks the same way until it hashed each with its own algorithm
(commit 1be65c56, 2021). A TPM that fails or does not answer a command (about a minute of polls)
stops the measuring and the boot goes on: what was extended before it is in the PCRs and the log
alike, so a verifier sees a boot measured short, never a log the PCRs disagree with.

## What holds each patch

The build greps each patch's code out of the tree it compiled, so a patch that applied to nothing
fails there. What it does is held where it shows: `write-pointer.patch` by `task boot:firmware`
(vmgenid published and the guest reseeded after a restore), `mtrr.patch` by `task boot:patches`
(the guest's `/proc/mtrr` has the PCI hole uncachable), and `tpm.patch` by `task boot:tpm`
(the guest's log has the events the machine named, and every bank of every PCR in it holds what
the log replays to), both of which compat runs. `pam.patch` and
`no-pci-walk.patch` only save time. Taken back alone, cold read +3.9 [−3.4, +8.0] and +5.2
[−1.7, +9.2] ms, 40 boots a side (lab 44993790452): inside the noise, kept because the upper
bounds are over +5 ms (docs/measuring.md, rule 5).

## Build

`spin-machine-dev build qemu` builds it, in the `qboot` stage of `qemu/Dockerfile`, and it lands beside
the SeaBIOS blobs as `_output/qemu/qboot.bin`, where a release, `spin-machine-dev fetch qemu` and CI all
find it. The stage clones the pinned commit, verifies the SHA-256 of the tar `git archive`
writes for it, applies `write-pointer.patch`, `pam.patch`, `mtrr.patch`, `no-pci-walk.patch` and `tpm.patch` with no fuzz, and compiles with upstream's
meson.build flags plus `-Os`. It asserts on what came out: 65536 bytes, the ROM window it is
linked for, and each patch's code in the tree that was compiled.

qboot is built with the kernel's pinned Debian and not QEMU's Alpine, because the compiler is
part of the firmware. The toolchain section below says by how much.

## Probe

`task boot:firmware` (`TestFirmwareRuns` in `boot/firmware_test.go`) boots qboot
with vmgenid off, then with it on. With vmgenid on, it requires the table published, saves the
guest, restores it into a new QEMU with `guid=auto`, and requires `crng reseeded due to virtual
machine fork` in the restored guest's log. Then it boots SeaBIOS, the fallback. `SPIN_QBOOT=` names another qboot
to try. `task boot:firmware-stages` says where qboot's time goes.

The initrd is `boot/probeinit`, built by the test, or `SPIN_PROBE_INITRD=`. Its `/init` must
mount proc, print dmesg lines containing `vmgenid` and then `SPIN-READY`, stay alive, and keep
printing new dmesg output so the host sees the reseed after restore.

Measured on the lab runner (AMD Ryzen 9 5900X, run 36650049960, 2026-09-29), 20 boots
interleaved: SeaBIOS 121.89 ms p50, qboot 114.76, 7.14 ms saved. `report`'s whole matrix,
106 rows, booted on qboot (run 36650559189).

## The toolchain is part of the number

The same commit, patch and flags saved 6.3 ms over SeaBIOS built by a host's GCC 15.2.0 and
3.9 ms built by Debian trixie's GCC 14.2.0 (i9-13900HK, a diagnostic initrd, 2026-09-21 and
-26): the compiler moved the result by more than a third of what qboot saves. That is why the
recipe is pinned rather than convenient, and why a number here compares only with one built
the same way. On the bench fleet in spin's shape, SeaBIOS instead of qboot costs the cold first
login +68.5 ms [+62.7, +71.7] (lab 44818953987, `docs/boot-time.md`), far more than these
diagnostic boots saw.

The BIOS's size is part of the machine's record (`machine.QEMU.Target`), and its contents are not. The BIOS is the
`pc.bios` RAM block, which a checkpoint carries whole, so a resumed guest keeps running the qboot
it booted with, and only a block of another length is refused. qboot.bin is linked for its
64 KiB window (`spin-machine-dev verify qemu` holds it there), so a rebuilt qboot is the same machine to a
checkpoint and new firmware to the next boot. The kernel and the option ROM QEMU adds for it
reach the guest through fw_cfg at a boot and are not in the record either. That ROM is
linuxboot_dma.bin for the bzImage, and qboot does not run it, because qboot loads the bzImage
from fw_cfg itself.
