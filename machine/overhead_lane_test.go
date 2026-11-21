// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestAMachineRunsInARootOfItsFilesWithinItsOverhead is the lane of Spec.Files and Spec.Overhead:
// spin's shape (1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug targets, a root and a home, a vsock and
// a NIC on a TAP), started as nobody in a root that holds Spec.Files and /dev/null alone, in a
// cgroup of its own. It is sized to 2048 MiB at its start as spin sizes it, then taken to its
// ceiling: every vCPU and all its memory plugged and four disks attached, one of them 64 GiB. It
// is checkpointed and continued while the guest fills its memory, reads, writes and trims every
// disk at once (testdata/overhead-check.sh), and checkpointed again at the end. QEMU may hold no
// more than Spec.Overhead and each Disk.Overhead beyond the guest's RAM, at any reading, nor its
// cgroup more than Overhead.Tasks threads. Root, for the
// root, the TAP and the cgroup, KVM, /dev/vhost-vsock and /dev/vhost-net: task boot:overhead.
func TestAMachineRunsInARootOfItsFilesWithinItsOverhead(t *testing.T) {
	if os.Getenv("SPIN_OVERHEAD_TEST") != "1" {
		t.Skip("set SPIN_OVERHEAD_TEST=1, as root, to take a machine to its ceiling in a root of its files: task boot:overhead")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the root, the TAP and QEMU's cgroup need root: task boot:overhead runs this under sudo")
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
	check, err := os.ReadFile(filepath.Join("testdata", "overhead-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root.write("/overhead-check.sh", string(check))
	root.write("/etc/systemd/system/overhead-check.service", "[Unit]\nAfter=multi-user.target\n[Service]\nType=oneshot\n"+
		"TasksMax=infinity\nExecStart=/bin/sh /overhead-check.sh\nStandardOutput=journal+console\nStandardError=journal+console\n")
	root.debugfs("symlink /etc/systemd/system/multi-user.target.wants/overhead-check.service /etc/systemd/system/overhead-check.service")

	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	spec.BootCPUs, spec.MaxCPUs = 1, 16
	spec.Memory = Memory{BaseMB: 128, CeilingMB: 8192}
	spec.HotplugDisks = 16
	spec.Places = []Place{{Serial: "root"}, {Serial: "home"}}
	spec.VsockCID = 5000 + os.Getpid()%10000
	spec.NICs = []NIC{{TapFD: 3, MAC: "52:54:00:5e:00:01"}}
	c := DefaultCmdline()
	c.Root, c.Init = "/dev/vda", "/sbin/init"
	spec.Cmdline = c
	want, err := spec.Overhead()
	if err != nil {
		t.Fatal(err)
	}

	jail := newJail(t, spec.Files())
	tap := newTAP(t)
	cgroup := filepath.Join("/sys/fs/cgroup", fmt.Sprintf("spin-overhead-test-%d", os.Getpid()))
	if err := os.Mkdir(cgroup, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cgroup) })
	dir, err := os.Open(cgroup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	console := &consoleText{news: make(chan struct{}, 1)}
	p, err := Start(ctx, spec, StartOptions{Files: []*os.File{tap}, Console: console, Cmd: func(c *exec.Cmd) {
		c.Dir = "/"
		c.SysProcAttr = &syscall.SysProcAttr{
			Chroot:      jail,
			Credential:  &syscall.Credential{Uid: 65534, Gid: 65534},
			UseCgroupFD: true, CgroupFD: int(dir.Fd()),
		}
	}})
	if err != nil {
		t.Fatalf("starting spin's shape in a root of %v and /dev/null: %v", spec.Files(), err)
	}
	defer p.Kill()
	rootFile, err := os.OpenFile(root.path, os.O_RDWR, 0) // #nosec G304 -- the root this test made
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootFile.Close() }()
	disks := []Disk{
		{Serial: "root", Images: []Image{{File: rootFile, Format: "raw"}}},
		{Serial: "home", Images: []Image{{File: newRawDisk(t, 256<<20), Format: "raw"}}},
	}
	if err := p.Boot(ctx, disks...); err != nil {
		t.Fatal(err)
	}
	// The first attached disk is 64 GiB, which the guest writes a block of every 512 MiB: one
	// L2 table of the overlay's each, its whole L2 cache (Disk.Overhead).
	for i, size := range []string{"64G", "", "", ""} {
		d := newOverlay(t, out, base, size)
		d.Serial = fmt.Sprintf("ext%d", i)
		disks = append(disks, d)
	}
	for _, d := range disks {
		n, err := d.Overhead()
		if err != nil {
			t.Fatal(err)
		}
		want.Memory += n
	}
	if err := p.SetMemory(ctx, 2048); err != nil {
		t.Fatal(err)
	}

	guest := []int64{int64(spec.Memory.BaseMB) << 20, int64(spec.Memory.CeilingMB-spec.Memory.BaseMB) << 20}
	s := &overheadSampler{cgroup: cgroup, pid: p.Pid(), sizes: guest}
	stop := s.run()
	defer func() {
		stop()
		got := s.result()
		for _, ph := range got.phases {
			t.Logf("%-12s %s", ph.name, ph.worst)
		}
		t.Logf("worst: %s; the guest's RAM at most %d MiB; pids.peak %d, memory.peak %d MiB",
			got.worst, got.guest>>20, got.tasks, got.memoryPeak>>20)
		t.Logf("most tasks: %s", got.most)
		t.Logf("Overhead: %d MiB, %d tasks", want.Memory>>20, want.Tasks)
		if t.Failed() {
			// A reading of a QEMU on its way out is not one of the machine.
			return
		}
		if got.guest < 7<<30 {
			t.Errorf("the guest's RAM was at most %d MiB of QEMU's, not near its ceiling: the check did not fill it", got.guest>>20)
		}
		if over := got.worst.over(); over > want.Memory {
			t.Errorf("QEMU held %d MiB beyond its guest's RAM, over Overhead's %d MiB", over>>20, want.Memory>>20)
		}
		if got.tasks > want.Tasks {
			t.Errorf("QEMU's cgroup held %d tasks, over Overhead's %d", got.tasks, want.Tasks)
		}
	}()
	phase := ""

	for text := console.String(); !strings.Contains(text, "OVERHEAD_CHECK_OK"); text = console.String() {
		if strings.Contains(text, "OVERHEAD_CHECK_FAILED") || ctx.Err() != nil {
			t.Fatalf("the check failed: %v\n%s\nQEMU %s", ctx.Err(), text, p.Kill())
		}
		select {
		case <-console.news:
		case <-p.Exited():
			t.Fatalf("QEMU %s\n%s", p.Wait(), text)
		case <-ctx.Done():
		}
		now := lastPhase(console.String())
		if now == phase {
			continue
		}
		phase = now
		s.enter(now)
		switch now {
		case "shape":
			// Plugged before the checkpoint and not after it: vCPUs plugged right after a
			// checkpoint's Continue never reached the guest in 2 of 5 runs (#433).
			if err := p.SetCPUs(ctx, spec.MaxCPUs); err != nil {
				t.Fatal(err)
			}
			if err := p.SetMemory(ctx, spec.Memory.CeilingMB); err != nil {
				t.Fatal(err)
			}
			for _, d := range disks[2:] {
				if err := p.AttachDisk(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
		case "plugged":
			checkpoint(t, ctx, p)
		}
	}
	s.enter("checkpoint")
	checkpoint(t, ctx, p)
}

// checkpoint checkpoints p to a file of its own, and continues it.
func checkpoint(t *testing.T, ctx context.Context, p *Process) {
	t.Helper()
	state, err := os.Create(filepath.Join(t.TempDir(), "state")) // #nosec G304 -- in the directory this test made
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if _, _, err := p.Checkpoint(ctx, state, SaveOptions{}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("checkpointing: %v", err)
	}
	if err := p.Continue(ctx); err != nil {
		t.Fatal(err)
	}
}

// newJail is a root holding each of files at its own path, copied, and /dev/null.
func newJail(t *testing.T, files []string) string {
	t.Helper()
	jail := t.TempDir()
	if err := os.Chmod(jail, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		dest := filepath.Join(jail, f)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(f, dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(jail, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	null := filepath.Join(jail, "dev", "null")
	if err := unix.Mknod(null, unix.S_IFCHR|0o666, int(unix.Mkdev(1, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(null, 0o666); err != nil {
		t.Fatal(err)
	}
	return jail
}

func copyFile(from, to string) error {
	in, err := os.Open(from) // #nosec G304 -- a file of the release
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	o, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm()|0o444) // #nosec G304 -- in the root this test made
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		_ = o.Close()
		return err
	}
	return o.Close()
}

// newTAP is a TAP device for the machine's NIC, gone when its descriptor is closed.
func newTAP(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var req struct {
		name  [unix.IFNAMSIZ]byte
		flags uint16
		_     [22]byte
	}
	copy(req.name[:], fmt.Sprintf("spinoh%d", os.Getpid()%100000))
	req.flags = unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_VNET_HDR
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.TUNSETIFF, uintptr(unsafe.Pointer(&req))); errno != 0 {
		t.Fatalf("making a TAP: %v", errno)
	}
	return f
}

// newRawDisk is a raw disk of size bytes, all holes, open read-write.
func newRawDisk(t *testing.T, size int64) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "disk.raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return f
}

// newOverlay is a disk of a fresh qcow2 overlay over the release's base, of size when it names one,
// with the base opened read-only: the guest reads the base's data through it and writes into the
// overlay.
func newOverlay(t *testing.T, out, base, size string) Disk {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overlay.qcow2")
	args := []string{"create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", base, path}
	if size != "" {
		args = append(args, size)
	}
	if b, err := exec.Command(filepath.Join(out, "bin", "qemu-img"), args...).CombinedOutput(); err != nil { // #nosec G204 -- the release's qemu-img
		t.Fatalf("making an overlay of %s: %v: %s", base, err, b)
	}
	d, err := OpenDisk(DiskPath{Path: path, Format: "qcow2", Under: []string{base}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// overheadSample is one reading of QEMU and its cgroup. Beyond the guest is QEMU's own anonymous
// memory, outside the mappings of the guest's RAM blocks in its smaps, and the kernel's memory
// charged to the cgroup that the host cannot reclaim (memory.stat's kernel less its reclaimable
// slab, and sock). The cgroup's anon is not used for it: a huge page of the guest's that QEMU
// has given back in part stays charged until the kernel splits it, which it does when the
// cgroup needs the memory, and it read 160 MiB more than smaps just after the guest freed 1.4 GiB.
type overheadSample struct {
	own, guest, anon, kernel, pagetables, secondary int64
	tasks                                           int
	// threads are QEMU's threads by name, and how many of each.
	threads map[string]int
}

func (o overheadSample) over() int64 { return o.own + o.kernel }

func (o overheadSample) String() string {
	return fmt.Sprintf("beyond the guest %4d MiB (QEMU's own %3d, kernel %3d, of it page tables %3d and KVM's %3d; guest %5d, cgroup anon %5d), %3d tasks %v",
		o.over()>>20, o.own>>20, o.kernel>>20, o.pagetables>>20, o.secondary>>20, o.guest>>20, o.anon>>20, o.tasks, o.threads)
}

type overheadPhase struct {
	name  string
	worst overheadSample
}

// overheadSampler reads QEMU's cgroup every 100 ms and keeps the reading with the most beyond the
// guest, overall and in each phase.
type overheadSampler struct {
	cgroup string
	pid    int
	// sizes are the sizes of the guest's RAM blocks, which name their mappings in smaps, and ram
	// the mappings once found.
	sizes []int64
	ram   []vma

	mu     sync.Mutex
	phases []overheadPhase
	worst  overheadSample
	guest  int64
	// most is the reading with the most tasks.
	most overheadSample
	err  error
}

type overheadResult struct {
	phases     []overheadPhase
	worst      overheadSample
	guest      int64
	most       overheadSample
	tasks      int
	memoryPeak int64
}

// enter starts phase name.
func (s *overheadSampler) enter(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phases = append(s.phases, overheadPhase{name: name})
}

func (s *overheadSampler) run() (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			s.sample()
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done); <-finished })
	}
}

func (s *overheadSampler) sample() {
	o, err := s.read()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		return
	}
	if o.over() > s.worst.over() {
		s.worst = o
	}
	s.guest = max(s.guest, o.guest)
	if o.tasks > s.most.tasks {
		s.most = o
	}
	if n := len(s.phases); n > 0 && o.over() > s.phases[n-1].worst.over() {
		s.phases[n-1].worst = o
	}
}

func (s *overheadSampler) read() (overheadSample, error) {
	var o overheadSample
	b, err := os.ReadFile(filepath.Join(s.cgroup, "memory.stat"))
	if err != nil {
		return o, err
	}
	stat := keyed(b)
	// The slab the host may reclaim is mostly the filesystem's for the files QEMU writes: a
	// checkpoint's state brought 30 MiB of it.
	o.anon, o.kernel = stat["anon"], stat["kernel"]-stat["slab_reclaimable"]+stat["sock"]
	o.pagetables, o.secondary = stat["pagetables"], stat["sec_pagetables"]
	if o.guest, o.own, s.ram, err = residents(s.pid, s.sizes, s.ram); err != nil {
		return o, err
	}
	b, err = os.ReadFile(filepath.Join(s.cgroup, "pids.current"))
	if err != nil {
		return o, err
	}
	if o.tasks, err = strconv.Atoi(string(bytes.TrimSpace(b))); err != nil {
		return o, err
	}
	o.threads = threadNames(s.pid)
	return o, nil
}

// threadNames counts pid's threads by name, the digits in a name dropped.
func threadNames(pid int) map[string]int {
	names := map[string]int{}
	tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	for _, task := range tasks {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/comm", pid, task.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimRight(strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return -1
			}
			return r
		}, strings.TrimSpace(string(b))), "-/")
		names[name]++
	}
	return names
}

func (s *overheadSampler) result() overheadResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := overheadResult{phases: s.phases, worst: s.worst, guest: s.guest, most: s.most}
	if b, err := os.ReadFile(filepath.Join(s.cgroup, "pids.peak")); err == nil {
		r.tasks, _ = strconv.Atoi(string(bytes.TrimSpace(b)))
	}
	if b, err := os.ReadFile(filepath.Join(s.cgroup, "memory.peak")); err == nil {
		r.memoryPeak, _ = strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
	}
	if s.err != nil {
		r.phases = append(r.phases, overheadPhase{name: "error: " + s.err.Error()})
	}
	return r
}

// vma is one mapping of a process, as smaps heads it.
type vma struct{ lo, hi uint64 }

// residents are the resident bytes of pid's mappings that lie in ram, the guest's, and the
// anonymous bytes of the rest, QEMU's own; and ram itself once it is found: the guest's RAM blocks,
// the mappings whose sizes are sizes when first read. QEMU splits a block's mapping as it runs, so
// a later read finds it by where it was.
func residents(pid int, sizes []int64, ram []vma) (guest, own int64, _ []vma, _ error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/smaps", pid))
	if err != nil {
		return 0, 0, ram, err
	}
	defer func() { _ = f.Close() }()
	find := len(ram) == 0
	inGuest := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if lo, hi, ok := strings.Cut(fields[0], "-"); ok && len(fields) >= 5 {
			a, err1 := strconv.ParseUint(lo, 16, 64)
			z, err2 := strconv.ParseUint(hi, 16, 64)
			if err1 != nil || err2 != nil {
				return 0, 0, ram, fmt.Errorf("reading the mapping %q of QEMU's smaps", fields[0])
			}
			inGuest = false
			for _, size := range sizes {
				if find && len(fields) == 5 && int64(z-a) == size {
					ram = append(ram, vma{a, z})
				}
			}
			for _, r := range ram {
				inGuest = inGuest || (a >= r.lo && z <= r.hi)
			}
			continue
		}
		if len(fields) != 3 || (fields[0] != "Rss:" && fields[0] != "Anonymous:") {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, 0, ram, err
		}
		switch {
		case inGuest && fields[0] == "Rss:":
			guest += kb << 10
		case !inGuest && fields[0] == "Anonymous:":
			own += kb << 10
		}
	}
	if find && len(ram) != len(sizes) {
		return 0, 0, nil, fmt.Errorf("QEMU's smaps has %d mappings of the sizes of the guest's RAM blocks, %v, not %d", len(ram), sizes, len(sizes))
	}
	return guest, own, ram, sc.Err()
}
