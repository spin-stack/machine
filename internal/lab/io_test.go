// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"testing"
	"time"
)

func TestQEMUsCPUIsUtimeAndStimeAfterItsName(t *testing.T) {
	// A command with a space and a parenthesis in it, as a thread's name can have.
	stat := "4242 (qemu-system-x86) (x) S 1 4242 4242 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 9 0 100 0 0"
	got, err := statCPU(stat)
	if err != nil || got != 3*time.Second {
		t.Errorf("statCPU = %v, %v; want 3s", got, err)
	}
	for _, bad := range []string{
		"4242 qemu S 1 1 1 0 -1 0 0 0 0 0 250 50 0",
		"4242 (q) S 1 1 1 0 -1 0 0 0 0 0 250",
		"4242 (q) S 1 1 1 0 -1 0 0 0 0 0 x 50",
		"4242 (q) S 1 1 1 0 -1 0 0 0 0 0 250 x",
	} {
		if _, err := statCPU(bad); err == nil {
			t.Errorf("statCPU(%q) read a CPU time", bad)
		}
	}
	// The thirteen fields after the command that utime and stime need, and no more.
	if got, err := statCPU("1 (q) S 1 1 1 0 -1 0 0 0 0 0 7 3"); err != nil || got != 100*time.Millisecond {
		t.Errorf("statCPU of the shortest stat = %v, %v", got, err)
	}
}

func TestTheGuestsBusyTimeLeavesOutIdleIowaitAndSteal(t *testing.T) {
	out := "cpu  100 1 50 9000 30 2 3 40 0 0\n{\"jobs\": []}\nline two\ncpu  300 1 70 9900 90 4 8 400 0 0\n"
	busy, between, err := guestBusyAround(out)
	if err != nil {
		t.Fatal(err)
	}
	// user +200, system +20, irq +2, softirq +5: 227 ticks of 10 ms.
	if busy != 2270*time.Millisecond {
		t.Errorf("busy = %v, want 2.27s", busy)
	}
	if between != "{\"jobs\": []}\nline two" {
		t.Errorf("between = %q", between)
	}
	if _, _, err := guestBusyAround("cpu 1 2 3 4 5 6 7 8\n"); err == nil {
		t.Error("output with nothing between the lines read as a run")
	}
	// The shortest: the two lines, eight counts each, and one line between.
	if busy, between, err := guestBusyAround("cpu 1 0 0 0 0 0 0 0\nx\ncpu 2 0 0 0 0 0 0 0\n"); err != nil || busy != 10*time.Millisecond || between != "x" {
		t.Errorf("guestBusyAround of the shortest output = %v, %q, %v", busy, between, err)
	}
	if _, _, err := guestBusyAround("cpu0 1 0 0 0 0 0 0 0\nx\ncpu0 2 0 0 0 0 0 0 0\n"); err == nil {
		t.Error("one CPU's line read as the guest's")
	}
}

func TestFiosReportIsReadFromTheSideThatMovedBytes(t *testing.T) {
	report := `{"jobs": [{"error": 0,
		"read": {"io_bytes": 0, "iops": 0, "runtime": 0},
		"write": {"io_bytes": 2147483648, "iops": 52428.8, "runtime": 10000,
			"clat_ns": {"percentile": {"50.000000": 18432, "99.000000": 61440}}}}]}`
	r, err := parseFio(report)
	if err != nil {
		t.Fatal(err)
	}
	if r.bytes != 2<<30 || r.iops != 52428.8 || r.seconds != 10 || r.p50 != 18.432 || r.p99 != 61.44 {
		t.Errorf("parseFio = %+v", r)
	}
	for _, bad := range []string{
		`{"jobs": [{"error": 5}]}`,
		`{"jobs": [{"error": 0, "read": {"io_bytes": 1, "runtime": 1}}, {"error": 0, "read": {"io_bytes": 1, "runtime": 1}}]}`,
		`{"jobs": [{"error": 0, "read": {"io_bytes": 4096, "iops": 1, "runtime": 0}}]}`,
	} {
		if _, err := parseFio(bad); err == nil {
			t.Errorf("parseFio(%s) read a result", bad)
		}
	}
}

func TestIOAndBuildMeasureInOnePass(t *testing.T) {
	for _, name := range []string{"io", "build"} {
		if n := Probes[name].Phases(); n != 1 {
			t.Errorf("%s makes %d passes: each measurement boots its own machine and measures it once", name, n)
		}
	}
}
