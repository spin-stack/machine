// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// spinsShape is the machine spin runs a workspace on (internal/lab's shape, spin c12bd87), but
// for its vsock and NIC, which every machine here is given apart: one vCPU of sixteen, 128 MiB of
// 8 GiB, sixteen hotplug targets.
var spinsShape = []string{"--cpus", "1", "--max-cpus", "16", "--memory", "128", "--max-memory", "8192", "--hotplug-disks", "16"}

// The record a machine reads of itself (machine.QEMU.Target) at spin's shape, held to what it is
// for: a machine resumes its own checkpoint after everything a workspace does to it while it runs,
// and refuses by name each difference QEMU loads silently (an extra device, a larger BIOS, more
// base RAM), while letting a machine with more hotplug targets take the checkpoint. And the BIOS
// block it reads is the BIOS file's size.
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
	out := releaseDir(t)
	r := &recorder{t: t, out: out, cli: filepath.Join(out, "bin", "spin-machine"), cid: 3000000 + os.Getpid()%100000*10}
	r.dir, _ = os.MkdirTemp("/tmp", "rec")
	t.Cleanup(func() { _ = os.RemoveAll(r.dir) })

	t.Run("a machine resumes its own checkpoint", r.selfResume)

	base, took := r.target(true)
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the record of spin's shape: %d bytes of JSON, %d CPU features, read in %s", len(b), len(base.CPU.Features), took.Round(time.Microsecond))

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

	noVsock, _ := r.target(false)
	seabios, _ := r.target(true, "--bios", "bios-256k.bin")
	moreRAM, _ := r.target(true, "--memory", "256")
	moreTargets, _ := r.target(true, "--hotplug-disks", "17")
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
	t             *testing.T
	out, cli, dir string
	n, cid        int
}

// next is the next machine's monitor, vsock context id and NIC.
func (r *recorder) next() (sock string, cid int, mac string) {
	r.n++
	return filepath.Join(r.dir, fmt.Sprintf("%d.sock", r.n)), r.cid + r.n, fmt.Sprintf("52:54:00:00:%02x:%02x", r.n>>8, r.n&0xff)
}

// target is the record of a held machine of spin's shape changed by flags, with a vsock or
// without, and how long the read took; the machine is ended once it has said.
func (r *recorder) target(vsock bool, flags ...string) (machine.Target, time.Duration) {
	t := r.t
	t.Helper()
	sock, cid, mac := r.next()
	more := []string{"--qmp", sock}
	if vsock {
		more = append(more, "--vsock-cid", fmt.Sprint(cid))
	}
	args, _ := machineArgs(t, r.cli, r.out, slices.Concat(spinsShape, more, flags))
	g := startGuest(t, append(args, spinsNIC(mac)...))
	m := attachTo(t, sock, g)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	tg, err := m.Target(ctx)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("the record of %v: %v\n%s", flags, err, g.said())
	}
	_ = m.Quit(ctx)
	return tg, took
}

// selfResume boots a machine of spin's shape, does to it what a workspace's life does - memory
// plugged, the balloon asked for and polled, a vCPU and a disk added - checkpoints it, and resumes
// the checkpoint into a fresh machine held for one, of the same shape with its own vsock and MAC.
func (r *recorder) selfResume(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "root.qcow2")
	if b, err := exec.Command(filepath.Join(r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", //nolint:gosec // the release's qemu-img
		"-b", filepath.Join(r.out, "image", "rootfs.qcow2"), "-F", "qcow2", overlay).CombinedOutput(); err != nil {
		t.Fatalf("an overlay of the base image: %v: %s", err, b)
	}
	ext := filepath.Join(dir, "ext.raw")
	if err := os.WriteFile(ext, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	disk := []string{"--disk", overlay, "--disk-format", "qcow2", "--init", "/bin/sh"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sock, cid, mac := r.next()
	raw := filepath.Join(r.dir, "raw.sock")
	args, start := machineArgs(t, r.cli, r.out, slices.Concat(spinsShape, []string{"--qmp", sock, "--vsock-cid", fmt.Sprint(cid)}, disk))
	// A second monitor, for what machine.VM has no call for: the balloon.
	args = append(args, "-qmp", "unix:"+raw+",server=on,wait=off")
	a := startGuest(t, append(args, spinsNIC(mac)...))
	start(t)
	a.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	a.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	src := attachTo(t, sock, a)
	if err := src.SetMemory(ctx, 1024); err != nil {
		t.Fatal(err)
	}
	for mb := 0; mb != 1024; {
		var err error
		if mb, err = src.Memory(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	q := dial(t, raw, a.said)
	var anon []struct{ Name, Type string }
	if err := json.Unmarshal(q.do(t, "qom-list", map[string]any{"path": "/machine/peripheral-anon"}), &anon); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(anon, func(p struct{ Name, Type string }) bool { return p.Type == "child<virtio-balloon-pci>" })
	if i < 0 {
		t.Fatalf("no balloon among %v", anon)
	}
	q.do(t, "qom-set", map[string]any{"path": "/machine/peripheral-anon/" + anon[i].Name, "property": "guest-stats-polling-interval", "value": 2})
	q.do(t, "balloon", map[string]any{"value": 960 << 20})
	q.close()
	if err := src.SetCPUs(ctx, 2); err != nil {
		t.Fatal(err)
	}
	extDisk := func(t *testing.T) machine.Disk {
		f, err := os.OpenFile(ext, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return machine.Disk{Serial: "ext", Locking: true, Images: []machine.Image{{File: f, Format: "raw"}}}
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
	saved, err := src.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, a.said())
	}
	_ = src.Quit(ctx)
	a.kill()

	sock, cid, mac = r.next()
	args, _ = machineArgs(t, r.cli, r.out, slices.Concat(spinsShape, []string{"--qmp", sock, "--vsock-cid", fmt.Sprint(cid), "--restore"}, disk))
	b := startGuest(t, append(args, spinsNIC(mac)...))
	dst := attachTo(t, sock, b)
	here, err := dst.Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.CanResume(saved, here); err != nil {
		t.Fatalf("a machine refuses its own checkpoint: %v", err)
	}
	root, err := rootDisk(overlay, filepath.Join(r.out, "image", "rootfs.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Resume(ctx, state, saved, here, machine.LoadOptions{}, root, extDisk(t)); err != nil {
		t.Fatalf("resuming: %v\n%s", err, b.said())
	}
	b.type_(t, "echo RESUMED-$((2+3))")
	b.await(t, regexp.MustCompile(`RESUMED-5\b`), 30*time.Second)
	t.Logf("a checkpoint taken on %s after a memory plug, the balloon, a vCPU and a disk resumed into a fresh machine of the shape: %s",
		saved.From.VMM, saved.Added)
}

// rootDisk is the root disk as a machine.Disk: the overlay, writable, over the base image, read
// only, as spin-machine opens a chain.
func rootDisk(overlay, base string) (machine.Disk, error) {
	top, err := os.OpenFile(overlay, os.O_RDWR, 0)
	if err != nil {
		return machine.Disk{}, err
	}
	under, err := os.Open(base)
	if err != nil {
		return machine.Disk{}, err
	}
	return machine.Disk{Serial: "root", Locking: true, Images: []machine.Image{{File: top, Format: "qcow2"}, {File: under, Format: "qcow2"}}}, nil
}

// spinsNIC is the NIC spin gives a workspace, as Spec.Args writes it (appendNICs) with spin's
// MTU, on a hub port in place of spin's TAP: spin-machine has no flag for a NIC, and a TAP needs
// CAP_NET_ADMIN.
func spinsNIC(mac string) []string {
	return []string{"-netdev", "hubport,id=net0,hubid=0",
		"-device", "virtio-net-pci,netdev=net0,mac=" + mac + ",romfile=,disable-legacy=on,addr=0x10,host_mtu=1450"}
}

// attachTo is machine.Attach on a machine's monitor, once QEMU has made it.
func attachTo(t *testing.T, sock string, g *guest) *machine.QEMU {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
		if err == nil {
			m, err := machine.Attach(c)
			if err != nil {
				t.Fatalf("attaching to %s: %v", sock, err)
			}
			t.Cleanup(func() { _ = m.Close() })
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("QEMU never answered on %s: %v\n%s", sock, err, g.said())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// guest is one QEMU whose console is its stdin and stdout.
type guest struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	out   bytes.Buffer
}

func startGuest(t *testing.T, args []string) *guest {
	t.Helper()
	g := &guest{cmd: exec.Command(args[0], args[1:]...)} //nolint:gosec // the machine's own command line
	stdin, err := g.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	g.stdin = stdin
	r, w := io.Pipe()
	g.cmd.Stdout, g.cmd.Stderr = w, w
	if err := g.cmd.Start(); err != nil {
		t.Fatalf("starting QEMU: %v", err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			g.mu.Lock()
			g.out.Write(buf[:n])
			g.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(g.kill)
	return g
}

// type_ is a line on the console. The guest's shell is not reading it when QEMU starts, so the
// first bytes are lost: blank lines go first, each costing only a prompt.
func (g *guest) type_(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(g.stdin, "\n\n\n\n\n\n\n\n\n\n"+line+"\n"); err != nil {
		t.Fatalf("typing on the console: %v", err)
	}
}

func (g *guest) said() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.out.String()
}

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

func (g *guest) kill() {
	if g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_ = g.cmd.Wait()
	}
}
