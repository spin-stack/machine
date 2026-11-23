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
	if _, err := statCPU("4242 qemu S 1"); err == nil {
		t.Error("a stat with no command read as one")
	}
}

func TestTheGuestsBusyTimeLeavesOutIdleIowaitAndSteal(t *testing.T) {
	out := "cpu  100 1 50 9000 30 2 3 40 0 0\n{\"jobs\": []}\nline two\ncpu  300 1 70 9900 90 4 5 400 0 0\n"
	busy, between, err := guestBusyAround(out)
	if err != nil {
		t.Fatal(err)
	}
	// user +200, system +20, irq +2, softirq +2: 224 ticks of 10 ms.
	if busy != 2240*time.Millisecond {
		t.Errorf("busy = %v, want 2.24s", busy)
	}
	if between != "{\"jobs\": []}\nline two" {
		t.Errorf("between = %q", between)
	}
	if _, _, err := guestBusyAround("cpu 1 2 3 4 5 6 7 8\n"); err == nil {
		t.Error("output with nothing between the lines read as a run")
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
	if _, err := parseFio(`{"jobs": [{"error": 5}]}`); err == nil {
		t.Error("a job that failed read as a result")
	}
}
