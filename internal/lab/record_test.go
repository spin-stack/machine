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
// with the host, the shape, the boots and when the ladder began; and each but the first against
// the release before it in the ladder, with that release's numbers and the Δ of each both have.
func TestALaddersReleasesAreRecords(t *testing.T) {
	got := testLadder().Records()
	if len(got) != 2 || got[0].Release != "v1" {
		t.Fatalf("records %+v", got)
	}
	noRestore, noHeld := "its spin-machine's boot has no --restore", "its spin-machine's boot has no --held"
	v1 := map[string]Number{"cold": {340, 340}, "qemu-rss-mib": {300, 300}}
	want := []Record{{
		Release: "v1", Measured: time.Date(2026, 11, 20, 11, 40, 7, 0, time.UTC), Host: "c8id.4xlarge, nested",
		Shape: spinsShape, Reps: 2, Numbers: v1,
		Cannot: map[string]string{"restore": noRestore, "save-ms": noRestore, "state-mib": noRestore,
			"cold-ahead": noHeld, "restore-ahead": noHeld, "kernel-ms": "not measured"},
	}, {
		Release: "v2", Measured: time.Date(2026, 11, 20, 11, 40, 7, 0, time.UTC), Host: "c8id.4xlarge, nested",
		Shape: spinsShape, Reps: 2,
		Numbers: map[string]Number{"cold": {321, 321}, "restore": {152, 152}, "cold-ahead": {296, 296},
			"save-ms": {151, 151}, "state-mib": {140, 140}, "qemu-rss-mib": {290, 290}},
		Cannot: map[string]string{"restore-ahead": "lost all 1", "kernel-ms": "not measured"},
		Against: &Against{Release: "v1", Numbers: v1,
			Delta: map[string]Delta{"cold": {-19, -19, -19}, "qemu-rss-mib": {-10, -10, -10}}},
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
// and the newest against the goal; and says where each was measured, linking its record. Why a
// number is a dash is the ladder's to say, not the page's.
func TestTheDocChartsAndTablesTheReleases(t *testing.T) {
	doc := Doc(docRecords(), "kept")
	if !strings.HasPrefix(doc, "<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json") {
		t.Errorf("the page does not open by saying what writes it:\n%s", doc)
	}
	for _, want := range []string{
		"158.9 ms p50 in one run and 167.0 in another",
		"\n![First SSH login, p50 and p95, release by release](kept.svg)\n",
		"| v1 | 340 / 340 |  | - |  | - | - |",
		"| v2 | 321 / 321 | -19.0 [-19.0, -19.0] | 152 / 152 |  | 296 / 296 | - |",
		// v1 is the first in its run: it has no Δ, and the page says why.
		"A release with no Δ was measured with no release before it in its run",
		"v2 against the goal: cold +221 ms, restore +52 ms.",
		"| v1 | - | - | - | 300 |",
		"| v2 | 151 | 140 | - | 290 |",
		"| [v1](kept/v1.json) | 2026-11-20 | c8id.4xlarge, nested | " + spinsShape + " | 2 |  |  |",
		"| [v2](kept/v2.json) | 2026-11-20 | c8id.4xlarge, nested | " + spinsShape + " | 2 | v1 | [7](https://x/actions/runs/7) |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("no %q in:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, dashesBefore) {
		t.Errorf("the page says why a number is a dash:\n%s", doc)
	}
	// A ladder's tables have no chart, and no record to link a release to; they say why each
	// number is a dash, and only for a number that is not there.
	ladder := Tables(docRecords())
	if strings.Contains(ladder, ".svg") || !strings.Contains(ladder, "| v1 | 2026-11-20 |") {
		t.Errorf("a ladder's tables:\n%s", ladder)
	}
	for _, want := range []string{
		"- v1, restore to first login: its spin-machine's boot has no --restore",
		"- v1, cold, QEMU started ahead: its spin-machine's boot has no --held",
		"- v2, restore, QEMU started ahead: lost all 1",
		"- v2, kernel to \"Freeing\" ms: not measured",
	} {
		if !strings.Contains(ladder, want) {
			t.Errorf("no %q in:\n%s", want, ladder)
		}
	}
	if strings.Contains(ladder, "- v1, cold boot to first login") {
		t.Errorf("a number that is there has a note:\n%s", ladder)
	}
	// One release, with one of the goal's numbers: its gap alone.
	if one := Doc(docRecords()[:1], "kept"); !strings.Contains(one, "v1 against the goal: cold +240 ms.\n") {
		t.Errorf("one release:\n%s", one)
	}
	// A Δ is the record's against the release measured with it, never the row above: a release
	// measured alone has none, and one whose interval includes 0 is within noise. A record from
	// before records named their shape booted the shape the probes booted then.
	alone, noisy := docRecords()[1], docRecords()[1]
	alone.Against, alone.Shape = nil, ""
	noisy.Release, noisy.Against = "v3", &Against{Release: "v1", Delta: map[string]Delta{"cold": {P50: -2, Lo: -5, Hi: 1}}}
	got := Tables([]Record{docRecords()[0], alone, noisy})
	for _, want := range []string{
		"| v2 | 321 / 321 |  | 152 / 152 |",
		"| v3 | 321 / 321 | -2.0 [-5.0, +1.0] within noise | 152 / 152 |",
		"| v2 | 2026-11-20 | c8id.4xlarge, nested | " + shapeBefore + " | 2 |  |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	// Every release measured with the one before it: nothing to say about a blank Δ.
	if got := Tables(docRecords()[1:]); strings.Contains(got, "A release with no Δ") {
		t.Errorf("a page with every release measured against another explains a blank Δ:\n%s", got)
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

// The bounds are a table of their own once a record has one, between the diagnostics and where each
// was measured, a release from before them a dash; until then the page has no such table.
func TestTheBoundsAreTabledOnceMeasured(t *testing.T) {
	if got := Doc(docRecords(), "kept"); strings.Contains(got, "## Bounds") {
		t.Errorf("a page with no bound measured tables them:\n%s", got)
	}
	records := docRecords()
	records[1].Numbers["floor-lane"], records[1].Numbers["floor"] = Number{404, 410}, Number{250, 255}
	records[1].Bounds = map[string]Delta{"floor": {P50: -154, Lo: -160.5, Hi: -150.2}}
	got := Doc(records, "kept")
	for _, want := range []string{
		"## Diagnostics\n",
		"## Bounds\n\nNever candidates",
		"| release | first login by the lane | no systemd before sshd | Δ |\n|---|---|---|---|\n| v1 | - | - |  |\n" +
			"| v2 | 404 / 410 | 250 / 255 | -154.0 [-160.5, -150.2] |\n",
		"## Where each was measured\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if d, b, w := strings.Index(got, "## Diagnostics"), strings.Index(got, "## Bounds"), strings.Index(got, "## Where"); d > b || b > w {
		t.Errorf("the bounds are not between the diagnostics and where:\n%s", got)
	}
	if !strings.Contains(got, "| v2 | 151 | 140 | - | 290 |\n") {
		t.Errorf("a bound is among the diagnostics:\n%s", got)
	}
}

// The floor's Δ is drawn from the boots themselves: testdata/floor-45018301368.json is the floor's
// samples from report.yml run 45018301368 (v20261122.02, 20 boots each, bench fleet), and its
// record keeps the Δ through its JSON to the page.
func TestAFloorsRecordKeepsItsDeltaFromItsBoots(t *testing.T) {
	l, err := ReadLadder(filepath.Join("testdata", "floor-45018301368.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "v20261122.02.json")
	if err := WriteRecord(path, l.Records()[1]); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Delta{P50: -166.3, Lo: -174.7, Hi: -160.3}); r.Bounds["floor"] != want {
		t.Errorf("the floor against the lane's login: %+v, not %+v", r.Bounds, want)
	}
	if got := Tables([]Record{r}); !strings.Contains(got, "| v20261122.02 | 407 / 420 | 240 / 246 | -166.3 [-174.7, -160.3] |\n") {
		t.Errorf("the floor's row:\n%s", got)
	}
}

// Two records are side by side, the older first whichever is named first. When the newer was
// measured against the older, the two are that run's numbers with the newer's Δ and its interval;
// otherwise each is its own run's, with no Δ, why not, and how to measure the two together.
func TestTwoRecordsAreComparedSideBySide(t *testing.T) {
	records := docRecords()
	records[1].Against.Numbers = map[string]Number{"cold": {338, 339}}
	got := Compare(records[1], records[0])
	if !strings.HasPrefix(got, "v2: measured 2026-11-20 on c8id.4xlarge, nested; "+spinsShape+"; 2 boots of each probe, "+
		"interleaved with v1 (https://x/actions/runs/7).\n\nv1's numbers are the ones booted with v2 in its run") {
		t.Errorf("the header:\n%s", got)
	}
	for _, want := range []string{
		"| | v1 | v2 | Δ p50 |\n|---|---|---|---|\n",
		"| cold boot to first login | 338 / 339 | 321 / 321 | -19.0 [-19.0, -19.0] |",
		"| restore to first login | - | 152 / 152 | |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}

	records[1].Against.Release = "v0"
	got = Compare(records[0], records[1])
	if !strings.HasPrefix(got, "v1: measured 2026-11-20 on c8id.4xlarge, nested; "+spinsShape+"; 2 boots of each probe.\n"+
		"v2: measured 2026-11-20 on c8id.4xlarge, nested; "+spinsShape+"; 2 boots of each probe, interleaved with v0 (https://x/actions/runs/7).\n") {
		t.Errorf("the headers of two runs:\n%s", got)
	}
	for _, want := range []string{
		"v1 and v2 were measured in different runs",
		"`spin-machine-dev lab ladder --newest v2 --releases N`, N the releases from v1 to v2.",
		"| | v1 | v2 |\n|---|---|---|\n",
		"| cold boot to first login | 340 / 340 | 321 / 321 |\n",
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

// A withdrawn release is named so on the page and the site, with why, the issue and when; the page
// says what that means; and the gap to the goal is the newest release's that was not withdrawn.
func TestTheDocNamesAWithdrawnRelease(t *testing.T) {
	records := docRecords()
	records[1].Withdrawn = &Withdrawn{Reason: "the cold boot, 12 ms slower", Issue: 7, Date: "2026-11-22"}
	doc := Doc(records, "kept")
	for _, want := range []string{
		"| v2 (withdrawn 2026-11-22, #7: the cold boot, 12 ms slower) | 321 / 321 |",
		"A release marked withdrawn regressed and is not to be adopted",
		"v1 against the goal: cold +240 ms.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("no %q in:\n%s", want, doc)
		}
	}
	if strings.Contains(Doc(docRecords(), "kept"), "marked withdrawn") {
		t.Error("a page with no release withdrawn says what one is")
	}
	if site, err := Site(records); err != nil || !strings.Contains(site, "v2 (withdrawn 2026-11-22, #7: the cold boot, 12 ms slower)") {
		t.Errorf("the site does not name v2 withdrawn: %v", err)
	}
	if h := records[1].Table(); !strings.Contains(h, ". Withdrawn 2026-11-22, #7: the cold boot, 12 ms slower.\n") {
		t.Errorf("v2's record:\n%s", h)
	}
	records[0].Withdrawn = records[1].Withdrawn
	if all := Doc(records, "kept"); strings.Contains(all, "against the goal") {
		t.Errorf("every release withdrawn, and a gap to the goal:\n%s", all)
	}
}

// A goal number regressed when its Δ's interval is wholly above 0; one faster, within noise, not a
// goal number, or measured alone did not.
func TestARegressionIsAGoalNumberSlowerBeyondNoise(t *testing.T) {
	r := Record{Release: "v2", Against: &Against{Release: "v1", Delta: map[string]Delta{
		"cold":       {P50: 6.8, Lo: 0.1, Hi: 13.6},
		"restore":    {P50: 5, Lo: 0, Hi: 9},
		"save-ms":    {P50: 20, Lo: 10, Hi: 30},
		"cold-ahead": {P50: 20, Lo: 10, Hi: 30},
	}}}
	if got := r.Regressions(); len(got) != 1 || got[0] != "cold boot to first login +6.8 [+0.1, +13.6]" {
		t.Errorf("Regressions = %q", got)
	}
	r.Against.Delta["restore"] = Delta{P50: 2, Lo: 0.1, Hi: 4}
	r.Against.Delta["cold"] = Delta{P50: -8, Lo: -12, Hi: -3}
	if got := r.Regressions(); len(got) != 1 || !strings.HasPrefix(got[0], "restore to first login +2.0") {
		t.Errorf("Regressions = %q", got)
	}
	if got := (Record{Release: "v1"}).Regressions(); got != nil {
		t.Errorf("a release measured alone regressed: %q", got)
	}
}
