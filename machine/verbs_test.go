// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// CPUs counts the vCPUs QEMU lists, the boot ones and the added ones alike.
func TestCPUs(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string {
		if c.Command == "query-cpus-fast" {
			return ret(`[{"qom-path": "/machine/unattached/device[0]"}, {"qom-path": "/machine/peripheral/vcpu1"}]`)
		}
		return refused("no")
	}}
	if n, err := f.attach(t).CPUs(ctx(t)); err != nil || n != 2 {
		t.Errorf("CPUs = %d, %v, want 2", n, err)
	}
	f = &fakeQEMU{answer: func(call) string { return refused("no") }}
	if _, err := f.attach(t).CPUs(ctx(t)); err == nil || !strings.Contains(err.Error(), "listing the machine's vCPUs") {
		t.Errorf("CPUs = %v, want QEMU's refusal", err)
	}
}

// Status is QEMU's run state as it named it, and a refusal an error.
func TestStatus(t *testing.T) {
	f := &fakeQEMU{answer: machineAnswers("postmigrate")}
	if st, err := f.attach(t).Status(ctx(t)); err != nil || st != Postmigrate {
		t.Errorf("Status = %q, %v, want %q", st, err, Postmigrate)
	}
	f = &fakeQEMU{answer: func(call) string { return refused("no") }}
	if _, err := f.attach(t).Status(ctx(t)); err == nil || !strings.Contains(err.Error(), "asking the machine's run state") {
		t.Errorf("Status = %v, want QEMU's refusal", err)
	}
}

// Hold activates every disk and sends nothing that would run the guest.
func TestHold(t *testing.T) {
	f := &fakeQEMU{answer: func(call) string { return ok }}
	if err := f.attach(t).Hold(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.recorded(); len(got) != 1 || got[0].Command != "blockdev-set-active" || !reflect.DeepEqual(got[0].Args, map[string]any{"active": true}) {
		t.Errorf("Hold sent %v, want blockdev-set-active with active alone", got)
	}
	f = &fakeQEMU{answer: func(call) string { return refused("no") }}
	if err := f.attach(t).Hold(ctx(t)); err == nil || !strings.Contains(err.Error(), "taking the machine's disks back") {
		t.Errorf("Hold = %v, want QEMU's refusal", err)
	}
}

// End is the last SHUTDOWN, readable once Done says the monitor is gone, and nothing before QEMU
// sent one.
func TestEndIsTheLastShutdown(t *testing.T) {
	f := &fakeQEMU{answer: func(call) string { return ok }}
	m := f.attach(t)
	if e, ok := m.End(); ok {
		t.Errorf("End = %+v before any SHUTDOWN", e)
	}
	f.send(`{"event": "SHUTDOWN", "data": {"guest": false, "reason": "host-qmp-quit"}, "timestamp": {"seconds": 1, "microseconds": 0}}` + "\n" +
		`{"event": "SHUTDOWN", "data": {"guest": true, "reason": "guest-reset"}, "timestamp": {"seconds": 1, "microseconds": 1}}` + "\n")
	// A command answered after the events is one the reader has passed them for.
	if err := m.Hold(ctx(t)); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	select {
	case <-m.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done stayed open after Close")
	}
	if e, ok := m.End(); !ok || e != (End{Guest: true, Reason: GuestReset}) {
		t.Errorf("End = %+v, %v, want the last SHUTDOWN, the guest's reset", e, ok)
	}
}

// Shutdown presses the power button, and a press QEMU refuses is an error.
func TestShutdown(t *testing.T) {
	f := &fakeQEMU{answer: func(call) string { return ok }}
	if err := f.attach(t).Shutdown(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.commands(); len(got) != 1 || got[0] != "system_powerdown" {
		t.Errorf("Shutdown sent %v, want system_powerdown", got)
	}
	f = &fakeQEMU{answer: func(call) string { return refused("no") }}
	if err := f.attach(t).Shutdown(ctx(t)); err == nil || !strings.Contains(err.Error(), "pressing the power button: system_powerdown: GenericError: no") {
		t.Errorf("Shutdown = %v, want QEMU's refusal", err)
	}
}

// balloonAnswers answers each read of the balloon's statistics with the next of reports.
func balloonAnswers(reports ...string) func(c call) string {
	return func(c call) string {
		if c.Command != "qom-get" {
			return ok
		}
		r := reports[0]
		reports = reports[1:]
		return ret(r)
	}
}

// GuestMemory turns the balloon's reports on once, at its first call, and reads the last report:
// none yet is ErrNoGuestStats, and one without the two numbers is an error.
func TestGuestMemory(t *testing.T) {
	f := &fakeQEMU{answer: balloonAnswers(
		`{"last-update": 0, "stats": {"stat-total-memory": 18446744073709551615, "stat-available-memory": 18446744073709551615}}`,
		`{"last-update": 1700000000, "stats": {"stat-total-memory": 1073741824, "stat-available-memory": 536870912}}`,
		`{"last-update": 1700000000, "stats": {"stat-total-memory": 1073741824, "stat-available-memory": 18446744073709551615}}`,
		`{"last-update": 1700000000, "stats": {"stat-total-memory": 18446744073709551615, "stat-available-memory": 1}}`,
	)}
	m := f.attach(t)
	if _, err := m.GuestMemory(ctx(t)); !errors.Is(err, ErrNoGuestStats) {
		t.Errorf("GuestMemory before a report = %v, want ErrNoGuestStats", err)
	}
	got, err := m.GuestMemory(ctx(t))
	if want := (GuestMemory{Total: 1024, Available: 512, At: time.Unix(1700000000, 0)}); err != nil || got != want {
		t.Errorf("GuestMemory = %+v, %v, want %+v", got, err, want)
	}
	for _, missing := range []string{"available", "total"} {
		if _, err := m.GuestMemory(ctx(t)); err == nil || errors.Is(err, ErrNoGuestStats) {
			t.Errorf("GuestMemory of a report without %s memory = %v, want an error", missing, err)
		}
	}
	want := []map[string]any{{"path": "/machine/peripheral/balloon0", "property": "guest-stats-polling-interval", "value": float64(1)}}
	if set := f.sent("qom-set"); !reflect.DeepEqual(set, want) {
		t.Errorf("qom-set %v, want the polling turned on once: %v", set, want)
	}
	if read := f.sent("qom-get"); len(read) != 4 || !reflect.DeepEqual(read[0], map[string]any{"path": "/machine/peripheral/balloon0", "property": "guest-stats"}) {
		t.Errorf("qom-get %v, want the balloon's guest-stats", read)
	}
}

// A polling QEMU refuses is not remembered as on, and a refused read is an error.
func TestGuestMemoryRefused(t *testing.T) {
	sets := 0
	f := &fakeQEMU{answer: func(c call) string {
		if c.Command == "qom-set" {
			if sets++; sets < 3 {
				return refused("no")
			}
			return ok
		}
		return refused("no")
	}}
	m := f.attach(t)
	for range 2 {
		if _, err := m.GuestMemory(ctx(t)); err == nil || !strings.Contains(err.Error(), "asking the guest to report its memory") {
			t.Errorf("GuestMemory = %v, want the polling's refusal", err)
		}
	}
	if _, err := m.GuestMemory(ctx(t)); err == nil || !strings.Contains(err.Error(), "reading the guest's report of its memory") {
		t.Errorf("GuestMemory = %v, want the read's refusal", err)
	}
	if got := f.sent("qom-set"); len(got) != 3 {
		t.Errorf("asked for the polling %d times, want once a call until QEMU took it", len(got))
	}
}
