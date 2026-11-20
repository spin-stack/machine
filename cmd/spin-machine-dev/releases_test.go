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

	"github.com/spin-stack/spin-machine/internal/lab"
)

// ladderOf writes, under the test's own directory, a ladder of releases whose cold boots read
// first, first+10 and so on, and answers its path.
func ladderOf(t *testing.T, first float64, releases ...string) string {
	t.Helper()
	r := &lab.Result{Probe: "ssh-first", Reps: 1, Host: lab.Host{Instance: "c8id.4xlarge", Nested: true},
		Started: time.Date(2026, 11, 20, 0, 25, 22, 0, time.UTC)}
	for i, rel := range releases {
		r.Samples = append(r.Samples, lab.Sample{Tree: rel, Row: "boot", Rep: 1,
			Values: map[string]float64{"first_login": first + 10*float64(i)}})
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
	if err != nil || v2.Numbers["cold"] != (lab.Number{P50: 330, P95: 330}) || v2.Run != "https://x/runs/9" || v2.Host != "c8id.4xlarge, nested" {
		t.Errorf("v2's record %+v, %v", v2, err)
	}
	page := read(t, records+".md")
	if !strings.Contains(page, "| v1 | 300 / 300 |") || !strings.Contains(page, "| v2 | 330 / 330 | +30.0 |") {
		t.Errorf("the page:\n%s", page)
	}
	if _, _, status := verb(t, "releases", "record", ladderOf(t, 320, "v1", "v2"), "--run", "https://x/runs/9"); status != 0 ||
		read(t, records+".md") != page || read(t, filepath.Join(records, "v1.json")) != v1 {
		t.Error("the same ladder recorded again changed something")
	}
	if _, _, status := verb(t, "releases", "record", ladderOf(t, 290, "v2")); status != 0 ||
		!strings.Contains(read(t, records+".md"), "| v2 | 290 / 290 | -10.0 |") {
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

func TestReleasesShowAndCompareReadTheRecords(t *testing.T) {
	checkout(t)
	verb(t, "releases", "record", ladderOf(t, 300, "v1"))
	verb(t, "releases", "record", ladderOf(t, 320, "v1", "v2"), "--run", "https://x/runs/9")
	if out, _, status := verb(t, "releases", "show", "v2"); status != 0 ||
		!strings.HasPrefix(out, "v2: measured 2026-11-20 on c8id.4xlarge, nested, 1 boots of each probe (https://x/runs/9).") ||
		!strings.Contains(out, "| cold boot to first login | 330 / 330 |") {
		t.Errorf("show: exit %d\n%s", status, out)
	}
	if out, _, status := verb(t, "releases", "compare", "v2", "v1"); status != 0 ||
		!strings.HasPrefix(out, "v1: measured") || !strings.Contains(out, "| cold boot to first login | 300 / 300 | 330 / 330 | +30.0 |") {
		t.Errorf("compare: exit %d\n%s", status, out)
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
	if chart := read(t, filepath.Join(dir, "docs", "releases.svg")); !strings.Contains(chart, "<title>v1 cold: p50 300, p95 300 ms</title>") {
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
	for _, want := range []string{`<a href="releases/v1.json">v1</a>`, `<a href="releases/v2.json">v2</a>`, "<title>v2 cold: p50 330, p95 330 ms</title>"} {
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
	if _, stderr, status := verb(t, "releases", "propose"); status != 0 || !strings.Contains(stderr, "nothing recorded") {
		t.Errorf("nothing recorded: exit %d, %q", status, stderr)
	}
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	if _, stderr, status := verb(t, "releases", "propose"); status != 0 || !strings.Contains(stderr, "https://x/pull/9") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh pr create --base main --head releases/v2 --title releases: v2, measured --body") ||
		!strings.Contains(got, "### v2\n\nv2: measured") || !strings.Contains(got, "### releases.md, as it reads now") {
		t.Errorf("gh ran %q", got)
	}
	if got := git("show", "--name-only", "--format=%s", "origin/releases/v2"); got != "releases: v2, measured\n\ndocs/releases.md\ndocs/releases.svg\ndocs/releases/v2.json\n" {
		t.Errorf("the branch proposed holds %q", got)
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

// Outside a repository git cannot say what was recorded.
func TestReleasesProposeNeedsAGitCheckout(t *testing.T) {
	dir := checkout(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	if _, _, status := verb(t, "releases", "propose"); status != 2 {
		t.Errorf("no git: exit %d", status)
	}
}

// TestReleasesMeasureRecordsEachAlone is measure's bench lane: the newest release, measured
// alone, one boot of each probe (SPIN_LAB_LADDER=1, on a host with KVM, vhost-vsock, sudo and gh).
// It downloads a release and builds its spin-machine.
func TestReleasesMeasureRecordsEachAlone(t *testing.T) {
	if os.Getenv("SPIN_LAB_LADDER") == "" {
		t.Skip("set SPIN_LAB_LADDER=1: downloads a release and boots it")
	}
	dir := t.TempDir()
	if _, stderr, status := verb(t, "releases", "measure", "--reps", "1", "--dir", dir); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	records, err := lab.ReadRecords(dir)
	if err != nil || len(records) != 1 || records[0].Numbers["cold"].P50 == 0 || !exists(dir+".md") {
		t.Errorf("records %+v, %v", records, err)
	}
}
