// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/lab"
)

// githubWith is a gh that knows v2 as state says (its title, notes and prerelease flag, as
// release view answers), knows issues 1 and 7, and answers v1 as the newest release not withdrawn.
func githubWith(t *testing.T, state onGitHub) string {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(t.TempDir(), "v2.json")
	put(t, view, string(raw), 0o644)
	return fakes(t, map[string]string{"gh": `case "$1 $2 $3" in
"release view v2") cat ` + view + `;;
"release view "*) echo "release not found" >&2; exit 1;;
"issue view 7"|"issue view 1") echo '{"number":'$3'}';;
"issue view "*) echo "Could not resolve to an issue" >&2; exit 1;;
"release list "*) echo v1;;
esac`})
}

// Withdrawing a release marks it on GitHub - a prerelease, so no consumer's newest - and in its
// record and the page, and makes the newest release not withdrawn GitHub's latest. Again, it
// changes nothing on GitHub and keeps the date; restored, the release is as it was published.
func TestReleasesWithdrawMarksTheReleaseAndRestoreUndoesIt(t *testing.T) {
	dir := checkout(t)
	records := filepath.Join(dir, "docs", "releases")
	verb(t, "releases", "record", ladderOf(t, 300, "v1"))
	verb(t, "releases", "record", ladderOf(t, 300, "v1", "v2"))
	published := onGitHub{Name: "v2", Body: "## What changed since v1\n"}
	calls := githubWith(t, published)
	if _, stderr, status := verb(t, "releases", "withdraw", "v2", "--reason", "cold +10 ms", "--issue", "7"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	withdrawn := onGitHub{Name: "v2 (withdrawn)", Body: withdrawnNotes(published.Body, "cold +10 ms", 7), Prerelease: true}
	got := read(t, calls)
	if !strings.Contains(got, "gh release edit v2 -R spin-stack/machine --title v2 (withdrawn) --notes **Withdrawn**: cold +10 ms (#7).") ||
		!strings.Contains(got, releaseNotesFollow+"## What changed since v1\n --prerelease=true\n") ||
		!strings.HasSuffix(got, "gh release edit v1 -R spin-stack/machine --latest\n") {
		t.Errorf("gh ran %q", got)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	if r, err := lab.ReadRecord(filepath.Join(records, "v2.json")); err != nil || r.Withdrawn == nil ||
		*r.Withdrawn != (lab.Withdrawn{Reason: "cold +10 ms", Issue: 7, Date: today}) {
		t.Errorf("v2's record: %+v, %v", r.Withdrawn, err)
	}
	if page := read(t, records+".md"); !strings.Contains(page, "| v2 (withdrawn "+today+", #7: cold +10 ms) |") {
		t.Errorf("the page:\n%s", page)
	}

	// Again, on a release GitHub has withdrawn already: nothing to edit, and the date it was.
	r, _ := lab.ReadRecord(filepath.Join(records, "v2.json"))
	r.Withdrawn.Date = "2026-11-01"
	if err := lab.WriteRecord(filepath.Join(records, "v2.json"), r); err != nil {
		t.Fatal(err)
	}
	calls = githubWith(t, withdrawn)
	if _, stderr, status := verb(t, "releases", "withdraw", "v2", "--reason", "cold +10 ms", "--issue", "7"); status != 0 {
		t.Fatalf("again: exit %d: %s", status, stderr)
	}
	if got := read(t, calls); strings.Contains(got, "release edit v2") {
		t.Errorf("withdrawn again, gh ran %q", got)
	}
	if r, _ := lab.ReadRecord(filepath.Join(records, "v2.json")); r.Withdrawn == nil || r.Withdrawn.Date != "2026-11-01" {
		t.Errorf("withdrawn again, v2's record: %+v", r.Withdrawn)
	}
	// Measured again, the release is still withdrawn.
	verb(t, "releases", "record", ladderOf(t, 290, "v1", "v2"))
	if r, _ := lab.ReadRecord(filepath.Join(records, "v2.json")); r.Withdrawn == nil || r.Numbers["cold"].P50 != 300 {
		t.Errorf("measured again, v2's record: %+v", r)
	}

	if _, stderr, status := verb(t, "releases", "restore", "v2"); status != 0 {
		t.Fatalf("restore: exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh release edit v2 -R spin-stack/machine --title v2 --notes ## What changed since v1\n --prerelease=false\n") {
		t.Errorf("restored, gh ran %q", got)
	}
	if r, _ := lab.ReadRecord(filepath.Join(records, "v2.json")); r.Withdrawn != nil || strings.Contains(read(t, records+".md"), "withdrawn") {
		t.Errorf("restored, v2's record says %+v, or the page:\n%s", r.Withdrawn, read(t, records+".md"))
	}
	calls = githubWith(t, published)
	if _, _, status := verb(t, "releases", "restore", "v2"); status != 0 || strings.Contains(read(t, calls), "release edit v2") {
		t.Errorf("a release not withdrawn, restored: exit %d, gh ran %q", status, read(t, calls))
	}
}

// A withdrawal names why and the issue that decided it, of a release there is; a release with no
// record is withdrawn on GitHub, and its page waits for its record.
func TestReleasesWithdrawRefusesWhatItCannotDo(t *testing.T) {
	checkout(t)
	calls := githubWith(t, onGitHub{Name: "v2"})
	for name, args := range map[string][]string{
		"no reason":                  {"v2", "--issue", "7"},
		"a blank reason":             {"v2", "--reason", " ", "--issue", "7"},
		"no issue":                   {"v2", "--reason", "cold +10 ms"},
		"issue 0":                    {"v2", "--reason", "cold +10 ms", "--issue", "0"},
		"an issue that is no number": {"v2", "--reason", "cold +10 ms", "--issue", "seven"},
		"no tag":                     {"--reason", "cold +10 ms", "--issue", "7"},
	} {
		if _, _, status := verb(t, append([]string{"releases", "withdraw"}, args...)...); status != 2 {
			t.Errorf("%s: exit %d", name, status)
		}
	}
	if got := read(t, calls); got != "" {
		t.Errorf("a refused withdrawal asked GitHub %q", got)
	}
	for name, args := range map[string][]string{
		"an issue there is not":   {"v2", "--reason", "cold +10 ms", "--issue", "8"},
		"a release there is not":  {"v9", "--reason", "cold +10 ms", "--issue", "7"},
		"a restore of no release": {"v9"},
	} {
		verbName := "withdraw"
		if len(args) == 1 {
			verbName = "restore"
		}
		if _, stderr, status := verb(t, append([]string{"releases", verbName}, args...)...); status != 2 || !strings.Contains(stderr, "not") {
			t.Errorf("%s: exit %d, %q", name, status, stderr)
		}
	}
	if got := read(t, calls); strings.Contains(got, "release edit") {
		t.Errorf("a refused withdrawal edited a release: %q", got)
	}
	if _, stderr, status := verb(t, "releases", "withdraw", "v2", "--reason", "cold +10 ms", "--issue", "1"); status != 0 ||
		!strings.Contains(stderr, "v2 has no record") {
		t.Errorf("a release with no record: exit %d, %q", status, stderr)
	}
	fakes(t, map[string]string{"gh": `[ "$1 $2" = "release edit" ] && exit 1; echo '{}'`})
	if _, _, status := verb(t, "releases", "restore", "v2"); status != 2 {
		t.Errorf("an edit GitHub refused: exit %d", status)
	}
	fakes(t, map[string]string{"gh": `[ "$1 $2" = "release view" ] && echo '{'; exit 0`})
	if _, _, status := verb(t, "releases", "restore", "v2"); status != 2 {
		t.Errorf("a release GitHub answered with no JSON: exit %d", status)
	}
	fakes(t, map[string]string{"gh": `[ "$1 $2" = "release list" ] && exit 1; echo '{}'`})
	if _, _, status := verb(t, "releases", "restore", "v2"); status != 2 {
		t.Errorf("no newest release to make latest: exit %d", status)
	}
	fakes(t, map[string]string{"gh": `[ "$*" = "release edit v1 -R spin-stack/machine --latest" ] && exit 1; [ "$1 $2" = "release list" ] && echo v1 && exit 0; echo '{}'`})
	if _, _, status := verb(t, "releases", "restore", "v2"); status != 2 {
		t.Errorf("a latest GitHub refused: exit %d", status)
	}
	calls = fakes(t, map[string]string{"gh": `[ "$1 $2" = "release list" ] || echo '{}'`})
	if _, stderr, status := verb(t, "releases", "restore", "v2"); status != 0 || !strings.Contains(stderr, "GitHub has no latest release") ||
		strings.Contains(read(t, calls), "--latest") {
		t.Errorf("every release withdrawn: exit %d, %q, gh ran %q", status, stderr, read(t, calls))
	}
}

// A release whose goal number regressed beyond noise is an issue, with its Δ, its run and the
// commits that could explain it, once; one that did not, or measured alone, is none.
func TestReleasesFlagOpensAnIssueForARegression(t *testing.T) {
	checkout(t)
	calls := fakes(t, map[string]string{"gh": `case "$1 $2" in
"issue list") echo '[{"title":"v1 regressed against v0","url":"https://x/issues/1"}]';;
"api repos/spin-stack/machine/compare/v1...v2") echo "- 0123456789ab image: a slower unit";;
"issue create") echo https://x/issues/9;;
esac`})
	if _, stderr, status := verb(t, "releases", "flag", ladderOf(t, 300, "v1", "v2"), "--run", "https://x/runs/5"); status != 0 ||
		!strings.Contains(stderr, "v2 regressed against v1: https://x/issues/9") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	got := read(t, calls)
	for _, want := range []string{
		"gh issue create -R spin-stack/machine --title v2 regressed against v1 --body v2's cold boot to first login +10.0 [+8.0, +12.0] against v1, booted interleaved with it in one run on the bench fleet (https://x/runs/5).",
		"gh workflow run withdraw.yml -R spin-stack/machine -f tag=v2",
		"## The commits from v1 to v2\n\n- 0123456789ab image: a slower unit\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in what gh ran:\n%s", want, got)
		}
	}

	calls = fakes(t, map[string]string{"gh": `[ "$1 $2" = "issue list" ] && echo '[{"title":"v2 regressed against v1","url":"https://x/issues/9"}]'; exit 0`})
	if _, stderr, status := verb(t, "releases", "flag", ladderOf(t, 300, "v1", "v2")); status != 0 || strings.Contains(read(t, calls), "issue create") ||
		!strings.Contains(stderr, "https://x/issues/9") {
		t.Errorf("flagged again: exit %d, %q, gh ran %q", status, stderr, read(t, calls))
	}
	calls = fakes(t, map[string]string{"gh": "exit 1"})
	for name, ladder := range map[string]string{"faster": ladderStepping(t, 300, -10, "v1", "v2"), "measured alone": ladderOf(t, 300, "v2")} {
		if _, stderr, status := verb(t, "releases", "flag", ladder); status != 0 || !strings.Contains(stderr, "v2: no goal number regressed") {
			t.Errorf("%s: exit %d, %q", name, status, stderr)
		}
	}
	if got := read(t, calls); got != "" {
		t.Errorf("no regression, and gh ran %q", got)
	}

	for name, gh := range map[string]string{
		"no issues listed":         "exit 1",
		"issues that are not JSON": "echo '['",
		"no commits":               `[ "$1 $2" = "issue list" ] && echo '[]' && exit 0; exit 1`,
		"no issue opened":          `[ "$1 $2" = "issue list" ] && echo '[]' && exit 0; [ "$1" = api ] && exit 0; exit 1`,
	} {
		fakes(t, map[string]string{"gh": gh})
		if _, _, status := verb(t, "releases", "flag", ladderOf(t, 300, "v1", "v2")); status != 2 {
			t.Errorf("%s: exit %d", name, status)
		}
	}
	for name, ladder := range map[string]string{"a ladder from nowhere": filepath.Join(t.TempDir(), "none.json"), "a ladder of nothing": ladderOf(t, 300)} {
		if _, _, status := verb(t, "releases", "flag", ladder); status != 2 {
			t.Errorf("%s: exit %d", name, status)
		}
	}
}

// A functional check the release failed is an issue named for the release and the check, with the
// run, once; a step that passed, or was skipped after another failed, is none.
func TestReleasesFailedOpensAnIssueForAFailedCheck(t *testing.T) {
	checkout(t)
	steps := `{"logind":{"outcome":"failure","conclusion":"failure","outputs":{}},"workspace":{"outcome":"skipped","conclusion":"skipped","outputs":{}}}`
	calls := fakes(t, map[string]string{"gh": `case "$1 $2" in
"issue list") echo '[{"title":"v1 failed boot:logind","url":"https://x/issues/1"}]';;
"issue create") echo https://x/issues/9;;
esac`})
	if _, stderr, status := verb(t, "releases", "failed", "v2", "--steps", steps, "--run", "https://x/runs/5"); status != 0 ||
		!strings.Contains(stderr, "v2 failed boot:logind: https://x/issues/9") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	got := read(t, calls)
	for _, want := range []string{
		"gh issue create -R spin-stack/machine --title v2 failed boot:logind --body v2 failed boot:logind in release.yml's pause job, on the bench fleet (https://x/runs/5).",
		"gh workflow run withdraw.yml -R spin-stack/machine -f tag=v2",
		"spin-machine-dev lab dispatch logind --tree 'release=--from release:v2'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in what gh ran:\n%s", want, got)
		}
	}
	if strings.Count(got, "issue create") != 1 {
		t.Errorf("a skipped check opened an issue too:\n%s", got)
	}

	calls = fakes(t, map[string]string{"gh": `[ "$1 $2" = "issue list" ] && echo '[{"title":"v2 failed boot:logind","url":"https://x/issues/9"}]'; exit 0`})
	if _, stderr, status := verb(t, "releases", "failed", "v2", "--steps", steps); status != 0 || strings.Contains(read(t, calls), "issue create") ||
		!strings.Contains(stderr, "https://x/issues/9") {
		t.Errorf("opened again: exit %d, %q, gh ran %q", status, stderr, read(t, calls))
	}
	calls = fakes(t, map[string]string{"gh": "exit 1"})
	if _, stderr, status := verb(t, "releases", "failed", "v2", "--steps", `{"logind":{"outcome":"success"}}`); status != 0 ||
		!strings.Contains(stderr, "v2: no check failed") || read(t, calls) != "" {
		t.Errorf("nothing failed: exit %d, %q, gh ran %q", status, stderr, read(t, calls))
	}

	for name, gh := range map[string]string{
		"no issues listed": "exit 1",
		"no issue opened":  `[ "$1 $2" = "issue list" ] && echo '[]' && exit 0; exit 1`,
	} {
		fakes(t, map[string]string{"gh": gh})
		if _, _, status := verb(t, "releases", "failed", "v2", "--steps", steps); status != 2 {
			t.Errorf("%s: exit %d", name, status)
		}
	}
	if _, _, status := verb(t, "releases", "failed", "v2", "--steps", "{"); status != 2 {
		t.Errorf("steps that are not JSON: exit %d", status)
	}
}

// A withdrawal's record is proposed on a branch and under a title of its own: the release's own
// name was its measurement's.
func TestAWithdrawalIsProposedUnderItsOwnName(t *testing.T) {
	dir := builderCheckout(t)
	put(t, filepath.Join(dir, releasedSchemas, "v1.json"), "[]\n", 0o644)
	for _, args := range [][]string{{"add", releasedSchemas}, {"commit", "-q", "-m", "v1's schema"}, {"push", "-q", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	calls := fakes(t, map[string]string{"gh": `[ "$1 $2" = "pr create" ] && echo https://x/pull/9; exit 0`})
	verb(t, "releases", "record", ladderOf(t, 300, "v2"))
	if _, stderr, status := verb(t, "releases", "propose", "--branch", "releases/v2-withdrawn-5", "--title", "releases: v2, withdrawn"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh pr create --base main --head releases/v2-withdrawn-5 --title releases: v2, withdrawn --body") {
		t.Errorf("gh ran %q", got)
	}
}
