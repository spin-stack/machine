// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
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
	kernel := pvhStub(t)

	socket := qmpSocket(t)
	spec := Spec{
		QEMU:         qemu,
		Kernel:       kernel,
		Firmware:     firmware,
		BootCPUs:     2,
		Memory:       Memory{SizeMB: 512},
		Cmdline:      DefaultCmdline(),
		Monitors:     []Monitor{{Socket: socket}, {Socket: socket + ".2"}},
		HotplugDisks: 2,
	}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	args, rewrites, err := tcgOnly(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rewrites {
		t.Logf("rewritten for the TCG binary: %s", r)
	}

	vm := startQEMU(t, qemu, args)
	q := dialQMP(t, socket, vm)
	q.do("query-status", nil)
	_ = q

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket + ".2", Net: "unix"})
	if err != nil {
		vm.fail(t, "dialling the second monitor", err)
	}
	m, err := Attach(conn)
	if err != nil {
		vm.fail(t, "attaching", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

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
		for property, want := range map[string]any{"drive": TopNode(serial), "scsi-id": float64(target), "serial": serial} {
			reply, err := q.try("qom-get", map[string]any{"path": "/machine/peripheral/" + hotDiskID(target), "property": property})
			if err != nil {
				t.Fatalf("the disk %s was accepted and has no %s: %v", serial, property, err)
			}
			var got any
			if err := json.Unmarshal(reply, &got); err != nil {
				t.Fatal(err)
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
		var sets []struct {
			FDs []struct {
				Opaque string `json:"opaque"`
			} `json:"fds"`
		}
		if err := m.q.run(ctx, "query-fdsets", nil, &sets); err != nil {
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
