// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// restoreFirst is the goal's second number: a checkpoint restored to the same first login. Each
// tree is booted cold, logged into, left to finish its boot (bootFinished), saved with
// `spin-machine save` - which ends it - and later
// restored with `boot --restore` and `spin-machine restore`, and logged into again, from the
// moment before QEMU is exec'd.
//
// Two phases: every cold boot first, then every restore, for Probe.Phases' reason. Each state is
// evicted from the host's page cache before its restore, and the cache is asked whether it was: a
// checkpoint kept for later is read from the disk.
//
// The cached row restores the same way from the page cache, the state's data read back first in
// one sequential pass - which times what the disk gives, read - so that evicted against cached is
// what the disk costs a restore, and read says whether QEMU reads the file as fast as the disk
// can. Its data and not the whole file: a mapped-ram state is as long as the machine's RAM at its
// ceilings, holes but for what the guest wrote, and QEMU reads only the data. Read whole, a 2 GiB
// state with 155 MiB of data took 422-675 ms against 106-112 for its data (a laptop, 2026-11-20),
// filled the cache with pages of zeros, and made the cached row slower than the evicted one in
// every ladder cell.
//
// The machine loads a state over machine.fastAbove fast where the host grants QEMU a userfaultfd,
// which a bench runner does (userfaultfd), so touched over it measures the fast load.
type restoreFirst struct{}

func (restoreFirst) Name() string { return "restore-first" }
func (restoreFirst) About() string {
	return "ms from the moment before QEMU is exec'd to the first SSH login, booted cold and restored from a saved state"
}

func (restoreFirst) Options() []Option {
	return append(shape, channelsOption,
		Option{"touched", "0", "MiB the guest writes into a tmpfs after its login and before its save, as root (login=spin): memory its checkpoint carries, as a workspace's that has run a while does"})
}

func (restoreFirst) Rows(o Options) []string {
	return withChannels(o, "evicted", "cached")
}

func (restoreFirst) Phases() int { return 2 }

func (restoreFirst) Needs(o Options) []Need {
	needs := append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "restore"},
		Need{"save", "to"}, Need{"restore", "disk"})
	return append(append(needs, channelsNeeds(o)...), plugNeeds(o)...)
}

func (restoreFirst) Metrics() []Metric {
	return []Metric{
		{"state", "MiB", "the saved state's blocks on the disk (a mapped-ram state's length is its RAM ceiling)"},
		{"cold_login", "ms", "the cold boot's first login"},
		{"finished", "ms", "the cold boot's guest saying its boot is over (bootFinished), from the moment before QEMU is exec'd"},
		{"save", "ms", "`spin-machine save`, which ends the machine"},
		{"read", "ms", "the state's data read, sequentially, from the disk (the cached row)"},
		{"listening", "ms", "the restoring QEMU's monitor accepting a connection: its chardevs made, its machine not yet"},
		{"running", "ms", "`spin-machine restore` returned: the guest is running"},
		{"login", "ms", "the restored guest's first login"},
	}
}

// mutate-lane: TestEveryProbeMeasuresATree
func (restoreFirst) Measure(_ context.Context, m *Measurement) error {
	way, channels, err := rowChannels(m.Row)
	if err != nil {
		return err
	}
	if m.Phase == 0 {
		return coldSave(m, channels...)
	}
	return restore(m, way == "cached", channels...)
}

// restore resumes m's saved state - evicted from the page cache, or read back into it first when
// cached - into a QEMU exec'd for it, and logs in. channels are restore's --channels, when the
// row names them.
// mutate-lane: TestEveryProbeMeasuresATree
func restore(m *Measurement, cached bool, channels ...string) error {
	c := m.State.(*checkpoint)
	if err := userfaultfd(); err != nil {
		return err
	}
	if err := evict(c.state); err != nil {
		return err
	}
	if cached {
		d, _, err := readData(c.state)
		if err != nil {
			return err
		}
		m.RecordMs("read", d)
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
	// When the socket listens and not when QEMU is ready: asking the monitor whether the machine is
	// made (settled) before `spin-machine restore` starts would put that exchange, and the restore's
	// own start, after QEMU's instead of beside it, in the login this probe measures. What QEMU's
	// whole start costs a restore is prestart's restored row against its restored-into-prestarted.
	m.RecordMs("listening", time.Since(t0))
	if err := m.SpinMachine(c.restoreArgs(m, qmp, channels...)...); err != nil {
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
	cid            uint32
}

// bootFlags are the flags of a boot of m's tree for c: its vsock, monitor, console and command
// line, the same for the boot that saves and the one that restores, and the given ones after them.
func (c *checkpoint) bootFlags(m *Measurement, qmp string, extra ...string) []string {
	return append([]string{"--vsock-cid", fmt.Sprint(c.cid), "--qmp", qmp, "--console", "file:" + qmp + ".console",
		"--append", m.Tree.Cmdline(m.Key.Init())}, extra...)
}

// restoreArgs are `spin-machine restore`'s arguments for c into the machine on qmp: its state and
// overlay, the tree's record when the experiment read one (Measurement.Target), and the given
// flags after them.
func (c *checkpoint) restoreArgs(m *Measurement, qmp string, more ...string) []string {
	args := []string{"restore", "--release", m.Tree.Dir, "--qmp", qmp, "--from", c.state, "--disk", c.overlay}
	if m.Target != "" {
		args = append(args, "--target", m.Target)
	}
	return append(args, more...)
}

// targetNeeds are what reading a tree's record and handing it to a restore run. A spin-machine
// from before 2026-11-21 has neither.
var targetNeeds = []Need{{"target", "qmp"}, {"restore", "target"}}

// readTarget boots a machine of m's tree held for a checkpoint, as a restore boots it - the
// experiment's shape, the same flags - asks it for its record with `spin-machine target`, ends
// it, and returns the file it wrote the record to: spin reads its host's once, when its runner
// starts, and hands it to every resume (machine.QEMU.Resume). It is "" for a tree whose
// spin-machine cannot print the record or take one, and boots nothing.
func readTarget(m *Measurement) (string, error) {
	if lacks(m.Tree, targetNeeds) != nil {
		return "", nil
	}
	// boot --restore binds no disk - restore does - so the overlay is named and never opened.
	c := &checkpoint{overlay: filepath.Join(m.Dir, "overlay.qcow2"), cid: NewCID()}
	qmp := filepath.Join(m.Socks, "record.qmp")
	mc, err := m.Launch(nil, m.BootArgs(c.overlay, c.bootFlags(m, qmp, "--restore")...)...)
	if err != nil {
		return "", err
	}
	defer mc.Stop()
	// Until the monitor is there, or the machine is gone saying why: a refusal is not waited out.
	for deadline := time.Now().Add(30 * time.Second); !fileExists(qmp); time.Sleep(5 * time.Millisecond) {
		if err := mc.Exited(); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the machine held to read the record opened no monitor in 30s: %s", strings.TrimSpace(mc.Stderr.String()))
		}
	}
	cmd := exec.Command(m.Tree.SpinMachine(), "target", "--qmp", qmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("spin-machine target: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	path := filepath.Join(m.Dir, "record.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", fmt.Errorf("keeping the record: %w", err)
	}
	return path, nil
}

// coldSave boots a fresh overlay of the tree, logs in, saves - which ends the machine - and evicts
// the state the save has just put in the page cache. It is m.State from here on. channels are
// save's --channels, when the row names them.
// mutate-lane: TestEveryProbeMeasuresATree
func coldSave(m *Measurement, channels ...string) error {
	c, err := saveCold(m, "", channels...)
	if err != nil {
		return err
	}
	m.RecordMs("cold_login", c.login)
	m.RecordMs("finished", c.finished)
	m.RecordMs("save", c.save)
	m.Record("state", c.stateMiB)
	m.State = c.checkpoint
	m.Logf("cold login %v, saved in %v", c.login.Round(time.Millisecond), c.save.Round(time.Millisecond))
	return nil
}

// saved is a checkpoint and what making it took.
type saved struct {
	*checkpoint
	login, finished, save time.Duration
	// stateMiB is the state's blocks on the disk and not its length: a mapped-ram state is as long
	// as the machine's RAM at its ceilings, and what was never written is holes.
	stateMiB float64
}

// saveCold is coldSave's checkpoint with nothing recorded, its files in m.Dir and its sockets in
// m.Socks named after prefix, so that one measurement can make several.
// mutate-lane: TestEveryProbeMeasuresATree
func saveCold(m *Measurement, prefix string, channels ...string) (*saved, error) {
	overlay, err := m.Overlay(prefix+"overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return nil, err
	}
	c := &saved{checkpoint: &checkpoint{overlay: overlay, state: filepath.Join(m.Dir, prefix+"state"), cid: NewCID()}}
	qmp := filepath.Join(m.Socks, prefix+"cold.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(overlay, c.bootFlags(m, qmp)...)...)
	if err != nil {
		return nil, err
	}
	defer mc.Stop()
	plugged := m.Plug(qmp)
	if c.login, err = m.Key.LoggedIn(mc, c.cid, t0); err != nil {
		return nil, err
	}
	if err := plugged(); err != nil {
		return nil, err
	}
	if err := bootFinished(m, c.cid); err != nil {
		return nil, err
	}
	c.finished = time.Since(t0)
	if err := touch(m, c.cid); err != nil {
		return nil, err
	}
	t0 = time.Now()
	if err := m.SpinMachine(append([]string{"save", "--qmp", qmp, "--to", c.state}, channels...)...); err != nil {
		return nil, err
	}
	c.save = time.Since(t0)
	select { // save quits QEMU, and the restore opens the same overlay
	case <-mc.Done():
	case <-time.After(30 * time.Second):
		return nil, Miss("QEMU was still running 30s after its state was saved")
	}
	if err := evict(c.state); err != nil {
		return nil, err
	}
	st, err := os.Stat(c.state)
	if err != nil {
		return nil, fmt.Errorf("reading the saved state: %w", err)
	}
	c.stateMiB = float64(st.Sys().(*syscall.Stat_t).Blocks*512) / (1 << 20)
	return c, nil
}

// finishedCommand returns once the guest's boot is over, as docs/measuring.md ("When a checkpoint
// is taken") defines it: the system manager out of its start, multi-user.target active and no job
// queued, and the logging-in user's manager out of its own start. multi-user.target and the empty
// queue are there because is-system-running alone can say running too soon: a unit that starts
// part of the boot once the first jobs are done (#581's Type=idle one) finds a queue that was
// empty for a moment, and the manager has called its start finished by then.
const finishedCommand = `timeout 60 sh -c '
s=$(systemctl is-system-running --wait)
case $s in running|degraded) ;; *) echo "the system manager is $s"; exit 1;; esac
until systemctl is-active -q multi-user.target && [ -z "$(systemctl list-jobs --no-legend)" ]; do sleep 0.02; done
s=$(systemctl --user is-system-running --wait)
case $s in running|degraded) ;; *) echo "the user manager is $s"; exit 1;; esac
' || { systemctl list-jobs --no-pager; exit 1; }`

// bootFinished returns once the guest on cid has finished its boot. A probe that saves waits for
// it first: spin checkpoints an empty workspace, a project's or a prebuild long after its boot,
// never while the boot is still running (#578), and a state saved mid-boot carries the boot's
// remaining jobs into every restore of it.
// mutate-lane: TestEveryProbeMeasuresATree
func bootFinished(m *Measurement, cid uint32) error {
	if _, _, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, finishedCommand); err != nil {
		return Miss("the guest's boot did not finish: %v", err)
	}
	return nil
}

// touch writes the touched option's MiB into a tmpfs in the guest on cid, which its checkpoint then
// carries. The bytes are not zeros: a save writes no page of zeros, and a load reads none.
// mutate-lane: TestEveryProbeMeasuresATree
func touch(m *Measurement, cid uint32) error {
	mib := m.Options.Int("touched")
	if mib == 0 {
		return nil
	}
	fill := fmt.Sprintf("mkdir -p /run/touched && mount -t tmpfs -o size=%dm touched /run/touched && "+
		"head -c %dM /dev/zero | tr '\\0' '\\377' > /run/touched/data", mib, mib)
	if _, _, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, fill); err != nil {
		return Miss("the guest did not write %d MiB: %v", mib, err)
	}
	return nil
}

// userfaultfd gives the runner's user /dev/userfaultfd on a bench runner, a throwaway host:
// spin-machine, run as that user, opens it and hands QEMU the descriptor (machine.Spec's
// UserfaultFDSet), as spin's runner does as root, and a state over machine.fastAbove then loads
// fast. vm.unprivileged_userfaultfd stays 0 and no other user gets the device. Anywhere else the
// host is left as it is, and loads every state as it allows.
// mutate-lane: TestEveryProbeMeasuresATree
func userfaultfd() error {
	const dev = "/dev/userfaultfd"
	if os.Getenv("GITHUB_ACTIONS") != "true" || unix.Access(dev, unix.R_OK|unix.W_OK) == nil {
		return nil
	}
	return run("sudo", "chown", strconv.Itoa(os.Getuid()), dev)
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

// readData reads path's data once, extent after extent (SEEK_DATA, SEEK_HOLE) in 1 MiB reads, and
// says how long that took and how many bytes it read: what the disk gives a sequential reader of
// what QEMU loads, and that in the page cache after it, and no page of a hole.
func readData(path string) (time.Duration, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd())
	buf := make([]byte, 1<<20) // mutate-exempt: the size of each read; any size reads the data once
	var n int64
	t0 := time.Now()
	for off := int64(0); ; {
		start, err := unix.Seek(fd, off, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) { // no data at or after off
			return time.Since(t0), n, nil
		}
		if err != nil {
			return 0, 0, fmt.Errorf("finding %s's data after byte %d: %w", path, off, err)
		}
		end, err := unix.Seek(fd, start, unix.SEEK_HOLE)
		if err != nil {
			return 0, 0, fmt.Errorf("finding %s's hole after byte %d: %w", path, start, err)
		}
		for off = start; off < end; {
			k, err := f.ReadAt(buf[:min(int64(len(buf)), end-off)], off)
			off, n = off+int64(k), n+int64(k)
			if err != nil {
				return 0, 0, fmt.Errorf("reading %s at byte %d: %w", path, off, err)
			}
		}
	}
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
func settled(socket string, state machine.Status) error {
	deadline := time.Now().Add(30 * time.Second)
	if err := waitSocket(socket, deadline); err != nil {
		return Miss("the held QEMU never opened its monitor %s: %v", socket, err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	m, err := machine.Dial(ctx, socket, qmp.Observer{})
	if err != nil {
		return fmt.Errorf("the held QEMU: %w", err)
	}
	defer func() { _ = m.Close() }()
	st, err := m.Status(ctx)
	if err != nil {
		return fmt.Errorf("the held QEMU: %w", err)
	}
	if st != state {
		return fmt.Errorf("the held QEMU is %s, want %s", st, state)
	}
	// What a pool's machine would have had: a moment idle after its start, not the instant.
	time.Sleep(100 * time.Millisecond)
	return nil
}
