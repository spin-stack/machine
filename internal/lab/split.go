// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// split is an experiment, not a goal probe: the restore into a QEMU started ahead, split. QEMU
// runs with its trace on (QMP commands, run states, the migration's states and sections, multifd's
// threads) and host timestamps; the guest's journal and per-task CPU are read after the login and
// before the save. The row says when the checkpoint is taken: right after the first login, as
// prestart and restore-first take it, or after the guest has settled.
type split struct{}

func init() { register(split{}) }

func (split) Name() string { return "split" }
func (split) About() string {
	return "a restore into a QEMU started ahead, traced: running, sshd accepting and the login"
}
func (split) Options() []Option {
	return append(shape, Option{"out", "", "directory for each restore's trace and guest readings"},
		Option{"rows", "leak clean clean-noprobe", "leak: the checkpoint holds the first login's session open, as main's probes left it; clean: it holds none"},
		Option{"probe", "true", "probe sshd accepting before the login, as LoggedIn does"})
}
func (split) Rows(o Options) []string { return o.Fields("rows") }
func (split) Phases() int             { return 2 }
func (split) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "restore"},
		Need{"save", "to"}, Need{"restore", "disk"}), plugNeeds(o)...)
}
func (split) Metrics() []Metric {
	return []Metric{
		{"running", "ms", "`spin-machine restore` returned"},
		{"accept", "ms", "vsock 22 accepted"},
		{"login", "ms", "the first SSH login"},
		{"second", "ms", "a second login right after it"},
		{"state", "MiB", "the state's data"},
		{"q_dial", "ms", "t0 to spin-machine's first QMP command (its exec and dial)"},
		{"q_qmp", "ms", "QMP before migrate-incoming"},
		{"q_header", "ms", "migrate-incoming to the RAM load's start"},
		{"q_ram", "ms", "the RAM load"},
		{"q_devices", "ms", "the device state"},
		{"q_running", "ms", "the device state to the run state running"},
	}
}

// guestReading is what the guest is asked before its save and after its restored login: each
// task's CPU (schedstat's ns on the CPU, and stat's utime+stime+cutime+cstime in ticks), uptime,
// and what systemd is doing.
const guestReading = `for p in /proc/[0-9]*; do
  read run wait n < $p/schedstat 2>/dev/null || continue
  set -- $(cut -d')' -f2 $p/stat 2>/dev/null)
  [ $# -ge 15 ] || continue
  printf 'T %s %s %s %s\n' "${p#/proc/}" "$run" "$(( ${12} + ${13} + ${14} + ${15} ))" "$(cat $p/comm)"
done
echo "U $(cat /proc/uptime)"
echo "S $(head -1 /proc/stat)"
systemctl is-system-running
systemctl list-jobs --no-legend --no-pager
grep -E '^(MemTotal|MemFree|Cached|Buffers|Slab|PageTables|AnonPages|KernelStack|Percpu)' /proc/meminfo
`

// mutate-lane: TestEveryProbeMeasuresATree
func (split) Measure(_ context.Context, m *Measurement) error {
	base := m.Options["out"]
	if base == "" { // the readings are kept only when asked for; the phases are in the result
		base = filepath.Join(m.Dir, "split")
	}
	out := filepath.Join(base, fmt.Sprintf("%s-%s-%d", m.Tree.Name(), strings.ReplaceAll(m.Row, " ", "_"), m.Rep))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if m.Phase == 0 {
		overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
		if err != nil {
			return err
		}
		c := &checkpoint{overlay: overlay, state: filepath.Join(m.Dir, "state"), cid: NewCID()}
		qmp := filepath.Join(m.Socks, "cold.qmp")
		t0 := time.Now()
		mc, err := m.Launch(nil, m.BootArgs(overlay, c.bootFlags(m, qmp)...)...)
		if err != nil {
			return err
		}
		defer mc.Stop()
		plugged := m.Plug(qmp)
		if _, err := m.Key.LoggedIn(mc, c.cid, t0); err != nil {
			return err
		}
		if err := plugged(); err != nil {
			return err
		}
		if m.Row == "settled" {
			time.Sleep(10 * time.Second)
		}
		_, before, err := m.Key.Run(c.cid, xssh.KeyExchangeMLKEM768X25519, guestReading)
		if err != nil {
			return err
		}
		if m.Row == "leak" {
			held, err := holdSession(m.Key, c.cid)
			if err != nil {
				return err
			}
			defer func() { _ = held.Close() }()
		}
		time.Sleep(300 * time.Millisecond) // the reading's own session torn down before the save
		if err := os.WriteFile(filepath.Join(out, "before.txt"), []byte(before), 0o644); err != nil {
			return err
		}
		if err := m.SpinMachine("save", "--qmp", qmp, "--to", c.state); err != nil {
			return err
		}
		<-mc.Done()
		if err := evict(c.state); err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Stat(c.state, &st); err != nil {
			return err
		}
		m.Record("state", float64(st.Blocks*512)/(1<<20))
		m.State = c
		return nil
	}
	c := m.State.(*checkpoint)
	if err := evict(c.state); err != nil {
		return err
	}
	if m.Row == "cached" {
		if _, _, err := readData(c.state); err != nil {
			return err
		}
	}
	rel, err := tracedRelease(m.Tree.Dir, m.Dir)
	if err != nil {
		return err
	}
	qmp := filepath.Join(m.Socks, "prestarted.qmp")
	args := m.BootArgs(c.overlay, c.bootFlags(m, qmp, "--restore")...)
	args[2] = rel // --release
	mc, err := m.Launch(nil, args...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	if err := settled(qmp, "inmigrate"); err != nil {
		return err
	}
	t0 := time.Now()
	marks := fmt.Sprintf("t0 %d\n", t0.UnixMicro())
	if err := m.SpinMachine("restore", "--release", m.Tree.Dir, "--qmp", qmp, "--disk", c.overlay, "--from", c.state); err != nil {
		return err
	}
	m.RecordMs("running", time.Since(t0))
	marks += fmt.Sprintf("running %d\n", time.Now().UnixMicro())
	if m.Options.Bool("probe") && m.Row != "clean-noprobe" {
		for !VsockAccepts(c.cid, 22) {
			time.Sleep(2 * time.Millisecond)
		}
		m.RecordMs("accept", time.Since(t0))
		marks += fmt.Sprintf("accept %d\n", time.Now().UnixMicro())
	}
	for {
		if _, err := m.Key.Login(c.cid, xssh.KeyExchangeMLKEM768X25519); err == nil {
			break
		} else if time.Since(t0) > 30*time.Second {
			return Miss("no login: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.RecordMs("login", time.Since(t0))
	marks += fmt.Sprintf("login %d\n", time.Now().UnixMicro())
	second, err := m.Key.Login(c.cid, xssh.KeyExchangeMLKEM768X25519)
	if err != nil {
		return err
	}
	m.RecordMs("second", second)
	_, after, err := m.Key.Run(c.cid, xssh.KeyExchangeMLKEM768X25519, guestReading+"journalctl -b -o short-monotonic --no-pager | tail -n 150\n")
	if err != nil {
		return err
	}
	for name, b := range map[string]string{"after.txt": after, "marks.txt": marks} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(b), 0o644); err != nil {
			return err
		}
	}
	mc.Stop()
	trace, err := os.ReadFile(filepath.Join(m.Dir, fmt.Sprintf("qemu-%d.log", mc.PID())))
	if err != nil {
		return err
	}
	recordPhases(m, string(trace), t0)
	return os.WriteFile(filepath.Join(out, "qemu.log"), trace, 0o644)
}

// recordPhases records, from QEMU's trace, where the time to running went: ms from t0 to the
// first QMP command after the greeting (spin-machine's exec and dial), to migrate-incoming (the
// QMP before the load), to the RAM load's start (the header), the RAM load, the device state,
// and to the run state running.
func recordPhases(m *Measurement, trace string, t0 time.Time) {
	at := func(name, contains string, after float64) float64 {
		for line := range strings.Lines(trace) {
			f := strings.SplitN(strings.TrimSpace(line), " ", 3)
			if len(f) < 2 || f[1] != name || (contains != "" && (len(f) < 3 || !strings.Contains(f[2], contains))) {
				continue
			}
			ts, err := time.Parse("2006-01-02T15:04:05.000000Z", f[0])
			if err != nil {
				continue
			}
			if ms := float64(ts.Sub(t0)) / float64(time.Millisecond); ms >= after {
				return ms
			}
		}
		return -1
	}
	dial := at("qmp_enter_qmp_capabilities", "", 0)
	incoming := at("qmp_enter_migrate_incoming", "", 0)
	ramStart := at("ram_load_start", "", 0)
	devices := at("qemu_loadvm_state_section_startfull", "(apic)", 0)
	post := at("qemu_loadvm_state_post_main", "", 0)
	running := at("runstate_set", "(running)", 0)
	for _, p := range []struct {
		name     string
		from, to float64
	}{{"q_dial", 0, dial}, {"q_qmp", dial, incoming}, {"q_header", incoming, ramStart}, {"q_ram", ramStart, devices},
		{"q_devices", devices, post}, {"q_running", post, running}} {
		if p.from >= 0 && p.to >= 0 {
			m.Record(p.name, p.to-p.from)
		}
	}
}

// holdSession logs in and keeps the connection open, as every login of main's probes did.
// mutate-lane: TestEveryProbeMeasuresATree
func holdSession(k *Key, cid uint32) (*xssh.Client, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	_ = unix.SetNonblock(fd, false)
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: 22}); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	_ = unix.SetNonblock(fd, true)
	conn := vsockConn{os.NewFile(uintptr(fd), "vsock")}
	c, chans, reqs, err := xssh.NewClientConn(conn, "vsock", &xssh.ClientConfig{
		User: k.User(), Auth: []xssh.AuthMethod{xssh.PublicKeys(k.Signer)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(), Config: xssh.Config{KeyExchanges: []string{xssh.KeyExchangeMLKEM768X25519}},
	})
	if err != nil {
		return nil, err
	}
	client := xssh.NewClient(c, chans, reqs)
	s, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	if err := s.Run("true"); err != nil {
		return nil, err
	}
	return client, nil
}

// traceEvents are the QEMU trace events split turns on.
const traceEvents = `qmp_enter_*
qmp_exit_*
runstate_set
migrate_set_state
migration_*
multifd_recv_*
ram_load_*
loadvm_state_*
qemu_loadvm_state_section_startfull
qemu_loadvm_state_post_main
process_incoming_migration_co_end
flatview_new
vhost_commit
kvm_set_user_memory
`

// tracedRelease is the release at dir with its QEMU run under a trace, written to
// work/qemu-<pid>.log with host timestamps.
func tracedRelease(dir, work string) (string, error) {
	rel := filepath.Join(work, "traced")
	if _, err := os.Stat(rel); err == nil {
		return rel, nil
	}
	if err := os.MkdirAll(filepath.Join(rel, "bin"), 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() == "bin" {
			continue
		}
		if err := os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(rel, e.Name())); err != nil {
			return "", err
		}
	}
	bins, err := os.ReadDir(filepath.Join(dir, "bin"))
	if err != nil {
		return "", err
	}
	for _, e := range bins {
		if e.Name() == "qemu-system-x86_64" {
			continue
		}
		if err := os.Symlink(filepath.Join(dir, "bin", e.Name()), filepath.Join(rel, "bin", e.Name())); err != nil {
			return "", err
		}
	}
	events := filepath.Join(work, "events")
	if err := os.WriteFile(events, []byte(traceEvents), 0o644); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dir, "bin", "qemu-system-x86_64"))
	if err != nil {
		return "", err
	}
	wrapper := fmt.Sprintf("#!/bin/sh\nexec %s -msg timestamp=on -D %s/qemu-$$.log -trace events=%s \"$@\"\n", real, work, events)
	return rel, os.WriteFile(filepath.Join(rel, "bin", "qemu-system-x86_64"), []byte(wrapper), 0o755)
}
