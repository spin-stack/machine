// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestThePressureProbeReportsEachPressure is the probe's lane: a guest of the release in
// SPIN_MACHINE_OUTPUT, with this checkout's sender (SPIN_PRESSURE_SENDER) and unit in a private raw
// copy of its root, put under each pressure in turn (testdata/pressure-check.sh): its CPU short,
// the host taking its vCPU's core, its disk, its memory, an OOM, then each limit the host sets on
// QEMU's cgroup: cpu.max, memory.high and io.max on a second disk in SPIN_PRESSURE_DISK_DIR, which
// must be on a block device, and last a DiskLimit on that disk inside QEMU (SetDiskLimit). Each
// phase must bring the Pressure that names it, from the vantage
// that sees it, no phase before the limits a Limited, and Usage's counters must move. Root, for
// the host's triggers and the cgroup QEMU runs in, KVM and /dev/vhost-vsock: task boot:pressure.
func TestThePressureProbeReportsEachPressure(t *testing.T) {
	if os.Getenv("SPIN_PRESSURE_TEST") != "1" {
		t.Skip("set SPIN_PRESSURE_TEST=1, as root, to put a guest under each pressure: task boot:pressure")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the host's triggers and QEMU's cgroup need root: task boot:pressure runs this under sudo")
	}
	out := outputDir(t)
	rel, err := OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}
	root := newGuestRoot(t, out, base)
	sender, err := os.ReadFile(os.Getenv("SPIN_PRESSURE_SENDER"))
	if err != nil {
		t.Fatalf("reading the sender task boot:pressure built (SPIN_PRESSURE_SENDER): %v", err)
	}
	check, err := os.ReadFile(filepath.Join("testdata", "pressure-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root.write("/usr/libexec/spin-machine-pressure", string(sender))
	root.debugfs("set_inode_field /usr/libexec/spin-machine-pressure mode 0100755")
	for _, unit := range []string{"spin-machine-pressure.service", "spin-machine-pressure.timer"} {
		path := "/usr/lib/systemd/system/" + unit
		text, err := os.ReadFile(filepath.Join("..", "image", "mkosi.extra", "usr", "local", "lib", "spin-base", "rootfs", path))
		if err != nil {
			t.Fatal(err)
		}
		root.write(path, string(text))
	}
	root.debugfs("symlink /etc/systemd/system/timers.target.wants/spin-machine-pressure.timer /usr/lib/systemd/system/spin-machine-pressure.timer")
	root.write("/pressure-check.sh", string(check))
	// OOMPolicy=continue: by default systemd stops a service one of whose processes the OOM killer
	// killed, and the check would end at its oom phase.
	root.write("/etc/systemd/system/pressure-check.service", "[Unit]\nAfter=multi-user.target\n[Service]\nType=oneshot\n"+
		"OOMPolicy=continue\nExecStart=/bin/sh /pressure-check.sh\nStandardOutput=journal+console\nStandardError=journal+console\n")
	root.debugfs("symlink /etc/systemd/system/multi-user.target.wants/pressure-check.service /etc/systemd/system/pressure-check.service")

	lim, limDev := newLimitDisk(t, os.Getenv("SPIN_PRESSURE_DISK_DIR"))

	cgroup := filepath.Join("/sys/fs/cgroup", fmt.Sprintf("spin-pressure-test-%d", os.Getpid()))
	if err := os.Mkdir(cgroup, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cgroup) })
	dir, err := os.Open(cgroup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	listener, err := ListenPressure()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	// The vCPU's core, which the steal phase's hogs share with it.
	core := cmpOr(os.Getenv("SPIN_PRESSURE_CORE"), "3")
	var mu sync.Mutex
	phase := "boot"
	seen := map[string][]Pressure{}
	watch := listener.Watch(func(p Pressure) {
		mu.Lock()
		defer mu.Unlock()
		seen[phase] = append(seen[phase], p)
	})
	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	spec.BootCPUs, spec.Memory.BaseMB = 1, 1024
	spec.Places = []Place{{Serial: "root"}, {Serial: "lim"}}
	spec.VsockCID = 4000 + os.Getpid()%10000
	c := DefaultCmdline()
	c.Root, c.Init = "/dev/vda", "/sbin/init"
	spec.Cmdline = c
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	console := &consoleText{news: make(chan struct{}, 1)}
	p, err := Start(ctx, spec, StartOptions{Pressure: watch, Console: console, Cmd: func(c *exec.Cmd) {
		taskset, err := exec.LookPath("taskset")
		if err != nil {
			t.Fatal(err)
		}
		c.Args = append([]string{"taskset", "-c", core}, c.Args...)
		c.Path = taskset
		c.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	f, err := os.OpenFile(root.path, os.O_RDWR, 0) // #nosec G304 -- the root this test made
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := p.Boot(ctx, Disk{Serial: "root", Images: []Image{{File: f, Format: "raw"}}},
		Disk{Serial: "lim", Images: []Image{{File: lim, Format: "raw"}}}); err != nil {
		t.Fatal(err)
	}

	var before, after Usage
	var hogs []*exec.Cmd
	set := func(file, value string) {
		if err := os.WriteFile(filepath.Join(cgroup, file), []byte(value), 0); err != nil {
			t.Errorf("setting %s to %q: %v", file, value, err)
		}
	}
	lift := func() {}
	defer func() { lift() }()
	defer func() {
		for _, h := range hogs {
			_ = h.Process.Kill()
			_ = h.Wait()
		}
	}()
	for text := console.String(); !strings.Contains(text, "PRESSURE_CHECK_OK"); text = console.String() {
		if strings.Contains(text, "PRESSURE_CHECK_FAILED") || ctx.Err() != nil {
			t.Fatalf("the check failed: %v\n%s\nQEMU %s", ctx.Err(), text, p.Kill())
		}
		select {
		case <-console.news:
		case <-p.Exited():
			t.Fatalf("QEMU %s\n%s", p.Wait(), text)
		case <-ctx.Done():
		}
		now := lastPhase(console.String())
		mu.Lock()
		changed := now != phase
		phase = now
		mu.Unlock()
		if !changed {
			continue
		}
		u, err := p.Usage(ctx)
		if err != nil {
			t.Errorf("Usage at %s: %v", now, err)
		}
		t.Logf("%s: %+v", now, u)
		lift()
		lift = func() {}
		switch now {
		case "idle":
			before = u
		case "done":
			after = u
		case "steal":
			for range 2 {
				h := exec.Command("taskset", "-c", core, "timeout", "8", "sh", "-c", "while :; do :; done")
				if err := h.Start(); err != nil {
					t.Fatal(err)
				}
				hogs = append(hogs, h)
			}
		case "cpulimit":
			set("cpu.max", "20000 100000")
			lift = func() { set("cpu.max", "max 100000") }
		case "memlimit":
			set("memory.high", fmt.Sprint(u.Memory.Current+32<<20))
			lift = func() { set("memory.high", "max") }
		case "iolimit":
			// What QEMU reads it reads through the host's cache, which has the disk from memlimit.
			if err := unix.Fadvise(int(lim.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
				t.Errorf("dropping the second disk from the host's cache: %v", err)
			}
			set("io.max", limDev+" rbps=4194304")
			lift = func() { set("io.max", limDev+" rbps=max") }
		case "disklimit":
			if err := p.SetDiskLimit(ctx, "lim", DiskLimit{ReadBytes: 4 << 20}); err != nil {
				t.Errorf("limiting the second disk: %v", err)
			}
			lift = func() {
				if err := p.SetDiskLimit(ctx, "lim", DiskLimit{}); err != nil {
					t.Errorf("clearing the second disk's limit: %v", err)
				}
			}
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, ph := range []string{"boot", "idle", "cpu", "steal", "io", "memory", "oom", "cpulimit", "memlimit", "iolimit", "disklimit", "done"} {
		for _, p := range seen[ph] {
			t.Logf("%-6s %T %+v", ph, p, p)
		}
	}
	stall := func(ph string, v Vantage, r Resource) {
		for _, p := range seen[ph] {
			if s, ok := p.(Stall); ok && s.Seen == v && s.Resource == r {
				return
			}
		}
		t.Errorf("phase %s brought no %s stall on %s", ph, v, r)
	}
	stall("cpu", Guest, CPUPressure)
	stall("steal", Host, CPUPressure)
	stall("io", Guest, IOPressure)
	stall("memory", Guest, MemoryPressure)
	killed := false
	for _, p := range seen["oom"] {
		_, ok := p.(OOMKill)
		killed = killed || ok
	}
	if !killed {
		t.Error("phase oom brought no OOMKill")
	}
	limited := func(ph string, l Limit) {
		for _, p := range seen[ph] {
			if x, ok := p.(Limited); ok && x.Limit == l {
				return
			}
		}
		t.Errorf("phase %s brought no Limited of %s", ph, l)
	}
	limited("cpulimit", CPUMax)
	limited("memlimit", MemoryHigh)
	limited("iolimit", IOReadBytes)
	limited("disklimit", DiskReadBytes)
	for _, p := range seen["disklimit"] {
		if x, ok := p.(Limited); ok && x.Limit == DiskReadBytes && (x.Device != "lim" || x.Set != 4<<20) {
			t.Errorf("the second disk's limit was reported as %+v, want disk lim at 4 MiB/s", x)
		}
	}
	for _, ph := range []string{"boot", "idle", "cpu", "steal", "io", "memory", "oom"} {
		for _, p := range seen[ph] {
			if x, ok := p.(Limited); ok {
				t.Errorf("phase %s, with no limit set, brought %+v", ph, x)
			}
		}
	}
	if after.CPU.Throttles <= before.CPU.Throttles || after.Memory.High <= before.Memory.High {
		t.Errorf("cpu.max's throttles or memory.high's reclaims did not move: %+v then %+v", before, after)
	}
	if after.CPU.Used <= before.CPU.Used {
		t.Errorf("QEMU's CPU did not move: %v then %v", before.CPU.Used, after.CPU.Used)
	}
	if len(after.Disks) == 0 || len(before.Disks) == 0 || after.Disks[0].Read <= before.Disks[0].Read {
		t.Errorf("the disk's reads did not move: %+v then %+v", before.Disks, after.Disks)
	}
	if after.Guest == nil || after.Guest.OOMKills == 0 {
		t.Errorf("the balloon reported no OOM kill: %+v", after.Guest)
	}
}

// newLimitDisk is a disk of 256 MiB written in a directory of dir, which must be on a block
// device: io.max limits a block device, and a hole or a tmpfs reads without one. It returns the
// disk, open, and the device, major:minor, its directory is on.
func newLimitDisk(t *testing.T, dir string) (*os.File, string) {
	t.Helper()
	if dir == "" {
		t.Fatal("SPIN_PRESSURE_DISK_DIR is not set: task boot:pressure sets it")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(dir, "limit-disk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f, err := os.Create(filepath.Join(dir, "lim.raw")) // #nosec G304 -- in the directory this test made
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	for range 256 {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	dev := fmt.Sprintf("%d:%d", unix.Major(st.Dev), unix.Minor(st.Dev))
	sys := "/sys/dev/block/" + dev
	if _, err := os.Stat(sys); err != nil {
		t.Fatalf("SPIN_PRESSURE_DISK_DIR %s is on device %s, which is no block device", dir, dev)
	}
	// io.max takes a whole disk, and a partition's disk is its parent.
	if _, err := os.Stat(filepath.Join(sys, "partition")); err == nil {
		b, err := os.ReadFile(sys + "/../dev")
		if err != nil {
			t.Fatal(err)
		}
		dev = strings.TrimSpace(string(b))
	}
	return f, dev
}

// lastPhase is the last phase the console named on a whole line.
func lastPhase(text string) string {
	phase := "boot"
	for line := range strings.Lines(text) {
		if name, ok := strings.CutSuffix(line, "\n"); ok {
			if _, after, ok := strings.Cut(name, "PHASE "); ok {
				phase = strings.TrimSpace(after)
			}
		}
	}
	return phase
}

// consoleText is the guest's console so far, and a note each time it grows.
type consoleText struct {
	mu   sync.Mutex
	b    strings.Builder
	news chan struct{}
}

func (c *consoleText) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.b.Write(p)
	c.mu.Unlock()
	select {
	case c.news <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (c *consoleText) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// guestRoot is a private raw copy of a release's root filesystem, edited by the release's own
// debugfs, which exits 0 whether or not an edit happened: what is written is read back.
type guestRoot struct {
	t          *testing.T
	path, tool string
}

func newGuestRoot(t *testing.T, out, base string) *guestRoot {
	t.Helper()
	r := &guestRoot{t: t, path: filepath.Join(t.TempDir(), "rootfs.raw"), tool: filepath.Join(out, "bin", "debugfs")}
	if b, err := exec.Command(filepath.Join(out, "bin", "qemu-img"), "convert", "-f", "qcow2", "-O", "raw", base, r.path).CombinedOutput(); err != nil {
		t.Fatalf("copying %s: %v: %s", base, err, b)
	}
	return r
}

func (r *guestRoot) debugfs(request string) {
	r.t.Helper()
	if b, err := exec.Command(r.tool, "-w", "-R", request, r.path).CombinedOutput(); err != nil {
		r.t.Fatalf("debugfs %s: %v: %s", request, err, b)
	}
}

func (r *guestRoot) write(path, content string) {
	r.t.Helper()
	in := filepath.Join(r.t.TempDir(), "in")
	if err := os.WriteFile(in, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	_ = exec.Command(r.tool, "-w", "-R", "mkdir "+filepath.Dir(path), r.path).Run()
	r.debugfs("write " + in + " " + path)
	if b, err := exec.Command(r.tool, "-R", "cat "+path, r.path).Output(); err != nil || string(b) != content {
		r.t.Fatalf("%s is not in the guest's root as written (%v)", path, err)
	}
}
