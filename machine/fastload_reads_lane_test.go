// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestAFastLoadReadingAColdDiskKeepsRunning is the lane of qemu/patches/0011 (spin-stack/spin#1747):
// a guest that reads its disk all the time is checkpointed and loaded fast, again and again, and
// each time it reads on and its state is read to the end.
//
// A read that misses the host's page cache completes in io_uring task work on QEMU's main thread,
// at whatever instruction that thread had reached, and copies into guest pages the load has not
// placed yet. When the main thread was inside malloc and the threads that place pages allocated,
// it waited for them while they waited for its lock, and the guest stopped for good. So each load
// is made long and its reads cold: the restored QEMU reads its disk through io.max, the guest's
// disk leaves the host's cache every 100 ms, and the main thread answers query-blockstats all
// the while, as spin's runner asks it, which allocates. Root, for the userfaultfd device, the
// cgroup and the cache: task boot:fastload-reads.
func TestAFastLoadReadingAColdDiskKeepsRunning(t *testing.T) {
	if os.Getenv("SPIN_FASTLOAD_READS_TEST") != "1" {
		t.Skip("set SPIN_FASTLOAD_READS_TEST=1, as root, to load a reading guest fast again and again: task boot:fastload-reads")
	}
	if os.Geteuid() != 0 {
		t.Fatal("opening /dev/userfaultfd needs root: task boot:fastload-reads runs this under sudo")
	}
	rounds, err := strconv.Atoi(cmp.Or(os.Getenv("SPIN_FASTLOAD_READS_ROUNDS"), "10"))
	if err != nil || rounds < 1 {
		t.Fatalf("SPIN_FASTLOAD_READS_ROUNDS=%q: %v", os.Getenv("SPIN_FASTLOAD_READS_ROUNDS"), err)
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
	// PID 1 fills 1700 MiB of a tmpfs, so the state carries more than fastAbove of data, and
	// then reads the second disk in four places at once through its page cache, emptied before
	// each pass, so that every pass reads it from QEMU into pages just taken from the free lists.
	root.write("/reads.sh", "#!/bin/sh\nmount -t proc proc /proc\n"+
		"mount -t tmpfs -o size=1800m fill /tmp && head -c 1700M /dev/zero | tr '\\0' '\\377' > /tmp/f && echo FILLED\n"+
		"n=0; while :; do n=$((n+1)); echo 3 > /proc/sys/vm/drop_caches\n"+
		"for q in 0 1 2 3; do dd if=/dev/vdb of=/dev/null bs=64k skip=$((q*1024)) count=1024 2>/dev/null & done; wait; echo READ-$n; done\n")
	root.debugfs("sif /reads.sh mode 0100755")
	data := filepath.Join(t.TempDir(), "data.raw")
	writeRandom(t, data, 256<<20)
	cgroup := throttled(t, data, cmp.Or(os.Getenv("SPIN_FASTLOAD_READS_RBPS"), "67108864"))

	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	spec.BootCPUs, spec.Memory = 2, Memory{BaseMB: 2560}
	spec.Places = []Place{{Serial: "root"}, {Serial: "data"}}
	c := DefaultCmdline()
	c.Root, c.Init = "/dev/vda", "/reads.sh"
	spec.Cmdline = c
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(rounds)*5*time.Minute)
	defer cancel()

	open := func(path string) *os.File {
		t.Helper()
		f, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- a file this test made
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	disks := func() []Disk {
		return []Disk{
			{Serial: "root", Images: []Image{{File: open(root.path), Format: "raw"}}},
			{Serial: "data", Images: []Image{{File: open(data), Format: "raw"}}},
		}
	}
	start := func(s Spec, cg *os.File) (*Process, *consoleText) {
		t.Helper()
		console := &consoleText{news: make(chan struct{}, 1)}
		opts := StartOptions{Console: console}
		if cg != nil {
			opts.Cmd = func(c *exec.Cmd) {
				c.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.Fd())}
			}
		}
		p, err := Start(ctx, s, opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Kill() })
		return p, console
	}
	read := regexp.MustCompile(`READ-(\d+)`)
	passes := func(console *consoleText) int { return len(read.FindAllString(console.String(), -1)) }
	// reads waits up to within for the console to show more than seen passes, and is false if
	// none came.
	reads := func(p *Process, console *consoleText, seen int, within time.Duration) bool {
		t.Helper()
		limit := time.After(within)
		for passes(console) <= seen {
			select {
			case <-console.news:
			case <-p.Exited():
				t.Fatalf("QEMU %s\n%s", p.Wait(), console.String())
			case <-limit:
				return false
			case <-ctx.Done():
				return false
			}
		}
		return true
	}

	p, console := start(spec, nil)
	if err := p.Boot(ctx, disks()...); err != nil {
		t.Fatal(err)
	}
	if !reads(p, console, 1, 3*time.Minute) {
		t.Fatalf("the guest did not fill and read its disk twice:\n%s", console.String())
	}
	spec.Restore = true
	for round := 1; round <= rounds; round++ {
		state, err := os.Create(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		saved, _, err := p.Checkpoint(ctx, state, SaveOptions{}, func(context.Context) error { return nil })
		if err != nil {
			t.Fatalf("round %d: checkpointing: %v", round, err)
		}
		p.Kill()
		if n := written(state); n <= fastAbove {
			t.Fatalf("round %d: the state holds %d MiB of data, not over fastAbove's %d: it would load in full", round, n>>20, fastAbove>>20)
		}
		for _, f := range []string{state.Name(), data, root.path} {
			uncache(t, f)
		}
		r, rconsole := start(spec, cgroup)
		here, err := r.Target(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var phases []string
		load := LoadOptions{Phases: func(ph Phase) { phases = append(phases, fmt.Sprintf("%T", ph)) }}
		resumed := time.Now()
		if err := r.Resume(ctx, state, saved, here, load, disks()...); err != nil {
			t.Fatalf("round %d: resuming: %v", round, err)
		}
		loading, stop := context.WithCancel(ctx)
		asked := make(chan int, 1)
		go func() {
			n := 0
			for loading.Err() == nil {
				if _, err := r.DiskUsage(loading); err == nil {
					n++
				}
			}
			asked <- n
		}()
		go func() {
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-loading.Done():
					return
				case <-tick.C:
					uncache(t, data)
				}
			}
		}()
		lctx, lcancel := context.WithTimeout(ctx, 3*time.Minute)
		err = r.Loaded(lctx)
		lcancel()
		took := time.Since(resumed)
		stop()
		if err != nil {
			t.Fatalf("round %d: the state was not read to its end within 3 minutes of the resume: %v\n%s\nconsole:\n%s", round, err, threads(r.Pid()), rconsole.String())
		}
		during := passes(rconsole)
		if !reads(r, rconsole, during, 2*time.Minute) {
			t.Fatalf("round %d: the guest read nothing for 2 minutes after its load\n%s\nconsole:\n%s", round, threads(r.Pid()), rconsole.String())
		}
		if got := strings.Join(phases, " "); got != "machine.LoadStarted machine.GuestRunning machine.StateLoaded" {
			t.Fatalf("round %d: phases %s: want the guest running before its state is loaded, a fast load", round, got)
		}
		t.Logf("round %d: a fast load of %d MiB read in %v, %d passes of the guest's reads and %d query-blockstats meanwhile",
			round, written(state)>>20, took.Round(time.Millisecond), during, <-asked)
		_ = state.Close()
		p = r
	}
}

// throttled is a cgroup whose processes read the block device path is on at no more than rbps
// bytes a second, open for exec.Cmd's CgroupFD.
func throttled(t *testing.T, path, rbps string) *os.File {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	dev := fmt.Sprintf("%d:%d", unix.Major(st.Dev), unix.Minor(st.Dev))
	sys := "/sys/dev/block/" + dev
	if _, err := os.Stat(sys); err != nil {
		t.Fatalf("%s is on device %s, which is no block device: TMPDIR must be on one (task boot:fastload-reads, SCRATCH)", path, dev)
	}
	// io.max takes a whole disk, and a partition's disk is its parent.
	if _, err := os.Stat(filepath.Join(sys, "partition")); err == nil {
		b, err := os.ReadFile(sys + "/../dev")
		if err != nil {
			t.Fatal(err)
		}
		dev = strings.TrimSpace(string(b))
	}
	cgroup := filepath.Join("/sys/fs/cgroup", fmt.Sprintf("spin-fastload-reads-%d", os.Getpid()))
	if err := os.Mkdir(cgroup, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cgroup) })
	if err := os.WriteFile(filepath.Join(cgroup, "io.max"), []byte(dev+" rbps="+rbps), 0); err != nil {
		t.Fatalf("throttling %s's reads in %s: %v", dev, cgroup, err)
	}
	dir, err := os.Open(cgroup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

// uncache writes path's dirty pages and drops all of it from the host's page cache, so that what
// QEMU reads of it next comes from the disk, through io_uring's retry from task work.
func uncache(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- a file this test made
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := unix.Fdatasync(int(f.Fd())); err != nil {
		t.Error(err)
	}
	if err := unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
		t.Error(err)
	}
}

// writeRandom writes size random bytes to path.
func writeRandom(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path) // #nosec G304 -- a file this test makes
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.CopyN(f, rand.Reader, size); err != nil {
		t.Fatal(err)
	}
}

// threads is each of process pid's threads as the kernel sees it: its name, state, wait channel,
// system call and kernel stack, which name what a stopped QEMU's threads wait on.
func threads(pid int) string {
	var b strings.Builder
	dirs, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*", pid))
	for _, d := range dirs {
		read := func(name string) string {
			v, _ := os.ReadFile(filepath.Join(d, name)) // #nosec G304 -- procfs
			return strings.TrimSpace(string(v))
		}
		state := ""
		for line := range strings.SplitSeq(read("status"), "\n") {
			if v, ok := strings.CutPrefix(line, "State:"); ok {
				state = strings.TrimSpace(v)
			}
		}
		fmt.Fprintf(&b, "=== %s %s state=%s wchan=%s\nsyscall: %s\n%s\n", filepath.Base(d), read("comm"), state, read("wchan"), read("syscall"), read("stack"))
	}
	return b.String()
}
