// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The phases come from QEMU's trace lines, in ms from the request, each the span between two
// events; one that is not in the trace records nothing rather than a span from nowhere.
func TestSplitRecordsWhereTheTimeToRunningWent(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 19, 39, 8, 0, time.UTC)
	trace := `2026-11-20T19:39:07.990000Z qmp_enter_qmp_capabilities {}
2026-11-20T19:39:08.002000Z qmp_enter_qmp_capabilities {}
2026-11-20T19:39:08.008000Z qmp_enter_migrate_incoming {"uri": "file:/dev/fdset/2"}
2026-11-20T19:39:08.009000Z ram_load_start
2026-11-20T19:39:08.009000Z qemu_loadvm_state_section_startfull 1(ram) 0 4
2026-11-20T19:39:08.059000Z qemu_loadvm_state_section_startfull 6(apic) 0 3
2026-11-20T19:39:08.062500Z qemu_loadvm_state_post_main 0
`
	m := &Measurement{values: map[string]float64{}}
	recordPhases(m, trace, t0)
	want := map[string]float64{"q_dial": 2, "q_qmp": 6, "q_header": 1, "q_ram": 50, "q_devices": 3.5}
	for name, v := range want {
		if got, ok := m.values[name]; !ok || got < v-0.01 || got > v+0.01 {
			t.Errorf("%s = %v (recorded %t), want %v", name, got, ok, v)
		}
	}
	if _, ok := m.values["q_running"]; ok {
		t.Error("q_running recorded from a trace with no run state running")
	}
}

// The traced release is the release with its QEMU wrapped, and nothing else changed: the wrapper
// runs the release's own QEMU with the trace, and every other part is the release's.
func TestTracedReleaseWrapsOnlyQEMU(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	for _, p := range []string{"bin/qemu-system-x86_64", "bin/qemu-img", "kernel/bzImage"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rel, err := tracedRelease(dir, work)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"bin/qemu-img", "kernel/bzImage"} {
		if b, err := os.ReadFile(filepath.Join(rel, p)); err != nil || string(b) != p {
			t.Errorf("%s reads %q, %v: want the release's", p, b, err)
		}
	}
	w, err := os.ReadFile(filepath.Join(rel, "bin/qemu-system-x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(w), filepath.Join(dir, "bin/qemu-system-x86_64")) || !strings.Contains(string(w), "-msg timestamp=on") {
		t.Errorf("the wrapper is %q", w)
	}
}
