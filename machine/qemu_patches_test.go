// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	held := func(t *testing.T, s Spec, extra ...string) *QEMU {
		t.Helper()
		s.QEMU, s.Accel, s.Kernel, s.Firmware, s.Cmdline = qemu, "tcg", kernel, firmware, DefaultCmdline()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		p, err := Start(ctx, s, StartOptions{Cmd: func(c *exec.Cmd) { c.Args = append(c.Args, extra...) }})
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
		// A disk written since its last flush, on io_uring, which QEMU traces each request to:
		// a save whose stop flushes it submits a flush (type 8, QEMU_AIO_FLUSH), and one under
		// x-defer-disk-flush submits none. The first is what proves the second: the trace sees
		// the flush that is said to be gone. Under cpr-reboot the stop flushes anyway, because the
		// host reboot that follows it loses the page cache. Postcopy's exclusion is not asked:
		// its return path needs a destination, and a save to a file has none.
		save := func(deferred, cpr bool) int {
			log := filepath.Join(t.TempDir(), "trace")
			m := held(t, Spec{BootCPUs: 1, Memory: Memory{BaseMB: 128}}, "-trace", "enable=luring_co_submit", "-D", log)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			for _, n := range []blockdevNode{
				{Driver: "file", NodeName: "written-file", Filename: rawDisk(t, "written.raw"), AIO: "io_uring"},
				{Driver: "raw", NodeName: "written", File: "written-file"},
			} {
				if _, err := qmp.Do(ctx, m.c, blockdevAddNode, n); err != nil {
					t.Fatalf("adding %s: %v", n.NodeName, err)
				}
			}
			said, err := qmp.Do(ctx, m.c, humanMonitorCommand, hmpCommand{Line: `qemu-io written "write 0 4k"`})
			// qemu-io prints what it wrote on QEMU's stdout, and HMP answers only an error.
			if err != nil || strings.Contains(said, "Error") {
				t.Fatalf("writing the disk: %q, %v", said, err)
			}
			if _, err := qmp.Do(ctx, m.c, migrateSetCapabilities, capabilities{Capabilities: []capabilityState{{Capability: deferDiskFlush, State: deferred}}}); err != nil {
				t.Fatal(err)
			}
			if cpr {
				if _, err := qmp.Do(ctx, m.c, migrateSetMode, migrationMode{Mode: cprReboot}); err != nil {
					t.Fatal(err)
				}
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
			if info.Status != completed {
				t.Fatalf("x-defer-disk-flush %v, cpr-reboot %v: the save %s: %s", deferred, cpr, info.Status, info.ErrorDesc)
			}
			b, err := os.ReadFile(log) // #nosec G304 -- the trace this test asked for
			if err != nil {
				t.Fatal(err)
			}
			writes, flushes := 0, 0
			for _, l := range strings.Split(string(b), "\n") {
				writes += strings.Count(l, "luring_co_submit") - strings.Count(l, "type 8")
				flushes += strings.Count(l, "type 8")
			}
			t.Logf("x-defer-disk-flush %v, cpr-reboot %v: %d other requests and %d flushes submitted", deferred, cpr, writes, flushes)
			if writes == 0 {
				t.Fatalf("the trace shows no request at all: it does not see this disk's I/O\n%s", b)
			}
			return flushes
		}
		if n := save(false, false); n == 0 {
			t.Fatal("a save's stop submitted no flush: the trace does not see it, and the next case proves nothing")
		}
		if n := save(true, false); n != 0 {
			t.Errorf("with x-defer-disk-flush, a save's stop submitted %d flushes to the host's disk, want none", n)
		}
		if n := save(true, true); n == 0 {
			t.Error("with x-defer-disk-flush under cpr-reboot, a save's stop submitted no flush: the host reboot after it loses the page cache")
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
// at spin's shape: 163 with 0001 and 429 without it, the TCG binary of 11.2.0-rc0 (ci run
// 44994248634 and parts run 44994258922, 2026-11-22).
const maxFlatViewsAtStart = 300

// migrateSetMode is migrate-set-parameters of the mode alone, which the machine never sets.
var migrateSetMode = qmpdef.Define[migrationMode, qmp.None]("migrate-set-parameters")

type migrationMode struct {
	Mode migrationModeName `json:"mode"`
}

type migrationModeName string

const cprReboot migrationModeName = "cpr-reboot"

func init() { qmpdef.Enum(cprReboot) }

// blockdevAddNode is blockdev-add of a node by its members, without the machine's places and
// descriptor sets around it.
var blockdevAddNode = qmpdef.Define[blockdevNode, qmp.None]("blockdev-add")

type blockdevNode struct {
	Driver   string `json:"driver"`
	NodeName string `json:"node-name"`
	Filename string `json:"filename,omitempty"`
	Image    string `json:"image,omitempty"`
	File     string `json:"file,omitempty"`
	AIO      string `json:"aio,omitempty"`
}

// humanMonitorCommand runs an HMP command: here qemu-io, which writes a node without a guest.
var humanMonitorCommand = qmpdef.Define[hmpCommand, string]("human-monitor-command")

type hmpCommand struct {
	Line string `json:"command-line"`
}

// queryBlockstatsRaw is query-blockstats with each device's statistics left as QEMU wrote them,
// to see which members it has.
var queryBlockstatsRaw = qmpdef.Define[blockstatsArgs, []map[string]json.RawMessage]("query-blockstats")
