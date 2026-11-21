// SPDX-License-Identifier: Apache-2.0

package machine_test

import (
	"context"
	"log"
	"os"
	"os/exec"
	"syscall"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// Example is the flow spin runs for one workspace: a Spec from a release, QEMU started from it
// and held, then a cold boot or a resume, a disk while it runs, a checkpoint, and the end. It has no Output, so it is compiled and never run: it needs a release, /dev/kvm
// and a TAP.
func Example() {
	ctx := context.Background()

	rel, err := machine.OpenRelease("/usr/share/spin-stack")
	if err != nil {
		log.Fatal(err)
	}
	spec, err := rel.Spec("kvm") // QEMU, Kernel, Firmware and Accel; the rest is the caller's
	if err != nil {
		log.Fatal(err)
	}
	spec.Initrd = "/var/lib/spin/initrd.img"
	spec.BootCPUs, spec.MaxCPUs = 1, 16
	spec.Memory = machine.Memory{BaseMB: 128, CeilingMB: 8192}
	spec.Places = []machine.Place{{Serial: "root"}, {Serial: "home"}}
	spec.HotplugDisks = 16
	spec.VsockCID = 42
	spec.NICs = []machine.NIC{{TapFD: 3, MAC: "52:54:00:00:00:01"}}
	spec.Monitors = []machine.Monitor{{Socket: "/run/spin/ws/qmp.sock"}}
	spec.Cmdline = machine.DefaultCmdline()

	saved, resuming := previousCheckpoint()
	spec.Restore = resuming // held for a checkpoint (-incoming defer) and not for a boot (-S)

	// QEMU is started held (-S, or -incoming defer for a checkpoint), handed /dev/kvm, the vsock
	// and the monitors by Start, and the TAP by the caller; Start returns once the machine is made.
	// The Observer is where a caller times and counts commands: the package has no clock.
	m, err := machine.Start(ctx, spec, machine.StartOptions{
		Files:    []*os.File{tap()}, // descriptor 3, the NIC's TapFD
		Stderr:   os.Stderr,
		Observer: qmp.Observer{},
		Cmd: func(c *exec.Cmd) {
			c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10042, Gid: 10042}}
		},
	})
	if err != nil {
		log.Fatal(err) // QEMU's own words are in it
	}
	defer m.Stop(ctx)

	root, home := disk("root"), disk("home")
	if resuming {
		here := hostTarget()
		if err := machine.CanResume(saved, here); err != nil {
			log.Fatal(err) // boot this workspace cold, or resume it on another host
		}
		state, err := os.Open("/var/lib/spin/ws/state")
		if err != nil {
			log.Fatal(err)
		}
		defer state.Close()
		if err := m.Resume(ctx, state, saved, here, machine.LoadOptions{Fast: true}, root, home); err != nil {
			log.Fatal(err)
		}
		if err := m.Loaded(ctx); err != nil { // the state file is free once this returns
			log.Fatal(err)
		}
	} else if err := m.Boot(ctx, root, home); err != nil {
		log.Fatal(err)
	}

	// A disk while the machine runs, on the hotplug controller.
	if err := m.AttachDisk(ctx, disk("ext")); err != nil {
		log.Fatal(err)
	}

	to, err := os.Create("/var/lib/spin/ws/state.new")
	if err != nil {
		log.Fatal(err)
	}
	defer to.Close()
	saved, cost, err := m.Checkpoint(ctx, to, machine.SaveOptions{}, func(context.Context) error {
		return nil // where the caller seals the disks to match the state
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("the guest paused %v", cost.Paused)
	if err := m.Continue(ctx); err != nil {
		log.Fatal(err)
	}
	_ = saved // stored beside the state, and handed to Resume next time
}

// previousCheckpoint is the caller's record of the last checkpoint, if there is one.
func previousCheckpoint() (machine.Saved, bool) { return machine.Saved{}, false }

// hostTarget is the caller's record of the machine its host resumes checkpoints into: asked once
// (QEMU.Target) of a machine started held in that shape, when the caller starts, and kept.
func hostTarget() machine.Target { return machine.Target{} }

// tap is the caller's TAP device, already opened.
func tap() *os.File { return nil }

// disk is one of the workspace's disks, its images opened by the caller.
func disk(serial string) machine.Disk {
	top, err := os.OpenFile("/var/lib/spin/ws/"+serial+".qcow2", os.O_RDWR, 0)
	if err != nil {
		log.Fatal(err)
	}
	return machine.Disk{Serial: serial, Images: []machine.Image{{File: top, Format: "qcow2"}}}
}
