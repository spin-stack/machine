// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Channels to compare are each count's restore rows over again, and the rows of a probe asked for
// none are the rows it always had: a ladder of releases reads restore-first's "evicted".
func TestEachChannelCountIsTheRestoreRowsOverAgain(t *testing.T) {
	rf, ps := Probes["restore-first"], Probes["prestart"]
	o, err := settle(rf, Options{"channels": "1 4"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rf.Rows(o); !slices.Equal(got, []string{"evicted, channels 1", "cached, channels 1", "evicted, channels 4", "cached, channels 4"}) {
		t.Errorf("restore-first's rows = %v", got)
	}
	o, err = settle(ps, Options{"channels": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ps.Rows(o); !slices.Equal(got, []string{rowCold, rowPrestarted, "restored, channels 2", "restored into prestarted, channels 2"}) {
		t.Errorf("prestart's rows = %v", got)
	}
	none, _ := settle(rf, nil)
	if got := rf.Rows(none); !slices.Equal(got, []string{"evicted", "cached"}) {
		t.Errorf("restore-first's rows with no channels = %v", got)
	}
}

// A row's count is spin-machine's --channels for its save and its restore; a row named for no
// count passes none, and one named for something that is not a count stops the experiment.
func TestARowsChannelsAreSpinMachinesFlag(t *testing.T) {
	for _, tc := range []struct {
		row, way string
		flags    []string
	}{
		{"evicted, channels 4", "evicted", []string{"--channels", "4"}},
		{"restored into prestarted, channels 1", rowRestoredAhead, []string{"--channels", "1"}},
		{"cached", "cached", nil},
	} {
		way, flags, err := rowChannels(tc.row)
		if err != nil || way != tc.way || !slices.Equal(flags, tc.flags) {
			t.Errorf("rowChannels(%q) = %q, %v, %v", tc.row, way, flags, err)
		}
	}
	for _, row := range []string{"evicted, channels many", "evicted, channels 0"} {
		for _, p := range []Probe{Probes["restore-first"], Probes["prestart"]} {
			err := p.Measure(context.Background(), &Measurement{Row: row})
			if err == nil || !strings.Contains(err.Error(), "not a number of channels") {
				t.Errorf("%s's Measure of row %q = %v, want refused", p.Name(), row, err)
			}
		}
	}
}
