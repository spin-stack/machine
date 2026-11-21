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
	if slices.Contains(p.Needs(o), Need{"restore", "channels"}) {
		t.Error("a run naming no channels needs restore --channels")
	}
}

// A row's number is how many restores go at once, whether from the page cache, under which THP
// defrag policy, and its count each one's --channels; a row that names no number, or a place for
// the states that is neither, stops the experiment at its first measurement.
func TestARestoreTogetherRowSaysHowManyAndWithWhat(t *testing.T) {
	for _, tc := range []struct {
		row  string
		want burst
	}{
		{"8 at once, channels 16", burst{n: 8, channels: []string{"--channels", "16"}}},
		{"1 at once", burst{n: 1}},
		{"8 at once from the cache, channels 8", burst{n: 8, cached: true, channels: []string{"--channels", "8"}}},
		{"8 at once from the cache under THP defrag defer+madvise, channels 8", burst{n: 8, cached: true, defrag: "defer+madvise", channels: []string{"--channels", "8"}}},
	} {
		got, err := rowTogether(tc.row)
		if err != nil || got.n != tc.want.n || got.cached != tc.want.cached || got.defrag != tc.want.defrag || !slices.Equal(got.channels, tc.want.channels) {
			t.Errorf("rowTogether(%q) = %+v, %v, want %+v", tc.row, got, err, tc.want)
		}
	}
	p := Probes["restore-together"]
	o, err := settle(p, Options{"together": "8", "states": "evicted cached", "channels": "8"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Rows(o), []string{"8 at once, channels 8", "8 at once from the cache, channels 8"}; !slices.Equal(got, want) {
		t.Errorf("rows with both states = %v, want %v", got, want)
	}
	o, err = settle(p, Options{"together": "1 8", "defrag": "madvise defer", "channels": ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1 at once under THP defrag madvise", "8 at once under THP defrag madvise", "1 at once under THP defrag defer", "8 at once under THP defrag defer"}
	if got := p.Rows(o); !slices.Equal(got, want) {
		t.Errorf("rows under two defrag policies = %v, want %v", got, want)
	}
	o, err = settle(p, Options{"together": "8", "states": "nowhere", "channels": ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range append(p.Rows(o), "0 at once", "many at once", "8", "8 at once, channels 0") {
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
			{Tree: "release", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 100, "login\x001": 110, hostDetail + "compact_stall": 7}},
			{Tree: "release", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 105, "login\x001": 900, hostDetail + "compact_stall": 50}, Missed: "lost"},
			{Tree: "release", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 95, "login\x001": 120, hostDetail + "compact_stall": 7, cpuDetail + "busy ms\x000": 10}},
			{Tree: "other", Row: "2 at once, channels 8", Detail: map[string]float64{"login\x000": 700, "login\x001": 800, hostDetail + "compact_stall": 100}},
		},
	}
	got := pooledLogins(r, "release", "2 at once, channels 8")
	slices.Sort(got)
	if !slices.Equal(got, []float64{95, 100, 110, 120}) {
		t.Errorf("pooled = %v, want the four of the tree's two measurements not lost", got)
	}
	d := (restoreTogether{}).Detail(r)
	for _, want := range []string{"2 at once, channels 1   ", "over 4", `Δ p50 against "2 at once, channels 1"`} {
		if !strings.Contains(d, want) {
			t.Errorf("detail lacks %q:\n%s", want, d)
		}
	}
	// The host's counters are the row's own measurements', of its tree, not lost.
	if !strings.Contains(d, "release, 2 at once, channels 8   compact_stall 7\n") || strings.Count(d, "compact_stall") != 1 {
		t.Errorf("detail lacks the row's host counters, or gives them to a row that has none:\n%s", d)
	}
	// Where on the host's CPUs a row's bursts ran is a line of its own, for the rows that recorded it.
	if !strings.Contains(d, "release, 2 at once, channels 8   busiest cpu0 100% of busy\n") || strings.Count(d, "busiest") != 1 {
		t.Errorf("detail lacks the row's CPU summary, or gives one to a row that has none:\n%s", d)
	}
	if strings.Count(d, "Δ") != 1 {
		t.Errorf("only the second row is set against the first:\n%s", d)
	}
	for _, line := range strings.Split(d, "\n") {
		if strings.Contains(line, "Δ") && !strings.HasPrefix(strings.TrimSpace(line), "release, 2 at once, channels 8") {
			t.Errorf("the Δ is on a row other than the one set against the first: %q", line)
		}
	}
}

// The host's THP defrag policy is the one its file brackets, among the kernel's five.
func TestTheHostsDefragPolicyIsTheBracketedOne(t *testing.T) {
	p, err := defragPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains([]string{"always", "defer", "defer+madvise", "madvise", "never"}, p) {
		t.Errorf("defragPolicy = %q", p)
	}
}
