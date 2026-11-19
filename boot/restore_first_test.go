// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/spin-machine/boot"
)

// The second of the two numbers in CLAUDE.md's goal: a checkpoint restored to the same first
// login. Each row of boot:ssh-first (firstLoginRows) is booted cold, logged into, saved with
// `spin-machine save` - which ends it - and later restored with `boot --incoming defer` and
// `spin-machine restore`, and logged into again, from the moment before QEMU is exec'd.
//
// Two phases, as #246's boot:score had them: every cold boot first, of every row and repetition
// interleaved, then every restore in the same order. On the bench fleet, whose hosts run KVM
// nested in a cloud VM, a cold boot run after a restored guest had run was ~1.8x slower in every
// phase alike (2026-11-13), so interleaving boot-save-restore measured that and not the machine.
// Each state is evicted from the host's page cache before its restore, and the cache is asked
// whether it was: a checkpoint kept for later is read from the disk.
//
// Each row is also restored a second way, interleaved with the first: its state evicted, read
// back whole in one sequential pass - which times what the disk gives, READ - and restored from
// the page cache. Evicted against cached is what the disk costs a restore; READ against that
// difference says whether QEMU reads the file as fast as the disk can (2026-11-19: a restore took
// ~790 ms to running on nested and on metal alike, and nothing said which part was the disk).
//
//	SPIN_RESTORE_FIRST=1  run at all
//	REPS=<n>              repetitions of each row (default 10)
//	CPUS=<n>              vCPUs (default 2)
func TestRestoreFirstSSH(t *testing.T) {
	if os.Getenv("SPIN_RESTORE_FIRST") == "" {
		t.Skip("set SPIN_RESTORE_FIRST=1: boots, saves and restores VMs with a vsock device")
	}
	out := releaseDir(t)
	if !canSudo() {
		t.Skip("this writes a key into the boot's own overlay through qemu-nbd, which needs sudo")
	}
	if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
		t.Skip("no /dev/vhost-vsock: run task vsock")
	}
	reps := envInt(t, "REPS", 10)
	rows, signer := firstLoginRows(t)

	type mode struct {
		label  string
		cached bool
	}
	var modes []mode
	for _, v := range rows {
		modes = append(modes, mode{v.label, false}, mode{v.label + ", cached", true})
	}
	var runs []*checkpointRun
	for i := range reps {
		for j, m := range modes {
			socks, err := os.MkdirTemp("/tmp", "rf")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socks) })
			r := &checkpointRun{v: rows[j/2], label: m.label, cached: m.cached, out: out, signer: signer, dir: t.TempDir(), socks: socks}
			r.cold(t)
			t.Logf("%s, repetition %d: cold login %v, saved %d MiB in %v", m.label, i+1,
				r.coldLogin.Round(time.Millisecond), r.size>>20, r.save.Round(time.Millisecond))
			runs = append(runs, r)
		}
	}
	for i, r := range runs {
		r.restore(t)
		t.Logf("%s, repetition %d: read %v, restored up %v, running %v, login %v", r.label, i/len(modes)+1,
			r.read.Round(time.Millisecond), r.up.Round(time.Millisecond), r.running.Round(time.Millisecond), r.login.Round(time.Millisecond))
	}

	ms := func(label string, f func(*checkpointRun) time.Duration) string {
		var ds []time.Duration
		for _, r := range runs {
			if r.label == label {
				ds = append(ds, f(r))
			}
		}
		p50, _ := boot.Percentile(ds, .5)
		p95, _ := boot.Percentile(ds, .95)
		return fmt.Sprintf("%6d/%-6d", p50.Milliseconds(), p95.Milliseconds())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nms from the moment before QEMU is exec'd, p50/p95 over %d of each, %s vCPUs; every cold boot before every restore\n\n",
		reps, rows[0].cpus)
	fmt.Fprintf(&b, "  %-30s %9s %13s %13s   %13s %13s %13s %13s\n", "", "STATE MiB", "COLD LOGIN", "SAVE", "READ", "RESTORE: UP", "RUNNING", "LOGIN")
	for _, m := range modes {
		var size int64
		for _, r := range runs {
			if r.label == m.label {
				size = max(size, r.size)
			}
		}
		read := "      -      "
		if m.cached {
			read = ms(m.label, func(r *checkpointRun) time.Duration { return r.read })
		}
		fmt.Fprintf(&b, "  %-30s %9d %s %s   %s %s %s %s\n", m.label, size>>20,
			ms(m.label, func(r *checkpointRun) time.Duration { return r.coldLogin }),
			ms(m.label, func(r *checkpointRun) time.Duration { return r.save }),
			read,
			ms(m.label, func(r *checkpointRun) time.Duration { return r.up }),
			ms(m.label, func(r *checkpointRun) time.Duration { return r.running }),
			ms(m.label, func(r *checkpointRun) time.Duration { return r.login }))
	}
	t.Log(b.String())
}

type checkpointRun struct {
	cli    string // the spin-machine every step runs; the release's when empty
	v      variant
	label  string // the row's, and ", cached" when its state is restored from the page cache
	cached bool
	out    string
	signer xssh.Signer
	dir    string // the overlay and the state, on the disk TMPDIR names
	socks  string // the QMP sockets: a Unix socket's path has to fit in 108 bytes, and dir's need not
	cid    uint32

	size                                      int64 // the state's bytes
	coldLogin, save, read, up, running, login time.Duration
}

// spinMachine is the spin-machine a run's steps execute: the one given, or the release's.
func (r *checkpointRun) spinMachine() string {
	if r.cli != "" {
		return r.cli
	}
	return filepath.Join(r.out, "bin", "spin-machine")
}

func (r *checkpointRun) overlay() string { return filepath.Join(r.dir, "overlay.qcow2") }
func (r *checkpointRun) state() string   { return filepath.Join(r.dir, "state") }

// launch starts the machine with its monitor on qmp and returns a channel closed when it has
// exited, and a stop that kills its process group and waits. The test's end stops it if nothing
// has.
func (r *checkpointRun) launch(t *testing.T, qmp string, extra ...string) (exited <-chan struct{}, stop func()) {
	t.Helper()
	return r.launchWith(t, qmp, append([]string{"--disk", r.overlay()}, extra...)...)
}

// launchWith is launch with no disk unless extra names one: boot:prestart's machine is given its
// disk after it has started.
func (r *checkpointRun) launchWith(t *testing.T, qmp string, extra ...string) (exited <-chan struct{}, stop func()) {
	t.Helper()
	args := append([]string{"boot", "--release", r.out, "--memory", r.v.memory,
		"--cpus", r.v.cpus, "--vsock-cid", fmt.Sprint(r.cid), "--qmp", qmp, "--console", "file:" + qmp + ".console",
		"--append", "init=/sbin/init"}, extra...)
	cmd := exec.Command(r.spinMachine(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: launching the machine: %v", r.v.label, err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	stop = func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	t.Cleanup(stop)
	return done, stop
}

// loggedIn waits for sshd to accept on vsock, then logs in, and returns when the login was done.
func (r *checkpointRun) loggedIn(t *testing.T, t0 time.Time, what string) time.Duration {
	t.Helper()
	deadline := t0.Add(60 * time.Second)
	for !vsockAccepts(r.cid, 22) {
		if time.Now().After(deadline) {
			t.Fatalf("%s, %s: sshd never accepted on vsock", r.v.label, what)
		}
		time.Sleep(2 * time.Millisecond)
	}
	var last error
	for time.Now().Before(deadline) {
		if _, last = sshLogin(r.cid, r.signer, xssh.KeyExchangeMLKEM768X25519); last == nil {
			return time.Since(t0)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s, %s: sshd accepted but was never logged into: %v", r.v.label, what, last)
	return 0
}

// cold boots a fresh overlay of the release with the row's edits, logs in, saves - which ends the
// machine - and evicts the state the save has just put in the host's page cache.
func (r *checkpointRun) cold(t *testing.T) {
	t.Helper()
	mustRun(t, filepath.Join(r.out, "bin", "qemu-img"), "create", "-f", "qcow2",
		"-F", "qcow2", "-b", filepath.Join(r.out, "image", "rootfs.qcow2"), r.overlay())
	editOverlay(t, r.overlay(), r.v)
	r.cid = uint32(100000 + rand.IntN(1<<20))

	qmp := filepath.Join(r.socks, "cold.qmp")
	t0 := time.Now()
	exited, _ := r.launch(t, qmp)
	r.coldLogin = r.loggedIn(t, t0, "cold")

	t0 = time.Now()
	mustRun(t, r.spinMachine(), "save", "--qmp", qmp, "--to", r.state())
	r.save = time.Since(t0)
	select { // save quits QEMU, and the restore opens the same overlay
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: QEMU was still running 30s after its state was saved", r.v.label)
	}
	evict(t, r.state())
	st, err := os.Stat(r.state())
	if err != nil {
		t.Fatal(err)
	}
	r.size = st.Size()
}

// restore resumes the saved state read from the disk, logs in, and kills the machine: a restored
// guest left running would be what the restores after it measured.
func (r *checkpointRun) restore(t *testing.T) {
	t.Helper()
	evict(t, r.state())
	if r.cached {
		r.read = readWhole(t, r.state())
	}
	qmp := filepath.Join(r.socks, "restored.qmp")
	t0 := time.Now()
	_, stop := r.launch(t, qmp, "--incoming", "defer")
	for {
		if c, err := net.Dial("unix", qmp); err == nil {
			_ = c.Close()
			break
		}
		if time.Since(t0) > 30*time.Second {
			t.Fatalf("%s: the restoring QEMU never opened its QMP socket", r.v.label)
		}
		time.Sleep(time.Millisecond)
	}
	r.up = time.Since(t0)
	mustRun(t, r.spinMachine(), "restore", "--qmp", qmp, "--from", r.state())
	r.running = time.Since(t0)
	r.login = r.loggedIn(t, t0, "restored")
	stop()
}

// readWhole reads path once, start to end, in 1 MiB reads, and says how long that took: what the
// disk gives a sequential reader, and the state in the page cache after it.
func readWhole(t *testing.T, path string) time.Duration {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1<<20)
	t0 := time.Now()
	for {
		_, err := f.Read(buf)
		if err == io.EOF {
			return time.Since(t0)
		}
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
	}
}

// evict drops path from the host's page cache, and asks the cache whether it did: a restore of a
// state still in memory measures the cache and not the disk, and fadvise says nothing about what
// it dropped.
func evict(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd())
	if err := unix.Fsync(fd); err != nil {
		t.Fatalf("syncing %s: %v", path, err)
	}
	if err := unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED); err != nil {
		t.Fatalf("evicting %s from the page cache: %v", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	m, err := unix.Mmap(fd, 0, int(st.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatalf("mapping %s: %v", path, err)
	}
	defer func() { _ = unix.Munmap(m) }()
	page := os.Getpagesize()
	vec := make([]byte, (len(m)+page-1)/page)
	// x/sys has no wrapper for mincore(2).
	if _, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&m[0])), uintptr(len(m)), uintptr(unsafe.Pointer(&vec[0]))); errno != 0 {
		t.Fatalf("asking which pages of %s are cached: %v", path, errno)
	}
	cached := 0
	for _, v := range vec {
		cached += int(v & 1)
	}
	if cached > 0 {
		t.Fatalf("%s: %d KiB still in the page cache after evicting it", path, cached*page/1024)
	}
}
