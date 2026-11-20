// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A ladder's release is a record: each number it has, to a tenth, and why it has not the others,
// with the host, the boots and when the ladder began.
func TestALaddersReleasesAreRecords(t *testing.T) {
	got := testLadder().Records()
	if len(got) != 2 || got[0].Release != "v1" {
		t.Fatalf("records %+v", got)
	}
	noRestore, noHeld := "its spin-machine's boot has no --restore", "its spin-machine's boot has no --held"
	want := []Record{{
		Release: "v1", Measured: time.Date(2026, 11, 20, 11, 40, 7, 0, time.UTC), Host: "c8id.4xlarge, nested", Reps: 2,
		Numbers: map[string]Number{"cold": {340, 340}, "qemu-rss-mib": {300, 300}},
		Cannot: map[string]string{"restore": noRestore, "save-ms": noRestore, "state-mib": noRestore,
			"cold-ahead": noHeld, "restore-ahead": noHeld, "kernel-ms": "not measured"},
	}, {
		Release: "v2", Measured: time.Date(2026, 11, 20, 11, 40, 7, 0, time.UTC), Host: "c8id.4xlarge, nested", Reps: 2,
		Numbers: map[string]Number{"cold": {321, 321}, "restore": {152, 152}, "cold-ahead": {296, 296},
			"save-ms": {151, 151}, "state-mib": {140, 140}, "qemu-rss-mib": {290, 290}},
		Cannot: map[string]string{"restore-ahead": "lost all 1", "kernel-ms": "not measured"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records\n%+v\nwant\n%+v", got, want)
	}
	// A host that is not nested is its name alone; a ladder that ran nothing says so.
	bare := &Ladder{Releases: []string{"v1"}, Results: []*Result{{Probe: "ssh-first", Reps: 3, Host: Host{Name: "metal"}}}}
	if r := bare.Records()[0]; r.Host != "metal" || r.Reps != 3 {
		t.Errorf("a host that is not nested: %+v", r)
	}
	if r := (&Ladder{Releases: []string{"v1"}}).Records()[0]; r.Host != "no host" || r.Reps != 0 || !r.Measured.IsZero() {
		t.Errorf("a ladder that ran nothing: %+v", r)
	}
}

func TestARecordIsWrittenAndReadBackOldestFirst(t *testing.T) {
	dir := t.TempDir()
	records := testLadder().Records()
	for _, r := range []Record{records[1], records[0]} {
		if err := WriteRecord(filepath.Join(dir, r.Release+".json"), r); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, "README"), []byte("not a record"))
	got, err := ReadRecords(dir)
	if err != nil || !reflect.DeepEqual(got, records) {
		t.Errorf("read back %+v, %v", got, err)
	}
	if err := WriteRecord(filepath.Join(dir, "no", "v1.json"), records[0]); err == nil {
		t.Error("a record written nowhere")
	}
	if _, err := ReadRecord(filepath.Join(dir, "v9.json")); err == nil {
		t.Error("a record from nowhere")
	}
	write(t, filepath.Join(dir, "v3.json"), []byte("{"))
	if _, err := ReadRecords(dir); err == nil || !strings.Contains(err.Error(), "v3.json") {
		t.Errorf("a record that is not JSON: %v", err)
	}
	if _, err := ReadRecords(filepath.Join(dir, "[")); err == nil {
		t.Error("a directory no pattern can name")
	}
}

// docRecords are the test ladder's records, the newest kept by a run.
func docRecords() []Record {
	records := testLadder().Records()
	records[1].Run = "https://x/actions/runs/7"
	return records
}

// The page shows the chart beside it, tables each release's numbers with Δ against the row above,
// and the newest against the goal; and says where each was measured, linking its record, and why
// a number is a dash.
func TestTheDocChartsAndTablesTheReleases(t *testing.T) {
	doc := Doc(docRecords(), "kept")
	if !strings.HasPrefix(doc, "<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json") {
		t.Errorf("the page does not open by saying what writes it:\n%s", doc)
	}
	for _, want := range []string{
		"7.3% later",
		"\n![First SSH login, p50 and p95, release by release](kept.svg)\n",
		"| v1 | 340 / 340 |  | - |  | - | - |",
		"| v2 | 321 / 321 | -19.0 | 152 / 152 |  | 296 / 296 | - |",
		"v2 against the goal: cold +221 ms, restore +52 ms.",
		"| v1 | - | - | - | 300 |",
		"| v2 | 151 | 140 | - | 290 |",
		"| [v1](kept/v1.json) | 2026-11-20 | c8id.4xlarge, nested | 2 |  |",
		"| [v2](kept/v2.json) | 2026-11-20 | c8id.4xlarge, nested | 2 | [7](https://x/actions/runs/7) |",
		"- v1, restore to first login: its spin-machine's boot has no --restore",
		"- v1, cold, QEMU started ahead: its spin-machine's boot has no --held",
		"- v2, restore, QEMU started ahead: lost all 1",
		"- v2, kernel to \"Freeing\" ms: not measured",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("no %q in:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "- v1, cold boot to first login") {
		t.Errorf("a number that is there has a note:\n%s", doc)
	}
	// A ladder's tables have no chart, and no record to link a release to.
	if ladder := Tables(docRecords()); strings.Contains(ladder, ".svg") || !strings.Contains(ladder, "| v1 | 2026-11-20 |") {
		t.Errorf("a ladder's tables:\n%s", ladder)
	}
	// One release, with one of the goal's numbers: its gap alone.
	if one := Doc(docRecords()[:1], "kept"); !strings.Contains(one, "v1 against the goal: cold +240 ms.\n") {
		t.Errorf("one release:\n%s", one)
	}
	// One dash is said as well as many.
	lone := Record{Release: "v3", Cannot: map[string]string{"cold": "lost all 2"}}
	if got := Tables([]Record{lone}); !strings.HasSuffix(got, "\nA dash is a number that release has not:\n\n- v3, cold boot to first login: lost all 2\n") {
		t.Errorf("one dash:\n%s", got)
	}
	empty := Doc(nil, "kept")
	if strings.Contains(empty, "against the goal") || strings.Contains(empty, "A dash") {
		t.Errorf("no records:\n%s", empty)
	}
}

// Two records are side by side, the older first whichever is named first, each with its run, and
// with how to measure the two together.
func TestTwoRecordsAreComparedSideBySide(t *testing.T) {
	records := docRecords()
	got := Compare(records[1], records[0])
	if !strings.HasPrefix(got, "v1: measured 2026-11-20 on c8id.4xlarge, nested, 2 boots of each probe.\n"+
		"v2: measured 2026-11-20 on c8id.4xlarge, nested, 2 boots of each probe (https://x/actions/runs/7).\n") {
		t.Errorf("the headers:\n%s", got)
	}
	for _, want := range []string{
		"`spin-machine-dev lab ladder --newest v2 --releases N`, N the releases from v1 to v2.",
		"| | v1 | v2 | Δ p50 |",
		"| cold boot to first login | 340 / 340 | 321 / 321 | -19.0 |",
		"| restore to first login | - | 152 / 152 | |",
		"- v1, restore to first login: its spin-machine's boot has no --restore",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	one := records[1].Table()
	if !strings.HasPrefix(one, "v2: measured") || !strings.Contains(one, "| | v2 |\n") || !strings.Contains(one, "| save ms | 151 / 151 |") ||
		strings.Contains(one, "Δ") {
		t.Errorf("one record:\n%s", one)
	}
}

// docs/releases.md and its chart, docs/releases.svg, are what Doc and Chart write from the records:
// a hand edit, or a record committed without the two written again, fails here (spin-machine-dev
// releases doc writes them).
func TestTheCommittedPageIsTheRecords(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "releases")
	records, err := ReadRecords(dir)
	if err != nil || len(records) == 0 {
		t.Fatalf("records %d, %v", len(records), err)
	}
	for path, want := range map[string]string{dir + ".md": Doc(records, "releases"), dir + ".svg": Chart(records)} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s is not what docs/releases/*.json say: run spin-machine-dev releases doc", path)
		}
	}
}
