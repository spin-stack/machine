// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	xssh "golang.org/x/crypto/ssh"
)

// ledgerName is the edit a tree carries to be accounted for, and the probe that accounts for it.
const ledgerName = "ledger"

// LedgerAppend is the kernel command line a ledger's tree boots with (--append): the scheduler's
// tracepoints recorded from the kernel's start, before any userspace runs, into a ring buffer on
// the kernel's CLOCK_MONOTONIC, so that the trace, the journal and /proc/uptime are one clock. The
// clock is named, not left at its default: with none named, the kernel moves the trace to another
// clock at late_initcall when it finds sched_clock unstable, and the move empties the buffer.
// nooverwrite keeps the boot when the buffer fills and drops what comes after it, the probe's own
// logins. A wakeup and a switch's prev_state are what split a task's wait into runnable (waiting
// for a vCPU) and asleep (waiting for something else).
const LedgerAppend = "trace_event=sched:sched_switch,sched:sched_wakeup,sched:sched_wakeup_new," +
	"sched:sched_process_fork,sched:sched_process_exec,sched:sched_process_exit " +
	"trace_clock=mono trace_options=nooverwrite trace_buf_size=" + ledgerBuffer

// ledgerBuffer is the ring buffer for each online CPU, which the guest takes from its 128 MiB
// before the boot sizes it: four times what a boot fills, and a boot that fills it is missed,
// since the trace then ends before the login.
// v20261122.05's boot to the first login and the probe's markers wrote 8,125 events, 1.2 MB as text
// (lab 45062548343).
const ledgerBuffer = "2M"

// The ledger edit's scripts in the overlay, run by the probe as root after the first login.
const (
	ledgerSync  = "/usr/local/lib/spin-lab/ledger-sync"
	ledgerUnits = "/usr/local/lib/spin-lab/ledger-units"
)

// ledgerEdit is what a ledger's boot needs in its overlay beside its command line: sshd logging at
// DEBUG1, whose lines mark the login's key exchange and authentication in the journal, and the
// probe's two scripts.
//
// ledger-sync answers each line the host sends with a marker in the trace, all shell builtins
// after its start so that a round trip is the transport's and little else. tracefs is mounted
// here because the image masks sys-kernel-tracing.mount (optimize-systemd.sh), and the trace
// records from boot whether or not it is mounted.
//
// ledger-units names a unit for every task it can: a live task's from its cgroup, and a service's
// main process from systemd, which remembers it after the process exits.
var ledgerEdit = Edit{
	Files: map[string]string{
		"/etc/ssh/sshd_config.d/00-ledger.conf": "LogLevel DEBUG1\n",
		ledgerSync: `exec 2>&1
set -e
mountpoint -q /sys/kernel/tracing || mount -t tracefs tracefs /sys/kernel/tracing
test -w /sys/kernel/tracing/trace_marker || { echo "no trace_marker in /sys/kernel/tracing: the kernel is locked down"; exit 1; }
echo ready
while read -r n; do
	echo "` + syncMarker + `$n" > /sys/kernel/tracing/trace_marker
	echo "$n"
done
`,
		ledgerUnits: `for p in /proc/[0-9]*; do
	read -r c < "$p/cgroup" 2>/dev/null && echo "${p#/proc/} ${c#0::}"
done
systemctl show --all --property=Id,ExecMainPID '*' | {
	pid= id=
	while read -r l; do
		case $l in
		ExecMainPID=*) pid=${l#*=} ;;
		Id=*) id=${l#*=} ;;
		'') [ "${pid:-0}" = 0 ] || echo "$pid $id"; pid= id= ;;
		esac
	done
	[ "${pid:-0}" = 0 ] || echo "$pid $id"
}
`,
	},
	Setup: `grep -qE '^Include /etc/ssh/sshd_config.d/\*\.conf' "$MNT/etc/ssh/sshd_config" || { echo "the overlay's sshd reads no sshd_config.d, so it would not log the login's phases" >&2; exit 1; }
test -x "$MNT/usr/bin/mountpoint" && test -x "$MNT` + ledgerCommand + `"
`,
}

// LedgerFiles are the names the ledger probe keeps repetition rep (from 1) of tree under: the
// ledger as a table and as JSON, the trace it was made from, and the boot's journal.
func LedgerFiles(tree string, rep int) (text, data, trace, journal string) {
	name := fmt.Sprintf("%s-%d", tree, rep)
	return name + ".ledger.txt", name + ".ledger.json", name + ".trace.txt", name + ".journal.txt"
}

// ledgerProbe accounts for one boot to the first login, from the moment before QEMU is exec'd: the
// ledger (ledger.go), kept beside result.json with the trace and journal it was made from. It is a
// diagnostic and never a goal number: the trace and sshd's DEBUG1 run beside the boot they record,
// and what that costs is measured (docs/measuring.md, "The ledger").
//
// It needs a kernel that lets the trace be read: the release's forces lockdown at confidentiality,
// which takes tracefs away (kernel/README.md), and a tree boots one built with lockdown forced to
// none (--kernel run:N), which no release ships (#536).
//
// The first login is ssh-first's, on the first connection accepted, with /usr/bin/true for its
// command so that the trace shows where the command started and exited. The host then asks the
// guest for markers in the trace over a second login, to put the kernel's clock on its own, and
// stops the trace and fetches it over later ones.
type ledgerProbe struct{}

func (ledgerProbe) Name() string { return ledgerName }
func (ledgerProbe) About() string {
	return "a boot accounted for from the moment before QEMU is exec'd to the first login, from a scheduler trace from the kernel's start: ledgers kept beside result.json"
}
func (ledgerProbe) Options() []Option     { return shape }
func (ledgerProbe) Rows(Options) []string { return []string{"boot"} }
func (ledgerProbe) Phases() int           { return 1 }

func (ledgerProbe) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "append"}), plugNeeds(o)...)
}

func (ledgerProbe) Metrics() []Metric {
	return []Metric{
		{"first_login", "ms", "the first login, /usr/bin/true, from the moment before QEMU is exec'd, with the trace running"},
		{"kernel_zero", "ms", "where the kernel's clock read 0, from the same moment: QEMU, the firmware and the kernel's decompression"},
		{"kernel_zero_error", "ms", "kernel_zero's uncertainty, half its narrowest bracket"},
		{"pid1", "ms", "PID 1's exec of init, on the kernel's clock"},
		{"connection", "ms", "the login's sshd-session's exec, on the kernel's clock"},
		{"login_end", "ms", "the login's command's exit, on the kernel's clock"},
		{"residue", "ms", "first_login less kernel_zero and login_end: after the command's exit, until the host had the answer"},
		{"unattributed", "ms", "CPU time from PID 1 to the login no unit could be named for"},
		{"breaks", "count", "switches the trace lost events at"},
		{"trace_dropped", "count", "events the full buffer dropped, after the boot or, when the ledger is missed, before its end"},
	}
}

// ledgerTrips is how many markers the host asks for, ledgerSpacing apart. The brackets of the
// kernel's 0 meet in one only while the two clocks run at one rate, so spread over a second they
// are the check that they do as well as the estimate.
const (
	ledgerTrips   = 20
	ledgerSpacing = 50 * time.Millisecond
)

// mutate-lane: TestEveryProbeMeasuresATree
func (ledgerProbe) Measure(_ context.Context, m *Measurement) error {
	if !slices.ContainsFunc(m.Tree.Manifest.Edits, func(e NamedEdit) bool { return e.Name == ledgerName }) || !strings.Contains(m.Tree.Manifest.Append, "trace_event=") {
		return Refuse("tree %s has no ledger edit and trace to account for it: make it with --edit ledger --append %q", m.Tree.Name(), LedgerAppend)
	}
	if m.Keep == "" {
		return fmt.Errorf("the ledger probe keeps its ledgers, and the experiment names no directory to keep them in")
	}
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	cid := NewCID()
	t0 := time.Now()
	qmp := filepath.Join(m.Socks, "m.qmp")
	args := append([]string{"--vsock-cid", fmt.Sprint(cid), "--append", m.Tree.Cmdline()}, m.qmpToPlug(qmp)...)
	mc, err := m.Launch(nil, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	plugged := m.Plug(qmp)
	defer mc.Stop()

	deadline := t0.Add(60 * time.Second)
	var first net.Conn
	for first == nil && time.Now().Before(deadline) {
		if first, err = DialVsock(cid); err != nil {
			if err := mc.Exited(); err != nil {
				return err
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if first == nil {
		return Miss("sshd never accepted on vsock; spin-machine: %s", strings.TrimSpace(mc.Stderr.String()))
	}
	// On the connection accepted, as ssh-first's first login (#450).
	_, why := m.Key.session(first, cid, xssh.KeyExchangeMLKEM768X25519, ledgerCommand)
	for why != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		_, _, why = m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, ledgerCommand)
	}
	if why != nil {
		return Miss("sshd accepted and no login came: %v", why)
	}
	login := time.Since(t0)
	if err := plugged(); err != nil {
		return err
	}
	asRoot := ""
	if m.Key.User() != "root" {
		asRoot = "sudo -n "
	}
	trips, err := m.Key.trips(cid, asRoot+"sh "+ledgerSync, ledgerTrips, t0)
	if err != nil {
		return Miss("no markers for the kernel's clock: %v", err)
	}
	fetched := map[string]string{}
	for _, f := range []struct{ name, command string }{
		{"stats", asRoot + "sh -c 'echo 0 > /sys/kernel/tracing/tracing_on && cat /sys/kernel/tracing/per_cpu/cpu*/stats' 2>&1"},
		{"trace", asRoot + "cat /sys/kernel/tracing/trace"},
		{"journal", asRoot + "journalctl -b -o short-monotonic --no-pager"},
		{"units", asRoot + "sh " + ledgerUnits},
	} {
		if _, fetched[f.name], err = m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, f.command); err != nil {
			return Miss("fetching the %s: %v", f.name, err)
		}
	}
	text, data, trace, journal := LedgerFiles(m.Tree.Name(), m.Rep+1)
	for name, body := range map[string]string{trace: fetched["trace"], journal: fetched["journal"]} {
		if err := os.WriteFile(filepath.Join(m.Keep, name), []byte(body), 0o644); err != nil { //nolint:gosec // for a person, and for Perfetto
			return fmt.Errorf("keeping %s: %w", name, err)
		}
	}
	dropped := traceDropped(fetched["stats"])
	m.Record("trace_dropped", float64(dropped))
	l, err := newLedger(ledgerInput{Trace: fetched["trace"], Journal: fetched["journal"], Units: fetched["units"],
		Trips: trips, FirstLogin: float64(login) / float64(time.Millisecond)})
	if err != nil {
		return Miss("no ledger, %d events dropped by the buffer: %v", dropped, err)
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("writing the ledger: %w", err)
	}
	for name, body := range map[string]string{data: string(b) + "\n", text: l.Text(fmt.Sprintf("%s, boot %d", m.Tree.Name(), m.Rep+1))} {
		if err := os.WriteFile(filepath.Join(m.Keep, name), []byte(body), 0o644); err != nil { //nolint:gosec // for a person to read
			return fmt.Errorf("keeping %s: %w", name, err)
		}
	}
	m.RecordMs("first_login", login)
	m.Record("kernel_zero", l.KernelZero)
	m.Record("kernel_zero_error", l.KernelZeroError)
	m.Record("pid1", l.Windows[1].Start)
	m.Record("connection", l.Windows[2].Start)
	m.Record("login_end", l.LoginEnd)
	m.Record("residue", l.Residue)
	m.Record("unattributed", l.unattributed())
	m.Record("breaks", float64(l.Breaks))
	m.Logf("first login %v: %.1f ms before the kernel's clock (±%.1f), %.1f on it to the command's exit, %.1f residue; kept as %s",
		login.Round(time.Millisecond), l.KernelZero, l.KernelZeroError, l.LoginEnd, l.Residue, text)
	return nil
}

// unattributed is the CPU time from PID 1's exec to the login no unit could be named for.
func (l Ledger) unattributed() float64 {
	var ms float64
	for _, w := range l.Windows[1:] {
		for _, s := range w.CPU {
			if s.Owner == ownerUnknown {
				ms += s.Ms
			}
		}
	}
	return ms
}

// traceDropped is the events the ring buffer dropped once full, summed over its CPUs' stats.
func traceDropped(stats string) int {
	n := 0
	for line := range strings.Lines(stats) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "dropped events:"); ok {
			d, _ := strconv.Atoi(strings.TrimSpace(v))
			n += d
		}
	}
	return n
}

// trips runs command as k.User over one login, a line-for-line answerer, and asks it n times,
// saying when each request left and its answer arrived, in ms from t0.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) trips(cid uint32, command string, n int, t0 time.Time) ([]Trip, error) {
	conn, err := DialVsock(cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	c, chans, reqs, err := xssh.NewClientConn(conn, "vsock", k.config(xssh.KeyExchangeMLKEM768X25519))
	if err != nil {
		return nil, fmt.Errorf("ssh to vsock %d:22: %w", cid, err)
	}
	client := xssh.NewClient(c, chans, reqs)
	defer func() { _ = client.Close() }()
	s, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("opening a session on vsock %d:22: %w", cid, err)
	}
	defer func() { _ = s.Close() }()
	in, err := s.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening %q's input: %w", command, err)
	}
	out, err := s.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening %q's output: %w", command, err)
	}
	if err := s.Start(command); err != nil {
		return nil, fmt.Errorf("running %q on vsock %d:22: %w", command, cid, err)
	}
	r := bufio.NewReader(out)
	if line, err := r.ReadString('\n'); err != nil || line != "ready\n" {
		return nil, fmt.Errorf("%q said %q before it was asked anything: %v", command, line, err)
	}
	ms := func() float64 { return float64(time.Since(t0)) / float64(time.Millisecond) }
	trips := make([]Trip, 0, n)
	for i := 1; i <= n; i++ {
		time.Sleep(ledgerSpacing)
		sent := ms()
		if _, err := fmt.Fprintf(in, "%d\n", i); err != nil {
			return nil, fmt.Errorf("asking %q for marker %d: %w", command, i, err)
		}
		line, err := r.ReadString('\n')
		answered := ms()
		if err != nil || line != fmt.Sprintf("%d\n", i) {
			return nil, fmt.Errorf("%q answered marker %d with %q: %v", command, i, line, err)
		}
		trips = append(trips, Trip{N: i, Sent: sent, Answered: answered})
	}
	_ = in.Close()
	return trips, nil
}

// Detail names the ledgers the probe kept, which result.json does not hold.
func (ledgerProbe) Detail(r *Result) string {
	var b strings.Builder
	b.WriteString("\nledgers, beside result.json:\n")
	for _, s := range r.Samples {
		if s.Missed == "" {
			text, data, trace, journal := LedgerFiles(s.Tree, s.Rep)
			fmt.Fprintf(&b, "  %s, %s, %s, %s\n", text, data, trace, journal)
		}
	}
	return b.String()
}
