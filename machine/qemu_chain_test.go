// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A disk handed over as descriptors is bound to its place and read without a path: the chain
// is opened after the directory that held it is gone, so a backing name in a header points
// nowhere and QEMU reads what it was given or nothing. Then the top is sealed under a new
// overlay through TopNode, as spin's storage does, and the place reads the overlay; and
// query-fdsets gives back what each descriptor was. The monitor is on a socket this test listens
// on and QEMU was handed, as a QEMU that may create no socket needs.
//
// Under the TCG binary, like TestQEMUAcceptsEveryArgument, and skipped where there is none.
func TestAChainOverDescriptorsIsBoundToItsPlaceAndSealedUnderAnOverlay(t *testing.T) {
	qemu, firmware := qemuTCG(t)
	kernel := pvhStub(t)

	dir := diskDir(t)
	base := qcow2In(t, qemu, dir, "base.qcow2", "")
	top := qcow2In(t, qemu, dir, "top.qcow2", base)
	next := qcow2In(t, qemu, dir, "next.qcow2", top)

	open := func(path string, flag int) *os.File {
		t.Helper()
		f, err := os.OpenFile(path, flag, 0) // #nosec G304 -- a file this test made
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	disk := Disk{Serial: "root", Locking: true, Images: []Image{
		{File: open(top, os.O_RDWR), Format: "qcow2"},
		{File: open(base, os.O_RDONLY), Format: "qcow2"},
	}}
	nextFile := open(next, os.O_RDWR)
	// The console, a FIFO as a runner keeps one: its reader first, or opening the writer blocks.
	fifo := filepath.Join(t.TempDir(), "console")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = open(fifo, os.O_RDONLY|syscall.O_NONBLOCK)
	files := []*os.File{open(fifo, os.O_WRONLY)}
	socket := qmpSocket(t)
	unix, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := unix.File()
	if err != nil {
		t.Fatal(err)
	}
	// QEMU holds the listening socket from here, at the path this test dials: Go unlinks it
	// on Close unless told not to.
	unix.SetUnlinkOnClose(false)
	_ = unix.Close()
	files = append(files, monitor)

	// Nothing QEMU could open by name is left where the headers say.
	if err := os.Rename(dir, dir+".gone"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(dir+".gone", dir) })

	spec := Spec{
		QEMU: qemu, Kernel: kernel, Firmware: firmware,
		BootCPUs: 1, Memory: Memory{SizeMB: 512}, Cmdline: DefaultCmdline(),
		Monitors:    []Monitor{{FD: 4}},
		FDSets:      []FDSet{{ID: 1, FDs: []FD{{Num: 3, Opaque: fifo}}}},
		SerialFDSet: 1,
		Disks:       []DiskSlot{{Serial: "root"}},
	}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	args, _, err = tcgOnly(args)
	if err != nil {
		t.Fatal(err)
	}
	vm := startQEMU(t, qemu, args, files...)

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		vm.fail(t, "dialling the monitor", err)
	}
	m, err := Attach(conn)
	if err != nil {
		vm.fail(t, "attaching", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.Boot(ctx, disk); err != nil {
		vm.fail(t, "booting", err)
	}
	// A bound image's backing file is rewritten through its place's node, as spin's storage
	// compacts a chain: QEMU takes change-backing-file only on a root whose chain, followed by
	// backing and filter links, contains the image - which a raw place's did not.
	if err := m.q.run(ctx, "change-backing-file", map[string]any{
		"device": slotNodePrefix + "0", "image-node-name": diskNode("root", 0), "backing-file": base,
	}, nil); err != nil {
		t.Errorf("rewriting the top's backing file through its place: %v", err)
	}
	// Sealed through the same monitor: a socket chardev serves one client at a time.
	set, err := m.q.addFD(ctx, nextFile, next)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []map[string]any{
		{"driver": "file", "node-name": "next-file", "filename": "/dev/fdset/" + strconv.Itoa(set), "aio": "io_uring", "locking": "on"},
		{"driver": "qcow2", "node-name": "next", "file": "next-file", "backing": nil},
	} {
		if err := m.q.run(ctx, "blockdev-add", n, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.q.run(ctx, "blockdev-snapshot", map[string]any{"node": TopNode("root"), "overlay": "next"}, nil); err != nil {
		t.Fatalf("sealing the top under an overlay: %v", err)
	}

	var blocks []struct {
		Inserted struct {
			NodeName string `json:"node-name"`
			Image    struct {
				BackingImage *struct {
					Filename string `json:"filename"`
				} `json:"backing-image"`
			} `json:"image"`
		} `json:"inserted"`
	}
	if err := m.q.run(ctx, "query-block", nil, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Inserted.NodeName != "slot0" {
		t.Fatalf("query-block = %+v, want the one place, slot0", blocks)
	}
	var sets []struct {
		FDs []struct {
			Opaque string `json:"opaque"`
		} `json:"fds"`
	}
	if err := m.q.run(ctx, "query-fdsets", nil, &sets); err != nil {
		t.Fatal(err)
	}
	var opaque []string
	for _, s := range sets {
		for _, fd := range s.FDs {
			opaque = append(opaque, fd.Opaque)
		}
	}
	b, _ := json.Marshal(opaque)
	// Each image's set says its path, as spin's storage finds an open image by it, and the
	// writable top's holds a read-only copy too (spin-machine#297): two descriptors for top.
	for want, n := range map[string]int{top: 2, base: 1, next: 1, fifo: 1} {
		if got := countOf(opaque, want); got != n {
			t.Errorf("%d descriptors say they are %q, want %d: %s", got, want, n, b)
		}
	}
}

// countOf is how many of xs are x.
func countOf(xs []string, x string) int {
	n := 0
	for _, s := range xs {
		if s == x {
			n++
		}
	}
	return n
}
