// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// What each patch in qemu/patches does, asked of the TCG binary with no guest. A patch dropped
// at a QEMU bump, or rebased so that it applies and does nothing, builds a QEMU that starts and
// runs every machine, and fails here instead: `spin-machine-dev verify args` runs this on every
// push against the published QEMU (ci.yml) and on every QEMU this repository builds (parts.yml).
// 0005, the restored TSC, needs KVM and a guest, and is boot:patches'.
func TestEachQEMUPatchDoesItsJob(t *testing.T) {
	qemu, firmware := qemuTCG(t)
	kernel := bzImageStub(t)
	held := func(t *testing.T, s Spec) *QEMU {
		t.Helper()
		s.QEMU, s.Accel, s.Kernel, s.Firmware, s.Cmdline = qemu, "tcg", kernel, firmware, DefaultCmdline()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		p, err := Start(ctx, s, StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Kill() })
		return p.QEMU
	}

	t.Run("0001 builds the memory map once for device creation", func(t *testing.T) {
		// Without the patch, each device added and each machine-done notifier rebuilt every
		// address space's flat view: 283 of a start's 438 rebuilds (the patch's message).
		s := Spec{
			QEMU: qemu, Kernel: kernel, Firmware: firmware, Cmdline: DefaultCmdline(),
			BootCPUs: 1, MaxCPUs: 16, Memory: Memory{BaseMB: 128, CeilingMB: 8192},
			HotplugDisks: maxHotplugDisks, Places: []Place{{Serial: "root"}},
			Monitors: []Monitor{{Socket: qmpSocket(t)}},
		}
		args, err := s.Args()
		if err != nil {
			t.Fatal(err)
		}
		if args, _, err = tcgOnly(args); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(t.TempDir(), "trace")
		vm := startQEMU(t, qemu, append(args, "-trace", "enable=flatview_new", "-D", log))
		askStatus(t, s.Monitors[0].Socket, vm)
		b, err := os.ReadFile(log) // #nosec G304 -- the trace this test asked for
		if err != nil {
			t.Fatal(err)
		}
		views := strings.Count(string(b), "flatview_new")
		t.Logf("%d flat views built from QEMU's start to its first answer, at spin's shape", views)
		if views == 0 || views > maxFlatViewsAtStart {
			t.Errorf("QEMU built %d flat views before it answered, want 1 to %d: device creation is not one memory transaction", views, maxFlatViewsAtStart)
		}
	})

	t.Run("0002 refuses a disk at a target its controller does not have", func(t *testing.T) {
		// HotplugDisks 2 is max_target 1: the guest scans targets 0 and 1, so a disk at 2 would
		// never appear in it, and QEMU says so instead. That the guest is told 1 is boot:patches'.
		m := held(t, Spec{BootCPUs: 1, Memory: Memory{BaseMB: 512}, HotplugDisks: 2})
		f, err := os.Open(qcow2In(t, qemu, t.TempDir(), "beyond.qcow2", "")) // #nosec G304 -- a file this test made
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = m.attachAt(ctx, 2, Disk{Serial: "beyond", Readonly: true, Images: []Image{{File: f, Format: "qcow2"}}})
		if err == nil || !strings.Contains(err.Error(), "max_target") {
			t.Errorf("a disk at target 2 of a controller of max_target 1: %v, want QEMU's refusal naming max_target", err)
		}
	})

	t.Run("0003 defers the stop's flush to the host's disk", func(t *testing.T) {
		// A disk written since its last flush, over blkdebug failing every flush to the host's
		// disk: a save whose stop flushes it fails, and one under x-defer-disk-flush completes.
		// The first is what proves the second: the instrument sees the flush it says is gone.
		save := func(deferred bool) migrationInfo {
			m := held(t, Spec{BootCPUs: 1, Memory: Memory{BaseMB: 128}})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			image := rawDisk(t, "written.raw")
			for _, n := range []blockdevNode{
				{Driver: "file", NodeName: "written-file", Filename: image},
				{Driver: "blkdebug", NodeName: "written-debug", Image: "written-file",
					InjectError: []blkdebugError{{Event: "flush_to_disk", IOType: "flush", Errno: 5}}},
				{Driver: "raw", NodeName: "written", File: "written-debug"},
			} {
				if _, err := qmp.Do(ctx, m.c, blockdevAddNode, n); err != nil {
					t.Fatalf("adding %s: %v", n.NodeName, err)
				}
			}
			said, err := qmp.Do(ctx, m.c, humanMonitorCommand, hmpCommand{Line: `qemu-io written "write 0 4k"`})
			if err != nil || !strings.Contains(said, "wrote") {
				t.Fatalf("writing the disk: %q, %v", said, err)
			}
			if _, err := qmp.Do(ctx, m.c, migrateSetCapabilities, capabilities{Capabilities: []capabilityState{{Capability: deferDiskFlush, State: deferred}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := qmp.Do(ctx, m.c, migrateOut, migrateArgs{URI: "file:" + filepath.Join(t.TempDir(), "state")}); err != nil {
				t.Fatal(err)
			}
			var info migrationInfo
			for info.Status != completed && info.Status != failed {
				if info, err = qmp.Do(ctx, m.c, queryMigrate, qmp.None{}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Logf("x-defer-disk-flush %v: the save %s %s", deferred, info.Status, info.ErrorDesc)
			return info
		}
		if info := save(false); info.Status != failed {
			t.Fatalf("a save over a disk whose flushes fail %s (%s), want failed: blkdebug does not see the stop's flush, and the next case proves nothing", info.Status, info.ErrorDesc)
		}
		if info := save(true); info.Status != completed {
			t.Errorf("with x-defer-disk-flush, a save over a disk whose flushes fail %s (%s), want completed: the stop flushed to the host's disk", info.Status, info.ErrorDesc)
		}
	})

	t.Run("0004 answers a device's counters without its chain under x-flat", func(t *testing.T) {
		m := held(t, Spec{BootCPUs: 1, Memory: Memory{BaseMB: 128}, Places: []Place{{Serial: "root"}}})
		dir := t.TempDir()
		base := qcow2In(t, qemu, dir, "base.qcow2", "")
		top := qcow2In(t, qemu, dir, "top.qcow2", base)
		disk, err := OpenDisk(DiskPath{Serial: "root", Path: top, Format: "qcow2", Under: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = disk.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := m.Boot(ctx, disk); err != nil {
			t.Fatal(err)
		}
		for _, flat := range []bool{false, true} {
			stats, err := qmp.Do(ctx, m.c, queryBlockstatsRaw, blockstatsArgs{Flat: flat})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range stats {
				_, parent := s["parent"]
				_, backing := s["backing"]
				if chained := parent || backing; chained == flat {
					t.Errorf("query-blockstats x-flat %v: %s has parent %v and backing %v", flat, s["device"], parent, backing)
				}
			}
			if len(stats) == 0 {
				t.Errorf("query-blockstats x-flat %v answered no device", flat)
			}
		}
	})
}

// maxFlatViewsAtStart is the most flat views QEMU may build from its start to its first answer
// at spin's shape: the board's, one for each address space at the transaction's commit, and the
// ROMs' reset after it.
const maxFlatViewsAtStart = 200

// blockdevAddNode is blockdev-add with the members a test sends that qmp.BlockNode has no variant
// for: blkdebug, which fails a flush at a chosen event.
var blockdevAddNode = qmpdef.Define[blockdevNode, qmp.None]("blockdev-add")

type blockdevNode struct {
	Driver      string          `json:"driver"`
	NodeName    string          `json:"node-name"`
	Filename    string          `json:"filename,omitempty"`
	Image       string          `json:"image,omitempty"`
	File        string          `json:"file,omitempty"`
	InjectError []blkdebugError `json:"inject-error,omitempty"`
}

// blkdebugError fails every request of IOType with Errno once Event has happened on the node.
type blkdebugError struct {
	Event  string `json:"event"`
	IOType string `json:"iotype"`
	Errno  int    `json:"errno"`
}

// humanMonitorCommand runs an HMP command: here qemu-io, which writes a node without a guest.
var humanMonitorCommand = qmpdef.Define[hmpCommand, string]("human-monitor-command")

type hmpCommand struct {
	Line string `json:"command-line"`
}

// queryBlockstatsRaw is query-blockstats with each device's statistics left as QEMU wrote them,
// to see which members it has.
var queryBlockstatsRaw = qmpdef.Define[blockstatsArgs, []map[string]json.RawMessage]("query-blockstats")
