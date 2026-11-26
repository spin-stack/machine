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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
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

// LedgerSuffix names the tree releases measure accounts for a release with, after the release:
// the release on its traceable kernel, with the ledger edit and LedgerAppend.
const LedgerSuffix = "-" + ledgerName

// ledgerProbe accounts for one boot to the first login, from the moment before QEMU is exec'd: the
// ledger (ledger.go), kept beside result.json with the trace and journal it was made from. It is a
// diagnostic and never a goal number: the trace and sshd's DEBUG1 run beside the boot they record,
// and what that costs is measured (docs/measuring.md, "The ledger").
//
// It needs a kernel that lets the trace be read: a release's guest runs lockdown at confidentiality,
// which takes tracefs away (kernel/README.md), and a tree boots the one built beside each release
// with lockdown forced to none (--kernel traceable:TAG), which the release does not ship, and
// whose boots leave lockdown= off the command line (keepUnlocked).
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
		{"kernel_zero", "ms", "where the kernel's clock read 0, from the same moment: QEMU, the firmware and the kernel's entry"},
		{"kernel_zero_error", "ms", "kernel_zero's uncertainty, half its narrowest bracket"},
		{"monotonic_lag", "ms", "CLOCK_MONOTONIC, the trace's and the journal's clock, behind the kernel's sched clock: what it lost on jiffies"},
		{"before_qemu", "ms", "kernel_zero's first part: spin-machine, from the same moment to its exec of QEMU"},
		{"qemu_start", "ms", "kernel_zero's second part: QEMU's start and the held start, from its exec to its KVM_SET_CLOCK as the guest goes to running"},
		{"guest_to_zero", "ms", "kernel_zero's third part: the firmware, the kernel's entry (through PVH, or a bzImage's decompression) and its setup, to the kernel's clock"},
		{"kvmclock_lead", "ms", "kvm-clock's 0, the kernel's 0 less its sched offset, before QEMU's KVM_SET_CLOCK: what KVM's master clock put it back by"},
		{"qemu_exec_error", "ms", "before_qemu's uncertainty, half the bracket the host's polling put QEMU's exec in"},
		{"pid1", "ms", "PID 1's exec of init, on the kernel's clock"},
		{"handoff", "ms", "spin's handoff as login=spin stands in for it, from its exec as PID 1 to its exec of systemd; 0 on the lane"},
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
	kernel, err := m.Tree.Kernel()
	if err != nil {
		return err
	}
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	_, _ = asRootSh(setClockScript(false))
	if _, err := asRootSh(setClockScript(true)); err != nil {
		_, _ = asRootSh(setClockScript(false))
		return fmt.Errorf("tracing QEMU's KVM_SET_CLOCK on the host: %w", err)
	}
	defer func() { _, _ = asRootSh(setClockScript(false)) }()
	cid := NewCID()
	var mono unix.Timespec
	t0 := time.Now()
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono); err != nil {
		return err
	}
	qmp := filepath.Join(m.Socks, "m.qmp")
	args := append([]string{"--vsock-cid", fmt.Sprint(cid), "--append", m.Tree.Cmdline(m.Key.Init())}, m.qmpToPlug(qmp)...)
	mc, err := m.Launch(nil, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	deadline := t0.Add(60 * time.Second)
	execd := make(chan execBracket, 1)
	go func() { execd <- mc.qemuExec(t0, deadline) }()
	plugged := m.Plug(qmp)
	defer mc.Stop()

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
	exec := <-execd
	if !exec.seen {
		return Miss("spin-machine's QEMU was never seen exec'd, so nothing splits the time before the kernel's clock")
	}
	hostTrace, err := asRootSh("cat " + setClockInstance + "/trace")
	if err != nil {
		return err
	}
	setClock, err := setClockAt(hostTrace, float64(mono.Nano())/1e6)
	if err != nil {
		return Miss("%v", err)
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
		Trips: trips, FirstLogin: float64(login) / float64(time.Millisecond), QEMUExecLo: exec.lo, QEMUExecHi: exec.hi,
		SetClock: setClock})
	if err != nil {
		return Miss("no ledger, %d events dropped by the buffer: %v", dropped, err)
	}
	l.PVH = machine.PVH(kernel) == nil
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
	m.Record("monotonic_lag", l.MonotonicLag)
	m.Record("before_qemu", l.QEMUExec)
	m.Record("qemu_start", l.Running-l.QEMUExec)
	m.Record("guest_to_zero", l.KernelZero-l.Running)
	m.Record("kvmclock_lead", l.KVMClockLead)
	m.Record("qemu_exec_error", l.QEMUExecError)
	m.Record("pid1", l.Windows[1].Start)
	handoff := l.window(windowHandoff)
	m.Record("handoff", handoff.End-handoff.Start)
	m.Record("connection", l.window(windowConnection).Start)
	m.Record("login_end", l.LoginEnd)
	m.Record("residue", l.Residue)
	m.Record("unattributed", l.unattributed())
	m.Record("breaks", float64(l.Breaks))
	m.Logf("first login %v: %.1f ms before the kernel's clock (±%.1f), %.1f on it to the command's exit, %.1f residue; kept as %s",
		login.Round(time.Millisecond), l.KernelZero, l.KernelZeroError, l.LoginEnd, l.Residue, text)
	return nil
}

// setClockInstance is the host's tracefs instance that sees QEMU's KVM_SET_CLOCK, which no clock in
// the guest sees: QEMU zeroes kvm-clock with it as the machine goes to running
// (hw/i386/kvm/clock.c, kvmclock_vm_state_change). The ioctl's entry is filtered to that one
// command in the kernel, and stamped on CLOCK_MONOTONIC, the clock Go's time.Since reads.
const setClockInstance = "/sys/kernel/tracing/instances/spin-ledger"

// kvmSetClock is KVM_SET_CLOCK: _IOW(KVMIO, 0x7b, struct kvm_clock_data), 48 bytes.
const kvmSetClock = "4030ae7b"

// setClockScript arms the instance, or with arm false removes it, ignoring what is not there.
func setClockScript(arm bool) string {
	script := setClockDisarm
	if arm {
		script = setClockArm
	}
	return "I=" + setClockInstance + "\n" + script
}

const setClockArm = `set -e
T=/sys/kernel/tracing
mountpoint -q $T || mount -t tracefs tracefs $T
mkdir $I
echo mono > $I/trace_clock
echo 'cmd == 0x4030ae7b' > $I/events/syscalls/sys_enter_ioctl/filter
echo 1 > $I/events/syscalls/sys_enter_ioctl/enable
`

const setClockDisarm = `[ -d $I ] || exit 0
echo 0 > $I/events/syscalls/sys_enter_ioctl/enable
rmdir $I
`

// reSetClock is the instance's line for the ioctl: its CLOCK_MONOTONIC stamp in s. Kernels print
// a syscall's arguments as 0x%lx or %08lx.
var reSetClock = regexp.MustCompile(`\s(\d+\.\d+): sys_ioctl\(fd: (?:0x)?[0-9a-f]+, cmd: (?:0x)?0*` + kvmSetClock + `,`)

// setClockAt is the first KVM_SET_CLOCK in trace at or after t0, both in ms on CLOCK_MONOTONIC, in
// ms from t0.
func setClockAt(trace string, t0 float64) (float64, error) {
	for _, m := range reSetClock.FindAllStringSubmatch(trace, -1) { // mutate-exempt: any n below 0 finds every match
		s, _ := strconv.ParseFloat(m[1], 64) // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
		if at := s*1000 - t0; at >= 0 {
			return at, nil
		}
	}
	return 0, fmt.Errorf("the host's trace has no KVM_SET_CLOCK after QEMU's start, so nothing says when the machine went to running")
}

// execBracket is where QEMU's exec was seen, in ms from the moment before spin-machine's exec: not
// yet at lo, done by hi.
type execBracket struct {
	lo, hi float64
	seen   bool
}

// qemuExec polls spin-machine for its QEMU until QEMU has exec'd, spin-machine has exited or the
// deadline has passed. A process's start time in /proc is in clock ticks, 10 ms each, so the host
// watches instead: a poll reads spin-machine's children and their names, and QEMU's exec is between
// the last poll that did not see it and the first that did.
// mutate-lane: TestEveryProbeMeasuresATree
func (mc *Machine) qemuExec(t0 time.Time, deadline time.Time) execBracket {
	ms := func() float64 { return float64(time.Since(t0)) / float64(time.Millisecond) }
	pid := mc.cmd.Process.Pid
	lo := 0.0
	for time.Now().Before(deadline) {
		before := ms()
		if isQEMU(qemuPID(pid)) {
			return execBracket{lo, ms(), true}
		}
		lo = before
		select {
		case <-mc.done:
			return execBracket{}
		default:
		}
	}
	return execBracket{}
}

// window is the window named name, and the zero Window, of no length, for a ledger without it.
func (l Ledger) window(name string) Window {
	for _, w := range l.Windows {
		if w.Name == name {
			return w
		}
	}
	return Window{}
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
