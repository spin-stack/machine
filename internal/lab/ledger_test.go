// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"compress/gzip"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A line that is not an event, or an event whose fields moved, refuses the trace rather than
// reading as a boot with nothing in it.
func TestATraceInAnotherFormatIsRefused(t *testing.T) {
	for _, text := range []string{
		"",
		"# tracer: nop\n#\n",
		"not a trace line\n",
		"          <idle>-0       [000] d..2.     0.081234: sched_switch: prev_comm=swapper/0 moved\n",
		"         systemd-1       [000] .....     0.091000: sched_process_exec: file=/sbin/init\n",
	} {
		if _, err := parseTrace(text); err == nil {
			t.Errorf("parseTrace(%q) took it", text)
		}
	}
	// The refusal names the event whose fields moved.
	if _, err := parseTrace("    sshd-77 [000] .....  0.200000: sched_process_fork: parent=77 child=94\n"); err == nil || !strings.Contains(err.Error(), "sched_process_fork has fields") {
		t.Errorf("a fork in another format: %v", err)
	}
}

// The kernel's 0 is where every marker's bracket agrees; markers that put it in two places are a
// clock the ledger cannot use, and none is a buffer that filled before them.
func TestTheKernelsZeroIsWhereTheMarkersAgree(t *testing.T) {
	b := &boot{marks: map[string]float64{syncMarker + "1": 1000, syncMarker + "2": 1100}}
	zero, half, err := b.kernelZero([]Trip{{1, 1040, 1046}, {2, 1142, 1150}, {3, 1200, 1201}})
	// 1: [40, 46]; 2: [42, 50]; 3 has no marker. They meet in [42, 46].
	if err != nil || zero != 44 || half != 2 {
		t.Errorf("kernelZero = %v ± %v, %v; want 44 ± 2", zero, half, err)
	}
	if _, _, err := b.kernelZero([]Trip{{1, 1040, 1041}, {2, 1145, 1150}}); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("brackets [40, 41] and [45, 50] gave %v", err)
	}
	if _, _, err := b.kernelZero([]Trip{{3, 1, 2}}); err == nil || !strings.Contains(err.Error(), "none of the 1 markers") {
		t.Errorf("no marker gave %v", err)
	}
}

// A task's unit is the first service or scope on its cgroup's path, or the unit systemd named
// for it, and the first line for a task wins.
func TestATasksUnitIsItsCgroupsFirstServiceOrScope(t *testing.T) {
	units := parseUnits(`1 /init.scope
2 /
230 /system.slice/systemd-udevd.service/udev
301 /user.slice/user-0.slice/user@0.service/init.scope
230 systemd-journald.service
180 systemd-tmpfiles-setup.service
x systemd-logind.service
three fields here
`)
	want := map[int]string{1: "init.scope", 230: "systemd-udevd.service", 301: "user@0.service", 180: "systemd-tmpfiles-setup.service"}
	if len(units) != len(want) {
		t.Errorf("units = %v; want %v", units, want)
	}
	for pid, u := range want {
		if units[pid] != u {
			t.Errorf("units[%d] = %q; want %q", pid, units[pid], u)
		}
	}
}

func TestTheBuffersDroppedEventsAreSummedOverItsCPUs(t *testing.T) {
	stats := "entries: 10\noverrun: 0\ndropped events: 3\nread events: 0\nentries: 2\ndropped events: 4\n"
	if n := traceDropped(stats); n != 7 {
		t.Errorf("traceDropped = %d; want 7", n)
	}
}

// ledgerFixture is a real boot's trace from the kernel's start to the login's command's exit, and
// the first three markers (lab 45062548343, bench, spin's shape, v20261122.05 with a kernel that
// can be traced), with its journal's login lines.
func ledgerFixture(t *testing.T) ledgerInput {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "ledger-45062548343.trace.txt.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join("testdata", "ledger-45062548343.journal.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return ledgerInput{
		// The run's journal kept only the login's lines; the kernel's two lines are as its kernel
		// prints them, with an offset that puts the machine's running at 50.0215, and
		// CLOCK_MONOTONIC 20 ms behind the sched clock.
		Trace: string(trace), Journal: "[    0.000000] localhost kernel: kvm-clock: using sched offset of 30000000 cycles\n" +
			"[    0.000000] localhost kernel: sched_clock: Marking stable (60000000, 5000000)->(80000000, -15000000)\n" + string(journal),
		// sshd (ssh.service), logind, journald, PID 1 and root's manager, as ledger-units prints them.
		Units: "1 /init.scope\n61 /system.slice/systemd-journald.service\n68 /system.slice/systemd-logind.service\n77 /system.slice/ssh.service\n97 /user.slice/user-0.slice/user@0.service/init.scope\n",
		// The markers are at 357.045, 357.297 and 357.412 ms on CLOCK_MONOTONIC, 20 more on the
		// kernel's clock: these put its 0 in [79.955, 80.055], [79.953, 80.103] and [79.988, 80.088]
		// on the host's.
		Trips:      []Trip{{1, 457.000, 457.100}, {2, 457.250, 457.400}, {3, 457.400, 457.500}},
		FirstLogin: 420,
		QEMUExecLo: 17.9, QEMUExecHi: 18.1,
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// A real boot's ledger: its windows tile the kernel's clock from 0 to the command's exit, cut
// where the trace shows the boot and the login change hands, each the trace's time moved forward
// by CLOCK_MONOTONIC's lag; on its one vCPU each window's CPU adds up to its length; and the
// host's clock, the kernel's and the residue add up to the first login the host measured.
func TestARealBootsLedgerAddsUp(t *testing.T) {
	l, err := newLedger(ledgerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// The cuts, read from the trace by hand: PID 1's exec of /sbin/init, sshd-session's exec,
	// sshd-auth's exec and exit, the fork of the login's process, its exec of bash and of
	// /usr/bin/true, and true's exit.
	want := []float64{0, 103.224, 272.151, 286.672, 316.796, 330.239, 332.642, 338.157}
	if len(l.Windows) != len(want) {
		t.Fatalf("%d windows; want %d: %+v", len(l.Windows), len(want), l.Windows)
	}
	for i, w := range l.Windows {
		end := 338.820
		if i+1 < len(want) {
			end = want[i+1]
		}
		if !near(w.Start, want[i]) || !near(w.End, end) {
			t.Errorf("window %q is [%v, %v); want [%v, %v)", w.Name, w.Start, w.End, want[i], end)
		}
		var sum float64
		for _, s := range w.CPU {
			sum += s.Ms
		}
		if !near(sum, w.End-w.Start) {
			t.Errorf("window %q: its CPU adds up to %v of its %v", w.Name, sum, w.End-w.Start)
		}
	}
	if !near(l.KernelZero, 80.0215) || !near(l.KernelZeroError, 0.0335) || !near(l.MonotonicLag, 20) || !near(l.LoginEnd, 338.82) || !near(l.Residue, 420-80.0215-338.82) || l.Breaks != 0 {
		t.Errorf("kernel's 0 %v ± %v, lag %v, login end %v, residue %v, breaks %d", l.KernelZero, l.KernelZeroError, l.MonotonicLag, l.LoginEnd, l.Residue, l.Breaks)
	}
	// The span before the kernel's clock splits at QEMU's exec, the host's bracket's middle, and at
	// the machine's running, the kernel's 0 less its sched offset; the three parts add up to it.
	before, qemu, guest := l.QEMUExec, l.Running-l.QEMUExec, l.KernelZero-l.Running
	if !near(before, 18) || !near(l.QEMUExecError, 0.1) || !near(qemu, 32.0215) || !near(guest, 30) {
		t.Errorf("before QEMU %v ± %v, QEMU to the guest %v, the guest to the kernel's 0 %v", before, l.QEMUExecError, qemu, guest)
	}
	if sum := before + qemu + guest; math.Abs(sum-l.KernelZero) > 0.15 {
		t.Errorf("the parts add up to %v of the kernel's 0 at %v", sum, l.KernelZero)
	}
	// true ran alone from its exec to its exit, and is sshd's: forked from the login's
	// sshd-session, which sshd forked.
	last := l.Windows[len(l.Windows)-1].CPU
	if len(last) != 1 || last[0].Owner != "ssh.service" || !near(last[0].Ms, 0.663) || last[0].Comms[0].Comm != "true" {
		t.Errorf("the command's window: %+v", last)
	}
	// Before PID 1 execs init its time is the kernel's, and kthreadd's children are the kernel's
	// threads.
	kernel := map[string]float64{}
	for _, s := range l.Windows[0].CPU {
		kernel[s.Owner] = s.Ms
	}
	if l.Windows[0].CPU[0].Owner != ownerKernel || kernel[ownerThreads] <= 0 || kernel[ownerUnknown] != 0 {
		t.Errorf("the kernel's window: %+v", l.Windows[0].CPU)
	}
	// Every task's running time from PID 1's exec to the command's exit is in the windows from
	// PID 1 on, and PID 1's own is systemd's, waiting on the disk at times.
	owners := map[string]States{}
	var running, busy float64
	for _, s := range l.Owners {
		owners[s.Owner] = s
		running += s.Running
	}
	for _, w := range l.Windows[1:] {
		for _, s := range w.CPU {
			if s.Owner != ownerIdle {
				busy += s.Ms
			}
		}
	}
	if !near(running, busy) || owners["init.scope"].Disk <= 0 {
		t.Errorf("owners ran %v, the windows from PID 1 %v; PID 1's: %+v", running, busy, owners["init.scope"])
	}
	for _, s := range l.Windows[1].CPU {
		if s.Owner == "init.scope" && s.Comms[0].Comm != "systemd" {
			t.Errorf("PID 1 ran as %+v", s.Comms)
		}
	}
	if _, ok := owners[ownerIdle]; ok || owners["user@0.service"].Running <= 0 || owners["init.scope"].Running <= 0 ||
		l.unattributed() <= 0 || !near(l.unattributed(), owners[ownerUnknown].Running) {
		t.Errorf("owners: %+v, unattributed %v", l.Owners, l.unattributed())
	}
	// The login's session in logind is session 2, the user's; session 1 is root's manager's.
	marks := map[string]float64{}
	for _, m := range l.Marks {
		marks[m.Name] = m.At
	}
	if !near(marks["logind's session"], 319.931) || !near(marks["connection"], 284.765) || len(marks) != len(loginPhases) {
		t.Errorf("marks: %+v", l.Marks)
	}
	text := l.Text("release, boot 1")
	for _, line := range []string{
		"   338.2    338.8      0.7  the login's command, to its exit\n",
		"80.0 before the kernel's clock + 338.8 to the command's exit + 1.2 residue = 420.0, the first login the host measured",
		"others under 0.5 ms each",
		"  319.9  logind's session: systemd-logind[68]: New session '2'",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the ledger's text has no %q:\n%s", line, text)
		}
	}
	if strings.Contains(text, "lost events") {
		t.Errorf("a trace with no breaks says it lost events:\n%s", text)
	}
	l.Breaks = 2
	if !strings.Contains(l.Text(""), "lost events at 2 switches") {
		t.Error("a trace with breaks does not say so")
	}
}

// A trace that ends before the login, or started after the kernel did, gives no ledger: each
// is the real trace cut short at the event the ledger needs.
func TestATraceThatMissesTheLoginGivesNoLedger(t *testing.T) {
	in := ledgerFixture(t)
	for _, c := range []struct{ upTo, why string }{
		{"filename=/sbin/init", "no exec by PID 1"},
		{"filename=/usr/lib/openssh/sshd-session", "no sshd-session"},
		{"filename=/usr/bin/true", "no /usr/bin/true"},
		{"sched_process_exit: comm=true", "not its exit"},
	} {
		cut := in
		i := strings.Index(in.Trace, c.upTo)
		cut.Trace = in.Trace[:strings.LastIndex(in.Trace[:i], "\n")+1] + "              sh-119     [000] ...1.     0.357045: tracing_mark_write: ledger-sync 1\n"
		if _, err := newLedger(cut); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("the trace up to %s gave %v; want %q", c.upTo, err, c.why)
		}
	}
	// Without the PID 1 exec at all, but the rest there.
	noInit := in
	noInit.Trace = strings.Replace(in.Trace, "filename=/sbin/init pid=1", "filename=/sbin/init pid=4000", 1)
	if _, err := newLedger(noInit); err == nil {
		t.Error("a trace with no exec by PID 1 gave a ledger")
	}
	noTrips := in
	noTrips.Trips = nil
	if _, err := newLedger(noTrips); err == nil {
		t.Error("no trips gave a ledger")
	}
	bad := in
	bad.Trace = "garbage\n"
	if _, err := newLedger(bad); err == nil {
		t.Error("a trace that is not one gave a ledger")
	}
}

// The ledger probe names what it kept for each boot it accounted for, and nothing for a boot it
// lost.
func TestTheLedgerProbeNamesWhatItKept(t *testing.T) {
	d := ledgerProbe{}.Detail(&Result{Samples: []Sample{{Tree: "release", Rep: 1}, {Tree: "release", Rep: 2, Missed: "no ledger"}}})
	if !strings.Contains(d, "release-1.ledger.txt, release-1.ledger.json, release-1.trace.txt, release-1.journal.txt\n") || strings.Contains(d, "release-2") {
		t.Errorf("Detail:\n%s", d)
	}
}

// A task's time off the CPUs is asleep until it is woken and runnable until it runs, in disk wait
// when it left in D, and each CPU's switches are its own.
func TestATasksWaitIsSplitByWhatItWaitedFor(t *testing.T) {
	trace := `          <idle>-0       [000] d..2.     0.000500: sched_switch: prev_comm=swapper/0 prev_pid=0 prev_prio=120 prev_state=R ==> next_comm=work next_pid=10 next_prio=120
            work-10      [000] d..2.     0.001000: sched_switch: prev_comm=work prev_pid=10 prev_prio=120 prev_state=S ==> next_comm=disk next_pid=11 next_prio=120
            disk-11      [000] d..2.     0.002000: sched_switch: prev_comm=disk prev_pid=11 prev_prio=120 prev_state=D ==> next_comm=swapper/0 next_pid=0 next_prio=120
          <idle>-0       [001] d..2.     0.002500: sched_switch: prev_comm=swapper/1 prev_pid=0 prev_prio=120 prev_state=R ==> next_comm=other next_pid=12 next_prio=120
          <idle>-0       [000] d.h3.     0.003000: sched_wakeup: comm=work pid=10 prio=120 target_cpu=000
          <idle>-0       [000] d..2.     0.004000: sched_switch: prev_comm=swapper/0 prev_pid=0 prev_prio=120 prev_state=R ==> next_comm=work next_pid=10 next_prio=120
           other-12      [001] d..2.     0.005000: sched_switch: prev_comm=other prev_pid=12 prev_prio=120 prev_state=R ==> next_comm=swapper/1 next_pid=0 next_prio=120
            work-10      [000] d..2.     0.006000: sched_switch: prev_comm=work prev_pid=10 prev_prio=120 prev_state=R+ ==> next_comm=disk next_pid=11 next_prio=120
`
	events, err := parseTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	b := replay(events)
	if b.breaks != 0 {
		t.Errorf("%d breaks in a trace with none", b.breaks)
	}
	byPID := func(pid int, at float64) string { return strconv.Itoa(pid) }
	states := map[string]States{}
	for _, s := range b.statesIn(0, 6, byPID) {
		states[s.Owner] = s
	}
	// work: ran 0.5-1, asleep 1-3, runnable 3-4, ran 4-6. disk: ran 1-2, in D 2-6. other ran 2.5-5 on CPU 1.
	if w := states["10"]; !near(w.Sleeping, 2) || !near(w.Runnable, 1) || !near(w.Running, 2.5) {
		t.Errorf("work: %+v", w)
	}
	if d := states["11"]; !near(d.Disk, 4) || !near(d.Running, 1) {
		t.Errorf("disk: %+v", d)
	}
	if o := states["12"]; !near(o.Running, 2.5) {
		t.Errorf("other: %+v", o)
	}
	cpu := map[string]float64{}
	for _, s := range b.cpuIn(0, 6, byPID) {
		cpu[s.Owner] = s.Ms
		if s.Owner == "10" && s.Comms[0].Comm != "work" {
			t.Errorf("work ran as %q", s.Comms[0].Comm)
		}
	}
	// PID 0 is CPU 0's from the kernel's 0 to its first switch and from 2 to 4, and CPU 1's from 5,
	// its first switch's time before that being the trace's to know.
	if !near(cpu["10"], 2.5) || !near(cpu["11"], 1) || !near(cpu["12"], 2.5) || !near(cpu["0"], 0.5+2+1) {
		t.Errorf("CPU by task: %v", cpu)
	}
}

// The cuts follow the login's own process tree: a /usr/bin/true that is not the login's, or a
// later login's sshd-session, moves none of them, and a cut the trace does not have is left out.
func TestTheLedgersCutsFollowTheLoginsProcesses(t *testing.T) {
	in := ledgerFixture(t)
	windows := func(trace string) []Window {
		t.Helper()
		changed := in
		changed.Trace = trace
		l, err := newLedger(changed)
		if err != nil {
			t.Fatal(err)
		}
		return l.Windows
	}
	starts := func(ws []Window) []float64 {
		var s []float64
		for _, w := range ws {
			s = append(s, w.Start)
		}
		return s
	}
	want := starts(windows(in.Trace))
	// sshd itself execs a /usr/bin/true before the login's, and a later login's sshd-session
	// starts after the login's command and before its exit.
	at := strings.Index(in.Trace, "            true-111     [000] .....     0.318157")
	foreign := in.Trace[:at] +
		"            sshd-77      [000] .....     0.300000: sched_process_exec: filename=/usr/bin/true pid=77 old_pid=77\n" +
		"            sshd-77      [000] .....     0.318400: sched_process_fork: comm=sshd pid=77 child_comm=sshd child_pid=113\n" +
		"    sshd-session-113     [000] .....     0.318500: sched_process_exec: filename=/usr/lib/openssh/sshd-session pid=113 old_pid=113\n" +
		in.Trace[at:]
	if got := starts(windows(foreign)); !slices.Equal(got, want) {
		t.Errorf("with another true and a later sshd-session the cuts are %v; want %v", got, want)
	}
	// No sshd-auth: its two cuts go and the window before runs on.
	noAuth := windows(strings.ReplaceAll(in.Trace, "filename=/usr/lib/openssh/sshd-auth", "filename=/usr/lib/openssh/sshd-other"))
	if got := starts(noAuth); !slices.Equal(got, slices.Delete(slices.Clone(want), 3, 5)) {
		t.Errorf("with no sshd-auth the cuts are %v", got)
	}
	// The command's process exec'd no shell first: the shell's window goes.
	noShell := windows(strings.Replace(in.Trace, "filename=/bin/bash pid=111", "filename=/bin/bash pid=4111", 1))
	if got := starts(noShell); !slices.Equal(got, slices.Delete(slices.Clone(want), 6, 7)) {
		t.Errorf("with no shell exec'd the cuts are %v", got)
	}
}

// On spin's path PID 1 is first the handoff, which execs systemd: the handoff gets a window of its
// own, the PID 1 window starts at systemd's exec, and the probe's numbers are read from them.
func TestSpinsHandoffHasAWindowOfItsOwn(t *testing.T) {
	in := ledgerFixture(t)
	lane, err := newLedger(in)
	if err != nil {
		t.Fatal(err)
	}
	const exec = "         systemd-1       [000] .....     0.083224: sched_process_exec: filename=/sbin/init pid=1 old_pid=1\n"
	const switched = "          <idle>-0       [000] d..2.     0.083971: sched_switch: prev_comm=swapper/0 prev_pid=0 prev_prio=120 prev_state=R ==> next_comm=init next_pid=1 next_prio=120\n"
	if !strings.Contains(in.Trace, exec) || !strings.Contains(in.Trace, switched) {
		t.Fatal("the fixture no longer has the lines this test edits")
	}
	trace := strings.Replace(in.Trace, exec, strings.Replace(exec, "/sbin/init", spinHandoffInit, 1), 1)
	in.Trace = strings.Replace(trace, switched, switched+"         systemd-1       [000] .....     0.083972: sched_process_exec: filename=/sbin/init pid=1 old_pid=1\n", 1)
	l, err := newLedger(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Windows) != len(lane.Windows)+1 || l.Windows[1].Name != windowHandoff || !near(l.Windows[1].Start, 103.224) ||
		!near(l.Windows[2].Start, 103.972) || !near(l.Windows[1].End, 103.972) {
		t.Fatalf("windows: %+v", l.Windows[:3])
	}
	if h := l.window(windowHandoff); !near(h.End-h.Start, 0.748) {
		t.Errorf("the handoff's window: %+v", h)
	}
	if h := lane.window(windowHandoff); h.End-h.Start != 0 {
		t.Errorf("the lane's ledger has a handoff: %+v", h)
	}
	// Another init before systemd, as systemd-bootchart is, is not the handoff.
	in.Trace = strings.Replace(in.Trace, spinHandoffInit, "/usr/lib/systemd/systemd-bootchart", 1)
	other, err := newLedger(in)
	if err != nil {
		t.Fatal(err)
	}
	if h := other.window(windowHandoff); len(other.Windows) != len(lane.Windows) || h.End-h.Start != 0 {
		t.Errorf("another init read as the handoff: %+v", other.Windows[:2])
	}
	if !near(l.window(windowConnection).Start, 272.151) || !near(lane.window(windowConnection).Start, 272.151) {
		t.Errorf("the connection: %v and %v", l.window(windowConnection), lane.window(windowConnection))
	}
}

// With no unit named for any task, a task's time is the kernel's, its threads', idle or
// unattributed, and nothing else.
func TestATaskNoUnitNamesIsUnattributed(t *testing.T) {
	in := ledgerFixture(t)
	in.Units = ""
	l, err := newLedger(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range l.Windows {
		for _, s := range w.CPU {
			if s.Owner != ownerKernel && s.Owner != ownerThreads && s.Owner != ownerIdle && s.Owner != ownerUnknown {
				t.Errorf("window %q has %q with no units named", w.Name, s.Owner)
			}
		}
	}
}

// PID 0 is the kernel starting until CPU 0's first switch and idle after it, and a task's run is
// under the name it had on each side of an exec.
func TestPIDZeroIsTheKernelUntilTheFirstSwitch(t *testing.T) {
	events, err := parseTrace(`          <idle>-0       [001] d..2.     0.000200: sched_switch: prev_comm=swapper/1 prev_pid=0 prev_prio=120 prev_state=R ==> next_comm=other next_pid=12 next_prio=120
          <idle>-0       [000] d..2.     0.000500: sched_switch: prev_comm=swapper/0 prev_pid=0 prev_prio=120 prev_state=R ==> next_comm=sh next_pid=10 next_prio=120
              sh-10      [000] .....     0.000800: sched_process_exec: filename=/usr/bin/true pid=10 old_pid=10
          stray-20       [000] .....     0.000900: sched_process_exec: filename=/usr/bin/stray pid=20 old_pid=20
            true-10      [000] d..2.     0.001000: sched_switch: prev_comm=true prev_pid=10 prev_prio=120 prev_state=S ==> next_comm=swapper/0 next_pid=0 next_prio=120
`)
	if err != nil {
		t.Fatal(err)
	}
	b := replay(events)
	owner := b.owners(nil)
	if owner(0, 0.2) != ownerKernel || owner(0, 0.6) != ownerIdle {
		t.Errorf("PID 0 at 0.2 ms is %q and at 0.6 %q", owner(0, 0.2), owner(0, 0.6))
	}
	// An exec by a task the trace does not have on that CPU, which only lost events make, credits
	// it no time.
	for _, s := range b.cpuIn(0, 1, func(pid int, _ float64) string { return strconv.Itoa(pid) }) {
		if s.Owner == "20" {
			t.Errorf("the stray exec was credited %v ms", s.Ms)
		}
		if s.Owner != "10" {
			continue
		}
		if len(s.Comms) != 2 || s.Comms[0].Comm != "sh" || !near(s.Comms[0].Ms, 0.3) || s.Comms[1].Comm != "true" || !near(s.Comms[1].Ms, 0.2) {
			t.Errorf("the task's runs: %+v", s.Comms)
		}
	}
	// No switch on CPU 0 at all: PID 0's time is the kernel's throughout.
	if owner := replay(events[2:4]).owners(nil); owner(0, 5) != ownerKernel {
		t.Error("with no switch on CPU 0, PID 0 is idle")
	}
}

// A journal with no kvm-clock line has nothing to place the machine's running by, one with no
// "Marking stable" line nothing to put the trace's clock on the kernel's by, and an offset that
// puts the running before QEMU's exec is an alignment that is wrong: each refuses the ledger.
func TestTheSplitBeforeTheKernelsClockIsRefusedWhenItCannotHold(t *testing.T) {
	for _, line := range []string{"sched offset", "Marking stable"} {
		in := ledgerFixture(t)
		var kept []string
		for l := range strings.Lines(in.Journal) {
			if !strings.Contains(l, line) {
				kept = append(kept, l)
			}
		}
		in.Journal = strings.Join(kept, "")
		if _, err := newLedger(in); err == nil || !strings.Contains(err.Error(), line) {
			t.Errorf("no %q line: %v", line, err)
		}
	}
	// The machine ran at 50.0215 ± 0.0335: a bracket of QEMU's exec that starts inside that is
	// taken, and one that starts after it is refused.
	in := ledgerFixture(t)
	in.QEMUExecLo, in.QEMUExecHi = 50.05, 50.2
	if _, err := newLedger(in); err != nil {
		t.Errorf("QEMU exec'd within the kernel's 0's uncertainty of the machine's running: %v", err)
	}
	in.QEMUExecLo = 50.06
	if _, err := newLedger(in); err == nil || !strings.Contains(err.Error(), "before QEMU's exec") {
		t.Errorf("QEMU exec'd after the machine ran: %v", err)
	}
}

// The table shows each share over 0.5 ms with its three busiest names, sums the rest, and has the
// owners' states and the journal's marks when there are any.
func TestTheLedgersTextShowsWhatMatters(t *testing.T) {
	l := Ledger{FirstLogin: 400, KernelZero: 90, KernelZeroError: 0.15, QEMUExec: 20, QEMUExecError: 0.05, Running: 70, LoginEnd: 309, Residue: 1, Breaks: 1,
		Windows: []Window{
			{Name: "first", Start: 0, End: 100, CPU: []Share{
				{Owner: "big", Ms: 99.6, Comms: []Comm{{"a", 50}, {"b", 30}, {"c", 10}, {"d", 9.6}}},
				{Owner: "small", Ms: 0.4, Comms: []Comm{{"s", 0.4}}},
			}},
			{Name: "second", Start: 100, End: 309, CPU: []Share{
				{Owner: "pair", Ms: 200, Comms: []Comm{{"x", 150}, {"y", 50}}},
				{Owner: "alone", Ms: 9, Comms: []Comm{{"z", 9}}},
			}},
		},
		Owners: []States{{Owner: "busy", Running: 10, Runnable: 2, Disk: 1, Sleeping: 3}, {Owner: "quiet", Running: 0.2}},
		Marks:  []Mark{{"connection", 250, "sshd-session[94]: Connection from"}},
	}
	text := l.Text("t")
	for _, line := range []string{
		"big (a 50.0, b 30.0, c 10.0)\n",
		"       0.4  1 others under 0.5 ms each\n",
		"pair (x 150.0, y 50.0)\n",
		"     9.0  alone\n",
		"lost events at 1 switches",
		"      20.0  spin-machine, to its exec of QEMU (±0.05)\n",
		"      50.0  QEMU's start and the held start, to the guest's first instruction\n",
		"      20.0  the firmware, the decompression and the kernel's setup, to the kernel's clock\n",
		"owners from PID 1's exec (100.0)",
		"     10.0       2.0       1.0       3.0  busy\n",
		"   250.0  connection: sshd-session[94]: Connection from\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("no %q in:\n%s", line, text)
		}
	}
	if strings.Contains(text, "quiet") || strings.Contains(text, " d 9.6") || strings.Count(text, "others under") != 1 {
		t.Errorf("the table shows what it should leave out:\n%s", text)
	}
	bare := Ledger{Windows: l.Windows[:1]}.Text("t")
	if strings.Contains(bare, "owners from") || strings.Contains(bare, "in the journal") || strings.Contains(bare, "lost events") {
		t.Errorf("a ledger of one window, no marks and no breaks:\n%s", bare)
	}
}
