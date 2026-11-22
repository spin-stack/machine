// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"compress/gzip"
	"io"
	"math"
	"os"
	"path/filepath"
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
		Trace: string(trace), Journal: string(journal),
		// sshd (ssh.service), logind, journald, PID 1 and root's manager, as ledger-units prints them.
		Units: "1 /init.scope\n61 /system.slice/systemd-journald.service\n68 /system.slice/systemd-logind.service\n77 /system.slice/ssh.service\n97 /user.slice/user-0.slice/user@0.service/init.scope\n",
		// The markers are at 357.045, 357.297 and 357.412 ms on the kernel's clock: these put its 0
		// in [99.955, 100.055], [99.953, 100.103] and [99.988, 100.088] on the host's.
		Trips:      []Trip{{1, 457.000, 457.100}, {2, 457.250, 457.400}, {3, 457.400, 457.500}},
		FirstLogin: 420,
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// A real boot's ledger: its windows tile the kernel's clock from 0 to the command's exit, cut
// where the trace shows the boot and the login change hands; on its one vCPU each window's CPU
// adds up to its length; and the host's clock, the kernel's and the residue add up to the first
// login the host measured.
func TestARealBootsLedgerAddsUp(t *testing.T) {
	l, err := newLedger(ledgerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// The cuts, read from the trace by hand: PID 1's exec of /sbin/init, sshd-session's exec,
	// sshd-auth's exec and exit, the fork of the login's process, its exec of bash and of
	// /usr/bin/true, and true's exit.
	want := []float64{0, 83.224, 252.151, 266.672, 296.796, 310.239, 312.642, 318.157}
	if len(l.Windows) != len(want) {
		t.Fatalf("%d windows; want %d: %+v", len(l.Windows), len(want), l.Windows)
	}
	for i, w := range l.Windows {
		end := 318.820
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
	if !near(l.KernelZero, 100.0215) || !near(l.KernelZeroError, 0.0335) || !near(l.LoginEnd, 318.82) || !near(l.Residue, 420-100.0215-318.82) || l.Breaks != 0 {
		t.Errorf("kernel's 0 %v ± %v, login end %v, residue %v, breaks %d", l.KernelZero, l.KernelZeroError, l.LoginEnd, l.Residue, l.Breaks)
	}
	// true ran alone from its exec to its exit, and is sshd's: forked from the login's
	// sshd-session, which sshd forked.
	last := l.Windows[len(l.Windows)-1].CPU
	if len(last) != 1 || last[0].Owner != "ssh.service" || !near(last[0].Ms, 0.663) || last[0].Comms[0].Comm != "true" {
		t.Errorf("the command's window: %+v", last)
	}
	// Before PID 1 execs init its time is the kernel's, and idle is no task's state.
	if first := l.Windows[0].CPU; first[0].Owner != ownerKernel {
		t.Errorf("the kernel's window: %+v", first)
	}
	owners := map[string]States{}
	for _, s := range l.Owners {
		owners[s.Owner] = s
	}
	if _, ok := owners[ownerIdle]; ok || owners["user@0.service"].Running <= 0 || owners["init.scope"].Running <= 0 || l.unattributed() <= 0 {
		t.Errorf("owners: %+v, unattributed %v", l.Owners, l.unattributed())
	}
	// The login's session in logind is session 2, the user's; session 1 is root's manager's.
	marks := map[string]float64{}
	for _, m := range l.Marks {
		marks[m.Name] = m.At
	}
	if !near(marks["logind's session"], 299.931) || !near(marks["connection"], 264.765) || len(marks) != len(loginPhases) {
		t.Errorf("marks: %+v", l.Marks)
	}
	text := l.Text("release, boot 1")
	for _, line := range []string{
		"   318.2    318.8      0.7  the login's command, to its exit\n",
		"100.0 before the kernel's clock + 318.8 to the command's exit + 1.2 residue = 420.0, the first login the host measured",
		"others under 0.5 ms each",
		"  299.9  logind's session: systemd-logind[68]: New session '2'",
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
