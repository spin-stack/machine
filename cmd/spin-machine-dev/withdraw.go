// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/lab"
)

// withdrawnTitle ends a withdrawn release's title on GitHub.
const withdrawnTitle = " (withdrawn)"

// releaseNotesFollow is the line between what a withdrawal put at the top of a release's notes and
// the notes the release was published with, which restore gives back as they were.
const releaseNotesFollow = "<!-- spin-machine-dev releases withdraw: the release's notes follow, as published -->\n\n"

// onGitHub is what withdraw and restore read of a release on GitHub and write back.
type onGitHub struct {
	Name       string `json:"name"`
	Body       string `json:"body"`
	Prerelease bool   `json:"isPrerelease"`
}

// published is r's title and notes as the release was published, without what a withdrawal added.
func (r onGitHub) published() (title, notes string) {
	title = strings.TrimSuffix(r.Name, withdrawnTitle)
	notes = r.Body
	if _, after, ok := strings.Cut(r.Body, releaseNotesFollow); ok {
		notes = after
	}
	return title, notes
}

// withdrawnNotes are a withdrawn release's notes: why, the issue, and the notes it was published
// with after them.
func withdrawnNotes(published, reason string, issue int) string {
	return fmt.Sprintf("**Withdrawn**: %s (#%d). Nothing is to adopt this release: take the newest release that "+
		"is not withdrawn. Everything it published stays, its sources, signatures and provenance among them, "+
		"and a checkpoint saved on it resumes as before (docs/releasing.md, \"Withdrawing a release\").\n\n", reason, issue) +
		releaseNotesFollow + published
}

// setRelease brings tag on GitHub to what want makes of it as it is, unless it is that already,
// and then makes the newest release not withdrawn GitHub's latest: a withdrawn release cannot be
// latest, and one restored is latest only when it is the newest.
func setRelease(ctx context.Context, c *cobra.Command, tag string, want func(onGitHub) onGitHub) error {
	repo := github().Repo
	raw, err := gh(ctx, "release", "view", tag, "-R", repo, "--json", "name,body,isPrerelease")
	if err != nil {
		return fmt.Errorf("reading release %s: %w", tag, err)
	}
	var now onGitHub
	if err := json.Unmarshal([]byte(raw), &now); err != nil {
		return fmt.Errorf("reading release %s: %w", tag, err)
	}
	if w := want(now); w != now {
		if _, err := gh(ctx, "release", "edit", tag, "-R", repo, "--title", w.Name, "--notes", w.Body,
			"--prerelease="+strconv.FormatBool(w.Prerelease)); err != nil {
			return fmt.Errorf("editing release %s: %w", tag, err)
		}
	}
	latest, err := gh(ctx, "release", "list", "-R", repo, "--exclude-drafts", "--exclude-pre-releases", "--limit", "1",
		"--json", "tagName", "-q", ".[0].tagName")
	if err != nil {
		return fmt.Errorf("finding the newest release not withdrawn: %w", err)
	}
	if latest == "" {
		say(c, "no release is left that is not withdrawn: GitHub has no latest release")
		return nil
	}
	if _, err := gh(ctx, "release", "edit", latest, "-R", repo, "--latest"); err != nil {
		return fmt.Errorf("making %s the latest release: %w", latest, err)
	}
	return nil
}

// markRecord sets the withdrawal on release's record, nil to clear it, keeping the date of one
// already there, and writes the page again. A release with no record is said, and changes nothing:
// report.yml proposes a release's record as a pull request, and a release can be withdrawn before
// that merges.
func markRecord(c *cobra.Command, dir, release string, w *lab.Withdrawn) error {
	path := filepath.Join(dir, release+".json")
	if !exists(path) {
		say(c, "%s has no record in %s: once its record is on main, run this again to mark it", release, dir)
		return nil
	}
	r, err := lab.ReadRecord(path)
	if err != nil {
		return err
	}
	if w != nil && r.Withdrawn != nil {
		w.Date = r.Withdrawn.Date
	}
	r.Withdrawn = w
	if err := lab.WriteRecord(path, r); err != nil {
		return err
	}
	return writeDoc(dir)
}

func releasesWithdraw(dir *string) *cobra.Command {
	var reason string
	var issue int
	c := &cobra.Command{
		Use:   "withdraw TAG --reason TEXT --issue N",
		Short: "Mark a published release withdrawn, so nothing adopts it, keeping everything it published",
		Long: `Mark TAG withdrawn on GitHub - a prerelease, its title ending "(withdrawn)", its notes opening with
the reason and the issue that decided it - and in its record (docs/releases/TAG.json), and write
the page again. GitHub's latest release is then the newest one not withdrawn, which is what spin's
versions.yaml track and ship-it take. Nothing is deleted: the tarball, SOURCES, the signatures and
provenance, the record and its charts stay, and compat goes on resuming the release's checkpoints.
The next release is measured against the newest release not withdrawn.

Running it again changes nothing, or only the reason. restore undoes it. docs/releasing.md,
"Withdrawing a release", says what counts as a regression.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" || issue <= 0 {
				return errors.New("a withdrawal says why, with --reason, and names the issue that decided it, with --issue")
			}
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			tag := args[0]
			if _, err := gh(c.Context(), "issue", "view", strconv.Itoa(issue), "-R", github().Repo, "--json", "number"); err != nil {
				return couldNot(fmt.Errorf("reading issue #%d, which decided %s's withdrawal: %w", issue, tag, err))
			}
			withdrawn := func(now onGitHub) onGitHub {
				title, notes := now.published()
				return onGitHub{Name: title + withdrawnTitle, Body: withdrawnNotes(notes, reason, issue), Prerelease: true}
			}
			if err := setRelease(c.Context(), c, tag, withdrawn); err != nil {
				return couldNot(err)
			}
			w := &lab.Withdrawn{Reason: reason, Issue: issue, Date: time.Now().UTC().Format(time.DateOnly)}
			if err := markRecord(c, d, tag, w); err != nil {
				return couldNot(err)
			}
			say(c, "%s withdrawn (#%d)", tag, issue)
			return nil
		},
	}
	c.Flags().StringVar(&reason, "reason", "", "why: the number that regressed, and by how much")
	c.Flags().IntVar(&issue, "issue", 0, "the issue that decided it")
	return c
}

func releasesRestore(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "restore TAG",
		Short: "Undo withdraw: the release as it was published, on GitHub and in its record",
		Long: `Give TAG back the title and notes it was published with, make it a release again, latest when it is
the newest not withdrawn, and clear the withdrawal from its record and the page. Running it on a
release that is not withdrawn changes nothing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			restored := func(now onGitHub) onGitHub {
				title, notes := now.published()
				return onGitHub{Name: title, Body: notes}
			}
			if err := setRelease(c.Context(), c, args[0], restored); err != nil {
				return couldNot(err)
			}
			if err := markRecord(c, d, args[0], nil); err != nil {
				return couldNot(err)
			}
			say(c, "%s restored", args[0])
			return nil
		},
	}
}

// regressionTitle is the issue flag opens for release, measured against against: one each pair,
// which flag looks for before it opens another.
func regressionTitle(release, against string) string {
	return release + " regressed against " + against
}

func releasesFlag() *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "flag LADDER_JSON...",
		Short: "Open an issue for each ladder's newest release whose goal number regressed against the release before it",
		Long: `Read each ladder.json as releases record does, and when its newest release's Δp50 of either goal
number against the release measured with it has its 95% interval wholly above 0 - slower, beyond
noise - open an issue saying so: the Δs, the run, and the commits between the two releases that
could explain it. One issue a pair: one already open or closed is not opened again. It withdraws
nothing: at 20 boots a side about 1 in 40 flags a number is noise, an accepted cost is not a
regression, and a person decides (docs/releasing.md, "Withdrawing a release"). report.yml runs it
once a release is measured.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			for _, path := range args {
				l, err := lab.ReadLadder(path)
				if err != nil {
					return couldNot(err)
				}
				records := l.Records()
				if len(records) == 0 {
					return couldNot(fmt.Errorf("%s measured no release", path))
				}
				r := records[len(records)-1]
				worse := r.Regressions()
				if len(worse) == 0 {
					say(c, "%s: no goal number regressed", r.Release)
					continue
				}
				url, err := openRegression(c.Context(), c, r, worse, run)
				if err != nil {
					return couldNot(err)
				}
				if err := summary(r.Release + " regressed: " + url + "\n"); err != nil {
					return couldNot(err)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&run, "run", "", "the URL of the run whose artifact keeps the ladders' samples")
	return c
}

// openRegression opens the issue for r, which regressed in worse, and answers its URL: the one
// already open or closed, when there is one.
func openRegression(ctx context.Context, c *cobra.Command, r lab.Record, worse []string, run string) (string, error) {
	repo, title := github().Repo, regressionTitle(r.Release, r.Against.Release)
	raw, err := gh(ctx, "issue", "list", "-R", repo, "--state", "all", "--limit", "200", "--json", "title,url")
	if err != nil {
		return "", fmt.Errorf("looking for %q among the issues: %w", title, err)
	}
	var issues []struct{ Title, URL string }
	if err := json.Unmarshal([]byte(raw), &issues); err != nil {
		return "", fmt.Errorf("reading the issues: %w", err)
	}
	for _, i := range issues {
		if i.Title == title {
			say(c, "%s: %s", title, i.URL)
			return i.URL, nil
		}
	}
	commits, err := gh(ctx, "api", "repos/"+repo+"/compare/"+r.Against.Release+"..."+r.Release,
		"--jq", `.commits[] | "- " + .sha[0:12] + " " + (.commit.message | split("\n")[0])`)
	if err != nil {
		return "", fmt.Errorf("listing the commits from %s to %s: %w", r.Against.Release, r.Release, err)
	}
	where := "booted interleaved with it in one run on the bench fleet"
	if run != "" {
		where += " (" + run + ")"
	}
	body := r.Release + "'s " + strings.Join(worse, "; ") + " against " + r.Against.Release + ", " + where + ". " +
		"Each interval is wholly above 0: slower, beyond noise. That is a regression by docs/releasing.md, " +
		"\"Withdrawing a release\".\n\n" +
		"At 20 boots a side about 1 in 40 such flags of a number is noise, and a cost docs/log.md records as accepted " +
		"(a correctness change) is no regression. Decide which, and either close this saying why, or withdraw the release:\n\n" +
		"```\ngh workflow run withdraw.yml -R " + repo + " -f tag=" + r.Release + " -f reason=\"...\" -f issue=THIS\n```\n\n" +
		"A measurement again, if the flag may be noise: `gh workflow run report.yml -R " + repo + " -f versions=" + r.Release + "`.\n\n" +
		"## What it was measured with\n\n" + r.Table() + "\n" +
		"## The commits from " + r.Against.Release + " to " + r.Release + "\n\n" + commits + "\n"
	url, err := gh(ctx, "issue", "create", "-R", repo, "--title", title, "--body", body)
	if err != nil {
		return "", fmt.Errorf("opening %q: %w", title, err)
	}
	say(c, "%s: %s", title, url)
	return url, nil
}
