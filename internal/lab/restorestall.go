// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// restoreStall is an experiment, to be removed once its question is answered: why ~20% of
// multifd restores in spin's shape wait ~400 ms between running and the first login (lab
// 44748674495, 44750713447). It is restore-first's evicted row with the save's and the load's
// channels apart - a stall that follows the save is in the state, one that follows the load is in
// QEMU's load - and, after the login, what the guest says of its memory (reclaim, refaults, PSI)
// and what QEMU's vCPU thread ran between running and the login: a guest busy the whole gap, or
// waiting.
type restoreStall struct{}

func init() { register(restoreStall{}) }

func (restoreStall) Name() string { return "restore-stall" }
func (restoreStall) About() string {
	return "ms from before QEMU's exec to the restored guest's first login, the save's and the load's channels apart, with the guest's memory after it"
}

func (restoreStall) Options() []Option {
	return append(shape, Option{"pairs", "1/1 8/8 8/1 1/8", "save/restore --channels, space-separated, a row each"})
}

func (restoreStall) Rows(o Options) []string {
	var rows []string
	for _, p := range o.Fields("pairs") {
		s, r, _ := strings.Cut(p, "/")
		rows = append(rows, fmt.Sprintf("saved %s, restored %s", s, r))
	}
	return rows
}

func (restoreStall) Phases() int { return 2 }

func (restoreStall) Needs(Options) []Need {
	return append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "restore"},
		Need{"save", "to"}, Need{"restore", "disk"}, Need{"save", "channels"}, Need{"restore", "channels"})
}

func (restoreStall) Metrics() []Metric {
	return []Metric{
		{"save", "ms", "`spin-machine save`"},
		{"running", "ms", "`spin-machine restore` returned"},
		{"accept", "ms", "sshd accepting on vsock"},
		{"login", "ms", "the restored guest's first login"},
		{"gap", "ms", "login less running"},
		{"vcpu_gap", "ms", "QEMU's vCPU threads on a host CPU between running and the login"},
		{"failed_logins", "count", "logins tried and failed before the one that was done"},
		{"pgscan", "count", "the guest's pages scanned for reclaim, kswapd and direct, at the login"},
		{"pgsteal", "count", "the guest's pages reclaimed"},
		{"refault", "count", "the guest's file pages read again after reclaim"},
		{"pgmajfault", "count", "the guest's major faults"},
		{"allocstall", "count", "the guest's direct reclaims"},
		{"oom_kill", "count", "the guest's OOM kills"},
		{"psi_mem_some", "ms", "the guest's PSI memory some total"},
		{"psi_mem_full", "ms", "the guest's PSI memory full total"},
		{"memfree", "MiB", "the guest's MemFree after the login"},
		{"guest_uptime", "ms", "the guest's uptime when its memory was read"},
		{"diag_at", "ms", "when the guest's memory was read, from before QEMU's exec"},
	}
}

// stallDiag is read as spin after the login, with the kernel's own account of each OOM kill.
// Nothing in it reads much: under 128 MiB a `journalctl -b` here woke the OOM killer (a laptop,
// 2026-11-20).
const stallDiag = `cat /proc/pressure/memory; grep -E '^(pgscan_kswapd|pgscan_direct|pgsteal_kswapd|pgsteal_direct|workingset_refault_file|pgmajfault|allocstall_normal|allocstall_dma32|allocstall_movable|oom_kill) ' /proc/vmstat; grep '^MemFree:' /proc/meminfo; echo uptime $(cut -d' ' -f1 /proc/uptime) $(cat /sys/devices/system/clocksource/clocksource0/current_clocksource); sudo -n journalctl -b -o short-monotonic --no-pager -n 40 2>/dev/null | grep -E 'sshd|session|logind|Started|user@' | tail -25; sudo -n dmesg | grep -E 'invoked oom-killer|Killed process' || true`

func (restoreStall) Measure(_ context.Context, m *Measurement) error {
	pair, _, _ := strings.Cut(strings.TrimPrefix(m.Row, "saved "), ", restored ")
	load := m.Row[strings.LastIndex(m.Row, " ")+1:]
	if m.Phase == 0 {
		return coldSave(m, "--channels", pair)
	}
	c := m.State.(*checkpoint)
	if err := evict(c.state); err != nil {
		return err
	}
	qmp := filepath.Join(m.Socks, "restored.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(c.overlay, c.bootFlags(m, qmp, "--restore")...)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	if err := waitSocket(qmp, t0.Add(30*time.Second)); err != nil {
		return Miss("the restoring QEMU never opened its monitor: %v; spin-machine: %s", err, strings.TrimSpace(mc.Stderr.String()))
	}
	if err := m.SpinMachine("restore", "--release", m.Tree.Dir, "--qmp", qmp, "--from", c.state, "--disk", c.overlay, "--channels", load); err != nil {
		return err
	}
	m.RecordMs("running", time.Since(t0))
	v0 := vcpuNs(mc.PID())
	// LoggedIn's loop, with the accept and each failed login written down.
	deadline := t0.Add(60 * time.Second)
	for !VsockAccepts(c.cid, 22) {
		if time.Now().After(deadline) {
			return Miss("sshd never accepted on vsock")
		}
		time.Sleep(2 * time.Millisecond)
	}
	m.RecordMs("accept", time.Since(t0))
	var failed []string
	for {
		ta := time.Now()
		trace, err := m.Key.tracedLogin(c.cid, t0)
		if err == nil {
			m.Logf("login trace (ms from t0): %s", strings.Join(trace, " "))
			break
		}
		failed = append(failed, fmt.Sprintf("+%v: %v after %v", ta.Sub(t0).Round(time.Millisecond), err, time.Since(ta).Round(time.Millisecond)))
		if time.Now().After(deadline) {
			return Miss("sshd accepted but was never logged into: %q", failed)
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.RecordMs("login", time.Since(t0))
	m.Record("vcpu_gap", float64(vcpuNs(mc.PID())-v0)/1e6)
	m.Record("gap", m.values["login"]-m.values["running"])
	m.Record("failed_logins", float64(len(failed)))
	diagAt := time.Since(t0)
	_, out, err := m.Key.Run(c.cid, xssh.KeyExchangeMLKEM768X25519, stallDiag)
	m.RecordMs("diag_at", diagAt)
	if err != nil {
		return Miss("reading the guest's memory after the login: %v", err)
	}
	readStallDiag(m, out)
	m.Logf("save %.0f running %.0f accept %.0f login %.0f gap %.0f vcpu %.0f; failed logins %q; guest: %s",
		m.values["save"], m.values["running"], m.values["accept"], m.values["login"], m.values["gap"], m.values["vcpu_gap"],
		failed, strings.Join(strings.Fields(out), " "))
	return nil
}

// tracedLogin is Key.Login with the time of each read and write on the connection, from t0: where
// a slow login waited, the guest's reply or the client.
func (k *Key) tracedLogin(cid uint32, t0 time.Time) ([]string, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening a vsock socket: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: 22}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("connecting to vsock %d:22: %w", cid, err)
	}
	tc := &tracedConn{vsockConn: vsockConn{os.NewFile(uintptr(fd), "vsock")}, t0: t0}
	tc.note("connected", 0)
	defer func() { _ = tc.Close() }()
	c, chans, reqs, err := xssh.NewClientConn(tc, "vsock", &xssh.ClientConfig{
		User: "spin", Auth: []xssh.AuthMethod{xssh.PublicKeys(k.Signer)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(),
		Config:          xssh.Config{KeyExchanges: []string{xssh.KeyExchangeMLKEM768X25519}},
	})
	if err != nil {
		return tc.events, fmt.Errorf("ssh to vsock %d:22: %w", cid, err)
	}
	tc.note("authenticated", 0)
	client := xssh.NewClient(c, chans, reqs)
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return tc.events, fmt.Errorf("opening a session: %w", err)
	}
	tc.note("session", 0)
	defer func() { _ = sess.Close() }()
	if _, err := sess.Output("true"); err != nil {
		return tc.events, fmt.Errorf("running true: %w", err)
	}
	tc.note("done", 0)
	return tc.events, nil
}

type tracedConn struct {
	vsockConn
	t0     time.Time
	mu     sync.Mutex
	events []string
}

func (c *tracedConn) note(what string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, fmt.Sprintf("%s%d@%.1f", what, n, float64(time.Since(c.t0).Microseconds())/1000))
}

func (c *tracedConn) Read(b []byte) (int, error) {
	n, err := c.vsockConn.Read(b)
	c.note("r", n)
	return n, err
}

func (c *tracedConn) Write(b []byte) (int, error) {
	c.note("w", len(b))
	return c.vsockConn.Write(b)
}

// vcpuNs is the time pid's vCPU threads have run on a host CPU, from their schedstat.
func vcpuNs(pid int) int64 {
	dir := fmt.Sprintf("/proc/%d/task", pid)
	ents, _ := os.ReadDir(dir)
	var ns int64
	for _, e := range ents {
		comm, _ := os.ReadFile(filepath.Join(dir, e.Name(), "comm"))
		if !strings.Contains(string(comm), "/KVM") {
			continue
		}
		ss, _ := os.ReadFile(filepath.Join(dir, e.Name(), "schedstat"))
		if f := strings.Fields(string(ss)); len(f) > 0 {
			n, _ := strconv.ParseInt(f[0], 10, 64)
			ns += n
		}
	}
	return ns
}

// readStallDiag records what stallDiag printed.
func readStallDiag(m *Measurement, out string) {
	add := func(metric string, v float64) { m.Record(metric, m.values[metric]+v) }
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "some", "full":
			for _, kv := range f[1:] {
				if v, ok := strings.CutPrefix(kv, "total="); ok {
					us, _ := strconv.ParseFloat(v, 64)
					m.Record("psi_mem_"+f[0], us/1000)
				}
			}
		case "pgscan_kswapd", "pgscan_direct":
			add("pgscan", n)
		case "pgsteal_kswapd", "pgsteal_direct":
			add("pgsteal", n)
		case "allocstall_normal", "allocstall_dma32", "allocstall_movable":
			add("allocstall", n)
		case "workingset_refault_file":
			m.Record("refault", n)
		case "pgmajfault", "oom_kill":
			m.Record(f[0], n)
		case "uptime":
			m.Record("guest_uptime", n*1000)
		case "MemFree:":
			m.Record("memfree", n/1024)
		}
	}
}
