// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// restoreFirst is the goal's second number: a checkpoint restored to the same first login. Each
// tree is booted cold, logged into, saved with `spin-machine save` - which ends it - and later
// restored with `boot --restore` and `spin-machine restore`, and logged into again, from the
// moment before QEMU is exec'd.
//
// Two phases: every cold boot first, then every restore, for Probe.Phases' reason. Each state is
// evicted from the host's page cache before its restore, and the cache is asked whether it was: a
// checkpoint kept for later is read from the disk.
//
// The cached row restores the same way from the page cache, its state read back whole first in
// one sequential pass - which times what the disk gives, read - so that evicted against cached is
// what the disk costs a restore, and read says whether QEMU reads the file as fast as the disk
// can. The fast row is QEMU 11.2's fast snapshot load (mapped-ram and postcopy-ram), where the
// guest runs once the device state is in; it needs userfaultfd for QEMU's user, and was slower to
// the first login on the bench fleet, 293 ms against 152 (lab 44585840498).
//
// The shared rows keep the base memory in a file beside the state (spin-machine boot
// --memory-file), which the save leaves out (x-ignore-shared) and the restore maps back: the host
// kernel serves the guest's pages from that file instead of QEMU copying them in (#276). shared
// restores with the file evicted, as a host that kept it on disk would; shared, cached with it read
// back first.
type restoreFirst struct{}

func init() { register(restoreFirst{}) }

func (restoreFirst) Name() string { return "restore-first" }
func (restoreFirst) About() string {
	return "ms from the moment before QEMU is exec'd to the first SSH login, booted cold and restored from a saved state"
}

func (restoreFirst) Options() []Option {
	return append(shape,
		Option{"fast", "false", "also restore each tree with the fast snapshot load (--fast)"},
		Option{"shared", "false", "also save and restore each tree with its base memory in a shared file (--memory-file)"},
		Option{"memory-dir", "", "where the shared rows' memory files go: the measurement's directory on the scratch disk, or e.g. /dev/shm"})
}

func (restoreFirst) Rows(o Options) []string {
	rows := []string{"evicted", "cached"}
	if o.Bool("fast") {
		rows = append(rows, "fast")
	}
	if o.Bool("shared") {
		rows = append(rows, "shared", "shared, cached")
	}
	return rows
}

func (restoreFirst) Phases() int { return 2 }

func (restoreFirst) Needs(o Options) []Need {
	needs := append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "restore"},
		Need{"save", "to"}, Need{"restore", "disk"})
	if o.Bool("fast") {
		needs = append(needs, Need{"restore", "fast"})
	}
	if o.Bool("shared") {
		needs = append(needs, Need{"boot", "memory-file"})
	}
	return needs
}

func (restoreFirst) Metrics() []Metric {
	return []Metric{
		{"state", "MiB", "the saved state's blocks on the disk (a mapped-ram state's length is its RAM ceiling)"},
		{"cold_login", "ms", "the cold boot's first login"},
		{"save", "ms", "`spin-machine save`, which ends the machine"},
		{"read", "ms", "the state read whole, sequentially, from the disk (the cached row)"},
		{"up", "ms", "the restoring QEMU's monitor socket open"},
		{"running", "ms", "`spin-machine restore` returned: the guest is running"},
		{"login", "ms", "the restored guest's first login"},
	}
}

// mutate-lane: TestEveryProbeMeasuresATree
func (restoreFirst) Measure(_ context.Context, m *Measurement) error {
	if m.Phase == 0 {
		return coldSave(m)
	}
	return restore(m, m.Row == "cached" || m.Row == "shared, cached", m.Row == "fast")
}

// restore resumes m's saved state - evicted from the page cache, or read back into it first when
// cached - into a QEMU exec'd for it, and logs in.
// mutate-lane: TestEveryProbeMeasuresATree
func restore(m *Measurement, cached, fast bool) error {
	c := m.State.(*checkpoint)
	var read time.Duration
	for _, f := range c.files() {
		if err := evict(f); err != nil {
			return err
		}
		if cached {
			d, err := readWhole(f)
			if err != nil {
				return err
			}
			read += d
		}
	}
	if cached {
		m.RecordMs("read", read)
	}
	qmp := filepath.Join(m.Socks, "restored.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(c.overlay, c.bootFlags(m, qmp, "--restore")...)...)
	if err != nil {
		return err
	}
	defer mc.Stop() // a restored guest left running would be what the restores after it measured
	if err := waitSocket(qmp, t0.Add(30*time.Second)); err != nil {
		return Miss("the restoring QEMU never opened its monitor: %v; spin-machine: %s", err, strings.TrimSpace(mc.Stderr.String()))
	}
	m.RecordMs("up", time.Since(t0))
	args := []string{"restore", "--release", m.Tree.Dir, "--qmp", qmp, "--from", c.state, "--disk", c.overlay}
	if fast {
		if err := userfaultfd(); err != nil {
			return err
		}
		args = append(args, "--fast")
	}
	if err := m.SpinMachine(args...); err != nil {
		return err
	}
	m.RecordMs("running", time.Since(t0))
	login, err := m.Key.LoggedIn(mc, c.cid, t0)
	if err != nil {
		return err
	}
	m.RecordMs("login", login)
	m.Logf("restored: running %v, login %v", time.Duration(m.values["running"]*1e6).Round(time.Millisecond), login.Round(time.Millisecond))
	return nil
}

// checkpoint is a machine booted cold, logged into and saved: what a restore resumes.
type checkpoint struct {
	overlay, state string
	// memory is the shared file the base memory lives in, for a shared row; empty otherwise.
	memory string
	cid    uint32
}

// files are what a restore reads from the disk: the state, and the memory file when there is one
// and it is on a disk. A memory file on tmpfs is the memory itself, which no eviction can drop, so
// it is neither evicted nor read back.
func (c *checkpoint) files() []string {
	if c.memory == "" || isTmpfs(filepath.Dir(c.memory)) {
		return []string{c.state}
	}
	return []string{c.state, c.memory}
}

// bootFlags are the flags of a boot of m's tree for c: its vsock, monitor, console, command line
// and memory file, the same for the boot that saves and the one that restores, and the given ones
// after them.
func (c *checkpoint) bootFlags(m *Measurement, qmp string, extra ...string) []string {
	flags := []string{"--vsock-cid", fmt.Sprint(c.cid), "--qmp", qmp, "--console", "file:" + qmp + ".console",
		"--append", m.Tree.Cmdline()}
	if c.memory != "" {
		flags = append(flags, "--memory-file", c.memory)
	}
	return append(flags, extra...)
}

// coldSave boots a fresh overlay of the tree, logs in, saves - which ends the machine - and evicts
// the state the save has just put in the page cache. It is m.State from here on.
// mutate-lane: TestEveryProbeMeasuresATree
func coldSave(m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edit())
	if err != nil {
		return err
	}
	c := &checkpoint{overlay: overlay, state: filepath.Join(m.Dir, "state"), cid: NewCID()}
	if strings.HasPrefix(m.Row, "shared") {
		dir := m.Dir
		if d := m.Options["memory-dir"]; d != "" {
			// A directory of its own, so the experiment's cleanup removes what it filled.
			if dir, err = os.MkdirTemp(d, "memory"); err != nil {
				return fmt.Errorf("making a directory for the memory file under %s: %w", d, err)
			}
			m.Cleanup(removeAll(dir))
		}
		c.memory = filepath.Join(dir, "memory")
	}
	qmp := filepath.Join(m.Socks, "cold.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(overlay, c.bootFlags(m, qmp)...)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	login, err := m.Key.LoggedIn(mc, c.cid, t0)
	if err != nil {
		return err
	}
	m.RecordMs("cold_login", login)
	t0 = time.Now()
	if err := m.SpinMachine("save", "--qmp", qmp, "--to", c.state); err != nil {
		return err
	}
	m.RecordMs("save", time.Since(t0))
	select { // save quits QEMU, and the restore opens the same overlay
	case <-mc.Done():
	case <-time.After(30 * time.Second):
		return Miss("QEMU was still running 30s after its state was saved")
	}
	for _, f := range c.files() {
		if err := evict(f); err != nil {
			return err
		}
	}
	st, err := os.Stat(c.state)
	if err != nil {
		return fmt.Errorf("reading the saved state: %w", err)
	}
	// The blocks on the disk and not the length: a mapped-ram state is as long as the machine's
	// RAM at its ceilings, and what was never written is holes.
	m.Record("state", float64(st.Sys().(*syscall.Stat_t).Blocks*512)/(1<<20))
	m.State = c
	m.Logf("cold login %v, saved in %v", login.Round(time.Millisecond), time.Duration(m.values["save"]*1e6).Round(time.Millisecond))
	return nil
}

// userfaultfd makes sure an unprivileged QEMU can have one, which the fast load serves the guest's
// faults with. On a bench runner, a throwaway host, it is turned on; anywhere else it is asked for.
// mutate-lane: TestEveryProbeMeasuresATree
func userfaultfd() error {
	const knob = "/proc/sys/vm/unprivileged_userfaultfd"
	if b, err := os.ReadFile(knob); err == nil && strings.TrimSpace(string(b)) == "1" {
		return nil
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return fmt.Errorf("the fast load needs userfaultfd for QEMU's user: sudo sysctl -w vm.unprivileged_userfaultfd=1")
	}
	return run("sudo", "sysctl", "-q", "-w", "vm.unprivileged_userfaultfd=1")
}

// waitSocket returns once something listens on the Unix socket path.
func waitSocket(path string, deadline time.Time) error {
	for {
		c, err := net.Dial("unix", path)
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

// readWhole reads path once, start to end, in 1 MiB reads, and says how long that took: what the
// disk gives a sequential reader, and the state in the page cache after it.
func readWhole(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	t0 := time.Now()
	if _, err := io.CopyBuffer(io.Discard, f, make([]byte, 1<<20)); err != nil { // mutate-exempt: the size of each read; any size reads the whole file once
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	return time.Since(t0), nil
}

// evict drops path from the host's page cache and asks the cache whether it did: a restore of a
// state still in memory measures the cache and not the disk, and fadvise says nothing about what
// it dropped.
// mutate-lane: TestEveryProbeMeasuresATree
func evict(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd())
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED); err != nil {
		return fmt.Errorf("evicting %s from the page cache: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	mm, err := unix.Mmap(fd, 0, int(st.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mapping %s: %w", path, err)
	}
	defer func() { _ = unix.Munmap(mm) }()
	page := os.Getpagesize()
	vec := make([]byte, (len(mm)+page-1)/page)
	// x/sys has no wrapper for mincore(2).
	if _, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&mm[0])), uintptr(len(mm)), uintptr(unsafe.Pointer(&vec[0]))); errno != 0 {
		return fmt.Errorf("asking which pages of %s are cached: %w", path, errno)
	}
	cached := 0
	for _, v := range vec {
		cached += int(v & 1)
	}
	if cached > 0 {
		return fmt.Errorf("%s: %d KiB still in the page cache after evicting it", path, cached*page/1024)
	}
	return nil
}

// settled returns once the machine on qmp has been created and is waiting in state: its monitor
// has answered query-status, which the main loop runs, and the main loop runs only once QEMU's
// start is over. The greeting alone says less - QMP's I/O thread can send it earlier.
// mutate-lane: TestEveryProbeMeasuresATree
func settled(qmp, state string) error {
	deadline := time.Now().Add(30 * time.Second)
	if err := waitSocket(qmp, deadline); err != nil {
		return Miss("the held QEMU never opened its monitor %s: %v", qmp, err)
	}
	c, err := net.Dial("unix", qmp)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", qmp, err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(deadline)
	in := bufio.NewScanner(c)
	in.Scan() // the greeting
	for _, cmd := range []string{"qmp_capabilities", "query-status"} {
		if _, err := fmt.Fprintf(c, `{"execute": %q}`+"\n", cmd); err != nil {
			return fmt.Errorf("asking the held QEMU %s: %w", cmd, err)
		}
		for in.Scan() && strings.Contains(in.Text(), `"event"`) {
		}
	}
	var status struct {
		Return struct {
			Status string `json:"status"`
		} `json:"return"`
	}
	if err := json.Unmarshal(in.Bytes(), &status); err != nil || status.Return.Status != state {
		return fmt.Errorf("the held QEMU answered %q (%v), want status %s", in.Text(), err, state)
	}
	// What a pool's machine would have had: a moment idle after its start, not the instant.
	time.Sleep(100 * time.Millisecond)
	return nil
}
