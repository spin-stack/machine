// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// transport is how much of each goal number the two transports it rides are: QMP, between the
// host and QEMU on a Unix socket, and vsock, between the host and the guest. Neither is a goal
// number; this probe says what the goal numbers would gain if either cost nothing.
//
// QEMU runs traced (tracedQEMU), so each boot and restore says which QMP commands the guest
// waited on, what each took, and how much of that was the main loop waiting on spin-machine's
// next command. After the cold boot's login, the probe times query-status on the machine's
// monitor, a one-byte echo over vsock, the same echo through a hop like spin's forwarder (vsock to
// TCP on the guest's loopback), and a warm login, counting the login's round trips. Then the
// machine finishes its boot (bootFinished), is saved and, in the second phase, restored into a traced QEMU and logged into, as
// restore-first does it.
type transport struct{}

func (transport) Name() string { return "transport" }
func (transport) About() string {
	return "what QMP and vsock cost the goal's paths: QEMU traced, cold from before its exec and restored, and their round trips timed"
}
func (transport) Options() []Option {
	return append(shape, Option{"echoes", "200", "round trips timed of each kind after the cold login"})
}
func (transport) Rows(Options) []string { return []string{"boot"} }
func (transport) Phases() int           { return 2 }

func (transport) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "restore"}, Need{"boot", "qemu"},
		Need{"save", "to"}, Need{"restore", "disk"}), plugNeeds(o)...)
}

func (transport) Metrics() []Metric {
	return []Metric{
		{"cold_login", "ms", "the traced cold boot's first login, from before the exec (a shell more than ssh-first's)"},
		{"cold_ready", "ms", "from before the exec to QEMU's main loop taking its first QMP command: the machine made"},
		{"cold_queued", "ms", "how long spin-machine's first command waited for QEMU's main loop: above 0, its start and dial were off the path"},
		{"cold_qmp", "ms", "from that command's taking to `cont` answered: the QMP the cold boot waits on"},
		{"cold_cmds", "count", "the commands in cold_qmp, qmp_capabilities and cont among them"},
		{"cold_client", "ms", "of cold_qmp, QEMU waiting for spin-machine's next command"},
		{"qmp_rtt", "µs", "the median of `echoes` query-status round trips on the running machine's monitor"},
		{"vsock_rtt", "µs", "the median of `echoes` one-byte round trips from the host to an echo in the guest"},
		{"forward_rtt", "µs", "the same through a hop like spin's forwarder: vsock to TCP on the guest's loopback"},
		{"warm_login", "ms", "a login after the first, from its connect"},
		{"login_turns", "count", "the warm login's round trips: each time the client wrote and then waited for the guest"},
		{"login", "ms", "the restored guest's first login, from before the restoring QEMU's exec"},
		{"restore_queued", "ms", "how long the restore's first command waited for QEMU's main loop"},
		{"restore_qmp", "ms", "the restore's first command taken to migrate-incoming taken: the QMP before the load"},
		{"restore_cmds", "count", "the commands in restore_qmp, migrate-incoming among them"},
		{"restore_client", "ms", "of restore_qmp, QEMU waiting for spin-machine's next command"},
		{"restore_after", "ms", "the guest running to the restore's last command answered"},
	}
}

// echoPorts are the guest's echoes: one on vsock, and one on TCP behind a vsock forwarder.
const (
	echoPort    = 7000
	forwardPort = 7001
	tcpEchoPort = 7002
)

// echoes starts them, outliving the session that starts them. socat's PIPE writes back what it
// reads; the forwarder is spin-lab-forward's socat (login.go) on other ports.
var echoes = fmt.Sprintf(`setsid -f socat VSOCK-LISTEN:%[1]d,fork,reuseaddr PIPE </dev/null >/dev/null 2>&1
setsid -f socat TCP-LISTEN:%[3]d,bind=127.0.0.1,fork,reuseaddr,nodelay PIPE </dev/null >/dev/null 2>&1
setsid -f socat VSOCK-LISTEN:%[2]d,fork,reuseaddr TCP:127.0.0.1:%[3]d,nodelay </dev/null >/dev/null 2>&1
`, echoPort, forwardPort, tcpEchoPort)

// mutate-lane: TestEveryProbeMeasuresATree
func (transport) Measure(_ context.Context, m *Measurement) error {
	if m.Phase == 0 {
		return transportCold(m)
	}
	return transportRestore(m)
}

// mutate-lane: TestEveryProbeMeasuresATree
func transportCold(m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	traced, err := tracedQEMU(m, m.Dir)
	if err != nil {
		return err
	}
	c := &checkpoint{overlay: overlay, state: filepath.Join(m.Dir, "state"), cid: NewCID()}
	sock := filepath.Join(m.Socks, "cold.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, append(m.BootArgs(overlay, c.bootFlags(m, sock)...), traced)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	plugged := m.Plug(sock)
	login, err := m.Key.LoggedIn(mc, c.cid, t0)
	if err != nil {
		return err
	}
	if err := plugged(); err != nil {
		return err
	}
	m.RecordMs("cold_login", login)
	n := m.Options.Int("echoes")
	if rtt, err := qmpRTT(sock, n); err != nil {
		return err
	} else {
		m.Record("qmp_rtt", micros(rtt))
	}
	d, turns, err := m.Key.countedLogin(c.cid)
	if err != nil {
		return err
	}
	m.RecordMs("warm_login", d)
	m.Record("login_turns", float64(turns))
	if _, _, err := m.Key.Run(c.cid, xssh.KeyExchangeMLKEM768X25519, echoes); err != nil {
		return fmt.Errorf("starting the guest's echoes: %w", err)
	}
	for metric, port := range map[string]uint32{"vsock_rtt": echoPort, "forward_rtt": forwardPort} {
		rtt, err := vsockRTT(c.cid, port, n)
		if err != nil {
			return err
		}
		m.Record(metric, micros(rtt))
	}
	if err := bootFinished(m, c.cid); err != nil {
		return err
	}
	// The echoes end with the machine: save checkpoints them, and nothing restores them.
	if err := m.SpinMachine("save", "--qmp", sock, "--to", c.state); err != nil {
		return err
	}
	select {
	case <-mc.Done():
	case <-time.After(30 * time.Second):
		return Miss("QEMU was still running 30s after its state was saved")
	}
	if err := evict(c.state); err != nil {
		return err
	}
	t, err := readQMPTrace(m.Dir, mc.PID())
	if err != nil {
		return err
	}
	if len(t.Commands) == 0 {
		return Miss("QEMU's trace holds no QMP command")
	}
	conv := t.conversation("cont")
	s, ok := span(conv, "cont", true)
	if !ok {
		return Miss("QEMU's trace holds no cont")
	}
	m.RecordMs("cold_ready", t.Commands[0].Taken.Sub(t0))
	m.RecordMs("cold_queued", conv[0].Taken.Sub(conv[0].Arrived))
	m.RecordMs("cold_qmp", s.Took)
	m.Record("cold_cmds", float64(s.Commands))
	m.RecordMs("cold_client", s.Client)
	detailCommands(m, "cold", conv[:s.Commands])
	m.State = c
	return nil
}

// mutate-lane: TestEveryProbeMeasuresATree
func transportRestore(m *Measurement) error {
	c := m.State.(*checkpoint)
	if err := evict(c.state); err != nil {
		return err
	}
	dir := filepath.Join(m.Dir, "restored")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making a directory for the restore's trace: %w", err)
	}
	traced, err := tracedQEMU(m, dir)
	if err != nil {
		return err
	}
	sock := filepath.Join(m.Socks, "restored.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, append(m.BootArgs(c.overlay, c.bootFlags(m, sock, "--restore")...), traced)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	if err := waitSocket(sock, t0.Add(30*time.Second)); err != nil {
		return Miss("the restoring QEMU never opened its monitor: %v; spin-machine: %s", err, strings.TrimSpace(mc.Stderr.String()))
	}
	if err := m.SpinMachine(c.restoreArgs(m, sock)...); err != nil {
		return err
	}
	login, err := m.Key.LoggedIn(mc, c.cid, t0)
	if err != nil {
		return err
	}
	m.RecordMs("login", login)
	mc.Stop()
	t, err := readQMPTrace(dir, mc.PID())
	if err != nil {
		return err
	}
	conv := t.conversation("migrate_incoming")
	s, ok := span(conv, "migrate_incoming", false)
	if !ok || t.Running.IsZero() {
		return Miss("QEMU's trace holds no migrate-incoming, or no guest running")
	}
	m.RecordMs("restore_queued", conv[0].Taken.Sub(conv[0].Arrived))
	m.RecordMs("restore_qmp", s.Took)
	m.Record("restore_cmds", float64(s.Commands))
	m.RecordMs("restore_client", s.Client)
	m.RecordMs("restore_after", conv[len(conv)-1].Answered.Sub(t.Running))
	detailCommands(m, "restore", conv)
	return nil
}

// detailCommands keeps each command of a path, by its place in it: its round trip as the main loop
// saw it (from the answer before it, or from its own taking for the first) and its own run.
func detailCommands(m *Measurement, path string, cs []qmpCommand) {
	for i, c := range cs {
		from := c.Taken
		if i > 0 {
			from = cs[i-1].Answered
		}
		key := fmt.Sprintf("%s\x00%02d\x00%s\x00", path, i, c.Name)
		m.Detail(key+"rt", ms(c.Answered.Sub(from)))
		m.Detail(key+"run", ms(c.Answered.Sub(c.Taken)))
	}
}

func ms(d time.Duration) float64     { return float64(d) / float64(time.Millisecond) }
func micros(d time.Duration) float64 { return float64(d) / float64(time.Microsecond) }

// Detail is the commands on each path, by place: the p50 of each one's round trip and of its own
// run, in ms, over every measurement of each tree.
func (transport) Detail(r *Result) string {
	var b strings.Builder
	for _, t := range r.Trees {
		rows := map[string][]float64{}
		for _, s := range r.Samples {
			if s.Tree != t.Name {
				continue
			}
			for k, v := range s.Detail {
				rows[k] = append(rows[k], v)
			}
		}
		var places []string
		for k := range rows {
			if p, ok := strings.CutSuffix(k, "\x00rt"); ok {
				places = append(places, p)
			}
		}
		if len(places) == 0 {
			continue
		}
		slices.Sort(places)
		fmt.Fprintf(&b, "\n  %s: QMP commands on each path, p50 ms of the round trip and of the command's own run\n", t.Name)
		for _, p := range places {
			f := strings.Split(p, "\x00")
			rt, _ := Percentile(rows[p+"\x00rt"], .5)
			run, _ := Percentile(rows[p+"\x00run"], .5)
			fmt.Fprintf(&b, "    %-8s %s %-28s %7.3f %7.3f\n", f[0], f[1], f[2], rt, run)
		}
	}
	return b.String()
}

// qmpRTT is the median of n query-status round trips on the machine's monitor at socket.
// mutate-lane: TestEveryProbeMeasuresATree
func qmpRTT(socket string, n int) (time.Duration, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return 0, fmt.Errorf("connecting to %s: %w", socket, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		return 0, fmt.Errorf("the monitor %s: %w", socket, err)
	}
	vm := machine.Attach(q)
	defer func() { _ = vm.Close() }()
	rtts := make([]time.Duration, 0, n)
	for range n {
		t0 := time.Now()
		if _, err := vm.Status(ctx); err != nil {
			return 0, fmt.Errorf("timing query-status: %w", err)
		}
		rtts = append(rtts, time.Since(t0))
	}
	p50, _ := Percentile(rtts, .5)
	return p50, nil
}

// vsockRTT is the median of n one-byte round trips to the echo on the guest cid's port, waiting a
// second at most for the echo to listen. Blocking reads and writes, so the time is the transport's
// and not Go's poller's.
// mutate-lane: TestEveryProbeMeasuresATree
func vsockRTT(cid, port uint32, n int) (time.Duration, error) {
	fd, err := dialEcho(cid, port, time.Now().Add(time.Second))
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	rtts := make([]time.Duration, 0, n)
	b := []byte{'x'}
	for range n {
		t0 := time.Now()
		if _, err := unix.Write(fd, b); err != nil {
			return 0, fmt.Errorf("writing to the echo on vsock %d:%d: %w", cid, port, err)
		}
		if k, err := unix.Read(fd, b); err != nil || k != 1 {
			return 0, fmt.Errorf("reading the echo on vsock %d:%d: read %d: %v", cid, port, k, err)
		}
		rtts = append(rtts, time.Since(t0))
	}
	p50, _ := Percentile(rtts, .5)
	return p50, nil
}

// dialEcho connects to the echo on the guest cid's port, trying until deadline: the echo is
// started a moment before over SSH, and may not listen yet.
// mutate-lane: TestEveryProbeMeasuresATree
func dialEcho(cid, port uint32, deadline time.Time) (int, error) {
	for {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return -1, fmt.Errorf("opening a vsock socket: %w", err)
		}
		err = unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
		if err == nil {
			return fd, nil
		}
		_ = unix.Close(fd)
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("connecting to the guest's echo on vsock %d:%d: %w", cid, port, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// countedLogin is Login with mlkem768x25519 over a connection that counts its round trips.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) countedLogin(cid uint32) (time.Duration, int, error) {
	t := &turns{}
	d, _, err := k.run(cid, xssh.KeyExchangeMLKEM768X25519, "true", func(c net.Conn) net.Conn { t.Conn = c; return t })
	return d, t.count(), err
}

// turns is a connection that counts round trips: each read that returns data after a write since
// the last such read. x/crypto/ssh reads on a goroutine of its own that waits in Read before
// anything is written, so a read is counted when it returns, not when it starts.
type turns struct {
	net.Conn
	mu    sync.Mutex
	wrote bool
	n     int
}

func (t *turns) Write(b []byte) (int, error) {
	t.mu.Lock()
	t.wrote = true
	t.mu.Unlock()
	return t.Conn.Write(b)
}

func (t *turns) Read(b []byte) (int, error) {
	n, err := t.Conn.Read(b)
	t.mu.Lock()
	if n > 0 && t.wrote {
		t.n++
		t.wrote = false
	}
	t.mu.Unlock()
	return n, err
}

func (t *turns) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}
