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

args, err := spec.Args()             // the QEMU command line
// … exec QEMU with args and the descriptors the spec names, then over its monitor:
m, err := machine.Attach(conn)
here, err := m.Target(ctx)           // what this machine is, as QEMU reports it
err = machine.CanResume(saved, here) // nil, or a *machine.Refusal naming every difference
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

## The record

`QEMU.Target` is the machine's record, read from the running QEMU over QMP. A checkpoint taken
on the machine carries it (`Saved.From`), and a checkpoint resumed into the machine is held to
it. `machine.CanResume` compares the two records part by part. It returns nil, or a
`*machine.Refusal` that names each difference, both values and the rule it breaks:

```
the checkpoint's machine is not this one: device 02.0: checkpoint none, here vhost-vsock-pci (same)
```

QEMU refuses some differences by itself and loads others silently (measured 2026-11-21): an
extra device, which the guest never finds; a larger BIOS block or more base RAM, because
mapped-ram refuses only a smaller block; a device property the migration stream does not carry,
such as a NIC's MTU or `ICH9-LPC.disable_s3`. The record names each of them. It holds:

- the QEMU version (`vmm`). A checkpoint crosses it upward only: a newer QEMU loads what an older
  one of the same `pc-q35-X.Y` saved, with the newer type's changes to defaults switched back off
  for the older type (`hw_compat_X_Y`, `pc_compat_X_Y`).
- the CPU the guest is shown, read off its vCPU: the vendor, the model, and the features it has
  on, by their canonical names. The features compare as one set. See
  [migration.md](migration.md).
- the machine: its **versioned** type (`machine.MachineType`, `pc-q35-11.1`), the accelerator,
  each `-machine` option, `-smp` and `-m`, as QEMU reads them back.
- every device the machine was started with. A device on the root complex is named by its slot
  (`device 02.0`), any other by its type (`device i8257/2` for the second one on its bus). Each
  property is a part, except its links and children, what is one VM's (`perVM`: the MAC, the
  vsock context id, vmgenid's GUID, backends, descriptors) and what changes while it runs
  (`runTime`: the balloon's statistics, the memory plugged, the clock). A disk's place and a
  hot-plugged device are not the machine: `Saved.Added` adds the hot-plugged ones again before
  the state loads.
- every RAM block by its maximum length, except fw_cfg's `/rom@` blocks, which QEMU builds and
  resizes on a load.

Where a part sits is its rule. An exact part is equal on both sides, and one only the machine
resumed into has is a difference. An at-least part may grow: virtio-scsi's `max_target`
(`mayHaveMore`), because the guest can still be given a disk at a target it never scanned. A
per-VM or run-time property missing from those lists makes a machine refuse its own checkpoint,
and `task boot:record` fails on it, as it does on a QEMU that changes what the record reads.

`Target` is read once per `QEMU` value: 8 round trips, 8.4 ms on a laptop at spin's shape
(2026-11-21). A caller that asks a held machine when it starts it, as spin's pool does, pays
nothing at the resume. The record is 25 KB of JSON at spin's shape, with every list sorted, so
two equal records are equal bytes.

It leaves out the files a guest does not reload on a resume: the kernel, the initrd and the
option ROM that entered the kernel (`linuxboot_dma.bin`). A resumed guest runs the kernel in its
saved memory until it reboots. A newer QEMU can add a property to a device, which the record
then refuses as a part the checkpoint's machine did not have. `task boot:compat` holds a release
to what the record says: a checkpoint the newest release saved resumes on this tree's build, or
the run names every difference.

Moving `machine.MachineType` moves every guest to new hardware. It is a decision, never a side
effect of a QEMU bump. `spin-machine-dev verify qemu` says when a QEMU offers a newer type and
fails once the pinned one is deprecated, and `spin-machine-dev bump qemu` prints what the newer
types change against it.

`spin-machine restore` prints a refusal one difference a line, before QEMU reads the state.

