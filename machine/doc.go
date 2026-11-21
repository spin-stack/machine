// SPDX-License-Identifier: Apache-2.0

// Package machine defines the virtual machine this repository builds: which
// chipset, which devices at which PCI slots, which kernel command line, and how
// much memory.
//
// It lives here, next to the QEMU binary, the kernel and the base image, because
// those four things are one thing. A VM resumed from a checkpoint loads device and
// CPU state into a machine that has to be the same shape as the one the checkpoint
// was saved from, and QEMU does not check all of it. So the shape is not a
// detail of whoever launches a VM; it is part of what a release *is*, and it has
// to move with the binary and the kernel it was written for.
//
// The rule that follows: a checkpoint carries the record of the machine it was taken on
// (Saved.From, a Target), read from the running QEMU - its version, its CPU, its devices and
// their properties, its options and its RAM blocks - and resumes only into a machine whose own
// record is the same, but for what may grow; CanResume says so, and names every difference when
// it is not. A new QEMU of the same machine type, or a new kernel, is the same machine to a
// checkpoint while the records agree (boot:compat holds a release to it).
//
// What is here is what a machine is, and the QMP that drives one QEMU through its life (QEMU:
// boot, attach and detach disks, checkpoint and resume). What is not here is the rest of
// running one: allocating a vsock context id, opening a TAP file descriptor, supervising the
// process, deciding what the guest's init is. A caller owns all of that and passes the results
// in.
//
// # How a caller uses it
//
// spin runs one machine per workspace in this order (Example has it as code):
//
//  1. OpenRelease finds a release's files and says which is missing. Release.Spec, given the
//     accelerator, starts the Spec: QEMU (the TCG build under "tcg"), Kernel, Firmware and
//     Accel.
//  2. The caller fills in the rest of the Spec: CPUs, Memory, Places, NICs, vsock, Monitors, the
//     Cmdline, and Restore when a checkpoint will be resumed.
//  3. Spec.Args is the QEMU command line. The caller execs QEMU with it and the descriptors the
//     Spec names. QEMU starts held.
//  4. The caller connects to a monitor with qmp.New, and Attach takes over the client and
//     returns the QEMU it drives.
//  5. QEMU.Boot binds the disks and starts the guest cold. Or, for a checkpoint: the caller holds
//     its host's Target, read once (QEMU.Target) from a machine started held in the shape it
//     resumes into; CanResume checks the Saved record against it to choose where the checkpoint
//     resumes; QEMU.Resume, handed the same Target, checks it again without asking QEMU and loads
//     the state; and QEMU.Loaded says when the state file is no longer read.
//  6. QEMU.AttachDisk and QEMU.DetachDisk change the disks while the guest runs.
//  7. QEMU.Checkpoint saves the guest and leaves it stopped, and QEMU.Continue runs it again.
package machine
