// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// The rows are the crowds asked for, and a crowd that is not a number stops the experiment
// before anything boots.
func TestADensityRowIsTheNumberOfWorkspacesAlreadyRunning(t *testing.T) {
	o, err := settle(density{}, Options{"crowds": "0 4 12"})
	if err != nil {
		t.Fatal(err)
	}
	if got := (density{}).Rows(o); !slices.Equal(got, []string{"0", "4", "12"}) {
		t.Errorf("rows = %v", got)
	}
	if o["load"] != "idle" || o["settle"] != "10" {
		t.Errorf("defaults: load %q, settle %q", o["load"], o["settle"])
	}
	// One pass: a measurement boots its crowd and ends it, so a second phase would boot every crowd
	// again for nothing it measures.
	if p := (density{}).Phases(); p != 1 {
		t.Errorf("density makes %d passes, want 1", p)
	}
	// A crowd that cannot be made stops at its first workspace and says so, with nothing left
	// running: this tree has no qemu-img to make the overlay with.
	m := &Measurement{Tree: fakeSpinMachine(t, "sleep 30"), Row: "2", Options: o, Dir: t.TempDir(), Key: &Key{}}
	if err := (density{}).Measure(context.Background(), m); err == nil || !strings.Contains(err.Error(), "qemu-img") {
		t.Errorf("Measure of a crowd whose overlays cannot be made = %v, want the overlay's error", err)
	}
	for _, row := range []string{"many", "-1"} {
		err := (density{}).Measure(context.Background(), &Measurement{Row: row, Options: o})
		if err == nil || !strings.Contains(err.Error(), "not a number") {
			t.Errorf("Measure of a crowd of %q = %v, want refused", row, err)
		}
	}
}
