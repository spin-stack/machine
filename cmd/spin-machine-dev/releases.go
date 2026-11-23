// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/internal/lab"
)

// releasesCommand is each release's numbers kept in the repository, one small file a release, and
// the page that reads them together.
func releasesCommand() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "releases",
		Short: "Each release's measured numbers, kept in docs/releases: record, measure, compare, docs/releases.md and the site",
		Long: `A release's record is docs/releases/RELEASE.json: the goal's two numbers, the prestarted ones,
the diagnostics and the no-systemd floor, a bound with its Δ, p50/p95, from one run on the bench fleet that booted it interleaved with the
release before it, in the shape spin boots a workspace in, with where and when, and that release's
numbers from the same run with the Δ of each and its 95% interval. docs/releases.md and
docs/releases.svg, the chart it shows, are written from the records, never by hand: the goal's
numbers release by release as a chart and a table, the diagnostics, and the run each was measured
in. site writes the same as one web page, which pages.yml publishes at
https://spin-stack.github.io/spin-machine/.

A Δ is only read between two releases booted in the same run: the bench moves between runs by as
much as a release does. Two records from different runs are compared by measuring them together
(lab ladder).`,
	}
	c.PersistentFlags().StringVar(&dir, "dir", "docs/releases", "the records' directory; the page is DIR.md")
	c.AddCommand(releasesRecord(&dir), releasesMeasure(), releasesMissing(&dir), releasesShow(&dir),
		releasesCompare(&dir), releasesDoc(&dir), releasesSite(&dir), releasesSchema(), releasesPropose(&dir),
		releasesWithdraw(&dir), releasesRestore(&dir), releasesFlag())
	return c
}

// recordsDir is dir in the repository, when it is not a path of its own.
func recordsDir(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	root, err := repo()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, dir), nil
}

// recordNewest writes the record of l's newest release in dir, the release the ladder was run
// for, against the release before it in the ladder. The older releases have no record of their
// own from it: each record is the release a run was made for (releases measure).
//
// The charts releases measure drew of that release, their dependency view and its ledger, in
// measured beside the ladder, are kept beside the record as RELEASE-bootchart.svg, RELEASE-plot.svg,
// RELEASE-chain.txt, RELEASE-order.dot, RELEASE-order.svg and RELEASE-ledger.txt, and the record
// names them.
func recordNewest(dir string, l *lab.Ladder, measured, run string) (lab.Record, error) {
	records := l.Records()
	if len(records) == 0 {
		return lab.Record{}, errors.New("the ladder measured no release")
	}
	r := records[len(records)-1]
	r.Run = run
	// A withdrawal is the release's, not a run's: measured again, the release is still withdrawn.
	if before, err := lab.ReadRecord(filepath.Join(dir, r.Release+".json")); err == nil {
		r.Withdrawn = before.Withdrawn
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return lab.Record{}, fmt.Errorf("making %s: %w", dir, err)
	}
	chart, plot := lab.ChartFiles(r.Release+lab.ChartSuffix, 1)
	chain, order, graph := lab.DependencyFiles(r.Release+lab.ChartSuffix, 1)
	ledger, _, _, _ := lab.LedgerFiles(r.Release+lab.LedgerSuffix, 1)
	for _, c := range []struct {
		from, to string
		field    *string
	}{{chart, r.Release + "-bootchart.svg", &r.Bootchart}, {plot, r.Release + "-plot.svg", &r.Plot},
		{chain, r.Release + "-chain.txt", &r.Chain}, {order, r.Release + "-order.dot", &r.Order},
		{graph, r.Release + "-order.svg", &r.Graph}, {ledger, r.Release + "-ledger.txt", &r.Ledger}} {
		from := filepath.Join(measured, c.from)
		if !exists(from) {
			continue
		}
		if err := copyFile(from, filepath.Join(dir, c.to)); err != nil {
			return lab.Record{}, err
		}
		*c.field = c.to
	}
	return r, lab.WriteRecord(filepath.Join(dir, r.Release+".json"), r)
}

// page is one file the records make beside their directory.
type page struct{ path, body string }

// pages are what dir's records make beside it: DIR.md, and DIR.svg, the chart it shows.
func pages(dir string) ([]page, error) {
	records, err := lab.ReadRecords(dir)
	if err != nil {
		return nil, err
	}
	return []page{{dir + ".md", lab.Doc(records, filepath.Base(dir))}, {dir + ".svg", lab.Chart(records)}}, nil
}

// writeDoc writes DIR.md and DIR.svg from dir's records.
func writeDoc(dir string) error {
	ps, err := pages(dir)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := os.WriteFile(p.path, []byte(p.body), 0o644); err != nil { //nolint:gosec // the repository's own page
			return fmt.Errorf("writing %s: %w", p.path, err)
		}
	}
	return nil
}

func releasesRecord(dir *string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "record LADDER_JSON...",
		Short: "Write the record of each ladder.json's newest release, replacing one before it, and the page",
		Long: `Write the record of each ladder.json's newest release - the release that ladder was measured for -
replacing a record of it, and then the page, DIR.md and DIR.svg, from every record. The release's
boot charts, their dependency view and its ledger, which releases measure took beside the
ladder.json, are kept beside its record. report.yml
records what releases measure measured with main's spin-machine-dev, whichever commit measured it,
so the page proposed is the one main's test holds the records to.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			for _, path := range args {
				l, err := lab.ReadLadder(path)
				if err != nil {
					return couldNot(err)
				}
				r, err := recordNewest(d, l, filepath.Dir(path), run)
				if err != nil {
					return couldNot(fmt.Errorf("recording %s: %w", path, err))
				}
				say(c, "recorded %s", r.Release)
			}
			if err := writeDoc(d); err != nil {
				return couldNot(err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&run, "run", "", "the URL of the run whose artifact keeps the ladders' samples")
	return c
}

// mutate-lane: TestReleasesMeasureLaddersEachWithTheOneBefore
func releasesMeasure() *cobra.Command {
	var reps int
	c := &cobra.Command{
		Use:   "measure [TAG...]",
		Short: "Measure each release interleaved with the one before it, here: a ladder.json each, for releases record (default the newest published)",
		Long: `Measure each TAG in a ladder of its own with the published release before it, interleaved - as a
release is when it is published - and keep the ladder.json, every sample, in
_output/lab/measured/TAG: releases record writes TAG's record from it, with the release before it
and the Δ of each number against it, and the page. The oldest published release is measured alone.
TAG is also measured beside its no-systemd floor, a bound re-measured with every release, and its
boot is drawn once, by the bootchart probe, and accounted for once, by the ledger probe, beside the
ladder.json. Each release boots with the spin-machine of the commit it was made from; one whose spin-machine
cannot run a probe is a dash that says why. Measuring and recording are two verbs because they are
two commits in report.yml: a release is measured at its own tag, and recorded by main's code, which
writes the page main's test holds the records to. Records are only worth keeping from the bench
fleet: report.yml runs this there. Exits 1 when a measurement was lost (the ladders are written),
2 when a release could not be measured (the ones before it are).`,
		RunE: func(c *cobra.Command, args []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			tags := args
			if len(tags) == 0 {
				newest, err := github().LatestRelease()
				if err != nil {
					return couldNot(err)
				}
				tags = []string{newest}
			}
			scratch, err := lab.Scratch(filepath.Join(root, "_output", "lab"))
			if err != nil {
				return couldNot(err)
			}
			published, err := github().Releases(publishedReleases)
			if err != nil {
				return couldNot(err)
			}
			var lost error
			for _, tag := range tags {
				pair, err := lab.LadderReleases(lab.Adopted(published, tag), tag, 2)
				if err != nil {
					return couldNot(err)
				}
				ts, err := releaseTrees(root, pair, c.ErrOrStderr())
				if err != nil {
					return couldNot(err)
				}
				l, err := lab.RunLadder(c.Context(), ts, reps, scratch, c.ErrOrStderr())
				if err != nil {
					return couldNot(err)
				}
				floor, err := trees(root, nil, []string{tag + lab.FloorSuffix + "=--from release:" + tag +
					" --spin-machine release --edit floor --append " + lab.FloorAppend}, c.ErrOrStderr())
				if err != nil {
					return couldNot(err)
				}
				if err := l.MeasureFloor(c.Context(), ts[len(ts)-1], floor[0], reps, scratch, c.ErrOrStderr()); err != nil {
					return couldNot(err)
				}
				measured := filepath.Join(root, "_output", "lab", "measured", tag)
				if err := writeLadder(c.OutOrStdout(), l, measured); exitCode(err) == 1 {
					lost = err
				} else if err != nil {
					return err
				}
				if err := drawRelease(c, root, tag, measured, scratch); err != nil {
					return couldNot(err)
				}
			}
			return lost
		},
	}
	c.Flags().IntVar(&reps, "reps", 20, "boots of each release for each probe")
	return c
}

// copyFile writes a copy of the file at from to to.
func copyFile(from, to string) error {
	b, err := os.ReadFile(from) //nolint:gosec // a file this tool made or kept
	if err != nil {
		return fmt.Errorf("reading %s: %w", from, err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil { //nolint:gosec // a chart for a person to read, or a kernel to publish
		return fmt.Errorf("writing %s: %w", to, err)
	}
	return nil
}

// drawRelease draws one boot of tag with the bootchart probe and accounts for another with the
// ledger probe, on the kernel built beside the release that can be traced (kernel/README.md), and
// keeps what each kept and its result, bootchart.json and ledger.json, in measured. Both are
// diagnostics: one that could not be taken - a release published before its traceable kernel was,
// a release whose spin-machine cannot run the probe, a boot whose sampler or trace wrote nothing -
// is said, and fails nothing. Two boots, not one with both: the sampler would be in the ledger's
// trace, and its cost in the chart's.
//
// mutate-lane: TestReleasesMeasureLaddersEachWithTheOneBefore
func drawRelease(c *cobra.Command, root, tag, measured, scratch string) error {
	for _, d := range []struct{ probe, suffix, flags string }{
		{"bootchart", lab.ChartSuffix, "--edit bootchart"},
		{"ledger", lab.LedgerSuffix, "--kernel traceable:" + tag + " --edit ledger --append '" + lab.LedgerAppend + "'"},
	} {
		ts, err := trees(root, nil, []string{tag + d.suffix + "=--from release:" + tag + " --spin-machine release " + d.flags}, c.ErrOrStderr())
		if err != nil {
			say(c, "%s's boot was not taken by %s: %v", tag, d.probe, err)
			continue
		}
		r, err := lab.Experiment{Probe: lab.Probes[d.probe], Trees: ts, Reps: 1, Scratch: scratch, Keep: measured, Log: c.ErrOrStderr()}.Run(c.Context())
		if err != nil {
			say(c, "%s's boot was not taken by %s: %v", tag, d.probe, err)
			continue
		}
		if err := r.Write(filepath.Join(measured, d.probe+".json")); err != nil {
			return err
		}
	}
	return nil
}

// publishedReleases is how many of the newest releases missing asks GitHub for: all of them.
const publishedReleases = 1000

func releasesMissing(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "missing",
		Short: "List the published releases with no record, oldest first: what releases measure has left to do",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			published, err := github().Releases(publishedReleases)
			if err != nil {
				return couldNot(err)
			}
			tags := lab.Tags(published)
			slices.Reverse(tags)
			for _, tag := range tags {
				if !exists(filepath.Join(d, tag+".json")) {
					_, _ = fmt.Fprintln(c.OutOrStdout(), tag)
				}
			}
			return nil
		},
	}
}

func releasesShow(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show RELEASE",
		Short: "Print a release's record: its numbers, and where and when they were measured",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			r, err := lab.ReadRecord(filepath.Join(d, args[0]+".json"))
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprint(c.OutOrStdout(), r.Table())
			return nil
		},
	}
}

func releasesCompare(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "compare A B",
		Short: "Print two releases' records side by side: with the newer's Δ when it was measured against the older, else each its own run's and how to measure them together",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			var rs []lab.Record
			for _, rel := range args {
				r, err := lab.ReadRecord(filepath.Join(d, rel+".json"))
				if err != nil {
					return couldNot(err)
				}
				rs = append(rs, r)
			}
			_, _ = fmt.Fprint(c.OutOrStdout(), lab.Compare(rs[0], rs[1]))
			return nil
		},
	}
}

func releasesDoc(dir *string) *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:   "doc",
		Short: "Write DIR.md and its chart, DIR.svg, from the records; with --check, exit 1 when either is not what they say",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			if !check {
				if err := writeDoc(d); err != nil {
					return couldNot(err)
				}
				return nil
			}
			ps, err := pages(d)
			if err != nil {
				return couldNot(err)
			}
			for _, p := range ps {
				if got, err := os.ReadFile(p.path); err != nil || string(got) != p.body { //nolint:gosec // the repository's own page
					return exitError{code: 1, err: fmt.Errorf("%s is not what %s/*.json say: spin-machine-dev releases doc writes it", p.path, d)}
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "write nothing; exit 1 when the page differs")
	return c
}

func releasesSite(dir *string) *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "site --out OUT",
		Short: "Write the records' web page, OUT/index.html, and each record beside it in OUT/releases",
		Long: `Write what docs/releases.md says as one HTML page that fetches nothing - the chart inline, so
its points' tooltips show - with each record and boot chart it links to copied beside it. pages.yml publishes it
at https://spin-stack.github.io/spin-machine/ from every merge that changes a record.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			records, err := lab.ReadRecords(d)
			if err != nil {
				return couldNot(err)
			}
			html, err := lab.Site(records)
			if err != nil {
				return couldNot(err)
			}
			kept := filepath.Join(out, lab.SiteRecords)
			if err := os.MkdirAll(kept, 0o755); err != nil {
				return couldNot(fmt.Errorf("making %s: %w", kept, err))
			}
			for _, r := range records {
				if err := lab.WriteRecord(filepath.Join(kept, r.Release+".json"), r); err != nil {
					return couldNot(err)
				}
				for _, chart := range []string{r.Bootchart, r.Plot, r.Chain, r.Order, r.Graph, r.Ledger} {
					if chart == "" {
						continue
					}
					if err := copyFile(filepath.Join(d, chart), filepath.Join(kept, chart)); err != nil {
						return couldNot(err)
					}
				}
			}
			index := filepath.Join(out, "index.html")
			if err := os.WriteFile(index, []byte(html), 0o644); err != nil { //nolint:gosec // a page to publish
				return couldNot(fmt.Errorf("writing %s: %w", index, err))
			}
			say(c, "wrote %s and %d records", index, len(records))
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "", "the directory the site is written in")
	_ = c.MarkFlagRequired("out")
	return c
}

// releasedSchemas is where each supported release's query-qmp-schema is kept, which
// qmpdef.CheckSupported holds the module's definitions to.
const releasedSchemas = "internal/qmpdef/testdata/released"

// mutate-lane: TestReleasesSchemaRecordsEachReleasesQEMU
func releasesSchema() *cobra.Command {
	return &cobra.Command{
		Use:   "schema TAG...",
		Short: "Record each release's QEMU's query-qmp-schema in " + releasedSchemas + ", which go test holds the machine's QMP to",
		Long: `Fetch each release, verified, into _output/lab/releases as the lab does (a release releases measure
fetched is not fetched again), start its qemu-system-x86_64 with no machine and write its answer
to query-qmp-schema to ` + releasedSchemas + `/TAG.json. go test holds every command, argument
and capability the machine sends to each of them (qmpdef.CheckSupported) in seconds and with no
VM, which compat proves on KVM by resuming each release's checkpoint. The production binary needs
/dev/kvm to start. report.yml records the new release's after its numbers, and releases propose
proposes both.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			dir := filepath.Join(root, releasedSchemas)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return couldNot(fmt.Errorf("making %s: %w", dir, err))
			}
			src := sources(root, c.ErrOrStderr())
			for _, tag := range args {
				share, err := src.Release(tag)
				if err != nil {
					return couldNot(err)
				}
				ctx, cancel := context.WithTimeout(c.Context(), time.Minute)
				schema, err := build.QMPSchema(ctx, filepath.Join(share, "bin", "qemu-system-x86_64"))
				cancel()
				if err != nil {
					return couldNot(fmt.Errorf("release %s: %w", tag, err))
				}
				path := filepath.Join(dir, tag+".json")
				if err := os.WriteFile(path, append(schema, '\n'), 0o644); err != nil { //nolint:gosec // the repository's own fixture
					return couldNot(fmt.Errorf("writing %s: %w", path, err))
				}
				say(c, "recorded %s", path)
			}
			return nil
		},
	}
}

// recorded are the releases whose records the checkout has added or changed, oldest first.
func recorded(c *cobra.Command, root, dir string) ([]string, error) {
	out, err := exec.CommandContext(c.Context(), "git", "-C", root, "status", "--porcelain", "--untracked-files=all", "--", dir).Output() //nolint:gosec // the records' directory
	if err != nil {
		return nil, fmt.Errorf("asking git what %s holds that is not committed: %w", dir, err)
	}
	var releases []string
	for line := range strings.Lines(string(out)) {
		if name, ok := strings.CutSuffix(filepath.Base(strings.TrimSpace(line)), ".json"); ok {
			releases = append(releases, name)
		}
	}
	slices.Sort(releases)
	return releases, nil
}

// proposalBranch is named for the releases it proposes, so a set proposed once is not proposed
// again: one release by its name, several by the oldest, the newest and a hash of them all, which
// a ref's length allows when a backfill is forty releases.
func proposalBranch(releases []string) string {
	if len(releases) == 1 {
		return "releases/" + releases[0]
	}
	sum := sha256.Sum256([]byte(strings.Join(releases, " ")))
	return "releases/" + releases[0] + "-" + releases[len(releases)-1] + "-" + hex.EncodeToString(sum[:4])
}

func releasesPropose(dir *string) *cobra.Command {
	var branch, title string
	c := &cobra.Command{
		Use:   "propose",
		Short: "Propose the records this checkout wrote, the page and the releases' schemas, as a pull request against main",
		Long: `Commit what releases record wrote - each new or changed record in DIR, DIR.md and DIR.svg -
and what releases schema wrote in ` + releasedSchemas + `, on a branch named for those
releases, push it and open a pull request against main. Nothing is proposed when nothing was
recorded, nor when the branch is on origin already: a set proposed once, merged or closed, is not
proposed again. report.yml runs it as the release app, so the checks on the pull request run.
withdraw.yml runs it with --branch and --title, for a withdrawal or its undoing: a release's
record changes again after its measurement was proposed under the release's name.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			d, err := recordsDir(*dir)
			if err != nil {
				return err
			}
			root, err := repo()
			if err != nil {
				return err
			}
			releases, err := recorded(c, root, d)
			if err != nil {
				return couldNot(err)
			}
			if len(releases) == 0 {
				say(c, "nothing recorded in %s to propose", d)
				return nil
			}
			if branch == "" {
				branch = proposalBranch(releases)
			}
			if exec.CommandContext(c.Context(), "git", "-C", root, "ls-remote", "--exit-code", "--heads", "origin", branch).Run() == nil { //nolint:gosec // a branch this names
				say(c, "%s was proposed already", branch)
				return nil
			}
			body := "Measured on the bench fleet by report.yml (spin-machine-dev releases measure), each release interleaved with the one before it.\n\n"
			for _, rel := range releases {
				r, err := lab.ReadRecord(filepath.Join(d, rel+".json"))
				if err != nil {
					return couldNot(err)
				}
				body += "### " + rel + "\n\n" + r.Table() + "\n"
			}
			page, err := os.ReadFile(d + ".md") //nolint:gosec // the repository's own page
			if err != nil {
				return couldNot(fmt.Errorf("reading the page: %w", err))
			}
			body += "### " + filepath.Base(d) + ".md, as it reads now\n\n" + string(page)
			if title == "" {
				title = "releases: " + strings.Join(releases, ", ") + ", measured"
			}
			// The branch is main with the records on it, not whatever this checkout is: a branch cut
			// from an older commit carries the workflows as they were, which GitHub refuses from the
			// release app once main's have moved (v20261120.03's record). report.yml records and
			// proposes from main's checkout; a person may run it from anywhere.
			for _, step := range [][]string{
				{"git", "fetch", "-q", "--depth=1", "origin", "main"},
				{"git", "switch", "-q", "--merge", "-c", branch, "FETCH_HEAD"},
				{"git", "add", "--", d, d + ".md", d + ".svg", releasedSchemas},
				{"git", "commit", "-q", "-m", title},
				{"git", "push", "origin", branch},
			} {
				if err := command(c.Context(), c, root, step[0], step[1:]...); err != nil {
					return err
				}
			}
			url, err := gh(c.Context(), "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body)
			if err != nil {
				return couldNot(err)
			}
			say(c, "%s", url)
			return summary(url + "\n")
		},
	}
	c.Flags().StringVar(&branch, "branch", "", "the branch proposed (default named for the releases recorded)")
	c.Flags().StringVar(&title, "title", "", "the pull request's title and commit's subject (default the releases, measured)")
	return c
}
