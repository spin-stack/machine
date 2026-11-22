// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/lab"
	"github.com/spin-stack/machine/internal/release"
)

// ladderOf writes, under the test's own directory, a ladder of releases whose cold boots read
// first, first+10 and so on, each the p50 of two boots 2 ms apart, and answers its path. A
// release's Δ against the one before it is then +10, and its interval [+8, +12]: each side's p50
// drawn again is its own or 2 ms more, both often enough to be inside the 95%.
func ladderOf(t *testing.T, first float64, releases ...string) string {
	t.Helper()
	r := &lab.Result{Probe: "ssh-first", Reps: 2, Host: lab.Host{Instance: "c8id.4xlarge", Nested: true},
		Started: time.Date(2026, 11, 20, 0, 25, 22, 0, time.UTC),
		Options: lab.Options{"cpus": "1", "max-cpus": "16", "memory": "128", "max-memory": "8192", "hotplug-disks": "4"}}
	for i, rel := range releases {
		for rep := range 2 {
			r.Samples = append(r.Samples, lab.Sample{Tree: rel, Row: "boot", Rep: rep + 1,
				Values: map[string]float64{"first_login": first + 10*float64(i) + 2*float64(rep)}})
		}
	}
	path := filepath.Join(t.TempDir(), "ladder.json")
	if err := (&lab.Ladder{Releases: releases, Results: []*lab.Result{r}}).Write(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// A ladder's newest release is recorded, replacing a record of it, and the page written; the older
// releases beside it are not, recorded or not; recording the same ladder again changes nothing.
func TestReleasesRecordWritesTheNewestAndThePage(t *testing.T) {
	dir := checkout(t)
	records := filepath.Join(dir, "docs", "releases")
	if _, stderr, status := verb(t, "releases", "record", ladderOf(t, 300, "v1")); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	v1 := read(t, filepath.Join(records, "v1.json"))
	if _, stderr, status := verb(t, "releases", "record", ladderOf(t, 320, "v1", "v2"), "--run", "https://x/runs/9"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if read(t, filepath.Join(records, "v1.json")) != v1 {
		t.Error("an older release's record was replaced by a ladder run for another")
	}
	v2, err := lab.ReadRecord(filepath.Join(records, "v2.json"))
	if err != nil || v2.Numbers["cold"] != (lab.Number{P50: 330, P95: 332}) || v2.Run != "https://x/runs/9" || v2.Host != "c8id.4xlarge, nested" ||
		v2.Shape != "1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks" || v2.Against == nil || v2.Against.Release != "v1" ||
		v2.Against.Numbers["cold"] != (lab.Number{P50: 320, P95: 322}) {
		t.Errorf("v2's record %+v, %v", v2, err)
	}
	// v2's Δ is against v1 as its own run measured it, 320, and not against v1's record, 300.
	page := read(t, records+".md")
	if !strings.Contains(page, "| v1 | 300 / 302 |  |") || !strings.Contains(page, "| v2 | 330 / 332 | +10.0 [+8.0, +12.0] |") {
		t.Errorf("the page:\n%s", page)
	}
	if _, _, status := verb(t, "releases", "record", ladderOf(t, 320, "v1", "v2"), "--run", "https://x/runs/9"); status != 0 ||
		read(t, records+".md") != page || read(t, filepath.Join(records, "v1.json")) != v1 {
		t.Error("the same ladder recorded again changed something")
	}
	if _, _, status := verb(t, "releases", "record", ladderOf(t, 290, "v2")); status != 0 ||
		!strings.Contains(read(t, records+".md"), "| v2 | 290 / 292 |  |") {
		t.Errorf("a newer measurement of v2 did not replace its record:\n%s", read(t, records+".md"))
	}

	for name, args := range map[string][]string{
		"a ladder from nowhere": {filepath.Join(dir, "none.json")},
		"a ladder of nothing":   {ladderOf(t, 300)},
		"a directory in a file": {ladderOf(t, 300, "v3"), "--dir", filepath.Join(records, "v1.json", "x")},
	} {
		if _, _, status := verb(t, append([]string{"releases", "record"}, args...)...); status != 2 {
			t.Errorf("%s exited %d", name, status)
		}
	}
	if err := os.Remove(records + ".md"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(records+".md", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, status := verb(t, "releases", "record", ladderOf(t, 300, "v3")); status != 2 {
		t.Errorf("a page that could not be written exited %d", status)
	}
}

// The ladders of a run that measured several releases are recorded together, as report.yml records
// them, each its own newest release, and the page from all of them.
func TestReleasesRecordTakesEachLadder(t *testing.T) {
	dir := checkout(t)
	records := filepath.Join(dir, "docs", "releases")
	if _, stderr, status := verb(t, "releases", "record", ladderOf(t, 300, "v1", "v2"), ladderOf(t, 330, "v2", "v3"), "--run", "https://x/runs/9"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if page := read(t, records+".md"); !strings.Contains(page, "| v2 | 310 / 312 | +10.0 [+8.0, +12.0] |") ||
		!strings.Contains(page, "| v3 | 340 / 342 | +10.0 [+8.0, +12.0] |") || exists(filepath.Join(records, "v1.json")) {
		t.Errorf("the page:\n%s", page)
	}
}

func TestReleasesShowAndCompareReadTheRecords(t *testing.T) {
	checkout(t)
	verb(t, "releases", "record", ladderOf(t, 300, "v1"))
	verb(t, "releases", "record", ladderOf(t, 320, "v1", "v2"), "--run", "https://x/runs/9")
	verb(t, "releases", "record", ladderOf(t, 360, "v3"))
	if out, _, status := verb(t, "releases", "show", "v2"); status != 0 ||
		!strings.HasPrefix(out, "v2: measured 2026-11-20 on c8id.4xlarge, nested; 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks; "+
			"2 boots of each probe, interleaved with v1 (https://x/runs/9).") ||
		!strings.Contains(out, "| cold boot to first login | 330 / 332 |") {
		t.Errorf("show: exit %d\n%s", status, out)
	}
	// v2 was measured against v1: the two as that run had them, and the Δ with its interval.
	if out, _, status := verb(t, "releases", "compare", "v2", "v1"); status != 0 ||
		!strings.HasPrefix(out, "v2: measured") || !strings.Contains(out, "| cold boot to first login | 320 / 322 | 330 / 332 | +10.0 [+8.0, +12.0] |") {
		t.Errorf("compare: exit %d\n%s", status, out)
	}
	// v3 was measured alone: each record its own run's, and no Δ across the two.
	if out, _, status := verb(t, "releases", "compare", "v3", "v2"); status != 0 ||
		!strings.HasPrefix(out, "v2: measured") || !strings.Contains(out, "v2 and v3 were measured in different runs") ||
		!strings.Contains(out, "| cold boot to first login | 330 / 332 | 360 / 362 |\n") {
		t.Errorf("compare across runs: exit %d\n%s", status, out)
	}
	for _, args := range [][]string{{"show", "v9"}, {"compare", "v1", "v9"}} {
		if _, _, status := verb(t, append([]string{"releases"}, args...)...); status != 2 {
			t.Errorf("%v of a release with no record exited %d", args, status)
		}
	}
}

// The page and its chart are what the records say: a hand edit to either, or either missing, is a
// 1 from --check that names it, and doc writes it again.
func TestReleasesDocChecksThePageAgainstTheRecords(t *testing.T) {
	dir := checkout(t)
	verb(t, "releases", "record", ladderOf(t, 300, "v1"))
	if _, _, status := verb(t, "releases", "doc", "--check"); status != 0 {
		t.Errorf("the page as written: exit %d", status)
	}
	if page := read(t, filepath.Join(dir, "docs", "releases.md")); !strings.Contains(page, "](releases.svg)") {
		t.Errorf("the page does not show its chart:\n%s", page)
	}
	if chart := read(t, filepath.Join(dir, "docs", "releases.svg")); !strings.Contains(chart, "<title>v1 cold: p50 300, p95 302 ms</title>") {
		t.Errorf("the chart:\n%s", chart)
	}
	for _, name := range []string{"releases.md", "releases.svg"} {
		page := filepath.Join(dir, "docs", name)
		put(t, page, read(t, page)+"a hand edit\n", 0o644)
		if _, stderr, status := verb(t, "releases", "doc", "--check"); status != 1 || !strings.Contains(stderr, name+" is not what") ||
			!strings.Contains(stderr, "releases doc writes it") {
			t.Errorf("a hand edit to %s: exit %d, %q", name, status, stderr)
		}
		if _, _, status := verb(t, "releases", "doc"); status != 0 || strings.Contains(read(t, page), "a hand edit") {
			t.Errorf("doc did not write %s again: exit %d", name, status)
		}
		if err := os.Remove(page); err != nil {
			t.Fatal(err)
		}
		if _, _, status := verb(t, "releases", "doc", "--check"); status != 1 {
			t.Errorf("no %s: exit %d", name, status)
		}
		verb(t, "releases", "doc")
	}
	put(t, filepath.Join(dir, "docs", "releases", "v2.json"), "{", 0o644)
	for _, args := range [][]string{{"doc"}, {"doc", "--check"}} {
		if _, _, status := verb(t, append([]string{"releases"}, args...)...); status != 2 {
			t.Errorf("%v over a record that is not JSON exited %d", args, status)
		}
	}
}

// The site is the page as HTML, with each record it links to beside it, read back as written.
func TestReleasesSiteWritesThePageAndItsRecords(t *testing.T) {
	dir := checkout(t)
	verb(t, "releases", "record", ladderOf(t, 300, "v1"))
	verb(t, "releases", "record", ladderOf(t, 320, "v1", "v2"), "--run", "https://x/runs/9")
	out := filepath.Join(t.TempDir(), "site")
	if _, stderr, status := verb(t, "releases", "site", "--out", out); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	index := read(t, filepath.Join(out, "index.html"))
	for _, want := range []string{`<a href="releases/v1.json">v1</a>`, `<a href="releases/v2.json">v2</a>`, "<title>v2 cold: p50 330, p95 332 ms</title>"} {
		if !strings.Contains(index, want) {
			t.Errorf("no %q in:\n%s", want, index)
		}
	}
	for _, rel := range []string{"v1", "v2"} {
		if got := read(t, filepath.Join(out, "releases", rel+".json")); got != read(t, filepath.Join(dir, "docs", "releases", rel+".json")) {
			t.Errorf("%s's record on the site is %s", rel, got)
		}
	}

	if _, _, status := verb(t, "releases", "site"); status != 2 {
		t.Errorf("a site written nowhere: exit %d", status)
	}
	if _, _, status := verb(t, "releases", "site", "--out", filepath.Join(dir, "docs", "releases.md")); status != 2 {
		t.Errorf("a site in a file: exit %d", status)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	put(t, filepath.Join(blocked, "releases"), "", 0o644)
	if _, _, status := verb(t, "releases", "site", "--out", blocked); status != 2 {
		t.Errorf("a site whose records cannot be written: exit %d", status)
	}
	taken := filepath.Join(t.TempDir(), "taken")
	if err := os.MkdirAll(filepath.Join(taken, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, status := verb(t, "releases", "site", "--out", taken); status != 2 {
		t.Errorf("a page that cannot be written: exit %d", status)
	}
	put(t, filepath.Join(dir, "docs", "releases", "v3.json"), "{", 0o644)
	if _, _, status := verb(t, "releases", "site", "--out", out); status != 2 {
		t.Errorf("a record that is not JSON: exit %d", status)
	}
}

func TestReleasesMissingListsThePublishedWithNoRecord(t *testing.T) {
	checkout(t)
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	fakes(t, map[string]string{"gh": "printf 'v3\\nv2\\nv1\\n'"})
	if out, stderr, status := verb(t, "releases", "missing"); status != 0 || out != "v1\nv3\n" {
		t.Errorf("missing: exit %d, %q, %s", status, out, stderr)
	}
	fakes(t, map[string]string{"gh": "exit 1"})
	if _, _, status := verb(t, "releases", "missing"); status != 2 {
		t.Errorf("a GitHub that did not answer: exit %d", status)
	}
}

func TestAProposalIsNamedForItsReleases(t *testing.T) {
	if got := proposalBranch([]string{"v1"}); got != "releases/v1" {
		t.Errorf("one: %s", got)
	}
	three := proposalBranch([]string{"v1", "v2", "v3"})
	if !regexp.MustCompile(`^releases/v1-v3-[0-9a-f]{8}$`).MatchString(three) || proposalBranch([]string{"v1", "v3"}) == three {
		t.Errorf("several: %s", three)
	}
}

// What was recorded is committed on a branch named for it and proposed against main; nothing
// recorded, or a branch on origin already, proposes nothing.
func TestReleasesProposeCommitsWhatWasRecorded(t *testing.T) {
	dir := builderCheckout(t)
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	calls := fakes(t, map[string]string{"gh": `[ "$1 $2" = "pr create" ] && echo https://x/pull/9`})
	// Measured at an older commit, as report.yml measures a release at its tag, while main moved on.
	measured := strings.TrimSpace(git("rev-parse", "HEAD"))
	put(t, filepath.Join(dir, ".github/workflows/release.yml"), "moved\n", 0o644)
	put(t, filepath.Join(dir, releasedSchemas, "v1.json"), "[]\n", 0o644)
	git("add", ".github/workflows/release.yml", releasedSchemas)
	git("commit", "-q", "-m", "main moves on")
	git("push", "-q", "origin", "main")
	git("switch", "-q", "--detach", measured)
	if _, stderr, status := verb(t, "releases", "propose"); status != 0 || !strings.Contains(stderr, "nothing recorded") {
		t.Errorf("nothing recorded: exit %d, %q", status, stderr)
	}
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	put(t, filepath.Join(dir, releasedSchemas, "v2.json"), "[]\n", 0o644)
	if _, stderr, status := verb(t, "releases", "propose"); status != 0 || !strings.Contains(stderr, "https://x/pull/9") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh pr create --base main --head releases/v2 --title releases: v2, measured --body") ||
		!strings.Contains(got, "### v2\n\nv2: measured") || !strings.Contains(got, "### releases.md, as it reads now") {
		t.Errorf("gh ran %q", got)
	}
	if got := git("show", "--name-only", "--format=%s", "origin/releases/v2"); got != "releases: v2, measured\n\ndocs/releases.md\ndocs/releases.svg\ndocs/releases/v2.json\n"+releasedSchemas+"/v2.json\n" {
		t.Errorf("the branch proposed holds %q", got)
	}
	if parent, main := git("rev-parse", "origin/releases/v2^"), git("rev-parse", "origin/main"); parent != main {
		t.Errorf("the proposal is on %s, not on main's %s", parent, main)
	}
	if got := read(t, os.Getenv("GITHUB_STEP_SUMMARY")); got != "https://x/pull/9\n" {
		t.Errorf("the run's summary is %q", got)
	}
	git("switch", "-q", "main")
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	if _, stderr, status := verb(t, "releases", "propose"); status != 0 || !strings.Contains(stderr, "releases/v2 was proposed already") {
		t.Errorf("the same release again: exit %d, %q", status, stderr)
	}

	verb(t, "releases", "record", ladderOf(t, 300, "v3"))
	page := filepath.Join(dir, "docs", "releases.md")
	saved := read(t, page)
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	if _, _, status := verb(t, "releases", "propose"); status != 2 {
		t.Errorf("no page to propose: exit %d", status)
	}
	put(t, page, saved, 0o644)
	fakes(t, map[string]string{"gh": "exit 1"})
	if _, _, status := verb(t, "releases", "propose"); status != 2 {
		t.Errorf("a pull request that could not be opened: exit %d", status)
	}
	git("switch", "-q", "-c", "v4", "main")
	put(t, filepath.Join(dir, "docs", "releases", "v4.json"), "{", 0o644)
	if _, _, status := verb(t, "releases", "propose"); status != 2 {
		t.Errorf("a record that is not JSON: exit %d", status)
	}
	if err := exec.Command("git", "-C", dir, "remote", "set-url", "origin", filepath.Join(dir, "no-such-origin")).Run(); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "docs", "releases", "v4.json"), read(t, filepath.Join(dir, "docs", "releases", "v3.json")), 0o644)
	if _, _, status := verb(t, "releases", "propose"); status == 0 {
		t.Error("a push that failed was a proposal")
	}
}

// fakeQEMUAnswering is a QEMU's QMP at descriptor 3 as build.QMPSchema meets it, answering
// query-qmp-schema with schema.
func fakeQEMUAnswering(schema string) string {
	return `#!/bin/sh
printf '{"QMP": {"version": {"qemu": {"micro": 0, "minor": 2, "major": 11}, "package": ""}, "capabilities": []}}\n' >&3
while read -r line <&3; do
	id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
	case "$line" in
	*qmp_capabilities*) printf '{"return": {}, "id": %s}\n' "$id" >&3 ;;
	*query-qmp-schema*) printf '{"return": %s, "id": %s}\n' '` + schema + `' "$id" >&3 ;;
	esac
done
`
}

// Each release's QEMU, from the lab's cache of releases, is asked for its schema, which is written
// where go test reads it; a QEMU that cannot answer writes nothing.
func TestReleasesSchemaRecordsEachReleasesQEMU(t *testing.T) {
	dir := checkout(t)
	qemu := func(tag string) string {
		return filepath.Join(dir, "_output", "lab", "releases", tag, release.Name(tag), "usr", "share", "spin-stack", "bin", "qemu-system-x86_64")
	}
	const schema = `[{"name": "int", "meta-type": "builtin", "json-type": "int"}]`
	put(t, qemu("v1"), fakeQEMUAnswering(schema), 0o755)
	put(t, qemu("v2"), fakeQEMUAnswering(`[]`), 0o755)
	put(t, qemu("v3"), "#!/bin/sh\necho 'qemu: no KVM' >&2\nexit 1\n", 0o755)
	if _, stderr, status := verb(t, "releases", "schema", "v1", "v2"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, filepath.Join(dir, releasedSchemas, "v1.json")); got != schema+"\n" {
		t.Errorf("v1.json is %q", got)
	}
	if got := read(t, filepath.Join(dir, releasedSchemas, "v2.json")); got != "[]\n" {
		t.Errorf("v2.json is %q", got)
	}
	if _, stderr, status := verb(t, "releases", "schema", "v3"); status != 2 || !strings.Contains(stderr, "release v3") || !strings.Contains(stderr, "no KVM") {
		t.Errorf("a QEMU that would not start: exit %d, %q", status, stderr)
	}
	if exists(filepath.Join(dir, releasedSchemas, "v3.json")) {
		t.Error("a QEMU that would not start left a schema")
	}
}

// Outside a repository git cannot say what was recorded.
func TestReleasesProposeNeedsAGitCheckout(t *testing.T) {
	dir := checkout(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	if _, _, status := verb(t, "releases", "propose"); status != 2 {
		t.Errorf("no git: exit %d", status)
	}
}

// TestReleasesMeasureLaddersEachWithTheOneBefore is measure's bench lane: the newest release,
// measured interleaved with the one before it, two boots of each probe, the fewest an interval
// is drawn from (SPIN_LAB_LADDER=1, on a host with KVM, vhost-vsock, sudo and gh), and its ladder
// recorded. It downloads two releases and builds their spin-machines.
func TestReleasesMeasureLaddersEachWithTheOneBefore(t *testing.T) {
	if os.Getenv("SPIN_LAB_LADDER") == "" {
		t.Skip("set SPIN_LAB_LADDER=1: downloads two releases and boots them")
	}
	tag, err := github().LatestRelease()
	if err != nil {
		t.Fatal(err)
	}
	root, err := repo()
	if err != nil {
		t.Fatal(err)
	}
	if _, stderr, status := verb(t, "releases", "measure", tag, "--reps", "2"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	dir := t.TempDir()
	if _, stderr, status := verb(t, "releases", "record", filepath.Join(root, "_output", "lab", "measured", tag, "ladder.json"), "--dir", dir); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	records, err := lab.ReadRecords(dir)
	if err != nil || len(records) != 1 || records[0].Numbers["cold"].P50 == 0 || !exists(dir+".md") {
		t.Fatalf("records %+v, %v", records, err)
	}
	if a := records[0].Against; a == nil || a.Numbers["cold"].P50 == 0 {
		t.Errorf("measured against %+v", a)
	} else if _, ok := a.Delta["cold"]; !ok {
		t.Errorf("no Δ of the cold boot against %s: %+v", a.Release, a.Delta)
	}
}
