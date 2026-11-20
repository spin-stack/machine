// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// The rows are every number at once for each count of channels, by default the question it was
// made for: 1, 4 and 8 restores at once with 1, 8 and 16 channels.
func TestARestoreTogetherRowIsANumberAtOnceAndItsChannels(t *testing.T) {
	p := Probes["restore-together"]
	o, err := settle(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"1 at once, channels 1", "4 at once, channels 1", "8 at once, channels 1",
		"1 at once, channels 8", "4 at once, channels 8", "8 at once, channels 8",
		"1 at once, channels 16", "4 at once, channels 16", "8 at once, channels 16",
	}
	if got := p.Rows(o); !slices.Equal(got, want) {
		t.Errorf("default rows = %v", got)
	}
	// Two passes: every checkpoint is made before any restore.
	if p.Phases() != 2 {
		t.Errorf("restore-together makes %d passes, want 2", p.Phases())
	}
	// No channels named is the tree's spin-machine's own count, which is how the default is
	// measured once it is not 1.
	o, err = settle(p, Options{"together": "2", "channels": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Rows(o); !slices.Equal(got, []string{"2 at once"}) {
		t.Errorf("rows with no channels = %v", got)
	}
	if slices.Contains(p.(Needer).Needs(o), Need{"restore", "channels"}) {
		t.Error("a run naming no channels needs restore --channels")
	}
}

// A row's number is how many restores go at once, and its count each one's --channels; a row
// that names no number stops the experiment before anything boots.
func TestARestoreTogetherRowSaysHowManyAndWithWhat(t *testing.T) {
	for _, tc := range []struct {
		row   string
		n     int
		flags []string
	}{
		{"8 at once, channels 16", 8, []string{"--channels", "16"}},
		{"1 at once", 1, nil},
	} {
		n, flags, err := rowTogether(tc.row)
		if err != nil || n != tc.n || !slices.Equal(flags, tc.flags) {
			t.Errorf("rowTogether(%q) = %d, %v, %v", tc.row, n, flags, err)
		}
	}
	for _, row := range []string{"0 at once", "many at once", "8", "8 at once, channels 0"} {
		err := (restoreTogether{}).Measure(context.Background(), &Measurement{Row: row})
		if err == nil || !strings.Contains(err.Error(), "not a number") {
			t.Errorf("Measure of row %q = %v, want refused", row, err)
		}
	}
}

// The detail pools every restore of every measurement not lost, and sets each row against the
// same number at once with the first count of channels.
func TestRestoreTogetherPoolsEveryRestore(t *testing.T) {
	r := &Result{
		Rows:  []string{"2 at once, channels 1", "2 at once, channels 8"},
		Trees: []Manifest{{Name: "release"}},
		Samples: []Sample{
			{Tree: "release", Row: "2 at once, channels 1", Detail: map[string]float64{"login\x000": 150, "login\x001": 160}},
			{Tree: "release", Row: "2 at once, channels 1", Detail: map[string]float64{"login\x000": 140, "login\x001": 170}},
			{Tree: "release", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 100, "login\x001": 110}},
			{Tree: "release", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 105, "login\x001": 900}, Missed: "lost"},
			{Tree: "release", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 95, "login\x001": 120}},
		},
	}
	if got := pooledLogins(r, "release", "2 at once, channels 8"); len(got) != 4 || slices.Contains(got, 900) {
		t.Errorf("pooled = %v, want the four of the two measurements not lost", got)
	}
	d := (restoreTogether{}).Detail(r)
	for _, want := range []string{"2 at once, channels 1   ", "over 4", `Δ p50 against "2 at once, channels 1"`} {
		if !strings.Contains(d, want) {
			t.Errorf("detail lacks %q:\n%s", want, d)
		}
	}
	if strings.Count(d, "Δ") != 1 {
		t.Errorf("only the second row is set against the first:\n%s", d)
	}
}
