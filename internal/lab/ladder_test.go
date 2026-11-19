// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestALadderIsTheNewestReleasesUpToOneOldestFirst(t *testing.T) {
	tags := []string{"v5", "v4", "v3", "v2", "v1"} // as gh lists them, newest first
	for _, tc := range []struct {
		newest string
		n      int
		want   []string
	}{
		{"", 3, []string{"v3", "v4", "v5"}},
		{"v5", 2, []string{"v4", "v5"}},
		{"v4", 2, []string{"v3", "v4"}},
		{"v2", 5, []string{"v1", "v2"}},
		{"", 9, []string{"v1", "v2", "v3", "v4", "v5"}},
	} {
		got, err := LadderReleases(tags, tc.newest, tc.n)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("LadderReleases(%q, %d) = %v, %v; want %v", tc.newest, tc.n, got, err, tc.want)
		}
	}
	if _, err := LadderReleases(tags, "v9", 2); err == nil || !strings.Contains(err.Error(), "no published release v9") {
		t.Errorf("an unknown newest: %v", err)
	}
	if got, _ := LadderReleases(tags, "", 2); tags[0] != "v5" || len(got) != 2 {
		t.Errorf("the tags given were changed: %v", tags)
	}
}

// samples is reps of one value for each of a tree's rows' metrics.
func samples(tree, row string, values map[string]float64, reps int) []Sample {
	var out []Sample
	for i := range reps {
		out = append(out, Sample{Tree: tree, Row: row, Rep: i + 1, Values: values})
	}
	return out
}

func testLadder() *Ladder {
	host := Host{Name: "h", Instance: "c8id.4xlarge", Nested: true}
	ssh := &Result{Probe: "ssh-first", Reps: 2, Host: host}
	ssh.Samples = append(samples("v1", "boot", map[string]float64{"first_login": 340, "qemu_rss": 300}, 2),
		samples("v2", "boot", map[string]float64{"first_login": 318.5, "qemu_rss": 290}, 2)...)
	rf := &Result{Probe: "restore-first", Reps: 2, Host: host,
		Samples: samples("v2", "evicted", map[string]float64{"login": 152, "save": 151, "state": 140}, 2)}
	ps := &Result{Probe: "prestart", Reps: 2, Host: host,
		Samples: append(samples("v2", rowPrestarted, map[string]float64{"login": 296}, 2),
			Sample{Tree: "v2", Row: rowRestoredAhead, Rep: 1, Missed: "never answered"})}
	return &Ladder{Releases: []string{"v1", "v2"}, Results: []*Result{ssh, rf, ps},
		Cannot: map[string]map[string]string{
			"restore-first": {"v1": "its spin-machine's boot has no --restore"},
			"prestart":      {"v1": "its spin-machine's boot has no --held"},
		}}
}

// The table is each release's goal numbers against the release above and against the goal, a
// dash with its reason where a release could not run a probe or lost every measurement.
func TestTheLadderSaysEachReleaseAgainstTheOneBeforeAndTheGoal(t *testing.T) {
	md := testLadder().Markdown()
	for _, want := range []string{
		"over 2 boots of each release, interleaved on one host (c8id.4xlarge, nested true)",
		"the goal is 100 ms",
		"| v1 | 340 / 340 | | - | | - | - |",
		"| v2 | 318 / 318 | -21.5 | 152 / 152 | | 296 / 296 | - |",
		"v2 against the goal: cold +218 ms, restore +52 ms.",
		"| v1 | - | - | - | 300 |",
		"| v2 | 151 | 140 | - | 290 |",
		"- v1, restore to first login: its spin-machine's boot has no --restore",
		"- v1, cold, QEMU started ahead: its spin-machine's boot has no --held",
		"- v2, restore, QEMU started ahead: lost all 1",
		"- v1, cold boot to first login",
	} {
		if want == "- v1, cold boot to first login" {
			if strings.Contains(md, want) {
				t.Errorf("a number that is there has a note:\n%s", md)
			}
			continue
		}
		if !strings.Contains(md, want) {
			t.Errorf("no %q in:\n%s", want, md)
		}
	}
	// A probe that never ran is said, not taken for zero.
	l := testLadder()
	l.Results = l.Results[:1]
	l.Cannot = nil
	if md := l.Markdown(); !strings.Contains(md, "v2, restore to first login: not measured") {
		t.Errorf("a probe that did not run:\n%s", md)
	}
	if md := (&Ladder{}).Markdown(); !strings.Contains(md, "no host") || !strings.Contains(md, "over 0 boots") ||
		strings.Contains(md, "against the goal") || strings.Contains(md, "A dash") {
		t.Errorf("an empty ladder:\n%s", md)
	}
	// One release, and one number it has not: the goal line and the one note.
	one := &Ladder{Releases: []string{"v1"}, Results: []*Result{
		{Probe: "ssh-first", Reps: 1, Samples: samples("v1", "boot", map[string]float64{"first_login": 300}, 1)},
		{Probe: "prestart", Reps: 1, Samples: append(samples("v1", rowPrestarted, map[string]float64{"login": 280}, 1),
			samples("v1", rowRestoredAhead, map[string]float64{"login": 120}, 1)...)},
	}, Cannot: map[string]map[string]string{"restore-first": {"v1": "no restore"}}}
	md = one.Markdown()
	if !strings.Contains(md, "v1 against the goal: cold +200 ms.") || !strings.Contains(md, "A dash is a number that release has not") ||
		strings.Count(md, "\n- v1, ") != 1 || !strings.Contains(md, "- v1, restore to first login: no restore") {
		t.Errorf("one release:\n%s", md)
	}
}

// A release whose spin-machine can run none of the ladder's probes is in it with why, and nothing
// boots for it.
func TestALadderRunsEachProbeOnlyOverTheReleasesThatCan(t *testing.T) {
	old := treeWith(t, map[string][]string{"boot": {"release"}})
	old.Manifest.Name = "v0"
	l, err := RunLadder(t.Context(), []*Tree{old}, 1, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Results) != 0 || !reflect.DeepEqual(l.Releases, []string{"v0"}) {
		t.Errorf("ladder %+v", l)
	}
	for _, p := range ladderProbes {
		if why := l.Cannot[p]["v0"]; !strings.Contains(why, "has no --") {
			t.Errorf("%s: why %q", p, why)
		}
	}
}

// A ladder runs each probe over the releases that can, and the others are why they cannot.
func TestALadderMeasuresWhatEachReleaseCanAndSaysWhatItCannot(t *testing.T) {
	Probes["recorder"] = &recorder{phases: 1, rows: []string{"r"}}
	t.Cleanup(func() { delete(Probes, "recorder") })
	saved := ladderProbes
	ladderProbes = []string{"recorder", "prestart"}
	t.Cleanup(func() { ladderProbes = saved })
	old := treeWith(t, map[string][]string{"boot": {"release"}})
	old.Manifest.Name = "v1"
	l, err := RunLadder(t.Context(), []*Tree{old}, 2, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Results) != 1 || l.Results[0].Probe != "recorder" || len(l.Results[0].Samples) != 2 {
		t.Fatalf("results %+v", l.Results)
	}
	if !strings.Contains(l.Cannot["prestart"]["v1"], "has no --") || len(l.Cannot) != 1 {
		t.Errorf("cannot %v", l.Cannot)
	}
	// A probe that fails stops the ladder, and says which.
	Probes["recorder"] = &recorder{phases: 1, rows: []string{"r"}, fail: "v1/r/0/0"}
	if _, err := RunLadder(t.Context(), []*Tree{old}, 1, t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "the ladder's recorder") {
		t.Errorf("a probe that fails: %v", err)
	}
}

func TestALadderIsWrittenAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ladder.json")
	l := testLadder()
	if err := l.Write(path); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLadder(path)
	if err != nil || got.Markdown() != l.Markdown() {
		t.Errorf("read back: %v", err)
	}
	if _, err := ReadLadder(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("a ladder from nowhere")
	}
	if err := l.Write(filepath.Join(t.TempDir(), "no", "dir", "ladder.json")); err == nil {
		t.Error("a ladder written nowhere")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	write(t, bad, []byte("{"))
	if _, err := ReadLadder(bad); err == nil {
		t.Error("a ladder that is not JSON")
	}
}

// A section published again replaces the one before it, from its marker on.
func TestASectionPublishedAgainReplacesItself(t *testing.T) {
	m := "<!-- m -->"
	once := WithSection("notes\n\n", m, "table 1\n")
	if once != "notes\n\n"+m+"\ntable 1\n" {
		t.Errorf("once: %q", once)
	}
	if twice := WithSection(once, m, "table 2\n"); twice != "notes\n\n"+m+"\ntable 2\n" {
		t.Errorf("twice: %q", twice)
	}
	// Notes that are only the section are only the new one.
	if only := WithSection(m+"\ntable 1\n", m, "table 2\n"); only != "\n\n"+m+"\ntable 2\n" {
		t.Errorf("only: %q", only)
	}
}
