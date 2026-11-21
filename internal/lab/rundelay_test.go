// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSchedstat(t *testing.T) {
	ran, waited, ok := parseSchedstat("1710591 2500000 2\n")
	if !ok || ran != 1710591 || waited != 2500*time.Microsecond {
		t.Fatalf("got %v %v %v", ran, waited, ok)
	}
	for _, bad := range []string{"", "1 2", "1 x 3", "1 2 3 4"} {
		if _, _, ok := parseSchedstat(bad); ok {
			t.Errorf("%q read as a schedstat", bad)
		}
	}
}

func TestStatSteal(t *testing.T) {
	d, ok := statSteal("cpu  100 0 50 9000 3 0 1 7 0 0\ncpu0 100 0 50 9000 3 0 1 7 0 0\n")
	if !ok || d != 70*time.Millisecond {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := statSteal("cpu  100 0 50 9000 3 0 1\n"); ok {
		t.Error("a line short of steal read as one")
	}
	// A kernel from before guest time ends its line at steal.
	if d, ok := statSteal("cpu  100 0 50 9000 3 0 1 8\n"); !ok || d != 80*time.Millisecond {
		t.Errorf("a line ending at steal: %v %v", d, ok)
	}
	if _, ok := statSteal("intr 100 0 50 9000 3 0 1 7 0 0\n"); ok {
		t.Error("a first line that is not the CPUs' summed read as one")
	}
}

func TestSummaryWaitedBySplitsAtTheLastSampleBefore(t *testing.T) {
	t0 := time.Now()
	s := schedSummary{points: []schedPoint{
		{t0, 1 * time.Millisecond},
		{t0.Add(5 * time.Millisecond), 3 * time.Millisecond},
		{t0.Add(10 * time.Millisecond), 8 * time.Millisecond},
	}}
	for _, c := range []struct {
		at   time.Time
		want time.Duration
	}{
		{t0.Add(-time.Millisecond), 0},
		{t0.Add(7 * time.Millisecond), 3 * time.Millisecond},
		{t0.Add(time.Second), 8 * time.Millisecond},
	} {
		if got := s.waitedBy(c.at); got != c.want {
			t.Errorf("waitedBy(t0%+v) = %v, want %v", c.at.Sub(t0), got, c.want)
		}
	}
}

func TestCrowdRow(t *testing.T) {
	for row, want := range map[string][2]int{"0": {0, 0}, "16": {16, 0}, "16:nice=10": {16, 10}} {
		n, nice, err := crowdRow(row)
		if err != nil || n != want[0] || nice != want[1] {
			t.Errorf("crowdRow(%q) = %d, %d, %v; want %v", row, n, nice, err, want)
		}
	}
	for _, bad := range []string{"", "x", "-1", "16:nice=", "16:nice=-5", "16:nice=20"} {
		if _, _, err := crowdRow(bad); err == nil {
			t.Errorf("crowdRow(%q) was taken", bad)
		}
	}
}

// A vCPU thread is KVM's, named "CPU N/KVM"; one QEMU emulates (TCG) and every other thread is not.
func TestAVCPUIsKVMs(t *testing.T) {
	for name, want := range map[string]bool{"CPU 0/KVM": true, "CPU 15/KVM": true, "CPU 0/TCG": false,
		"vhost": false, "qemu-system-x86": false, "worker/KVM": false} {
		if got := isVCPU(name); got != want {
			t.Errorf("isVCPU(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestThreadKindDropsAWorkersPid(t *testing.T) {
	for name, want := range map[string]string{"vhost-12828": "vhost", "iou-wrk-9342": "iou-wrk",
		"CPU 0/KVM": "CPU 0/KVM", "mig/dst/recv_3": "mig/dst/recv_3", "qemu-system-x86": "qemu-system-x86"} {
		if got := threadKind(name); got != want {
			t.Errorf("threadKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSummarizeCountsExitedThreadsAndTheVCPUs(t *testing.T) {
	threads := map[int]threadSched{
		1: {name: "qemu-system-x86", ran: 10 * time.Millisecond, waited: 2 * time.Millisecond},
		2: {name: "CPU 0/KVM", ran: 300 * time.Millisecond, waited: 4 * time.Millisecond},
		3: {name: "worker", waited: time.Millisecond}, // exited, at its last sample
		4: {name: "worker", waited: time.Millisecond},
	}
	s := summarize(9, threads, map[int]threadSched{7: {waited: 5 * time.Millisecond}}, nil, 3)
	if s.threads != 4 || s.waited != 8*time.Millisecond || s.vcpuWaited != 4*time.Millisecond ||
		s.ran != 310*time.Millisecond || s.spinWaited != 5*time.Millisecond || s.byName["worker"] != 2*time.Millisecond {
		t.Fatalf("got %+v", s)
	}
}

// writeTask makes one thread of a fixture task directory: its schedstat, comm and status, each
// left out when empty, as a thread that exited mid-read leaves them.
func writeTask(t *testing.T, task, tid, schedstat, comm, status string) {
	t.Helper()
	dir := filepath.Join(task, tid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"schedstat": schedstat, "comm": comm, "status": status} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Every thread with a schedstat is read by its tid under its kind of name, and what is not a
// thread, has exited or reads as no schedstat is passed over. The vCPUs' preemptions are their
// involuntary context switches, the other threads' left out.
func TestTaskSchedsReadsEachThreadAndTheVCPUsPreemptions(t *testing.T) {
	task := t.TempDir()
	writeTask(t, task, "101", "5000000 2000000 3\n", "qemu-system-x86\n", "voluntary_ctxt_switches:\t40\nnonvoluntary_ctxt_switches:\t100\n")
	writeTask(t, task, "102", "300000000 4000000 9\n", "CPU 0/KVM\n", "voluntary_ctxt_switches:\t1\nnonvoluntary_ctxt_switches:\t7\n")
	writeTask(t, task, "103", "200000000 1000000 9\n", "CPU 1/KVM\n", "nonvoluntary_ctxt_switches:\t5\n")
	writeTask(t, task, "104", "1000 500000 1\n", "vhost-4242\n", "")
	writeTask(t, task, "105", "garbled\n", "worker\n", "")
	writeTask(t, task, "106", "", "worker\n", "")
	writeTask(t, task, "self", "1 1 1\n", "worker\n", "")

	got := taskScheds(task)
	want := map[int]threadSched{
		101: {name: "qemu-system-x86", ran: 5 * time.Millisecond, waited: 2 * time.Millisecond},
		102: {name: "CPU 0/KVM", ran: 300 * time.Millisecond, waited: 4 * time.Millisecond},
		103: {name: "CPU 1/KVM", ran: 200 * time.Millisecond, waited: time.Millisecond},
		104: {name: "vhost", ran: time.Microsecond, waited: 500 * time.Microsecond},
	}
	if !maps.Equal(got, want) {
		t.Errorf("taskScheds = %+v, want %+v", got, want)
	}

	// A vCPU seen earlier that has exited since has no status left to read.
	got[107] = threadSched{name: "CPU 2/KVM"}
	if n := preemptions(task, got); n != 12 {
		t.Errorf("preemptions = %v, want the two live vCPUs' 7+5", n)
	}
}

// startAs runs a copy of sleep under name, a child of this process, as spin-machine boot starts
// QEMU, and returns it running.
func startAs(t *testing.T, name string) *exec.Cmd {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep to stand in for QEMU")
	}
	b, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// The watch finds the QEMU its boot started and keeps each of its threads at the last sample taken,
// a thread that exits before the watch ends included; the boot's own threads are spin-machine's.
func TestTheWatchSeesQEMUsThreadsAndKeepsTheOnesThatExit(t *testing.T) {
	if _, err := os.Stat("/proc/self/schedstat"); err != nil {
		t.Skip("this kernel keeps no schedstat")
	}
	qemu := startAs(t, "qemu-system-x86_64")
	w := watchSched(os.Getpid())
	deadline := time.Now().Add(5 * time.Second)
	for !isQEMU(qemu.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(4 * schedEvery)
	_ = qemu.Process.Kill()
	_ = qemu.Wait()
	s := w.end()

	if s.qemu != qemu.Process.Pid {
		t.Fatalf("the watch found QEMU at %d, want %d", s.qemu, qemu.Process.Pid)
	}
	if s.threads != 1 || s.byName["qemu-system-x86"] != s.waited {
		t.Errorf("QEMU's one thread, exited before the watch ended, was not kept: %d threads, by name %v", s.threads, s.byName)
	}
	if s.ran <= 0 {
		t.Errorf("QEMU's thread shows no time on a CPU: exec alone takes some")
	}
	if len(s.points) < 2 || s.waitedBy(time.Now()) != s.waited {
		t.Errorf("%d samples, the last at %v of the %v waited", len(s.points), s.waitedBy(time.Now()), s.waited)
	}
}

// A boot that has started no QEMU yet counts only its own threads, and the probe misses it rather
// than recording a QEMU that waited nothing.
func TestAWatchOfABootWithNoQEMUIsAMiss(t *testing.T) {
	boot := startAs(t, "spin-machine")
	s := watchSched(boot.Process.Pid).end()
	if s.qemu != 0 || s.threads != 0 || len(s.points) != 0 {
		t.Errorf("a boot with no QEMU: %+v", s)
	}
	m := &Measurement{Tree: &Tree{}, values: map[string]float64{}, detail: map[string]float64{}, log: io.Discard}
	var miss *MissError
	if err := recordDelay(m, "cold", time.Second, s, qmpTrace{}, 0, 0); !errors.As(err, &miss) {
		t.Errorf("recordDelay of no QEMU = %v, want a miss", err)
	}
}

// The wait is split at the guest's first run in QEMU's trace, and each kind of thread's wait is
// the detail that Detail lists: by tree and row, path by path, the longest first, the p50 of the
// repetitions.
func TestRunDelayRecordsTheWaitAndDetailListsItByThread(t *testing.T) {
	t0 := time.Now()
	summary := func(vcpu time.Duration) schedSummary {
		return summarize(9, map[int]threadSched{
			1: {name: "qemu-system-x86", ran: 20 * time.Millisecond, waited: 3 * time.Millisecond},
			2: {name: "CPU 0/KVM", ran: 50 * time.Millisecond, waited: vcpu},
		}, map[int]threadSched{7: {waited: 2 * time.Millisecond}}, []schedPoint{
			{t0, time.Millisecond},
			{t0.Add(5 * time.Millisecond), 2 * time.Millisecond},
			{t0.Add(10 * time.Millisecond), 3*time.Millisecond + vcpu},
		}, 4)
	}
	running := qmpTrace{Running: t0.Add(7 * time.Millisecond)}
	var samples []Sample
	for _, vcpu := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 9 * time.Millisecond} {
		m := &Measurement{Tree: &Tree{}, values: map[string]float64{}, detail: map[string]float64{}, log: io.Discard}
		if err := recordDelay(m, "cold", 80*time.Millisecond, summary(vcpu), running, 10*time.Millisecond, 20*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := recordDelay(m, "restore", 30*time.Millisecond, summary(vcpu/10), running, 0, 0); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, Sample{Tree: "A", Row: "16", Values: m.values, Detail: m.detail})
	}
	got := samples[1].Values
	for metric, want := range map[string]float64{
		"cold_login": 80, "cold_wait": 8, "cold_wait_before": 2, "cold_wait_vcpu": 5, "cold_ran": 70,
		"cold_preempted": 4, "cold_threads": 2, "cold_wait_spin": 2, "cold_steal_guest": 10, "cold_steal_host": 20,
		"restore_login": 30, "restore_wait_vcpu": 0.5,
	} {
		if got[metric] != want {
			t.Errorf("%s = %v, want %v", metric, got[metric], want)
		}
	}

	d := runDelay{}.Detail(&Result{Trees: []Manifest{{Name: "A"}, {Name: "B"}}, Rows: []string{"0", "16"}, Samples: samples})
	want := strings.Join([]string{
		"\n  A, crowd 16: p50 ms each of QEMU's threads waited for a CPU, by name",
		"    cold     CPU 0/KVM                   5.000",
		"    cold     qemu-system-x86             3.000",
		"    restore  qemu-system-x86             3.000",
		"    restore  CPU 0/KVM                   0.500",
		"",
	}, "\n")
	if d != want {
		t.Errorf("detail:\n%s\nwant:\n%s", d, want)
	}
}

// The probe misses a boot it cannot read the wait of, rather than recording a wait of nothing: a
// host whose schedstat counts nothing, or a trace with no guest running to split the wait at.
func TestRunDelayMissesAWaitItCannotRead(t *testing.T) {
	m := &Measurement{Tree: &Tree{}, values: map[string]float64{}, detail: map[string]float64{}, log: io.Discard}
	idle := schedSummary{threads: 3}
	ran := schedSummary{threads: 3, ran: time.Millisecond}
	for _, c := range []struct {
		s schedSummary
		t qmpTrace
	}{{idle, qmpTrace{Running: time.Now()}}, {ran, qmpTrace{}}} {
		var miss *MissError
		if err := recordDelay(m, "cold", time.Second, c.s, c.t, 0, 0); !errors.As(err, &miss) {
			t.Errorf("recordDelay(%+v, %+v) = %v, want a miss", c.s, c.t, err)
		}
	}
	if len(m.values) != 0 {
		t.Errorf("a missed wait recorded %v", m.values)
	}
	// A QEMU that ran and never waited is a wait of nothing, which an idle host is.
	if err := recordDelay(m, "cold", time.Second, ran, qmpTrace{Running: time.Now()}, 0, 0); err != nil {
		t.Errorf("recordDelay of a QEMU that never waited = %v", err)
	}
	if w, ok := m.values["cold_wait"]; !ok || w != 0 {
		t.Errorf("a QEMU that never waited recorded a wait of %v (%v)", w, ok)
	}
}

// A row that is not a crowd stops the experiment before anything boots, and a crowd that cannot be
// made stops at its first workspace and says why: this tree has no qemu-img to make overlays with.
// So does the measured boot of an empty crowd, cold, and its restore without the checkpoint.
func TestRunDelayStopsAtACrowdOrABootItCannotMake(t *testing.T) {
	o, err := settle(runDelay{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rows := (runDelay{}).Rows(o); !slices.Equal(rows, []string{"0", "16"}) {
		t.Errorf("rows = %v", rows)
	}
	for _, row := range []string{"many", "16:nice=40"} {
		err := (runDelay{}).Measure(context.Background(), &Measurement{Row: row, Options: o})
		if err == nil || !strings.Contains(err.Error(), "not a number") {
			t.Errorf("Measure of a crowd of %q = %v, want refused", row, err)
		}
	}
	m := func(row string, phase int) *Measurement {
		return &Measurement{Tree: fakeSpinMachine(t, "sleep 30"), Row: row, Phase: phase, Options: o, Dir: t.TempDir(), Key: &Key{},
			State: &delayCheckpoint{checkpoint: checkpoint{state: filepath.Join(t.TempDir(), "state")}}}
	}
	for _, c := range []struct {
		row   string
		phase int
		want  string
	}{{"2", 0, "qemu-img"}, {"0", 0, "qemu-img"}, {"0", 1, "state"}} {
		if err := (runDelay{}).Measure(context.Background(), m(c.row, c.phase)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Measure of crowd %s, phase %d = %v, want an error naming %s", c.row, c.phase, err, c.want)
		}
	}
}
