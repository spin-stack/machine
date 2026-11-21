// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// What a person in a workspace expects of a snapshot (#408), at spin's shape booted to systemd and
// logged into over SSH through vsock as root. In the guest, the terminal loop `while true; do date
// +%T.%N; sleep 0.1; done` and a process holding 4 MiB of random bytes and a counter run detached
// from any session, and a file of random content is synced.
//
//  1. A checkpoint with memory, its disk sealed under a new overlay as spin's storage seals one,
//     continued in place: the process goes on with the same bytes, and an SSH session open across
//     it still answers.
//  2. A rollback with memory onto the sealed disk, after the guest changed the file and the bytes:
//     the file, read from memory and again from the disk, and the bytes are what they were at the
//     checkpoint, the process is the same one, its counter and the loop go on from their lines at
//     the checkpoint, and the kernel reseeds its RNG for a VM fork (vmgenid).
//  3. A disk-only rollback, a cold boot from the sealed disk: the file synced before the seal is
//     there and one written after is not.
//  4. A disk-only snapshot of the running guest, stop and cont around blockdev-snapshot as spin's
//     storage takes one: the process goes on.
//
// It logs what each cost the guest, from the guest's own output: the largest gaps in the loop's and
// the process's clocks, the largest gap the host saw between the process's lines on the serial
// console, and the guest's wall clock against the host's before and after. Then a pause held for a
// second, which says what the guest's clocks do across a stop. What the rollback does to the
// guest's clock and to the SSH session open at the checkpoint is logged, not held: #408 asks what
// it is (spin-stack/spin#1483).
//
//	SPIN_ROLLBACK_TEST=1   it starts machines (task boot:rollback)
func TestARollbackBringsBackTheSameMemoryAndDisk(t *testing.T) {
	if os.Getenv("SPIN_ROLLBACK_TEST") == "" {
		t.Skip("set SPIN_ROLLBACK_TEST=1: this starts machines")
	}
	x := newRollback(t)
	x.checkpoint()
	dst, top := x.rollBack()
	x.snapshotDiskOnly(dst, top)
	x.holdAPause(dst)
	dst.Kill()
	x.bootTheSealedDisk()
}

// rollback is one run of #408's scenario: the chain its machines boot from, and what the guest was
// at the checkpoint, which every step after it is held to.
type rollback struct {
	t      *testing.T
	r      *recorder
	ctx    context.Context
	signer xssh.Signer
	dir    string
	// raw is a private copy of the base image at the bottom of every chain, and frozen the
	// overlay over it that the first machine writes and the checkpoint seals.
	raw, frozen string

	file    string      // the file's SHA-256
	process tickerState // the process just before the checkpoint
	state   *os.File
	saved   machine.Saved
	stopped time.Time // GuestStopped
	sshd    string    // the PID of the sshd of the SSH session open at the checkpoint
	// loop and ticks are the logs as the checkpointed machine had them when it was killed.
	loop, ticks []string
}

func newRollback(t *testing.T) *rollback {
	t.Helper()
	x := &rollback{t: t, r: newRecorder(t, 3800000), dir: t.TempDir()}
	rel, err := machine.OpenRelease(x.r.out)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if x.signer, err = xssh.NewSignerFromKey(key); err != nil {
		t.Fatal(err)
	}
	ticker, err := os.ReadFile("testdata/rollback-ticker.pl")
	if err != nil {
		t.Fatal(err)
	}
	// A private raw copy under every overlay here: the base is never opened for writing, and
	// debugfs edits a raw image only.
	root := newRawRoot(t, x.r.out, base)
	root.write("/root/.ssh/authorized_keys", string(xssh.MarshalAuthorizedKey(x.signer.PublicKey())))
	root.edit("sif /root/.ssh/authorized_keys uid 0")
	root.edit("sif /root/.ssh/authorized_keys gid 0")
	root.write("/usr/local/lib/rollback-ticker.pl", string(ticker))
	x.raw = root.path
	x.frozen = x.overlay("frozen.qcow2", x.raw, "raw")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	x.ctx = ctx
	return x
}

// overlay is a new qcow2 overlay named name over below, an image in format.
func (x *rollback) overlay(name, below, format string) string {
	x.t.Helper()
	p := filepath.Join(x.dir, name)
	mustRun(x.t, filepath.Join(x.r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", "-b", below, "-F", format, p)
	return p
}

// checkpoint is case 1: a machine booted on frozen, checkpointed with memory and continued in
// place. Then the guest changes the file and the bytes, runs on 5 s and is killed.
func (x *rollback) checkpoint() {
	t := x.t
	src := x.r.workspace(t, x.signer, false)
	src.boot(x.ctx, t, x.frozen, []string{x.raw})
	x.file = src.sh(t, "head -c 1048576 /dev/urandom > /var/tmp/random && sync && sha256sum < /var/tmp/random")
	src.sh(t, "setsid -f sh -c 'while true; do date +%T.%N; sleep 0.1; done' > /var/tmp/loop.log 2>&1 < /dev/null")
	src.sh(t, "setsid -f perl /usr/local/lib/rollback-ticker.pl > /var/tmp/ticker.log 2>&1 < /dev/null")
	src.awaitTicks(t, 20)

	held := src.dial(t, 10*time.Second)
	t.Cleanup(func() { _ = held.Close() })
	session, err := held.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	// A terminal, as a person has one: on a pty, so that its shell is hung up when sshd goes.
	if err := session.RequestPty("xterm", 24, 80, xssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err := session.Start("exec sleep 4242"); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- session.Wait() }()
	x.sshd = src.sh(t, "for i in $(seq 100); do p=$(pgrep -fx 'sleep 4242') && break; sleep 0.05; done && ps -o ppid= -p $p")

	before := src.mark(t)
	x.process = before.process
	state, err := os.Create(filepath.Join(x.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	x.state = state
	told := &timeline{}
	saved, cost, err := src.Checkpoint(x.ctx, x.state, machine.SaveOptions{Phases: told.tell}, func(ctx context.Context) error {
		return seal(ctx, src.mon, x.frozen, x.overlay("after-checkpoint.qcow2", x.frozen, "qcow2"), "after-checkpoint")
	})
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, tail([]byte(src.said()), 2000))
	}
	if err := src.Continue(x.ctx); err != nil {
		t.Fatal(err)
	}
	x.saved = saved
	x.stopped = told.at(t, "GuestStopped")
	running := told.at(t, "GuestRunning")
	src.awaitTicks(t, 10)
	after := src.tickerNow(t)
	if after.pid != x.process.pid || after.n <= x.process.n || after.bytes != x.process.bytes {
		t.Errorf("the process across the checkpoint: %+v, then %+v; want the same one, its bytes the same, its counter on", x.process, after)
	}
	if out, err := run(held, "true"); err != nil {
		t.Errorf("the SSH session open at the checkpoint, after it: %v: %s", err, out)
	}
	t.Logf("1. a checkpoint with memory, continued in place:\n"+
		"   Cost.Paused %s, Total %s; GuestStopped to GuestRunning %s\n%s\n"+
		"   the SSH session open across it answered after it\nphases:\n%s",
		cost.Paused, cost.Total, running.Sub(x.stopped).Round(time.Microsecond), src.since(t, before, x.stopped, running), told)

	// The guest changes the file and the bytes, and writes a file the disk-only rollback must not
	// have.
	src.sh(t, "head -c 1048576 /dev/urandom > /var/tmp/random && echo after > /var/tmp/after && sync")
	// Perl runs pending signals in number order, USR1 before USR2: the new bytes are drawn before
	// the next look.
	src.sh(t, "pkill -USR2 -f '"+theProcess+"' && sleep 0.3")
	if changed := src.tickerNow(t); changed.pid != x.process.pid || changed.bytes == x.process.bytes {
		t.Fatalf("the process after new bytes: %+v, at the checkpoint %+v", changed, x.process)
	}
	// The rollback comes 5 s after the checkpoint, so that a clock it took back is told from one
	// it did not.
	src.awaitTicks(t, 50)
	x.loop, x.ticks = src.lines(t, "/var/tmp/loop.log"), src.lines(t, "/var/tmp/ticker.log")
	src.Kill()
	select {
	case err := <-ended:
		t.Logf("the SSH session open at the checkpoint, on the host, as its machine was killed: %v", err)
	case <-time.After(5 * time.Second):
		t.Error("the SSH session open at the checkpoint did not end on the host 5 s after its machine was killed")
	}
}

// rollBack is case 2: the checkpoint resumed into a fresh machine of spin's shape, on a new overlay
// over the sealed disk, which it returns with that overlay.
func (x *rollback) rollBack() (*rollbackGuest, string) {
	t := x.t
	dst := x.r.workspace(t, x.signer, true)
	top := x.overlay("rollback.qcow2", x.frozen, "qcow2")
	told := &timeline{}
	if err := dst.Resume(x.ctx, x.state, x.saved, dst.here, machine.LoadOptions{Phases: told.tell}, openRoot(t, top, []string{x.raw, x.frozen})); err != nil {
		t.Fatalf("resuming: %v\n%s", err, tail([]byte(dst.said()), 2000))
	}
	resumed := time.Now()
	dst.connect(t, 30*time.Second)
	firstLogin := time.Since(resumed)
	offFirst := dst.offset(t)
	sshdGone := dst.waitGone(t, "kill -0 "+x.sshd, resumed, 10*time.Second)
	shellGone := dst.waitGone(t, "pgrep -fx 'sleep 4242'", resumed, time.Second)
	dst.awaitTicks(t, 50)
	offLater := dst.offset(t)

	if now := dst.tickerNow(t); now.pid != x.process.pid || now.bytes != x.process.bytes || now.n <= x.process.n {
		t.Errorf("the process after the rollback: %+v; at the checkpoint %+v: want the same one, the same bytes, its counter on", now, x.process)
	}
	if got := dst.sh(t, "sha256sum < /var/tmp/random"); got != x.file {
		t.Errorf("the file after the rollback, from memory: %s, want %s", got, x.file)
	}
	if got := dst.sh(t, "sync && echo 3 > /proc/sys/vm/drop_caches && sha256sum < /var/tmp/random"); got != x.file {
		t.Errorf("the file after the rollback, from the disk: %s, want %s", got, x.file)
	}
	if got := dst.sh(t, "test -e /var/tmp/after && echo there || echo gone"); got != "gone" {
		t.Errorf("a file written after the checkpoint is %s after the rollback", got)
	}
	loop, ticks := dst.lines(t, "/var/tmp/loop.log"), dst.lines(t, "/var/tmp/ticker.log")
	loopAt, tickAt := goesOn(t, "the loop", x.loop, loop), goesOn(t, "the process", x.ticks, ticks)
	for i, l := range ticks {
		if f := strings.Fields(l); len(f) != 3 || f[0] != strconv.Itoa(i+1) {
			t.Errorf("the process's line %d after the rollback is %q: its counter did not go on by one", i+1, l)
			break
		}
	}
	if !strings.Contains(dst.sh(t, "dmesg"), "crng reseeded due to virtual machine fork") {
		t.Error("the restored kernel did not reseed its RNG for a VM fork (vmgenid)")
	}
	loopDay, tickReal, tickMono := clockOfDay(loop), column(ticks, 1), column(ticks, 2)
	t.Logf("2. a rollback with memory onto the sealed disk:\n"+
		"   the loop's line at the checkpoint %d: %s, then %s, %s on the guest's clock\n"+
		"   the process's line at the checkpoint %d, then its realtime %s and monotonic %s on\n"+
		"   the guest's wall clock against the host's %s at the first login, %s after the resume (the checkpoint's stop %s before it); %s 5 s on\n"+
		"   the SSH session open at the checkpoint, in the guest: its sshd %s, its shell %s\n"+
		"   the journal since the checkpoint:\n%s\nphases:\n%s",
		loopAt, loop[loopAt-1], loop[loopAt], secs(loopDay[loopAt]-loopDay[loopAt-1]),
		tickAt, secs(tickReal[tickAt]-tickReal[tickAt-1]), secs(tickMono[tickAt]-tickMono[tickAt-1]),
		offFirst, firstLogin.Round(time.Millisecond), resumed.Sub(x.stopped).Round(time.Millisecond), offLater,
		sshdGone, shellGone, linesSince(dst.sh(t, "journalctl -b -o short-monotonic --no-pager"), tickMono[tickAt-1]), told)
	return dst, top
}

// snapshotDiskOnly is case 4: the running guest's disk sealed between a stop and a cont, as spin's
// storage takes a snapshot without memory.
func (x *rollback) snapshotDiskOnly(w *rollbackGuest, top string) {
	t := x.t
	before := w.mark(t)
	stop := time.Now()
	if _, err := qmp.Do(x.ctx, w.mon, qmp.Stop, qmp.None{}); err != nil {
		t.Fatal(err)
	}
	sealing := time.Now()
	if err := seal(x.ctx, w.mon, top, x.overlay("disk-only.qcow2", top, "qcow2"), "disk-only"); err != nil {
		t.Fatal(err)
	}
	sealed := time.Now()
	if _, err := qmp.Do(x.ctx, w.mon, qmp.Cont, qmp.None{}); err != nil {
		t.Fatal(err)
	}
	cont := time.Now()
	w.awaitTicks(t, 10)
	if after := w.tickerNow(t); after.pid != x.process.pid || after.n <= before.process.n {
		t.Errorf("the process across a disk-only snapshot: %+v, then %+v", before.process, after)
	}
	t.Logf("4. a disk-only snapshot, stop and cont around blockdev-snapshot:\n"+
		"   stop to cont %s, the seal %s (host clock)\n%s",
		cont.Sub(stop).Round(time.Microsecond), sealed.Sub(sealing).Round(time.Microsecond), w.since(t, before, stop, cont))
}

// holdAPause stops the guest for a second, long against the loop's 100 ms: a checkpoint's or a
// seal's pause at spin's shape is shorter than one of its lines.
func (x *rollback) holdAPause(w *rollbackGuest) {
	t := x.t
	before := w.mark(t)
	stop, cont := w.hold(x.ctx, t, time.Second)
	w.awaitTicks(t, 10)
	t.Logf("a pause held for a second:\n   stop to cont %s (host clock)\n%s",
		cont.Sub(stop).Round(time.Microsecond), w.since(t, before, stop, cont))
}

// bootTheSealedDisk is case 3: a cold boot from a new overlay over the sealed disk.
func (x *rollback) bootTheSealedDisk() {
	t := x.t
	cold := x.r.workspace(t, x.signer, false)
	cold.boot(x.ctx, t, x.overlay("cold.qcow2", x.frozen, "qcow2"), []string{x.raw, x.frozen})
	if got := cold.sh(t, "sha256sum < /var/tmp/random"); got != x.file {
		t.Errorf("the file synced before the seal, after a cold boot from it: %s, want %s", got, x.file)
	}
	if got := cold.sh(t, "test -e /var/tmp/after && echo there || echo gone"); got != "gone" {
		t.Errorf("a file written after the seal is %s after a cold boot from it", got)
	}
	t.Logf("3. a disk-only rollback, a cold boot from the sealed disk:\n"+
		"   the loop or the process running: %s; the file synced before the seal is there, the one written after is not\n"+
		"   the loop's log, never synced, on the disk: %s",
		cold.sh(t, "pgrep -f '"+theLoop+"|"+theProcess+"' || echo none"),
		cold.sh(t, "test -e /var/tmp/loop.log && echo $(wc -l < /var/tmp/loop.log) lines || echo absent"))
}

// mark is where the guest's logs and clock stood before a pause.
type mark struct {
	loop, ticks int
	offset      time.Duration
	process     tickerState
}

func (w *rollbackGuest) mark(t *testing.T) mark {
	t.Helper()
	return mark{
		loop: w.lineCount(t, "/var/tmp/loop.log"), ticks: w.lineCount(t, "/var/tmp/ticker.log"),
		offset: w.offset(t), process: w.tickerNow(t),
	}
}

// since says what a pause from stop to cont cost the guest, by its logs and its clock since m.
func (w *rollbackGuest) since(t *testing.T, m mark, stop, cont time.Time) string {
	t.Helper()
	off := w.offset(t)
	ticks := w.lines(t, "/var/tmp/ticker.log")[m.ticks:]
	return fmt.Sprintf("   the loop's largest gap %s, the process's realtime %s and monotonic %s (guest clocks)\n"+
		"   the console's largest gap between the process's lines %s (host clock)\n"+
		"   the guest's wall clock against the host's %s before, %s after (%s)",
		largestGap(clockOfDay(w.lines(t, "/var/tmp/loop.log")[m.loop:])), largestGap(column(ticks, 1)), largestGap(column(ticks, 2)),
		w.screen.largestTickGap(stop.Add(-time.Second), cont.Add(time.Second)),
		m.offset, off, off-m.offset)
}

// hold stops the guest for d and lets it go, and returns when it stopped and went on.
func (w *rollbackGuest) hold(ctx context.Context, t *testing.T, d time.Duration) (stop, cont time.Time) {
	t.Helper()
	stop = time.Now()
	if _, err := qmp.Do(ctx, w.mon, qmp.Stop, qmp.None{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(d) // the pause itself: its length is what is measured
	if _, err := qmp.Do(ctx, w.mon, qmp.Cont, qmp.None{}); err != nil {
		t.Fatal(err)
	}
	return stop, time.Now()
}

// awaitTicks waits for the process to print n more lines on the console.
func (w *rollbackGuest) awaitTicks(t *testing.T, n int) {
	t.Helper()
	want := w.screen.tickCount() + n
	for deadline := time.Now().Add(30 * time.Second); w.screen.tickCount() < want; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the process printed %d of %d lines in 30 s:\n%s", w.screen.tickCount()-want+n, n, tail([]byte(w.said()), 2000))
		}
	}
}

// rollbackGuest is a machine of spin's shape booted to systemd and logged into over SSH as root,
// with a second monitor of its own, as spin's storage drives a machine's disks through one.
type rollbackGuest struct {
	*guest
	cid    uint32
	signer xssh.Signer
	ssh    *xssh.Client
	mon    *qmp.Client
	// here is the record of a machine held for a checkpoint (machine.ReadTarget).
	here machine.Target
}

// workspace starts the next machine, held to boot or, with restore, to load a checkpoint.
func (r *recorder) workspace(t *testing.T, signer xssh.Signer, restore bool) *rollbackGuest {
	t.Helper()
	s := r.spec(t, "")
	s.Restore = restore
	w := &rollbackGuest{cid: uint32(s.VsockCID), signer: signer}
	if restore {
		// The record a runner reads once for its host, of a machine of the shape held for a
		// checkpoint, its NIC in the Spec; read before the machine starts, whose vsock it shares.
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if w.here, err = machine.ReadTarget(ctx, s, machine.StartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mac := s.NICs[0].MAC
	s.NICs = nil
	sock := filepath.Join(r.dir, fmt.Sprintf("m%d.sock", r.n))
	s.Monitors = []machine.Monitor{{Socket: sock}}
	w.guest = startGuest(t, s, mac, nil)
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if w.mon, err = qmp.New(ctx, qmp.Unix(c), qmp.Observer{}); err != nil {
		t.Fatalf("talking to %s: %v", sock, err)
	}
	t.Cleanup(func() {
		_ = w.mon.Close()
		if w.ssh != nil {
			_ = w.ssh.Close()
		}
	})
	return w
}

// boot boots the machine from top, the images below it in under, sized as spin sizes a workspace,
// and logs into it.
func (w *rollbackGuest) boot(ctx context.Context, t *testing.T, top string, under []string) {
	t.Helper()
	// spin asks virtio-mem for the workspace's 2048 MiB before the guest has its filesystem
	// (internal/lab's Plug). Left at 128 MiB, the guest's OOM killer took the SSH session.
	if err := w.SetMemory(ctx, 2048); err != nil {
		t.Fatal(err)
	}
	if err := w.Boot(ctx, openRoot(t, top, under)); err != nil {
		t.Fatalf("booting: %v\n%s", err, w.said())
	}
	w.connect(t, 90*time.Second)
}

func openRoot(t *testing.T, top string, under []string) machine.Disk {
	t.Helper()
	d, err := machine.OpenDisk(machine.DiskPath{Serial: "root", Path: top, Format: "qcow2", Under: under})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func (w *rollbackGuest) connect(t *testing.T, within time.Duration) {
	t.Helper()
	w.ssh = w.dial(t, within)
}

// dial logs in over SSH through vsock, as many times as it takes within the time given.
func (w *rollbackGuest) dial(t *testing.T, within time.Duration) *xssh.Client {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		c, err := sshOverVsock(w.cid, w.signer)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("logging in over vsock %d: %v\n%s", w.cid, err, tail([]byte(w.said()), 2000))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sshOverVsock is a login as root on cid's port 22 with the one key exchange the image's sshd
// accepts.
func sshOverVsock(cid uint32, signer xssh.Signer) (*xssh.Client, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening a vsock socket: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: 22}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("connecting to vsock %d:22: %w", cid, err)
	}
	// Non-blocking, so that the file is on Go's poller and Close ends a reader blocked on it.
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("making the vsock socket non-blocking: %w", err)
	}
	conn := vsockFile{os.NewFile(uintptr(fd), "vsock")}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	c, chans, reqs, err := xssh.NewClientConn(conn, "vsock", &xssh.ClientConfig{
		User:            "root",
		Auth:            []xssh.AuthMethod{xssh.PublicKeys(signer)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(), // a fresh VM's keys, made in its boot
		Config:          xssh.Config{KeyExchanges: []string{xssh.KeyExchangeMLKEM768X25519}},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh to vsock %d:22: %w", cid, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return xssh.NewClient(c, chans, reqs), nil
}

// vsockFile is a connected vsock socket as the net.Conn x/crypto/ssh takes: net.FileConn knows no
// AF_VSOCK address.
type vsockFile struct{ *os.File }

func (vsockFile) LocalAddr() net.Addr  { return vsockAddress{} }
func (vsockFile) RemoteAddr() net.Addr { return vsockAddress{} }

type vsockAddress struct{}

func (vsockAddress) Network() string { return "vsock" }
func (vsockAddress) String() string  { return "vsock" }

// run runs command in a session of its own on c, and returns what it wrote, trimmed.
func run(c *xssh.Client, command string) (string, error) {
	s, err := c.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening a session: %w", err)
	}
	defer func() { _ = s.Close() }()
	out, err := s.CombinedOutput(command)
	return strings.TrimSpace(string(out)), err
}

// sh runs command as root in the guest, and fails the test when it fails.
func (w *rollbackGuest) sh(t *testing.T, command string) string {
	t.Helper()
	out, err := run(w.ssh, command)
	if err != nil {
		t.Fatalf("%s: %v: %s\nthe console:\n%s", command, err, out, tail([]byte(w.said()), 3000))
	}
	return out
}

func (w *rollbackGuest) lines(t *testing.T, file string) []string {
	t.Helper()
	out := w.sh(t, "cat "+file)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (w *rollbackGuest) lineCount(t *testing.T, file string) int {
	t.Helper()
	return len(w.lines(t, file))
}

// theLoop and theProcess match the loop's and the process's command lines, and not that of a shell
// that names them, as every command run over SSH does.
const (
	theLoop    = "^sh -c while true; do date"
	theProcess = "^perl /usr/local/lib/rollback-ticker.pl"
)

// tickerState is what the process holding the bytes says of itself.
type tickerState struct {
	pid, n int
	bytes  string // their SHA-256
}

func (w *rollbackGuest) tickerNow(t *testing.T) tickerState {
	t.Helper()
	out := w.sh(t, "rm -f /run/ticker.state && pkill -USR1 -f '"+theProcess+"' && "+
		"for i in $(seq 100); do test -e /run/ticker.state && break; sleep 0.05; done && cat /run/ticker.state")
	var s tickerState
	if _, err := fmt.Sscan(out, &s.pid, &s.n, &s.bytes); err != nil {
		t.Fatalf("the process's state %q: %v", out, err)
	}
	return s
}

// offset is the guest's wall clock less the host's, at the middle of the round trip that read it.
func (w *rollbackGuest) offset(t *testing.T) time.Duration {
	t.Helper()
	t0 := time.Now()
	out := w.sh(t, "date +%s.%N")
	t1 := time.Now()
	sec, nsec, ok := strings.Cut(out, ".")
	s, err1 := strconv.ParseInt(sec, 10, 64)
	ns, err2 := strconv.ParseInt(nsec, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		t.Fatalf("the guest's date %q", out)
	}
	return time.Unix(s, ns).Sub(t0.Add(t1.Sub(t0) / 2)).Round(time.Millisecond)
}

// waitGone polls command until it fails, and says when that was after since, or that it did not
// within the time given.
func (w *rollbackGuest) waitGone(t *testing.T, command string, since time.Time, within time.Duration) string {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := run(w.ssh, command); err != nil {
			return fmt.Sprintf("gone at a look %s after the resume", time.Since(since).Round(time.Millisecond))
		}
	}
	return fmt.Sprintf("still there %s after the resume", within)
}

// seal puts the image top under the new overlay next through mon, as spin's storage seals a disk:
// next is handed over as a descriptor and put in place of the qcow2 node over top's descriptor set,
// found by the set's opaque, under every parent. top is read-only from then on.
func seal(ctx context.Context, mon *qmp.Client, top, next, name string) error {
	f, err := os.OpenFile(next, os.O_RDWR, 0) // #nosec G304 -- an overlay this test made
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // QEMU keeps its own copy
	set, err := qmp.Do(ctx, mon, qmp.AddFD, qmp.AddFDArgs{File: f, Opaque: next})
	if err != nil {
		return fmt.Errorf("handing over %s: %w", next, err)
	}
	for _, n := range []qmp.BlockNode{
		qmp.FileNode{NodeName: name + "-f", Filename: set.Path(), AIO: "io_uring", Locking: "on"},
		qmp.Qcow2Node{NodeName: name, File: qmp.NodeRef(name + "-f"), Backing: qmp.NoBacking},
	} {
		if _, err := qmp.Do(ctx, mon, qmp.BlockdevAdd, n); err != nil {
			return fmt.Errorf("adding %s: %w", next, err)
		}
	}
	node, err := qcow2Over(ctx, mon, top)
	if err != nil {
		return err
	}
	if _, err := qmp.Do(ctx, mon, qmp.BlockdevSnapshot, qmp.BlockdevSnapshotArgs{Node: node, Overlay: name}); err != nil {
		return fmt.Errorf("sealing %s under %s: %w", top, next, err)
	}
	return nil
}

// qcow2Over is the qcow2 node that reads the image at path, found as spin's storage finds one: by
// its descriptor set's opaque, never by a node name, which is the machine's own.
func qcow2Over(ctx context.Context, mon *qmp.Client, path string) (string, error) {
	sets, err := qmp.Do(ctx, mon, qmp.QueryFDSets, qmp.None{})
	if err != nil {
		return "", err
	}
	nodes, err := qmp.Do(ctx, mon, qmp.QueryNamedBlockNodes, qmp.QueryNamedBlockNodesArgs{Flat: true})
	if err != nil {
		return "", err
	}
	for _, s := range sets {
		if !slices.ContainsFunc(s.FDs, func(fd struct {
			FD     int    `json:"fd"`
			Opaque string `json:"opaque"`
		}) bool {
			return fd.Opaque == path
		}) {
			continue
		}
		for _, n := range nodes {
			// A node given its backing by name is called json:{...}, its file's filename inside.
			file := n.File
			if j, ok := strings.CutPrefix(file, "json:"); ok {
				var named struct {
					File struct {
						Filename string `json:"filename"`
					} `json:"file"`
				}
				if err := json.Unmarshal([]byte(j), &named); err != nil {
					return "", fmt.Errorf("node %s's name %s: %w", n.NodeName, n.File, err)
				}
				file = named.File.Filename
			}
			if id, ok := qmp.FDSetOf(file); ok && n.Driver == "qcow2" && id == s.ID {
				return n.NodeName, nil
			}
		}
	}
	return "", fmt.Errorf("no qcow2 node reads %s", path)
}

// goesOn is the number of lines before and after a rollback have in common, which is where the
// checkpoint was in the log, and fails unless the log after goes on from there with lines of its
// own: what the guest wrote after the checkpoint is gone, and the writer kept writing.
func goesOn(t *testing.T, what string, before, after []string) int {
	t.Helper()
	n := 0
	for n < len(before) && n < len(after) && before[n] == after[n] {
		n++
	}
	if n == 0 || n == len(before) || n == len(after) {
		t.Fatalf("%s's log: %d lines before the rollback and %d after, %d of them in common: want a common start, and each going on with lines of its own", what, len(before), len(after), n)
	}
	return n
}

// clockOfDay is each `date +%T.%N` line in seconds since midnight, a day added at each wrap.
func clockOfDay(lines []string) []float64 {
	var out []float64
	day := 0.0
	for _, l := range lines {
		var h, m int
		var s float64
		if _, err := fmt.Sscanf(l, "%d:%d:%f", &h, &m, &s); err != nil {
			continue
		}
		v := float64(h*3600+m*60) + s + day
		if len(out) > 0 && v < out[len(out)-1]-43200 {
			day += 86400
			v += 86400
		}
		out = append(out, v)
	}
	return out
}

// column is field i of each line, in seconds.
func column(lines []string, i int) []float64 {
	var out []float64
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) <= i {
			continue
		}
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

func secs(s float64) time.Duration {
	return time.Duration(s * float64(time.Second)).Round(time.Millisecond)
}

// largestGap is the largest step between consecutive times, in seconds.
func largestGap(ts []float64) time.Duration {
	var g float64
	for i := 1; i < len(ts); i++ {
		g = max(g, ts[i]-ts[i-1])
	}
	return secs(g)
}

// linesSince is the lines of the kernel's or the journal's stamped after the monotonic time given,
// in seconds: dmesg's and journalctl -o short-monotonic's stamp is the guest's monotonic clock.
func linesSince(log string, after float64) string {
	stamp := regexp.MustCompile(`^\[\s*([0-9.]+)\]`)
	var b strings.Builder
	for l := range strings.Lines(log) {
		m := stamp.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= after {
			b.WriteString("     " + l)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// at is when the phase named was told, or the test fails.
func (l *timeline) at(t *testing.T, name string) time.Time {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.told {
		if strings.TrimPrefix(fmt.Sprintf("%T", p), "machine.") == name {
			return p.When()
		}
	}
	t.Fatalf("no %s was told", name)
	return time.Time{}
}

// tickCount is how many of the process's lines the console has said.
func (s *screen) tickCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(tickLine.FindAllIndex(s.b.Bytes(), -1))
}

// tickLine is a line of the process's on the console.
var tickLine = regexp.MustCompile(`TICK [0-9]+ [^\n]*\n`)

// largestTickGap is the largest step between the arrivals of consecutive lines of the process's on
// the console, of those that arrived between from and to, on the host's clock.
func (s *screen) largestTickGap(from, to time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	var g time.Duration
	var last time.Time
	for _, m := range tickLine.FindAllIndex(s.b.Bytes(), -1) {
		i, _ := slices.BinarySearchFunc(s.writes, m[1], func(w stamp, end int) int { return w.end - end })
		if i == len(s.writes) {
			break
		}
		at := s.writes[i].at
		if at.Before(from) || at.After(to) {
			continue
		}
		if !last.IsZero() {
			g = max(g, at.Sub(last))
		}
		last = at
	}
	return g.Round(time.Millisecond)
}
