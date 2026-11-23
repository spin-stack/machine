// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A ledger is one boot's account from the moment before QEMU is exec'd to the first login, every
// stretch of it owned: QEMU and the firmware until the kernel's clock starts, then, from the
// scheduler's trace, each vCPU's time by whose task ran on it and its idle, in windows cut where the
// boot and the login change hands. It is a diagnostic and never a goal number (docs/measuring.md,
// "The ledger").
//
// One clock carries all of it: the kernel's CLOCK_MONOTONIC, which the trace is put on
// (trace_clock=mono), the journal stamps its lines with, and /proc/uptime reads. The host's clock
// meets it through markers the guest writes into the trace on the host's request (Trip): the
// kernel's 0 lies between each request's send, less the marker's time, and its answer's arrival,
// less the same.

// Ledger is what ledger.json holds, in ms. Times on the kernel's clock are from its 0; FirstLogin
// and KernelZero are from the moment before QEMU is exec'd.
type Ledger struct {
	// FirstLogin is the host's: the login's command returned.
	FirstLogin float64 `json:"first_login"`
	// KernelZero is where the kernel's clock read 0 on the host's, and KernelZeroError half the
	// width of the narrowest bracket the trips gave it: before it is QEMU, the firmware and the
	// kernel's decompression, which no clock in the guest sees.
	KernelZero      float64 `json:"kernel_zero"`
	KernelZeroError float64 `json:"kernel_zero_error"`
	// QEMUExec is where spin-machine exec'd QEMU, and QEMUExecError half the bracket the host's
	// polling put it in: before it is spin-machine's own start.
	QEMUExec      float64 `json:"qemu_exec"`
	QEMUExecError float64 `json:"qemu_exec_error"`
	// Running is where the machine went to running and its vCPU ran the guest's first instruction:
	// KernelZero less the kvm-clock the kernel read as it made kvm-clock the scheduler's clock, which
	// QEMU zeroed at that moment (schedOffset). From QEMUExec to Running is QEMU's start and the held
	// start's monitor; from Running to KernelZero, the firmware, the bzImage's decompression and the
	// kernel's setup until its clock runs.
	Running float64 `json:"running"`
	// LoginEnd is the login's command's exit on the kernel's clock: where the trace's account ends.
	LoginEnd float64 `json:"login_end"`
	// Residue is FirstLogin less KernelZero and LoginEnd: what the host waited for after the
	// command exited, sshd's reply crossing vsock and the client reading it.
	Residue float64 `json:"residue"`
	// Breaks are switches away from a task that was not the one the trace had running on that
	// CPU: each is lost events, and a window's account is short by what they hid.
	Breaks int `json:"breaks"`
	// Windows are the stretches the kernel's clock is cut into, in order, from 0 to LoginEnd.
	Windows []Window `json:"windows"`
	// Owners are what each owner's tasks did from PID 1's exec to LoginEnd.
	Owners []States `json:"owners"`
	// Marks are the login's lines in the journal, at journald's receipt of each.
	Marks []Mark `json:"marks"`
}

// Window is a stretch of the kernel's clock, [Start, End), and the CPU time spent in it by owner,
// idle included: on each CPU, the shares add up to the window's length.
type Window struct {
	Name  string  `json:"name"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	CPU   []Share `json:"cpu"`
}

// Share is an owner's CPU time in a window, and its tasks' by the name each ran under.
type Share struct {
	Owner string  `json:"owner"`
	Ms    float64 `json:"ms"`
	Comms []Comm  `json:"comms,omitempty"`
}

// Comm is the CPU time of the tasks that ran under one name.
type Comm struct {
	Comm string  `json:"comm"`
	Ms   float64 `json:"ms"`
}

// States is an owner's tasks' time by what each was doing, summed over its tasks, so two tasks
// waiting at once count twice: running on a CPU, runnable and waiting for one, in uninterruptible
// sleep (disk, mostly), and asleep waiting for something else.
type States struct {
	Owner    string  `json:"owner"`
	Running  float64 `json:"running"`
	Runnable float64 `json:"runnable"`
	Disk     float64 `json:"disk"`
	Sleeping float64 `json:"sleeping"`
}

// Mark is a journal line about the login, on the kernel's clock.
type Mark struct {
	Name string  `json:"name"`
	At   float64 `json:"at"`
	Line string  `json:"line"`
}

// Trip is one marker the host asked the guest to write: when the request left and its answer
// arrived, in ms on the host's clock from the moment before QEMU is exec'd.
type Trip struct {
	N              int
	Sent, Answered float64
}

// The owners that are not units.
const (
	ownerKernel   = "kernel"
	ownerThreads  = "kernel threads"
	ownerIdle     = "idle"
	ownerUnknown  = "unattributed"
	syncMarker    = "ledger-sync "
	ledgerCommand = "/usr/bin/true"
)

// ledgerInput is what the probe fetched from one boot.
type ledgerInput struct {
	Trace, Journal string
	// Units are lines of "PID UNIT" for the tasks the guest could name, read after the login.
	Units      string
	Trips      []Trip
	FirstLogin float64
	// QEMU had not been exec'd at QEMUExecLo and had by QEMUExecHi, in ms on the host's clock from
	// the moment before spin-machine's exec.
	QEMUExecLo, QEMUExecHi float64
}

// newLedger is one boot's ledger, or why the trace cannot give one.
func newLedger(in ledgerInput) (Ledger, error) {
	events, err := parseTrace(in.Trace)
	if err != nil {
		return Ledger{}, err
	}
	b := replay(events)
	zero, zeroErr, err := b.kernelZero(in.Trips)
	if err != nil {
		return Ledger{}, err
	}
	offset, err := schedOffset(in.Journal)
	if err != nil {
		return Ledger{}, err
	}
	exec, execErr, running := (in.QEMUExecLo+in.QEMUExecHi)/2, (in.QEMUExecHi-in.QEMUExecLo)/2, zero-offset
	if running < exec-execErr-zeroErr {
		return Ledger{}, fmt.Errorf("the machine ran at %.3f ms, before QEMU's exec at %.3f ± %.3f: the kernel's sched offset of %.3f ms does not put kvm-clock's 0 on the host's clock", running, exec, execErr, offset)
	}
	cuts, end, err := b.cuts()
	if err != nil {
		return Ledger{}, err
	}
	owner := b.owners(parseUnits(in.Units))
	l := Ledger{FirstLogin: in.FirstLogin, KernelZero: zero, KernelZeroError: zeroErr,
		QEMUExec: exec, QEMUExecError: execErr, Running: running, LoginEnd: end,
		Residue: in.FirstLogin - zero - end, Breaks: b.breaks, Marks: loginMarks(in.Journal)}
	for i, c := range cuts {
		to := end
		if i+1 < len(cuts) {
			to = cuts[i+1].at
		}
		l.Windows = append(l.Windows, Window{Name: c.name, Start: c.at, End: to, CPU: b.cpuIn(c.at, to, owner)})
	}
	l.Owners = b.statesIn(cuts[1].at, end, owner)
	return l, nil
}

// schedEvent is one line of the trace.
type schedEvent struct {
	at   float64 // ms on the kernel's clock
	cpu  int
	kind string // switch, wakeup, fork, exec, exit, mark
	// pid is whom the event is about: a switch's next, the task woken, a fork's child, the task
	// that exec'd or exited. other is a switch's prev and a fork's parent.
	pid, other int
	// comm is a switch's next_comm and otherComm its prev_comm, and state is prev's. An exec's
	// file is what it exec'd; a mark's text is what was written.
	comm, otherComm, state, file, text string
}

var (
	traceLine   = regexp.MustCompile(`^\s*.+-(\d+)\s+(?:\(.*?\)\s+)?\[(\d+)\]\s+\S+\s+(\d+\.\d+): (\w+): (.*)$`)
	traceSwitch = regexp.MustCompile(`^prev_comm=(.*) prev_pid=(\d+) prev_prio=-?\d+ prev_state=(\S+) ==> next_comm=(.*) next_pid=(\d+) next_prio=-?\d+`)
	traceWakeup = regexp.MustCompile(`^comm=.* pid=(\d+) prio=-?\d+ target_cpu=\d+`)
	traceFork   = regexp.MustCompile(`^comm=.* pid=(\d+) child_comm=.* child_pid=(\d+)`)
	traceExec   = regexp.MustCompile(`^filename=(.*) pid=(\d+) old_pid=\d+`)
	traceExit   = regexp.MustCompile(`^comm=.* pid=(\d+) prio=-?\d+`)
)

// parseTrace reads the trace file's text: the ftrace format, which Perfetto opens as it is.
// Events the ledger does not read are skipped; a line that is none of them is refused, since a
// format that moved would otherwise read as a boot with nothing in it.
func parseTrace(text string) ([]schedEvent, error) {
	var events []schedEvent
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := traceLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("the trace has a line that is not an ftrace event: %q", line)
		}
		cpu, _ := strconv.Atoi(m[2])
		s, _ := strconv.ParseFloat(m[3], 64) // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
		e := schedEvent{at: s * 1000, cpu: cpu}
		body := m[5]
		var f []string
		switch m[4] {
		case "sched_switch":
			if f = traceSwitch.FindStringSubmatch(body); f != nil {
				e.kind, e.otherComm, e.other, e.state, e.comm, e.pid = "switch", f[1], atoi(f[2]), f[3], f[4], atoi(f[5])
			}
		case "sched_wakeup", "sched_wakeup_new":
			if f = traceWakeup.FindStringSubmatch(body); f != nil {
				e.kind, e.pid = "wakeup", atoi(f[1])
			}
		case "sched_process_fork":
			if f = traceFork.FindStringSubmatch(body); f != nil {
				e.kind, e.other, e.pid = "fork", atoi(f[1]), atoi(f[2])
			}
		case "sched_process_exec":
			if f = traceExec.FindStringSubmatch(body); f != nil {
				e.kind, e.file, e.pid = "exec", f[1], atoi(f[2])
			}
		case "sched_process_exit":
			if f = traceExit.FindStringSubmatch(body); f != nil {
				e.kind, e.pid = "exit", atoi(f[1])
			}
		case "tracing_mark_write":
			e.kind, e.text, f = "mark", body, m
		default:
			continue
		}
		if f == nil {
			return nil, fmt.Errorf("the trace's %s has fields the ledger does not know: %q", m[4], body)
		}
		events = append(events, e)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("the trace holds no scheduler events in its %d bytes", len(text))
	}
	return events, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ranOn is a task on a CPU, [from, to), under the name it ran under.
type ranOn struct {
	cpu, pid int
	comm     string
	from, to float64
}

// wait is a task off every CPU, [from, to), in state: runnable, disk or sleeping.
type wait struct {
	pid      int
	state    string
	from, to float64
}

// execAt is a task's exec of file at a time.
type execAt struct {
	at   float64
	file string
}

// boot is the trace replayed: who ran where, who waited for what, and the process tree.
type boot struct {
	runs   []ranOn
	waits  []wait
	parent map[int]int
	forked map[int]float64
	execs  map[int][]execAt
	exited map[int]float64
	marks  map[string]float64
	breaks int
	// cpu0 is the first switch on CPU 0: until it, PID 0 is the kernel starting, not idle. A trace
	// with no switch on CPU 0 leaves it infinite, and all of PID 0's time the kernel's.
	cpu0 float64
}

// replay walks the events in order. Each CPU's time goes to the task the last switch put on it
// until the next switch, and CPU 0's from the kernel's 0, so a CPU's runs tile its time; a run is
// cut at an exec as well, so that each part is under the name it ran under. A task's time off the
// CPUs is in the state its switch away left it in, until a wakeup makes it runnable.
func replay(events []schedEvent) *boot {
	b := &boot{parent: map[int]int{}, forked: map[int]float64{}, execs: map[int][]execAt{}, exited: map[int]float64{}, marks: map[string]float64{}, cpu0: math.Inf(1)} // mutate-exempt: Inf reads only its argument's sign
	type onCPU struct {
		pid   int
		comm  string
		since float64
	}
	cpus := map[int]*onCPU{0: {comm: "swapper/0"}}
	type off struct {
		state string
		since float64
	}
	offs := map[int]off{}
	leave := func(pid int, at float64) {
		if o, ok := offs[pid]; ok {
			b.waits = append(b.waits, wait{pid, o.state, o.since, at})
			delete(offs, pid)
		}
	}
	var end float64
	for _, e := range events {
		end = e.at
		c := cpus[e.cpu]
		switch e.kind {
		case "switch":
			if c == nil {
				c = &onCPU{pid: e.other, since: e.at}
				cpus[e.cpu] = c
			}
			if c.pid != e.other {
				b.breaks++
			}
			b.runs = append(b.runs, ranOn{e.cpu, e.other, e.otherComm, c.since, e.at})
			if e.cpu == 0 {
				b.cpu0 = min(b.cpu0, e.at)
			}
			if e.other != 0 {
				switch {
				case strings.HasPrefix(e.state, "R"):
					offs[e.other] = off{"runnable", e.at}
				case strings.HasPrefix(e.state, "D"):
					offs[e.other] = off{"disk", e.at}
				case strings.HasPrefix(e.state, "X"), strings.HasPrefix(e.state, "Z"):
				default:
					offs[e.other] = off{"sleeping", e.at}
				}
			}
			leave(e.pid, e.at)
			c.pid, c.comm, c.since = e.pid, e.comm, e.at
		case "wakeup":
			if o, ok := offs[e.pid]; !ok || o.state != "runnable" {
				leave(e.pid, e.at)
				offs[e.pid] = off{"runnable", e.at}
			}
		case "fork":
			b.parent[e.pid], b.forked[e.pid] = e.other, e.at
		case "exec":
			b.execs[e.pid] = append(b.execs[e.pid], execAt{e.at, e.file})
			if c != nil && c.pid == e.pid {
				b.runs = append(b.runs, ranOn{e.cpu, e.pid, c.comm, c.since, e.at})
				c.since = e.at
			}
		case "exit":
			b.exited[e.pid] = e.at
		case "mark":
			if _, seen := b.marks[e.text]; !seen {
				b.marks[e.text] = e.at
			}
		}
	}
	for cpu, c := range cpus {
		b.runs = append(b.runs, ranOn{cpu, c.pid, c.comm, c.since, end})
	}
	for pid := range offs {
		leave(pid, end)
	}
	return b
}

// kernelZero is where the kernel's clock read 0 on the host's, from the trips: the marker for
// each was written after its request left and before its answer arrived, so 0 is no earlier than
// Sent less the marker's time and no later than Answered less it. The trips' brackets meet in one,
// and its middle is the estimate, give or take half its width.
func (b *boot) kernelZero(trips []Trip) (zero, halfWidth float64, err error) {
	lo, hi, n := math.Inf(-1), math.Inf(1), 0 // mutate-exempt: Inf reads only its argument's sign
	for _, t := range trips {
		at, ok := b.marks[fmt.Sprintf("%s%d", syncMarker, t.N)]
		if !ok {
			continue
		}
		lo, hi = max(lo, t.Sent-at), min(hi, t.Answered-at)
		n++
	}
	if n == 0 {
		return 0, 0, fmt.Errorf("the trace has none of the %d markers the host asked for: the buffer filled before them", len(trips))
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("the host's clock and the kernel's disagree: the markers put the kernel's 0 no earlier than %.3f ms and no later than %.3f", lo, hi)
	}
	return (lo + hi) / 2, (hi - lo) / 2, nil
}

// schedOffset is the kvm-clock the kernel read as it made kvm-clock the scheduler's clock, in ms:
// QEMU zeroes kvm-clock as the machine goes to running (hw/i386/kvm/clock.c,
// kvmclock_vm_state_change, KVM_SET_CLOCK with the 0 a machine never run has), and the kernel's
// clock, CLOCK_MONOTONIC's 0 included, starts from that reading (arch/x86/kernel/kvmclock.c,
// kvm_sched_clock_init, which prints it in ns despite the word; timekeeping_init's boot offset is
// local_clock()). boot:firmware-stages places the kernel's clock on QEMU's trace by the same line.
func schedOffset(journal string) (float64, error) {
	m := reSchedOffset.FindStringSubmatch(journal)
	if m == nil {
		return 0, fmt.Errorf("the journal has no \"kvm-clock: using sched offset\" line: a kernel not on kvm-clock, or a journal that lost the kernel's first lines, puts nothing between QEMU's exec and the kernel's 0")
	}
	ns, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("reading the kernel's sched offset %q: %w", m[1], err)
	}
	return float64(ns) / 1e6, nil
}

var reSchedOffset = regexp.MustCompile(`kvm-clock: using sched offset of (\d+) cycles`)

// The windows the ledger probe records by name.
const (
	windowHandoff    = "spin's handoff, to PID 1's exec of systemd"
	windowConnection = "sshd-session, to sshd-auth"
)

// cut is where a window starts, and what the window is.
type cut struct {
	name string
	at   float64
}

// cuts are where the boot and the login change hands on the kernel's clock, each found in the
// process tree the trace recorded, and where the login's command exited, which ends the account.
// A cut the login did not make (a release whose sshd has no sshd-auth) is left out, and the window
// before it runs on to the next.
func (b *boot) cuts() ([]cut, float64, error) {
	inits := b.execs[1]
	if len(inits) == 0 {
		return nil, 0, fmt.Errorf("the trace has no exec by PID 1: it did not start at the kernel's start")
	}
	pid1 := inits[0].at
	conn, connPID, ok := b.execOf("/sshd-session", pid1, 1)
	if !ok {
		return nil, 0, fmt.Errorf("the trace has no sshd-session after PID 1: it ended before the login")
	}
	login, loginPID, ok := b.execOf(ledgerCommand, conn, connPID)
	if !ok {
		return nil, 0, fmt.Errorf("the trace has no %s from the login's sshd-session: it ended before the login's command", ledgerCommand)
	}
	end, ok := b.exited[loginPID]
	if !ok {
		return nil, 0, fmt.Errorf("the trace has the login's command and not its exit: it ended inside the login")
	}
	cuts := []cut{{"the kernel, to PID 1's exec of init", 0}}
	// On spin's path PID 1 is first the lab's stand-in for spin's handoff, which execs systemd
	// (login.go, spinHandoff): its own window, so that its cost is not read as systemd's.
	if len(inits) > 1 && strings.HasSuffix(inits[0].file, spinHandoffInit) {
		cuts = append(cuts, cut{windowHandoff, pid1})
		pid1 = inits[1].at
	}
	cuts = append(cuts, cut{"PID 1, to the login's sshd-session", pid1}, cut{windowConnection, conn})
	if auth, authPID, ok := b.execOf("/sshd-auth", conn, connPID); ok {
		cuts = append(cuts, cut{"the key exchange and authentication, in sshd-auth", auth})
		if done, ok := b.exited[authPID]; ok {
			cuts = append(cuts, cut{"authenticated: PAM's session, logind, the user's manager", done})
		}
	}
	// The command's task descends from sshd-session, so the trace has its fork.
	cuts = append(cuts, cut{"the session's process, to the user's shell", b.forked[loginPID]})
	// The shell execs the command in its own process, so the process's first exec is the shell's
	// unless the command is all it exec'd.
	if shell := b.execs[loginPID][0].at; shell < login {
		cuts = append(cuts, cut{"the shell, to the login's command", shell})
	}
	cuts = append(cuts, cut{"the login's command, to its exit", login})
	return cuts, end, nil
}

// execOf is the first exec of a file ending in suffix at or after from by a task under ancestor,
// and the task that made it.
func (b *boot) execOf(suffix string, from float64, ancestor int) (float64, int, bool) {
	var first *execAt
	var who int
	for pid, es := range b.execs {
		for i, e := range es {
			if e.at >= from && strings.HasSuffix(e.file, suffix) && b.descends(pid, ancestor) && (first == nil || e.at < first.at) {
				first, who = &es[i], pid
			}
		}
	}
	if first == nil {
		return 0, 0, false // mutate-exempt: a caller reads neither number when the bool is false
	}
	return first.at, who, true
}

// descends is whether pid is ancestor or forked from it, however far down.
func (b *boot) descends(pid, ancestor int) bool {
	for seen := 0; seen <= len(b.parent); seen++ { // mutate-exempt: a bound against a cycle only a reused pid could make, which no boot's trace has
		if pid == ancestor {
			return true
		}
		p, ok := b.parent[pid]
		if !ok {
			return false
		}
		pid = p
	}
	return false
}

// parseUnits reads ledger-units' lines, "PID UNIT" or "PID CGROUP": the unit each task the guest
// could name after the login belongs to, the first line for a task its own. A cgroup's unit is the
// first service or scope on its path, so the user's manager owns what it starts.
func parseUnits(text string) map[int]string {
	units := map[int]string{}
	for line := range strings.Lines(text) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if _, seen := units[pid]; err != nil || seen {
			continue
		}
		unit := f[1]
		if strings.HasPrefix(unit, "/") {
			unit = ""
			for _, part := range strings.Split(f[1], "/") {
				if strings.HasSuffix(part, ".service") || strings.HasSuffix(part, ".scope") {
					unit = part
					break
				}
			}
		}
		if unit != "" {
			units[pid] = unit
		}
	}
	return units
}

// owners is whose a task's time is, at a time: PID 0's on CPU 0 is the kernel starting until the
// first switch and idle after it, PID 1's is the kernel's until it execs init, a kernel thread's
// (kthreadd and what it forked) is the kernel's threads', and every other task's
// is its unit, the one the guest named or else its parent's. A task forked by PID 1 that the
// guest could not name is unattributed: systemd moves its children into their units' cgroups, so
// PID 1's own is not theirs.
func (b *boot) owners(units map[int]string) func(pid int, at float64) string {
	pid1 := math.Inf(1) // mutate-exempt: Inf reads only its argument's sign
	if inits := b.execs[1]; len(inits) > 0 {
		pid1 = inits[0].at
	}
	known := map[int]string{}
	var unitOf func(pid int) string
	unitOf = func(pid int) string {
		if u, ok := known[pid]; ok {
			return u
		}
		u, ok := units[pid]
		switch {
		case ok:
		case pid == 2:
			u = ownerThreads
		case b.parent[pid] == 0 || b.parent[pid] == 1:
			u = ownerUnknown
		default:
			// A task's parent is forked before it, so the walk ends.
			u = unitOf(b.parent[pid])
		}
		known[pid] = u
		return u
	}
	return func(pid int, at float64) string {
		switch {
		case pid == 0 && at < b.cpu0:
			return ownerKernel
		case pid == 0:
			return ownerIdle
		case pid == 1 && at < pid1:
			return ownerKernel
		}
		return unitOf(pid)
	}
}

// cpuIn is each owner's CPU time in [from, to), its tasks' by name, the largest first.
func (b *boot) cpuIn(from, to float64, owner func(int, float64) string) []Share {
	ms := map[string]map[string]float64{}
	for _, r := range b.runs {
		d := min(r.to, to) - max(r.from, from)
		if d <= 0 {
			continue
		}
		o := owner(r.pid, r.from)
		if ms[o] == nil {
			ms[o] = map[string]float64{}
		}
		ms[o][r.comm] += d
	}
	var shares []Share
	for o, comms := range ms {
		s := Share{Owner: o}
		for c, d := range comms {
			s.Ms += d
			s.Comms = append(s.Comms, Comm{c, d})
		}
		slices.SortFunc(s.Comms, func(a, b Comm) int { return cmp.Or(cmp.Compare(b.Ms, a.Ms), cmp.Compare(a.Comm, b.Comm)) })
		shares = append(shares, s)
	}
	slices.SortFunc(shares, func(a, b Share) int { return cmp.Or(cmp.Compare(b.Ms, a.Ms), cmp.Compare(a.Owner, b.Owner)) })
	return shares
}

// statesIn is each owner's tasks' time in [from, to) by state, the most running first, idle
// left out: it is no task's.
func (b *boot) statesIn(from, to float64, owner func(int, float64) string) []States {
	by := map[string]*States{}
	add := func(o string) *States {
		if by[o] == nil {
			by[o] = &States{Owner: o}
		}
		return by[o]
	}
	for _, r := range b.runs {
		if d := min(r.to, to) - max(r.from, from); d > 0 && r.pid != 0 {
			add(owner(r.pid, r.from)).Running += d
		}
	}
	for _, w := range b.waits {
		d := min(w.to, to) - max(w.from, from)
		if d <= 0 {
			continue
		}
		s := add(owner(w.pid, w.from))
		switch w.state {
		case "runnable":
			s.Runnable += d
		case "disk":
			s.Disk += d
		default:
			s.Sleeping += d
		}
	}
	var out []States
	for _, o := range slices.Sorted(maps.Keys(by)) {
		out = append(out, *by[o])
	}
	slices.SortStableFunc(out, func(a, b States) int { return cmp.Compare(b.Running, a.Running) })
	return out
}

// journalLine is a line of `journalctl -o short-monotonic`.
var journalLine = regexp.MustCompile(`^\[\s*(\d+\.\d+)\] \S+ (.*)$`)

// loginPhases are the journal's lines that mark the first login's phases, the first of each: sshd
// at LogLevel DEBUG1 (the ledger edit) and logind. Each is at journald's receipt of it, which on a
// busy vCPU is after the line was written, so a mark says no earlier than, and the trace's cuts
// are where the account's windows are cut.
var loginPhases = []struct{ name, text string }{
	{"connection", "Connection from "},
	{"key exchange offered", "SSH2_MSG_KEXINIT sent"},
	{"keys in use", "SSH2_MSG_NEWKEYS received"},
	{"authenticated", "Accepted publickey for "},
	{"PAM session opened", "pam_unix(sshd:session): session opened"},
	{"logind's session", "with class 'user"},
	{"the command started", "Starting session: command"},
}

// loginMarks are the first journal line of each login phase, in the order they came.
func loginMarks(journal string) []Mark {
	var marks []Mark
	for _, p := range loginPhases {
		for line := range strings.Lines(journal) {
			m := journalLine.FindStringSubmatch(strings.TrimRight(line, "\n"))
			if m != nil && strings.Contains(m[2], p.text) {
				s, _ := strconv.ParseFloat(m[1], 64) // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
				marks = append(marks, Mark{p.name, s * 1000, m[2]})
				break
			}
		}
	}
	slices.SortStableFunc(marks, func(a, b Mark) int { return cmp.Compare(a.At, b.At) })
	return marks
}

// Text is the ledger as a table for a person: each window with its owners' CPU, the largest
// first and those under 0.5 ms summed, then the owners' states over the boot to the login.
func (l Ledger) Text(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: from the moment before QEMU is exec'd to the first login, ms\n\n", title)
	fmt.Fprintf(&b, "%8s %8s %8s  %s\n", "start", "end", "ms", "window, on the kernel's clock")
	fmt.Fprintf(&b, "%8s %8s %8.1f  before the kernel's clock (±%.2f)\n", "", "0.0", l.KernelZero, l.KernelZeroError)
	fmt.Fprintf(&b, "%26.1f  spin-machine, to its exec of QEMU (±%.2f)\n", l.QEMUExec, l.QEMUExecError)
	fmt.Fprintf(&b, "%26.1f  QEMU's start and the held start, to the guest's first instruction\n", l.Running-l.QEMUExec)
	fmt.Fprintf(&b, "%26.1f  the firmware, the decompression and the kernel's setup, to the kernel's clock\n", l.KernelZero-l.Running)
	for _, w := range l.Windows {
		fmt.Fprintf(&b, "%8.1f %8.1f %8.1f  %s\n", w.Start, w.End, w.End-w.Start, w.Name)
		var rest float64
		var others int
		for _, s := range w.CPU {
			if s.Ms < 0.5 {
				rest += s.Ms
				others++
				continue
			}
			fmt.Fprintf(&b, "%26.1f  %s%s\n", s.Ms, s.Owner, comms(s))
		}
		if others > 0 {
			fmt.Fprintf(&b, "%26.1f  %d others under 0.5 ms each\n", rest, others)
		}
	}
	fmt.Fprintf(&b, "%8.1f %8s %8.1f  after the command's exit, until the host had its answer\n", l.LoginEnd, "", l.Residue)
	fmt.Fprintf(&b, "\n%.1f before the kernel's clock + %.1f to the command's exit + %.1f residue = %.1f, the first login the host measured", l.KernelZero, l.LoginEnd, l.Residue, l.FirstLogin)
	if l.Breaks > 0 {
		fmt.Fprintf(&b, "\nthe trace lost events at %d switches: the windows' CPU is short by what they hid", l.Breaks)
	}
	if len(l.Windows) > 1 {
		fmt.Fprintf(&b, "\n\nowners from PID 1's exec (%.1f) to the command's exit, summed over their tasks, ms\n", l.Windows[1].Start)
		fmt.Fprintf(&b, "%9s %9s %9s %9s  %s\n", "running", "runnable", "disk", "sleeping", "owner")
		for _, s := range l.Owners {
			if s.Running < 0.5 {
				continue
			}
			fmt.Fprintf(&b, "%9.1f %9.1f %9.1f %9.1f  %s\n", s.Running, s.Runnable, s.Disk, s.Sleeping, s.Owner)
		}
	}
	if len(l.Marks) > 0 {
		b.WriteString("\nthe login in the journal, at journald's receipt of each line\n")
		for _, m := range l.Marks {
			fmt.Fprintf(&b, "%8.1f  %s: %s\n", m.At, m.Name, m.Line)
		}
	}
	return b.String()
}

// comms is a share's three busiest names, when it ran under more than one.
func comms(s Share) string {
	if len(s.Comms) < 2 {
		return ""
	}
	var parts []string
	for _, c := range s.Comms[:min(3, len(s.Comms))] {
		parts = append(parts, fmt.Sprintf("%s %.1f", c.Comm, c.Ms))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
