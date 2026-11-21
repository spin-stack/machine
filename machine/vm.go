// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"os"
)

// VM is a machine that has been started and is held, waiting to be booted or to be handed a
// checkpoint, and everything that is done to it after that.
//
// It is an interface with one implementation, QEMU, and that is deliberate: the user asked for
// the machine to be swappable, so that another VMM can be put behind the same calls and the two
// compared on the bench fleet with the same probes (2026-11-19). So its words are a VMM's and
// not QEMU's: nothing in it names QMP, a block node, a descriptor set, a migration capability or
// a PCI slot. Everything that is QEMU's - the command line (Spec.Args), the fingerprint
// (Spec.Fingerprint, Contract), the slot layout - stays on the QEMU side.
//
// The shape is the one the other two VMMs worth comparing already have:
//
//   - Firecracker configures a microVM before it runs (PUT /drives, /machine-config) and starts
//     it with PUT /actions InstanceStart; Boot is that start, with the disks given in the same
//     call. Its snapshots are a memory file and a vmstate file loaded with PUT /snapshot/load,
//     whose mem_backend may be File or Uffd, the latter serving the guest's page faults from
//     the memory file as it touches them; Resume with Load.Fast is that.
//   - Cloud Hypervisor creates a VM, then vm.boot; vm.snapshot and vm.restore, the restore
//     with memory_restore_mode=ondemand doing the same userfaultfd load. vm.resize changes vCPUs
//     and memory, which SetCPUs and SetMemory are; vm.add-disk and vm.remove-device are
//     AttachDisk and DetachDisk.
//
// The machine is started by the caller, who owns the process: its user, namespaces, cgroup,
// seccomp and descriptors are the caller's business, and every VMM is started from a command
// line of its own. A VM is what is reached once it is running.
//
// Which checkpoints a VM may resume is not settled here. The QEMU side has a fingerprint
// (Spec.Fingerprint), and what it promises between releases of this machine and of spin is an
// open question (spin-stack/spin#1348). What a VM does promise is the format of what it writes
// (Saved.Format), which a VM refuses when it does not read it, and the CPU the guest was shown
// (Saved.CPU), which a VM refuses when it cannot show it again (CanResume).
type VM interface {
	// Boot binds the disks and starts the guest from its firmware. The disks are the ones the
	// machine was started with places for, matched by Serial; a place no disk names stays
	// empty, and a disk with no place is an error.
	Boot(ctx context.Context, disks ...Disk) error

	// Resume loads a checkpoint into a machine started to receive one, with the disks the
	// checkpoint was saved with, and runs it. It returns once the guest runs: with Load.Fast
	// that is before the guest's memory has all been read, and Loaded says when it has.
	Resume(ctx context.Context, state *os.File, saved Saved, load Load, disks ...Disk) error
	// Loaded returns once a resumed machine has read all of its state: until then the state's
	// file is still being read, and must not be moved, deleted or written, nor another
	// checkpoint started.
	Loaded(ctx context.Context) error

	// Checkpoint writes the running machine's state to the file, and leaves the machine stopped.
	// seal runs while the guest is stopped and its disks quiet, before the state is complete:
	// it is where the caller makes the disks match the state (a layer sealed, a snapshot
	// named). An error from seal fails the checkpoint and the machine stays stopped.
	Checkpoint(ctx context.Context, to *os.File, save Save, seal func(context.Context) error) (Saved, error)
	// Continue runs a machine a checkpoint left stopped.
	Continue(ctx context.Context) error

	// AttachDisk gives the running machine a disk, which the guest finds by its Serial.
	AttachDisk(ctx context.Context, d Disk) error
	// DetachDisk takes the disk with this serial away, and returns once the VMM has let it go.
	DetachDisk(ctx context.Context, serial string) error

	// SetCPUs makes the machine have n vCPUs, up to the ceiling it was started with. Before
	// Boot it is the number the guest boots with.
	SetCPUs(ctx context.Context, n int) error
	// SetMemory asks for the guest's memory to be mb MiB in all, between the size it was
	// started with and its ceiling. The guest plugs and unplugs it on its own time; Memory is
	// what it has now.
	SetMemory(ctx context.Context, mb int) error
	// Memory is the guest's memory now, in MiB.
	Memory(ctx context.Context) (int, error)

	// CPU is the CPU the guest is shown: what a checkpoint of it records, and what a machine
	// a checkpoint is resumed into has to be able to show again (CanResume).
	CPU(ctx context.Context) (CPU, error)
	// Target is what this machine can resume: the checkpoints CanResume lets into it. A
	// caller choosing where a checkpoint resumes asks each candidate for its Target before
	// starting anything, rather than learning from a failed load.
	Target(ctx context.Context) (Target, error)

	// Quit ends the VMM without asking the guest.
	Quit(ctx context.Context) error
	// Close lets the machine go without ending it.
	Close() error
}
