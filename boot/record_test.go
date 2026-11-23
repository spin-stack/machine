// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// spinsShape is the machine spin runs a workspace on (internal/lab's shape, spin c12bd87), but
// for its vsock and NIC, which every machine here is given apart: one vCPU of sixteen, 128 MiB of
// 8 GiB, sixteen hotplug targets, its root on /dev/vda and init as given.
func spinsShape(t *testing.T, out, init string) machine.Spec {
	t.Helper()
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	s, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	s.BootCPUs, s.MaxCPUs = 1, 16
	s.Memory = machine.Memory{BaseMB: 128, CeilingMB: 8192}
	s.HotplugDisks = 16
	s.Places = []machine.Place{{Serial: "root"}}
	c := machine.DefaultCmdline()
	c.Root, c.Init = "/dev/vda", init
	s.Cmdline = c
	return s
}

// balloon sets the size the balloon leaves the guest: a call machine.QEMU has no use for, which
// the record's test sends to put an inflated balloon in the stream.
var balloon = qmpdef.Define[balloonTarget, qmp.None]("balloon")

// balloonTarget is the guest's memory, in bytes, once the balloon has taken the rest.
type balloonTarget struct {
	Value int64 `json:"value"`
}

// The record a machine reads of itself (machine.QEMU.Target) at spin's shape, held to what it is
// for: a machine resumes its own checkpoint after everything a workspace does to it while it runs,
// and refuses by name each difference QEMU loads silently (an extra device, a larger BIOS, more
// base RAM), while letting a machine with more hotplug targets take the checkpoint. And the BIOS
// block it reads is the BIOS file's size, and the vCPU's x-consistent-cache what -cpu gives it.
//
// A missing entry in the record's per-VM or run-time lists makes a machine refuse its own
// checkpoint, which the first part fails on; a QEMU bump that changes what the record reads fails
// here, at the bump.
//
//	SPIN_RECORD_TEST=1   it starts machines (task boot:record)
func TestTheRecordRefusesWhatQEMULoadsSilently(t *testing.T) {
	if os.Getenv("SPIN_RECORD_TEST") == "" {
		t.Skip("set SPIN_RECORD_TEST=1: this starts machines")
	}
	r := newRecorder(t, 3000000)
	out := r.out

	t.Run("a machine resumes its own checkpoint", r.selfResume)

	base, took := r.target(func(*machine.Spec) {})
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the record of spin's shape: %d bytes of JSON, %d CPU features, a machine started, read and ended in %s", len(b), len(base.CPU.Features), took.Round(time.Microsecond))

	t.Run("the BIOS block is the BIOS file's size", func(t *testing.T) {
		bios, err := os.Stat(filepath.Join(out, "qemu", machine.DefaultBIOS))
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Machine struct {
				Exact []struct{ Name, Value string }
			}
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(rec.Machine.Exact, func(p struct{ Name, Value string }) bool { return p.Name == "ram pc.bios" })
		if i < 0 || rec.Machine.Exact[i].Value != fmt.Sprint(bios.Size()) {
			t.Errorf("the record's ram pc.bios is %v, and %s is %d bytes", rec.Machine.Exact, machine.DefaultBIOS, bios.Size())
		}
	})

	t.Run("the record holds the switch -cpu gives leaf 2", func(t *testing.T) {
		if got := base.CPU.Properties["x-consistent-cache"]; got != "false" {
			t.Errorf("the record's x-consistent-cache is %q, want false as -cpu gives it, among %v", got, base.CPU.Properties)
		}
	})
	// A machine of v20261122.07, before -cpu set x-consistent-cache=off; and a checkpoint a
	// spin-machine wrote before the record held the vCPU's properties (#569).
	leaf2FF := base
	leaf2FF.CPU.Properties = maps.Clone(base.CPU.Properties)
	leaf2FF.CPU.Properties["x-consistent-cache"] = "true"
	before := base
	before.CPU.Properties = nil
	var eachProperty []string
	for _, n := range slices.Sorted(maps.Keys(base.CPU.Properties)) {
		eachProperty = append(eachProperty, "cpu "+n)
	}

	noVsock, _ := r.target(func(s *machine.Spec) { s.VsockCID = 0 })
	seabios, _ := r.target(func(s *machine.Spec) { s.BIOS = "bios-256k.bin" })
	moreRAM, _ := r.target(func(s *machine.Spec) { s.Memory.BaseMB = 256 })
	moreTargets, _ := r.target(func(s *machine.Spec) { s.HotplugDisks = 17 })
	for _, tc := range []struct {
		name     string
		from, to machine.Target
		want     []string // the parts the refusal names, all of them; none when it resumes
	}{
		{"an extra device here: a vsock", noVsock, base, []string{"device 02.0"}},
		{"a device missing here: the vsock", base, noVsock, []string{"device 02.0"}},
		{"a larger BIOS here: SeaBIOS for qboot", base, seabios, []string{"ram pc.bios"}},
		{"more base RAM here", base, moreRAM, []string{"machine memory.size", "ram 0000:00:1e.0/mem.growth", "ram pc.ram"}},
		{"more hotplug targets here", base, moreTargets, nil},
		{"fewer hotplug targets here", moreTargets, base, []string{"device 1a.0 max_target"}},
		{"leaf 2 at 0xFF in the checkpoint's machine", leaf2FF, base, []string{"cpu x-consistent-cache"}},
		{"a checkpoint from before the record held the vCPU's properties", before, base, eachProperty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := machine.CanResume(machine.Saved{Format: machine.FormatMappedRAM, From: tc.from}, tc.to)
			var refusal *machine.Refusal
			var named []string
			if errors.As(err, &refusal) {
				for _, d := range refusal.Differences {
					named = append(named, d.Part)
				}
			}
			if (err == nil) != (tc.want == nil) || !slices.Equal(named, tc.want) {
				t.Errorf("CanResume = %v, want a refusal naming %v", err, tc.want)
			}
			if err != nil {
				t.Log(err)
			}
		})
	}
}

// recorder starts machines of spin's shape and reads their records.
type recorder struct {
	t        *testing.T
	out, dir string
	n, cid   int
}

// newRecorder is a recorder of the release under test, its vsock context ids from cid on, kept
// apart per process so that runs side by side do not share one.
func newRecorder(t *testing.T, cid int) *recorder {
	t.Helper()
	r := &recorder{t: t, out: releaseDir(t), cid: cid + os.Getpid()%100000*10}
	// Second monitors' sockets, in /tmp: a socket's path fits in 108 bytes.
	var err error
	if r.dir, err = os.MkdirTemp("/tmp", "rec"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(r.dir) })
	return r
}

// next is the next machine: spin's shape running init, with a vsock of its own, and its NIC's MAC.
func (r *recorder) next(t *testing.T, init string) (machine.Spec, string) {
	t.Helper()
	r.n++
	s := spinsShape(t, r.out, init)
	s.VsockCID = r.cid + r.n
	return s, fmt.Sprintf("52:54:00:00:%02x:%02x", r.n>>8, r.n&0xff)
}

// overlay is a new qcow2 overlay of the release's base image in dir.
func (r *recorder) overlay(t *testing.T, dir string) string {
	t.Helper()
	overlay := filepath.Join(dir, "root.qcow2")
	if b, err := exec.Command(filepath.Join(r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", //nolint:gosec // the release's qemu-img
		"-b", r.base(), "-F", "qcow2", overlay).CombinedOutput(); err != nil {
		t.Fatalf("an overlay of the base image: %v: %s", err, b)
	}
	return overlay
}

func (r *recorder) base() string { return filepath.Join(r.out, "image", "rootfs.qcow2") }

// target is the record of a machine of spin's shape changed by edit (machine.ReadTarget), and how
// long ReadTarget took.
func (r *recorder) target(edit func(*machine.Spec)) (machine.Target, time.Duration) {
	t := r.t
	t.Helper()
	s := r.spec(t, "")
	edit(&s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	tg, err := machine.ReadTarget(ctx, s, machine.StartOptions{})
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	return tg, time.Since(start)
}

// spec is the next machine as spin starts it, its NIC and console in the Spec: what ReadTarget is
// handed. The console is the serial port the record names, which a machine without one lacks.
func (r *recorder) spec(t *testing.T, init string) machine.Spec {
	t.Helper()
	s, mac := r.next(t, init)
	s.NICs = []machine.NIC{{MAC: mac, MTU: spinsMTU}}
	s.Console = "stdio"
	return s
}

// selfResume boots a machine of spin's shape, does to it what a workspace's life does - memory
// plugged, the balloon asked for and polled, a vCPU and a disk added - checkpoints it, and resumes
// the checkpoint into a fresh machine held for one, of the same shape with its own vsock and MAC.
func (r *recorder) selfResume(t *testing.T) {
	dir := t.TempDir()
	overlay := r.overlay(t, dir)
	ext := filepath.Join(dir, "ext.raw")
	if err := os.WriteFile(ext, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	s, mac := r.next(t, "/bin/sh")
	// A second monitor, for what machine.QEMU has no call for: inflating the balloon.
	raw := filepath.Join(r.dir, "raw.sock")
	s.Monitors = []machine.Monitor{{Socket: raw}}
	src := startGuest(t, s, mac, nil)
	src.boot(t, overlay, r.base())
	src.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	src.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	if err := src.SetMemory(ctx, 1024); err != nil {
		t.Fatal(err)
	}
	for size := (machine.MemorySize{}); size.Now != 1024; {
		var err error
		if size, err = src.Memory(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The first call turns the balloon's polling on, which is state the stream carries.
	if _, err := src.GuestMemory(ctx); err != nil && !errors.Is(err, machine.ErrNoGuestStats) {
		t.Fatal(err)
	}
	inflate(ctx, t, raw)
	if err := src.SetCPUs(ctx, 2); err != nil {
		t.Fatal(err)
	}
	extDisk := func(t *testing.T) machine.Disk {
		d, err := machine.OpenDisk(machine.DiskPath{Serial: "ext", Path: ext, Format: "raw"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}
	if err := src.AttachDisk(ctx, extDisk(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // the balloon's statistics polled at least once
	state, err := os.Create(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	saved, _, err := src.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, src.said())
	}
	src.Kill()

	// The record a runner reads once for its host, of a machine of the shape held for a checkpoint
	// and its NIC in the Spec, is what the machine resumed into is held to.
	s = r.spec(t, "/bin/sh")
	s.Restore = true
	here, err := machine.ReadTarget(ctx, s, machine.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mac = s.NICs[0].MAC
	s.NICs = nil
	dst := startGuest(t, s, mac, nil)
	if err := machine.CanResume(saved, here); err != nil {
		t.Fatalf("a machine refuses its own checkpoint: %v", err)
	}
	root := rootDisk(t, overlay, r.base())
	if err := dst.Resume(ctx, state, saved, here, machine.LoadOptions{}, root, extDisk(t)); err != nil {
		t.Fatalf("resuming: %v\n%s", err, dst.said())
	}
	dst.type_(t, "echo RESUMED-$((2+3))")
	dst.await(t, regexp.MustCompile(`RESUMED-5\b`), 30*time.Second)
	t.Logf("a checkpoint taken on %s after a memory plug, the balloon, a vCPU and a disk resumed into a fresh machine of the shape: %s",
		saved.From.VMM, saved.Added)
}

// inflate leaves the guest 960 MiB through the monitor at sock. machine.Dial would do for the
// connection, but a machine.QEMU sends only the calls it declares, and the balloon is not one.
func inflate(ctx context.Context, t *testing.T, sock string) {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		t.Fatalf("talking to %s: %v", sock, err)
	}
	defer func() { _ = q.Close() }()
	if _, err := qmp.Do(ctx, q, balloon, balloonTarget{Value: 960 << 20}); err != nil {
		t.Fatalf("inflating the balloon: %v", err)
	}
}

// rootDisk is the root disk as a machine.Disk: the overlay, writable, over the base image, read
// only, its chain read from the overlay's header as spin-machine reads one. Its files close when t ends:
// the overlay's lock is on the description QEMU was handed, and one left to the garbage collector
// outlives a killed machine and refuses the next machine its disk (#459).
func rootDisk(t *testing.T, overlay, base string) machine.Disk {
	t.Helper()
	d, err := machine.OpenDisk(machine.DiskPath{Serial: "root", Path: overlay, Format: "qcow2", Under: []string{base}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// spinsMTU is the MTU spin announces to a workspace's NIC.
const spinsMTU = 1450

// spinsNIC is the NIC spin gives a workspace, as Spec.Args writes it (appendNICs) with spin's
// MTU, on a hub port in place of spin's TAP, which needs CAP_NET_ADMIN.
func spinsNIC(mac string) func(*exec.Cmd) {
	return func(c *exec.Cmd) {
		c.Args = append(c.Args, "-netdev", "hubport,id=net0,hubid=0",
			"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s,romfile=,disable-legacy=on,addr=0x10,host_mtu=%d", mac, spinsMTU))
	}
}

// screen is what a console has said, written by QEMU's side and read by the test's.
type screen struct {
	mu sync.Mutex
	b  bytes.Buffer
	// writes is when each write arrived, by the length of b after it.
	writes []stamp
	// news has a value once something was written since it was last taken.
	news chan struct{}
}

// stamp is when a write to a screen arrived, and the screen's length after it.
type stamp struct {
	end int
	at  time.Time
}

func newScreen() *screen { return &screen{news: make(chan struct{}, 1)} }

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.b.Write(p)
	s.writes = append(s.writes, stamp{end: s.b.Len(), at: time.Now()})
	s.mu.Unlock()
	select {
	case s.news <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// guest is one machine whose console is QEMU's stdin and stdout.
type guest struct {
	*machine.Process
	stdin  *os.File
	screen *screen
	// n counts the commands side typed, whose output it tells apart by it.
	n int
}

// startGuest starts a machine of s, held, with spin's NIC, its events from the handshake on going
// to events when it is not nil.
func startGuest(t *testing.T, s machine.Spec, mac string, events func(qmp.Event)) *guest {
	t.Helper()
	s.Console = "stdio"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	defer func() { _ = r.Close() }()
	g := &guest{stdin: w, screen: newScreen()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	g.Process, err = machine.Start(ctx, s, machine.StartOptions{
		Stdin: r, Stdout: g.screen, Stderr: g.screen,
		Observer: qmp.Observer{Event: events}, Cmd: spinsNIC(mac),
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, g.said())
	}
	t.Cleanup(func() { g.Kill() })
	return g
}

// boot binds the overlay over base as the root disk and boots the machine.
func (g *guest) boot(t *testing.T, overlay, base string) {
	t.Helper()
	root := rootDisk(t, overlay, base)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := g.Boot(ctx, root); err != nil {
		t.Fatalf("booting: %v\n%s", err, g.said())
	}
}

// type_ is a line on the console. The guest's shell is not reading it when QEMU starts, so the
// first bytes are lost: blank lines go first, each costing only a prompt.
func (g *guest) type_(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(g.stdin, "\n\n\n\n\n\n\n\n\n\n"+line+"\n"); err != nil {
		t.Fatalf("typing on the console: %v", err)
	}
}

func (g *guest) said() string { return g.screen.String() }

// await is the console printing what matches re, or the test failing with what it did print.
func (g *guest) await(t *testing.T, re *regexp.Regexp, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !re.MatchString(g.said()) {
		if time.Now().After(deadline) {
			t.Fatalf("the console never said %s in %s:\n%s", re, timeout, tail([]byte(g.said()), 2000))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
