// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/spin-stack/machine/qmp"
)

// TestADiskArrivesAtItsTargetOnTheHotplugController is the question the hotplug controller
// exists to answer, asked of a machine that is running rather than of one that started: a
// disk HotplugDisk describes is taken at run time, at the target it names, over a block node
// added the way a caller adds one, and a second disk at the same target is refused rather than
// put somewhere the caller did not say.
//
// The machine is stopped at -S, so no guest is involved: what is under test is QEMU's side of
// the contract. How quickly a guest sees the disk and lets it go is Spec.HotplugDisks'
// measurement.
func TestADiskArrivesAtItsTargetOnTheHotplugController(t *testing.T) {
	qemu, firmware := qemuTCG(t)
	kernel := bzImageStub(t)

	spec := Spec{
		QEMU:         qemu,
		Accel:        "tcg",
		Kernel:       kernel,
		Firmware:     firmware,
		BootCPUs:     2,
		Memory:       Memory{BaseMB: 512},
		Cmdline:      DefaultCmdline(),
		HotplugDisks: 2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := Start(ctx, spec, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Kill() })
	m := p.QEMU

	image := qcow2In(t, qemu, t.TempDir(), "extension.qcow2", "")
	disk := func(serial string) Disk {
		f, err := os.Open(image) // #nosec G304 -- a file this test made
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return Disk{Serial: serial, Readonly: true, Images: []Image{{File: f, Format: "qcow2"}}}
	}
	for _, serial := range []string{"vsc-a", "vsc-b"} {
		if err := m.AttachDisk(ctx, disk(serial)); err != nil {
			t.Fatalf("attaching %s: %v", serial, err)
		}
	}

	// Each disk exists at the first target free when it came, on its top node.
	for target, serial := range []string{"vsc-a", "vsc-b"} {
		for property, want := range map[string]any{"drive": diskNode(serial, 0), "scsi-id": float64(target), "serial": serial} {
			got, err := readProperty[any](ctx, m, peripheral+hotDiskID(target), property)
			if err != nil {
				t.Fatalf("the disk %s was accepted and has no %s: %v", serial, property, err)
			}
			if got != want {
				t.Errorf("the disk %s's %s is %v, want %v", serial, property, got, want)
			}
		}
	}

	// A target is one disk's.
	if err := m.attachAt(ctx, 1, disk("vsc-c")); err == nil {
		t.Error("a third disk was taken at a target that holds one")
	}

	// Detached, its target, its nodes and its descriptors are free for the next.
	held := func() int {
		t.Helper()
		sets, err := qmp.Do(ctx, m.c, qmp.QueryFDSets, qmp.None{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range sets {
			for _, fd := range s.FDs {
				if fd.Opaque == image {
					n++
				}
			}
		}
		return n
	}
	// Both disks are the one image: a set each.
	before := held()
	if err := m.DetachDisk(ctx, "vsc-a"); err != nil {
		t.Fatalf("detaching: %v", err)
	}
	// A detached disk's image is no longer the machine's to read through it: its set is gone,
	// and the other disk's stays.
	if after := held(); after != before-1 {
		t.Errorf("QEMU holds %s in %d sets after a detach, and held it in %d before", image, after, before)
	}
	if err := m.AttachDisk(ctx, disk("vsc-a")); err != nil {
		t.Fatalf("attaching a disk again after it was detached: %v", err)
	}
	if err := m.DetachDisk(ctx, "nothing"); err == nil {
		t.Error("detaching a disk the machine never had succeeded")
	}
}
