// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/qmp"
)

// A disk handed over as descriptors is bound to its place and read without a path: the chain
// is opened after the directory that held it is gone, so a backing name in a header points
// nowhere and QEMU reads what it was given or nothing. Then the top is sealed under a new
// overlay through the qcow2 node over the top's descriptor set, found as spin's storage finds it,
// and the place reads the overlay; and
// query-fdsets gives back what each descriptor was. The monitor is on a socket this test listens
// on and QEMU was handed, as a QEMU that may create no socket needs.
//
// Under the TCG binary, like TestQEMUAcceptsEveryArgument, and skipped where there is none.
func TestAChainOverDescriptorsIsBoundToItsPlaceAndSealedUnderAnOverlay(t *testing.T) {
	qemu, firmware := qemuTCG(t)
	kernel := bzImageStub(t)

	dir := t.TempDir()
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
		Monitors:     []Monitor{{FD: 4}},
		FDSets:       []FDSet{{ID: 1, FDs: []FD{{Num: 3, Opaque: fifo}}}},
		ConsoleFDSet: 1,
		Places:       []Place{{Serial: "root"}},
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
	m, err := connect(conn)
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
	if _, err := qmp.Do(ctx, m.c, qmp.ChangeBackingFile, qmp.ChangeBackingFileArgs{
		Device: slotNodePrefix + "0", ImageNodeName: diskNode("root", 0), BackingFile: base,
	}); err != nil {
		t.Errorf("rewriting the top's backing file through its place: %v", err)
	}
	// Sealed through the same monitor: a socket chardev serves one client at a time.
	set, err := qmp.Do(ctx, m.c, qmp.AddFD, qmp.AddFDArgs{File: nextFile, Opaque: next})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []qmp.BlockNode{
		qmp.FileNode{NodeName: "next-file", Filename: set.Path(), AIO: "io_uring", Locking: "on"},
		qmp.Qcow2Node{NodeName: "next", File: qmp.NodeRef("next-file"), Backing: qmp.NoBacking},
	} {
		if _, err := qmp.Do(ctx, m.c, qmp.BlockdevAdd, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := qmp.Do(ctx, m.c, qmp.BlockdevSnapshot, qmp.BlockdevSnapshotArgs{Node: qcow2NodeOf(ctx, t, m, top), Overlay: "next"}); err != nil {
		t.Fatalf("sealing the top under an overlay: %v", err)
	}

	blocks, err := qmp.Do(ctx, m.c, qmp.QueryBlock, qmp.None{})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Inserted == nil || blocks[0].Inserted.NodeName != "slot0" {
		t.Fatalf("query-block = %+v, want the one place, slot0", blocks)
	}
	sets, err := qmp.Do(ctx, m.c, qmp.QueryFDSets, qmp.None{})
	if err != nil {
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

// qcow2NodeOf is the qcow2 node that reads the image at path, found as spin's storage finds the
// node it seals (its qcow.topOf): by the opaque of the image's descriptor set and the filename
// of the node's file, never by a node name, which is this package's own.
func qcow2NodeOf(ctx context.Context, t *testing.T, m *QEMU, path string) string {
	t.Helper()
	sets, err := qmp.Do(ctx, m.c, qmp.QueryFDSets, qmp.None{})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := qmp.Do(ctx, m.c, qmp.QueryNamedBlockNodes, qmp.QueryNamedBlockNodesArgs{Flat: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sets {
		if !slices.ContainsFunc(s.FDs, func(fd struct {
			FD     int    `json:"fd"`
			Opaque string `json:"opaque"`
		}) bool {
			return fd.Opaque == path
		}) {
			continue
		}
		for _, n := range nodes {
			// A node given its backing by name is called json:{...}, its file's filename inside.
			file := n.File
			if j, ok := strings.CutPrefix(file, "json:"); ok {
				var named struct {
					File struct {
						Filename string `json:"filename"`
					} `json:"file"`
				}
				if err := json.Unmarshal([]byte(j), &named); err != nil {
					t.Fatalf("node %s's name %s: %v", n.NodeName, n.File, err)
				}
				file = named.File.Filename
			}
			if set, ok := qmp.FDSetOf(file); ok && n.Driver == "qcow2" && set == s.ID {
				return n.NodeName
			}
		}
	}
	t.Fatalf("no qcow2 node reads %s: sets %+v, nodes %+v", path, sets, nodes)
	return ""
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
