// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestAFastLoadReadingAColdDiskKeepsRunning is the lane of qemu/patches/0011 (spin-stack/spin#1747):
// a guest that reads its disk all the time is checkpointed and loaded fast, again and again, with
// the host's page cache dropped before each load, and it keeps reading and its state is all read
// each time. A read that misses the host's page cache completes in io_uring task work on QEMU's
// main thread, wherever that thread is, and copies into guest pages the load has not placed yet.
// When the main thread was inside malloc, and the threads that place pages allocated, it waited
// for them while they waited for its lock, and the guest stopped for good. Root, for the
// userfaultfd device and the cache: task boot:fastload-reads.
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
	// then reads the second disk through its page cache, emptied before each pass, so every pass
	// reads it from QEMU into pages the guest has just taken from its free lists.
	root.write("/reads.sh", "#!/bin/sh\nmount -t proc proc /proc\n"+
		"mount -t tmpfs -o size=1800m fill /tmp && head -c 1700M /dev/zero | tr '\\0' '\\377' > /tmp/f && echo FILLED\n"+
		"n=0; while :; do n=$((n+1)); echo 3 > /proc/sys/vm/drop_caches; cat /dev/vdb > /dev/null; echo READ-$n; done\n")
	root.debugfs("sif /reads.sh mode 0100755")
	data := filepath.Join(t.TempDir(), "data.raw")
	writeRandom(t, data, 512<<20)

	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	spec.BootCPUs, spec.Memory = 2, Memory{BaseMB: 2560}
	spec.Places = []Place{{Serial: "root"}, {Serial: "data"}}
	c := DefaultCmdline()
	c.Root, c.Init = "/dev/vda", "/reads.sh"
	spec.Cmdline = c
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(rounds)*4*time.Minute)
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
	start := func(s Spec) (*Process, *consoleText) {
		t.Helper()
		console := &consoleText{news: make(chan struct{}, 1)}
		p, err := Start(ctx, s, StartOptions{Console: console})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Kill() })
		return p, console
	}
	read := regexp.MustCompile(`READ-(\d+)`)
	// reads waits up to within for the console to show passes beyond the ones it showed
	// before, and is false if they never came.
	reads := func(p *Process, console *consoleText, passes int, within time.Duration) bool {
		t.Helper()
		limit := time.After(within)
		for len(read.FindAllString(console.String(), -1)) < passes {
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

	p, console := start(spec)
	if err := p.Boot(ctx, disks()...); err != nil {
		t.Fatal(err)
	}
	if !reads(p, console, 2, 3*time.Minute) {
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
		r, rconsole := start(spec)
		here, err := r.Target(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var phases []string
		load := LoadOptions{Phases: func(ph Phase) { phases = append(phases, fmt.Sprintf("%T", ph)) }}
		if err := r.Resume(ctx, state, saved, here, load, disks()...); err != nil {
			t.Fatalf("round %d: resuming: %v", round, err)
		}
		loaded := make(chan error, 1)
		go func() {
			lctx, lcancel := context.WithTimeout(ctx, 2*time.Minute)
			defer lcancel()
			loaded <- r.Loaded(lctx)
		}()
		if !reads(r, rconsole, 2, time.Minute) {
			t.Fatalf("round %d: the guest stopped reading within a minute of its fast load\n%s\nconsole:\n%s", round, threads(r.Pid()), rconsole.String())
		}
		if err := <-loaded; err != nil {
			t.Fatalf("round %d: loading: %v\n%s", round, err, threads(r.Pid()))
		}
		if got := strings.Join(phases, " "); got != "machine.LoadStarted machine.GuestRunning machine.StateLoaded" {
			t.Fatalf("round %d: phases %s: want the guest running before its state is loaded, a fast load", round, got)
		}
		t.Logf("round %d: a fast load of %d MiB, the guest reading throughout", round, written(state)>>20)
		_ = state.Close()
		p = r
	}
}

// uncache writes path's dirty pages and drops all of it from the host's page cache, so that what
// QEMU reads of it next comes from the disk, through io_uring's retry from task work.
func uncache(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- a file this test made
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Fdatasync(int(f.Fd())); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
		t.Fatal(err)
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
