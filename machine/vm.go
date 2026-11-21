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
// a PCI slot. Everything that is QEMU's - the command line (Spec.Args), what a QEMU machine's
// record is made of (QEMU.Target), the slot layout - stays on the QEMU side.
//
// The shape is the one the other two VMMs worth comparing already have:
//
//   - Firecracker configures a microVM before it runs (PUT /drives, /machine-config) and starts
//     it with PUT /actions InstanceStart; Boot is that start, with the disks given in the same
//     call. Its snapshots are a memory file and a vmstate file loaded with PUT /snapshot/load,
//     whose mem_backend may be File or Uffd, the latter serving the guest's page faults from
//     the memory file as it touches them; Resume with LoadOptions.Fast is that.
//   - Cloud Hypervisor creates a VM, then vm.boot; vm.snapshot and vm.restore, the restore
//     with memory_restore_mode=ondemand doing the same userfaultfd load. vm.resize changes vCPUs
//     and memory, which SetCPUs and SetMemory are; vm.add-disk and vm.remove-device are
//     AttachDisk and DetachDisk.
//
// The machine is started by the caller, who owns the process: its user, namespaces, cgroup,
// seccomp and descriptors are the caller's business, and every VMM is started from a command
// line of its own. A VM is what is reached once it is running.
//
// Which checkpoints a VM resumes is one rule for every VMM, CanResume, over two records the VMM
// gives (Target): the one a checkpoint carries (Saved.From) and the machine's own, which the
// caller holds and hands to Resume. A VMM says
// what its record holds and which facts may grow (Hardware); the comparison, and the refusal that
// names each difference, are the same whatever made them. Another VMM's Target is read from its
// own API - Cloud Hypervisor's vm.info, Firecracker's GET /vm/config - into the same Target, and
// a checkpoint of one VMM is refused by another as one difference, the VMM.
type VM interface {
	// Boot binds the disks and starts the guest from its firmware. The disks are the ones the
	// machine was started with places for, matched by Serial; a place no disk names stays
	// empty, and a disk with no place is an error.
	Boot(ctx context.Context, disks ...Disk) error

	// Resume loads a checkpoint into a machine started to receive one, with the disks the
	// checkpoint was saved with, and runs it, once CanResume(saved, here) allows it. here is this
	// machine's Target as the caller holds it, and is trusted as given: the caller is responsible
	// for it describing this machine, and QEMU.Resume says how spin keeps it. It returns once the
	// guest runs: with LoadOptions.Fast that is before the guest's memory has all been read, and
	// Loaded says when it has.
	Resume(ctx context.Context, state *os.File, saved Saved, here Target, load LoadOptions, disks ...Disk) error
	// Loaded returns once a resumed machine has read all of its state: until then the state's
	// file is still being read, and must not be moved, deleted or written, nor another
	// checkpoint started.
	Loaded(ctx context.Context) error

	// Checkpoint writes the running machine's state to the file, and leaves the machine stopped.
	// seal runs while the guest is stopped and its disks quiet, before the state is complete:
	// it is where the caller makes the disks match the state (a layer sealed, a snapshot
	// named). An error from seal fails the checkpoint and the machine stays stopped.
	Checkpoint(ctx context.Context, to *os.File, save SaveOptions, seal func(context.Context) error) (Saved, error)
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

	// CPU is the CPU the guest is shown, which its Target holds.
	CPU(ctx context.Context) (CPU, error)
	// Target is what this machine is, as its VMM reports it: what a checkpoint of it records
	// (Saved.From), and what CanResume holds a checkpoint resumed into it to. A caller choosing
	// where a checkpoint resumes asks a machine of the kind that will take it - one started held
	// for a checkpoint, with the devices a workspace has - before starting anything, rather than
	// learning from a failed load, and hands the answer to Resume.
	Target(ctx context.Context) (Target, error)

	// Quit ends the VMM without asking the guest.
	Quit(ctx context.Context) error
	// Close lets the machine go without ending it.
	Close() error
}
