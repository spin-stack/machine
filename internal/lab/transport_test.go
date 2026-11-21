// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A trace as QEMU 11.2.0-rc0 writes it (laptop, 2026-11-21), cut down: the monitor spin-machine
// boots through (0xA) and a second one (0xB, the probe sizing the guest) interleaved with it, the
// greeting and an event answered the way a command is, a line of another event between, lines
// that are not whole, and the guest paused and run again after it first ran.
const sampleTrace = `2026-11-21T13:39:29.414375Z qmp_enter_query_version {}
2026-11-21T13:39:29.414380Z runstate_set current_run_state 6 (prelaunch) new_state 4 (inmigrate)
2026-11-21T13:39:29.414390Z monitor_qmp_in_band_dequeue 0xUNSEEN len 0
2026-11-21T13:39:29.414391Z monitor_qmp_in_band_enqueue 0xR0 len 0
2026-11-21T13:39:29.414392Z monitor_qmp_respond 0xA resp: {"return": {}, "id": 0}
2026-11-21T13:39:29.414400Z monitor_qmp_respond mon 0xA resp: {"QMP": {"version": {}, "capabilities": ["oob"]}}
2026-11-21T13:39:29.414792Z monitor_qmp_in_band_enqueue 0xR1 mon 0xA len 0
2026-11-21T13:39:29.445174Z monitor_qmp_in_band_dequeue 0xR1 len 0
2026-11-21T13:39:29.445195Z qmp_enter_qmp_capabilities {}
2026-11-21T13:39:29.445300Z monitor_qmp_respond mon 0xA resp: {"return": {}, "id": 1}
2026-11-21T13:39:29.445500Z monitor_qmp_in_band_enqueue 0xR9 mon 0xB len 0
2026-11-21T13:39:29.445600Z monitor_qmp_in_band_enqueue 0xR2 mon 0xA len 1
2026-11-21T13:39:29.445700Z monitor_qmp_in_band_dequeue 0xR9 len 1
2026-11-21T13:39:29.445710Z qmp_enter_qmp_capabilities {}
2026-11-21T13:39:29.445800Z monitor_qmp_respond mon 0xB resp: {"return": {}, "id": 1}
2026-11-21T13:39:29.445900Z monitor_qmp_in_band_dequeue 0xR2 len 0
2026-11-21T13:39:29.445910Z qmp_enter_blockdev_add {"node-name": "d-root-0"}
2026-11-21T13:39:29.446100Z vhost_commit 0x1
2026-11-21T13:39:29.446400Z monitor_qmp_respond mon 0xA resp: {"error": {"class": "GenericError"}, "id": 2}
2026-11-21T13:39:29.447000Z monitor_qmp_in_band_enqueue 0xR3 mon 0xA len 0
2026-11-21T13:39:29.447100Z monitor_qmp_in_band_dequeue 0xR3 len 0
2026-11-21T13:39:29.447110Z qmp_enter_cont {}
2026-11-21T13:39:29.447120Z monitor_qmp_respond mon 0xA resp: {"timestamp": {"seconds": 1795268334}, "event": "RESUME"}
2026-11-21T13:39:29.447130Z runstate_set current_run_state 6 (prelaunch) new_state 9 (running)
2026-11-21T13:39:29.447200Z monitor_qmp_respond mon 0xA resp: {"return": {}, "id": 3}
not a trace line
2026-11-21T13:39:29.500000Z runstate_set current_run_state 9 (running) new_state 7 (paused)
2026-11-21T13:39:29.600000Z runstate_set current_run_state 7 (paused) new_state 9 (running)
2026-11-21T13:39:30.000000Z runstate_set current_run_state 9 (running) new_state 11 (shutdown)
`

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("15:04:05.000000", s)
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, 11, 21, ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(), time.UTC)
}

// Each command is placed by the monitor's own events: when it arrived, when the main loop took it,
// when it was answered - an error is an answer, an event and the greeting are not - and on which
// connection. The guest is let go at the first run state of running.
func TestATraceIsEachCommandAndWhenTheGuestRan(t *testing.T) {
	tr := parseQMPTrace(sampleTrace)
	want := []qmpCommand{
		{"qmp_capabilities", "0xA", at(t, "13:39:29.414792"), at(t, "13:39:29.445174"), at(t, "13:39:29.445300")},
		{"qmp_capabilities", "0xB", at(t, "13:39:29.445500"), at(t, "13:39:29.445700"), at(t, "13:39:29.445800")},
		{"blockdev_add", "0xA", at(t, "13:39:29.445600"), at(t, "13:39:29.445900"), at(t, "13:39:29.446400")},
		{"cont", "0xA", at(t, "13:39:29.447000"), at(t, "13:39:29.447100"), at(t, "13:39:29.447200")},
	}
	if len(tr.Commands) != len(want) {
		t.Fatalf("commands %+v", tr.Commands)
	}
	for i := range want {
		if tr.Commands[i] != want[i] {
			t.Errorf("command %d: %+v, want %+v", i, tr.Commands[i], want[i])
		}
	}
	if !tr.Running.Equal(at(t, "13:39:29.447130")) {
		t.Errorf("running at %v", tr.Running)
	}
}

// The path is the conversation of the monitor that sent the command which ends it. Through cont, it
// ends at cont's answer; up to migrate-incoming, at migrate-incoming's taking, where the load starts.
// The client's share is each gap from an answer to the next command's arrival on that monitor.
func TestAPathIsOneMonitorsCommandsUpToTheOneThatEndsIt(t *testing.T) {
	tr := parseQMPTrace(sampleTrace)
	conv := tr.conversation("cont")
	if len(conv) != 3 || conv[1].Name != "blockdev_add" || tr.conversation("migrate_incoming") != nil {
		t.Fatalf("conversation %+v", conv)
	}
	s, ok := span(conv, "cont", true)
	if want := (qmpSpan{Took: 2026 * time.Microsecond, Commands: 3, Client: 900 * time.Microsecond}); !ok || s != want {
		t.Errorf("through cont: %+v %v, want %+v", s, ok, want)
	}
	s, ok = span(conv, "blockdev_add", false)
	if want := (qmpSpan{Took: 726 * time.Microsecond, Commands: 2, Client: 300 * time.Microsecond}); !ok || s != want {
		t.Errorf("up to blockdev_add: %+v %v, want %+v", s, ok, want)
	}
	if _, ok := span(conv, "migrate_incoming", false); ok {
		t.Error("a path ended by a command never sent")
	}
}

// The traced QEMU is a script that runs the tree's QEMU with the trace into the given directory,
// and the trace is read back by the machine's PID.
func TestTheTracedQEMURunsTheTreesWithItsTrace(t *testing.T) {
	tree, dir := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", "qemu-system-x86_64"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Measurement{Tree: &Tree{Dir: tree}}
	flag, err := tracedQEMU(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	if flag != "--qemu="+filepath.Join(dir, "qemu-traced") {
		t.Errorf("flag %q", flag)
	}
	script, _ := os.ReadFile(filepath.Join(dir, "qemu-traced"))
	events, _ := os.ReadFile(filepath.Join(dir, "qmp-events"))
	real, _ := filepath.EvalSymlinks(filepath.Join(tree, "bin", "qemu-system-x86_64"))
	wantScript := "#!/bin/sh\nexec " + real + " -msg timestamp=on -D " + dir + "/qemu-$$.log -trace events=" + dir + "/qmp-events \"$@\"\n"
	if string(script) != wantScript || string(events) != qmpTraceEvents {
		t.Errorf("script %q, events %q", script, events)
	}
	if _, err := tracedQEMU(&Measurement{Tree: &Tree{Dir: t.TempDir()}}, dir); err == nil {
		t.Error("a tree with no QEMU traced")
	}
	if err := os.WriteFile(filepath.Join(dir, "qemu-42.log"), []byte(sampleTrace), 0o600); err != nil {
		t.Fatal(err)
	}
	if tr, err := readQMPTrace(dir, 42); err != nil || len(tr.Commands) != 4 {
		t.Errorf("read back: %d commands, %v", len(tr.Commands), err)
	}
	if _, err := readQMPTrace(dir, 43); err == nil {
		t.Error("a trace read for a machine that wrote none")
	}
}

// A round trip is a write answered: writes in a row before an answer are one, and reads in a row
// after one are one, whichever goroutine reads.
func TestALoginsRoundTripsAreWritesAnswered(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	tc := &turns{Conn: client}
	go func() {
		b := make([]byte, 8)
		for _, k := range []int{2, 1, 1} { // reads each turn's writes, then answers twice
			for range k {
				if _, err := server.Read(b); err != nil {
					return
				}
			}
			_, _ = server.Write([]byte("a"))
			_, _ = server.Write([]byte("b"))
		}
	}()
	b := make([]byte, 1)
	for _, writes := range []int{2, 1, 1} {
		for range writes {
			if _, err := tc.Write([]byte("q")); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if _, err := tc.Read(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := tc.count(); got != 3 {
		t.Errorf("%d round trips, want 3", got)
	}
}

// The detail lists each command of each path by its place, with its round trip and its run.
func TestTransportDetailListsEachPathsCommands(t *testing.T) {
	m := &Measurement{detail: map[string]float64{}}
	tr := parseQMPTrace(sampleTrace)
	detailCommands(m, "cold", tr.conversation("cont"))
	r := &Result{Trees: []Manifest{{Name: "A"}, {Name: "B"}}, Samples: []Sample{{Tree: "A", Detail: m.detail}}}
	got := transport{}.Detail(r)
	for _, want := range []string{
		"A: QMP commands on each path",
		"cold     00 qmp_capabilities               0.126   0.126",
		"cold     01 blockdev_add                   1.100   0.500",
		"cold     02 cont                           0.800   0.100",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "B:") {
		t.Errorf("a tree with no detail listed:\n%s", got)
	}
}
