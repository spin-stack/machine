# The machine

`machine/` is the definition. It is Go rather than a document because a definition that
nothing executes drifts. `spin-machine-dev shell` boots a VM through it, so a slot that moved or a
kernel argument that stopped working fails here first.

```go
rel, err := machine.OpenRelease("/usr/share/spin-stack")  // says which file is missing, if one is
spec := rel.Spec()                                        // QEMU, Kernel, Firmware
spec.Initrd = …                                           // the caller's: a release carries none
spec.BootCPUs = 2
spec.Memory = machine.Memory{SizeMB: 2048}
spec.Disks = []machine.DiskSlot{{Serial: "root"}}         // a place: the images come at Boot
spec.VsockCID = 7
spec.Monitors = []machine.Monitor{{Socket: "/run/vm/qmp.sock"}}
spec.Cmdline = machine.DefaultCmdline()

args, err := spec.Args()          // the QEMU command line
fp, err := spec.Fingerprint()     // which checkpoints this machine may resume
```

`Args` validates first. Each spec it refuses would otherwise boot and be subtly wrong: two disk
places with one serial, a monitor that is both a path and a descriptor, or a kernel command line
the kernel would truncate. A disk's images arrive later, at `VM.Boot`, `VM.Resume` or
`VM.AttachDisk`, and each refuses an image whose format is not qcow2 or raw rather than guess it.

## What is load-bearing and invisible from outside

- **Every device on the command line sits at a fixed slot on bus 0.** A fixed slot makes
  the guest's enumeration order independent of the order code runs in. Because every device is
  on bus 0, the kernel can be told `pci=lastbus=0`. The flag stops the search for peer host
  bridges across all 256 buses, which costs 8192 configuration reads, each one a VM exit. Kernel
  to init went from 87.6 ms to 51.1 ms (2026-09-07, 5e7d60e). The flag bounds only that search, so the kernel would
  not find a second host bridge. Disks attached while the machine runs are SCSI disks on the
  `HotplugDisks` controller, not devices behind a bridge.
- **`vmgenid`.** A VM resumed from a checkpoint starts with the checkpoint's memory,
  including the state of the guest's random pool. Without `vmgenid`, two guests resumed from
  one checkpoint, or one resumed twice, would produce the same "random" bytes until something
  reseeded them.

## The fingerprint

`Spec.Fingerprint` is the machine's contract. A checkpoint resumes only onto a machine with
the fingerprint of the one it was saved on. QEMU does not check it: across two fingerprints that differ, a
resume is undefined rather than an error, so `machine.CanResume` refuses it. The fingerprint hashes:

- the shape: the `-machine` argument with its **versioned** machine type
  (`machine.MachineType`, `pc-q35-11.1`), `-accel`, `-cpu`, `-smp` and `-m`.
- the chipset's globals and every device present when state is loaded, each as the command line
  gives it, properties and slot included (`Spec.Contract`): vmgenid, the RNG and the balloon,
  and, when the spec has them, the vsock, the virtio-mem region, the serial port, the hotplug
  controller and the NICs with their MTU.
- the BIOS's size, because the BIOS is the `pc.bios` RAM block, which a checkpoint carries whole
  and a resume refuses at another length. Every other read-only block is the machine type's or the chipset's
  (`machineBlocks` in `boot/roms_test.go`), and `task boot:roms` fails when a booted machine maps one that is
  neither.
- not the host's CPU. A checkpoint records the CPU its guest was shown and is resumed only
  where it can be shown again (`machine.CanResume`). See [migration.md](migration.md).

The fingerprint leaves out what a resume is allowed to differ in: paths, descriptors, ids, the
backends devices are bound to, the vsock context id, and MAC addresses. It also leaves out the
disks, which are the VM's and not the machine's. A host computes its fingerprint before any VM
exists, and whoever resumes a checkpoint gives it the disks it was saved with.

It leaves out the files a guest does not reload on a resume: the QEMU binary, the kernel,
the initrd and the option ROM that entered the kernel (`linuxboot_dma.bin`). QEMU's migration promise is per versioned machine type. A newer QEMU loads what an
older one of the same `pc-q35-X.Y` saved, with the newer type's changes to defaults switched back
off for the older type (`hw_compat_X_Y`, `pc_compat_X_Y`). A resumed guest runs the kernel in its
saved memory until it reboots. `task boot:compat` holds this tree to that promise:
a checkpoint the newest release saved resumes on this tree's build.

Moving `machine.MachineType` moves every guest to new hardware. It is a decision, never a side
effect of a QEMU bump. `spin-machine-dev verify qemu` says when a QEMU offers a newer type and fails once the pinned one is
deprecated, and `spin-machine-dev bump qemu` prints what the newer types change against it.

`spin-machine fingerprint` prints the fingerprint with the shape and the contract that went into it, because
the number alone answers "did it change?" and not "what changed?".

