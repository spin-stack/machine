// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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

// spins are the options of spin's shape, as a ladder's results keep them.
var spins = Options{"cpus": "1", "max-cpus": "16", "memory": "128", "max-memory": "8192", "hotplug-disks": "4", "flags": ""}

// spinsShape is spin's shape as a table says it.
const spinsShape = "1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks"

func testLadder() *Ladder {
	host := Host{Name: "h", Instance: "c8id.4xlarge", Nested: true}
	// The ladder began with its second probe's start: the first's clock is a minute later.
	began := time.Date(2026, 11, 20, 11, 40, 7, 370263306, time.UTC)
	ssh := &Result{Probe: "ssh-first", Reps: 2, Host: host, Started: began.Add(time.Minute), Options: spins}
	ssh.Samples = append(samples("v1", "boot", map[string]float64{"first_login": 340, "qemu_rss": 300}, 2),
		samples("v2", "boot", map[string]float64{"first_login": 321.04, "qemu_rss": 290}, 2)...)
	rf := &Result{Probe: "restore-first", Reps: 2, Host: host, Started: began,
		Samples: samples("v2", "evicted", map[string]float64{"login": 152, "save": 151, "state": 140}, 2)}
	ps := &Result{Probe: "prestart", Reps: 2, Host: host, Started: began.Add(2 * time.Minute),
		Samples: append(samples("v2", rowPrestarted, map[string]float64{"login": 296}, 2),
			Sample{Tree: "v2", Row: rowRestoredAhead, Rep: 1, Missed: "never answered"})}
	return &Ladder{Releases: []string{"v1", "v2"}, Results: []*Result{ssh, rf, ps},
		Cannot: map[string]map[string]string{
			"restore-first": {"v1": "its spin-machine's boot has no --restore"},
			"prestart":      {"v1": "its spin-machine's boot has no --held"},
		}}
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
	if err != nil || !reflect.DeepEqual(got.Records(), l.Records()) {
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

// The no-systemd floor is ssh-first over the release and its floor, both by the lane, as a result
// of its own; a release whose spin-machine cannot run it is why, and nothing boots.
func TestALadderMeasuresTheFloorBesideARelease(t *testing.T) {
	sshFirst := Probes["ssh-first"]
	t.Cleanup(func() { Probes["ssh-first"] = sshFirst })
	rec := &recorder{phases: 1, rows: []string{"boot"}}
	Probes["ssh-first"] = rec
	rel, floor := treeWith(t, map[string][]string{"boot": {"release"}}), treeWith(t, map[string][]string{"boot": {"release"}})
	rel.Manifest.Name, floor.Manifest.Name = "v2", "v2"+FloorSuffix
	l := &Ladder{Releases: []string{"v1", "v2"}, Cannot: map[string]map[string]string{}}
	if err := l.MeasureFloor(t.Context(), rel, floor, 2, t.TempDir(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(l.Results) != 1 || l.Results[0].Probe != "floor" || l.Results[0].Options["login"] != "lane" ||
		len(l.Results[0].Values("v2", "boot", "a")) != 2 || len(l.Results[0].Values("v2-floor", "boot", "a")) != 2 {
		t.Fatalf("results %+v", l.Results)
	}
	if len(rec.spins) != 4 || slices.Contains(rec.spins, true) {
		t.Errorf("the floor logged in by spin's path, which its sshd does not serve: %v", rec.spins)
	}
	Probes["ssh-first"] = &recorder{phases: 1, rows: []string{"boot"}, fail: "v2-floor/boot/0/0"}
	if err := l.MeasureFloor(t.Context(), rel, floor, 1, t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "the ladder's floor") {
		t.Errorf("a floor that fails: %v", err)
	}
	Probes["ssh-first"] = sshFirst
	l = &Ladder{Releases: []string{"v2"}, Cannot: map[string]map[string]string{}}
	if err := l.MeasureFloor(t.Context(), rel, floor, 1, t.TempDir(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(l.Results) != 0 || !strings.Contains(l.Cannot["floor"]["v2"], "has no --") {
		t.Errorf("a release that cannot: results %+v, cannot %v", l.Results, l.Cannot)
	}
}

// The floor is the newest record's, beside its release by the lane, with its Δ against the lane's
// login: the release it is against has none and says nothing of it, and no Δ against that release
// is read of it. A floor whose boots were all lost says so, and has no Δ.
func TestTheFloorIsTheNewestRecordsBound(t *testing.T) {
	l := testLadder()
	fl := &Result{Probe: "floor", Reps: 2, Host: l.Results[0].Host, Started: l.Results[0].Started, Options: spins}
	fl.Samples = append(samples("v2", "boot", map[string]float64{"first_login": 404}, 2),
		samples("v2"+FloorSuffix, "boot", map[string]float64{"first_login": 250.04}, 2)...)
	l.Results = append(l.Results, fl)
	got := l.Records()
	if n := got[1].Numbers; n["floor-lane"] != (Number{404, 404}) || n["floor"] != (Number{250, 250}) {
		t.Errorf("v2's floor: %+v", n)
	}
	if b := got[1].Bounds; !reflect.DeepEqual(b, map[string]Delta{"floor": {P50: -154, Lo: -154, Hi: -154}}) {
		t.Errorf("v2's floor against its lane login: %+v", b)
	}
	if got[0].Bounds != nil {
		t.Errorf("v1 has bounds: %+v", got[0].Bounds)
	}
	for _, k := range []string{"floor", "floor-lane"} {
		if _, ok := got[0].Numbers[k]; ok {
			t.Errorf("v1 has %s", k)
		}
		if why, ok := got[0].Cannot[k]; ok {
			t.Errorf("v1 says why it has no %s: %s", k, why)
		}
		if d, ok := got[1].Against.Delta[k]; ok {
			t.Errorf("a Δ of %s against v1: %v", k, d)
		}
	}
	fl.Samples = append(samples("v2", "boot", map[string]float64{"first_login": 404}, 2),
		Sample{Tree: "v2" + FloorSuffix, Row: "boot", Rep: 1, Missed: "never answered"})
	if r := l.Records()[1]; r.Cannot["floor"] != "lost all 1" || r.Bounds != nil {
		t.Errorf("a floor that lost every boot: %q, %+v", r.Cannot["floor"], r.Bounds)
	}
}

// A release is measured against the newest release before it that was not withdrawn; a withdrawn
// one asked for by name is measured all the same, against the same.
func TestAWithdrawnReleaseIsNeverTheOneMeasuredAgainst(t *testing.T) {
	published := []Published{{Tag: "v4"}, {Tag: "v3", Withdrawn: true}, {Tag: "v2"}, {Tag: "v1"}}
	for newest, want := range map[string][]string{"v4": {"v2", "v4"}, "v3": {"v2", "v3"}, "v2": {"v1", "v2"}} {
		if got, err := LadderReleases(Adopted(published, newest), newest, 2); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s measured with %v, %v; want %v", newest, got, err, want)
		}
	}
	if got := Tags(published); !reflect.DeepEqual(got, []string{"v4", "v3", "v2", "v1"}) {
		t.Errorf("Tags = %v", got)
	}
}
