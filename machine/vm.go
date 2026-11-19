// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
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
	Checkpoint(ctx context.Context, to *os.File, seal func(context.Context) error) (Saved, error)
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

// Disk is one disk of a VM: an image and every image under it, top first, each opened by the
// caller. A VMM reads no image by path and follows no backing file an image's header names:
// what it reads is what it was handed.
type Disk struct {
	// Serial is the disk's name to the guest, which finds it by this and not by the order the
	// disks were probed in. At most 20 characters of A-Z, a-z, 0-9, '-' and '_'.
	Serial string
	// Images are the disk's images, top first. Each is opened for what it will be: the top
	// read-write unless the disk is Readonly, every other image read-only.
	Images []Image
	// Readonly gives the guest a disk it cannot write.
	Readonly bool
	// Locking has the VMM hold an image lock on the top image, for a caller outside it that
	// takes the same lock to find out whether a VM has the image open.
	Locking bool
	// DirectOverBacking reads and writes the top image around the host's page cache and the
	// images under it through it: the top is this VM's alone, cached in the guest already, and
	// the images under it are shared by every VM on the host and worth one copy there. Only
	// for a disk of more than one image, and not on tmpfs, which has no O_DIRECT.
	DirectOverBacking bool
}

// Image is one image of a Disk.
type Image struct {
	File *os.File
	// Format is qcow2 or raw, and never guessed: a guessed format is how an image the guest
	// can write is talked into being read as another one. Only the last image may be raw.
	Format string
}

// Load is how a checkpoint is read.
type Load struct {
	// Fast runs the guest as soon as its devices are loaded and reads its memory as it touches
	// it, the rest behind it. Without Fast the guest runs once all of it has been read.
	//
	// Slower to the first login on the bench fleet (nested, QEMU 11.2.0-rc0, a 140 MiB state read
	// from disk, 2026-11-19, lab 44585840498): running at 38 ms instead of 102, logged in at 293
	// instead of 152. Every page the guest touches on the way to a login is a fault served by one
	// read from the file, and a nested fault is expensive; the full load reads the file in one
	// pass. It is kept for a caller whose state is large and whose guest needs little of it soon.
	Fast bool
}

// Saved is what a checkpoint is besides its state file: what the caller keeps beside the state
// and hands back to Resume.
type Saved struct {
	// Format names how the state file is written. A VM resumes only formats it reads, and
	// says so before reading the file.
	Format string `json:"format"`
	// CPU is the CPU the guest was shown. A VM resumes it only where CanResume says it may: a
	// record without one, from before it was kept, is not checked.
	CPU CPU `json:"cpu"`
	// Fingerprint is the machine the guest was saved from, as the VMM names it (for QEMU,
	// Spec.Fingerprint): the devices, the shape and the firmware a resume has to rebuild. A
	// record without one, from before it was kept, is not checked.
	Fingerprint string `json:"fingerprint,omitempty"`
	// VMM is the program and version that wrote the state, "qemu 11.2.0": a VMM loads what
	// its own or an older version wrote, and promises nothing about a newer one's. A record
	// without one is not checked.
	VMM string `json:"vmm,omitempty"`
	// Machine is what this VMM needs besides the state to rebuild the machine it was saved
	// from - the vCPUs and the disks added while it ran - in a form only the VMM reads.
	Machine json.RawMessage `json:"machine,omitempty"`
}

// Target is what a machine can resume (VM.Target): the formats it reads, the machine it is, the
// VMM it runs under and the CPU it shows its guest.
type Target struct {
	Formats     []string `json:"formats"`
	Fingerprint string   `json:"fingerprint"`
	VMM         string   `json:"vmm"`
	CPU         CPU      `json:"cpu"`
}

// CanResume says whether a checkpoint may be resumed into a machine of this Target, and if not,
// why. It is the one rule: a caller placing checkpoints, and Resume before it reads a byte, ask
// the same question.
//
//   - the format is one the machine reads;
//   - the machine is the same one (Fingerprint), since a resume rebuilds the saved machine's
//     devices, shape and firmware and QEMU does not refuse every difference;
//   - the VMM is the same program, at the version that wrote the state or newer: QEMU loads
//     what an older QEMU of the same machine type wrote, never the other way, so a rolling
//     update does not send a checkpoint back to a host that has not been updated yet;
//   - the CPU can be shown again (canShowCPU).
//
// A field the record does not have, from before it was kept, is not checked.
func CanResume(saved Saved, here Target) error {
	if !slices.Contains(here.Formats, saved.Format) {
		return fmt.Errorf("the checkpoint is in format %q, and this machine reads %s", saved.Format, strings.Join(here.Formats, ", "))
	}
	if saved.Fingerprint != "" && saved.Fingerprint != here.Fingerprint {
		return fmt.Errorf("the checkpoint is of machine %s, and this is machine %s", saved.Fingerprint, here.Fingerprint)
	}
	if saved.VMM != "" {
		if err := newEnough(saved.VMM, here.VMM); err != nil {
			return err
		}
	}
	if saved.CPU.Vendor != "" {
		return canShowCPU(saved.CPU, here.CPU)
	}
	return nil
}

// newEnough says whether a VMM here, "qemu 11.2.0", loads a state the VMM saved wrote.
func newEnough(saved, here string) error {
	sp, sv, err := vmmVersion(saved)
	if err != nil {
		return fmt.Errorf("the checkpoint's VMM: %w", err)
	}
	hp, hv, err := vmmVersion(here)
	if err != nil {
		return fmt.Errorf("this machine's VMM: %w", err)
	}
	if sp != hp {
		return fmt.Errorf("the checkpoint was written by %s, and this machine runs %s", sp, hp)
	}
	if slices.Compare(hv, sv) < 0 {
		return fmt.Errorf("the checkpoint was written by %s, newer than this machine's %s", saved, here)
	}
	return nil
}

// vmmVersion splits "qemu 11.2.0" into its program and version numbers.
func vmmVersion(v string) (string, []int, error) {
	program, version, ok := strings.Cut(v, " ")
	if !ok || program == "" {
		return "", nil, fmt.Errorf("%q is not a program and a version", v)
	}
	var nums []int
	for _, p := range strings.Split(version, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", nil, fmt.Errorf("%q is not a program and a version: %w", v, err)
		}
		nums = append(nums, n)
	}
	return program, nums, nil
}
