// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Number is one of a release's numbers, ms or MiB, over the boots of one run.
type Number struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// kind is how a number is read: as one of the goal's, beside them, or as a diagnostic.
type kind int

const (
	// goalNumber is one of the goal's two (AGENTS.md, "The goal"): it has a Δ and a gap to Goal.
	goalNumber kind = iota
	// besideGoal is read beside the goal's two: the same login into a QEMU started ahead.
	besideGoal
	// diagnostic is not a target: what moves behind the goal's numbers.
	diagnostic
)

// number is one number a record keeps: its key in a record's JSON, its title where it is read,
// and where a ladder has it.
type number struct {
	key, title string
	kind       kind
	cell       cell
}

// numbers are a record's, in the order they are read. A key is in every record under
// docs/releases: renaming one leaves the numbers recorded under the old one unread.
var numbers = []number{
	{"cold", "cold boot to first login", goalNumber, cell{"ssh-first", "boot", "first_login"}},
	{"restore", "restore to first login", goalNumber, cell{"restore-first", "evicted", "login"}},
	{"cold-ahead", "cold, QEMU started ahead", besideGoal, cell{"prestart", rowPrestarted, "login"}},
	{"restore-ahead", "restore, QEMU started ahead", besideGoal, cell{"prestart", rowRestoredAhead, "login"}},
	{"save-ms", "save ms", diagnostic, cell{"restore-first", "evicted", "save"}},
	{"state-mib", "state MiB", diagnostic, cell{"restore-first", "evicted", "state"}},
	{"kernel-ms", `kernel to "Freeing" ms`, diagnostic, cell{"initcalls", "boot", "freeing"}},
	{"qemu-rss-mib", "QEMU RSS MiB", diagnostic, cell{"ssh-first", "boot", "qemu_rss"}},
}

// Record is a release's numbers as docs/releases/RELEASE.json keeps them: what one run on one
// host measured of it, and why it has none of the others.
type Record struct {
	Release  string    `json:"release"`
	Measured time.Time `json:"measured"`
	Host     string    `json:"host"`
	// Shape is the machine the run booted (shapeOf); a record from before it was named booted
	// shapeBefore.
	Shape string `json:"shape,omitempty"`
	Reps  int    `json:"reps"`
	// Run is the workflow run whose artifact keeps every sample, when one did.
	Run     string            `json:"run,omitempty"`
	Numbers map[string]Number `json:"numbers"`
	Cannot  map[string]string `json:"cannot,omitempty"`
	// Against is the release before it, booted interleaved with it in the same run: the only
	// numbers its Δ is read against. A release measured alone has none.
	Against *Against `json:"against,omitempty"`
}

// Against is the release a record was measured interleaved with: that release's numbers from the
// same run, and the record's Δ p50 against each.
type Against struct {
	Release string            `json:"release"`
	Numbers map[string]Number `json:"numbers"`
	Delta   map[string]Delta  `json:"delta,omitempty"`
}

// shape is the machine r was measured in.
func (r Record) shape() string { return cmp.Or(r.Shape, shapeBefore) }

// Records are the ladder's releases as records, oldest first, each with the ladder's run, and
// each but the first against the release before it.
func (l *Ladder) Records() []Record {
	var measured time.Time
	for _, r := range l.Results {
		if measured.IsZero() || r.Started.Before(measured) {
			measured = r.Started
		}
	}
	machine := ""
	if len(l.Results) > 0 {
		machine = shapeOf(l.Results[0].Options)
	}
	var out []Record
	for i, rel := range l.Releases {
		r := Record{Release: rel, Measured: measured.UTC().Truncate(time.Second), Host: l.host(), Shape: machine,
			Reps: l.reps(), Numbers: map[string]Number{}}
		if i > 0 {
			r.Against = l.against(out[i-1], rel)
		}
		for _, n := range numbers {
			p50, p95, why := l.value(rel, n.cell)
			if why != "" {
				if r.Cannot == nil {
					r.Cannot = map[string]string{}
				}
				r.Cannot[n.key] = why
				continue
			}
			r.Numbers[n.key] = Number{P50: tenth(p50), P95: tenth(p95)}
		}
		out = append(out, r)
	}
	return out
}

// against is before's numbers as the ladder measured them beside rel, and rel's Δ against each.
func (l *Ladder) against(before Record, rel string) *Against {
	a := &Against{Release: before.Release, Numbers: maps.Clone(before.Numbers), Delta: map[string]Delta{}}
	for _, n := range numbers {
		r := l.result(n.cell.probe)
		if r == nil {
			continue
		}
		if d, ok := DeltaP50(r.Values(before.Release, n.cell.row, n.cell.metric), r.Values(rel, n.cell.row, n.cell.metric)); ok {
			a.Delta[n.key] = d
		}
	}
	return a
}

// tenth is v to a tenth: what a record keeps of a number whose noise between runs is whole ms, so
// that a record reads as the doc does and a change to one is a line a person can review.
func tenth(v float64) float64 { return math.Round(v*10) / 10 }

// WriteRecord writes r as JSON at path.
func WriteRecord(path string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// ReadRecord reads a record's JSON.
func ReadRecord(path string) (Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return r, nil
}

// ReadRecords reads every record in dir, oldest release first: a CalVer tag sorts as its date.
func ReadRecords(dir string) ([]Record, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []Record
	for _, p := range paths {
		r, err := ReadRecord(p)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Record) int { return cmp.Compare(a.Release, b.Release) })
	return out, nil
}

// drift is what the bench moved between two runs of the same release: why no Δ is read across two
// runs.
const drift = "the bench moves between runs by as much as a release does: v20261120.01's restore to first login " +
	"was 158.9 ms p50 in one run and 167.0 in another (labs 44641240861, 44727451080)"

// apart is why two records measured in different runs are not compared, and how to measure a,
// older, and b together instead.
func apart(a, b string) string {
	return a + " and " + b + " were measured in different runs, and " + drift + ". No Δ is read across two runs; " +
		"to decide between the two, measure them together: `spin-machine-dev lab ladder --newest " + b +
		" --releases N`, N the releases from " + a + " to " + b + "."
}

// about is what the page and the site say first: where the records come from, the goal, and what
// a Δ is read against.
func about() string {
	return "The bench fleet measures each release once it is published (report.yml). It boots the release " +
		"interleaved with the release before it, each by the spin-machine of its own commit, in the shape spin " +
		"boots a workspace in. A release from before report.yml was measured by `spin-machine-dev releases measure`. " +
		"Each record says the shape it booted, and a number is read only beside one in the same shape. " +
		fmt.Sprintf("The goal is %.0f ms (AGENTS.md). ", Goal) +
		"A Δ is read only between two releases booted in the same run, because " + drift + ". " +
		"So a release's Δ is against the release measured with it, with its 95% interval. A Δ whose interval " +
		"includes 0 is within noise, whatever its size. `spin-machine-dev releases compare A B` puts two side by side."
}

// chartTitle is what the chart shows, and its alternative text where it is an image.
const chartTitle = "First SSH login, p50 and p95, release by release"

// Doc is docs/releases.md: the goal's two numbers release by release, as a chart and as tables, the
// diagnostics, and the run each release was measured in. A missing number is a dash and nothing
// more: why a probe lost its boots is the ladder's to say (Tables), not the page's. kept is the records' directory beside
// the page: the chart is kept.svg (Chart, which releases doc writes) and each record is linked as
// kept/RELEASE.json.
func Doc(records []Record, kept string) string {
	return "<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json: edit those, never this. -->\n\n" +
		"# Releases, measured\n\n" + about() + "\n\n" +
		"![" + chartTitle + "](" + kept + ".svg)\n\n" +
		markdown(tables(records, kept), nil)
}

// Tables are a ladder's records as tables (lab ladder's ladder.md): the page's, with no record to
// link to.
func Tables(records []Record) string {
	return markdown(tables(records, ""), dashes(records))
}

// entry is one cell of a table, and where it links when it does.
type entry struct{ Text, Link string }

// table is one of the records' tables, as the Markdown page and the site both write it: its
// heading, a line before the rows and one after, either of which may be empty.
type table struct {
	Title, Before string
	Head          []string
	Rows          [][]entry
	After         string
}

// alone says what a blank Δ is, when a release has one. No code in it: the site writes it as text.
const alone = " A release with no Δ was measured with no release before it in its run, and spin-machine-dev " +
	"releases measure measures it again with the one before it."

// tables are records' tables in the order they are read: the goal's numbers, each goal number's Δ
// against the release measured with it and the newest's gap to the goal; the diagnostics; and
// where, in what shape and with what each was measured, each release linked to its record at
// kept/RELEASE.json when kept is not empty.
func tables(records []Record, kept string) []table {
	goal := table{Title: "The goal, release by release",
		Before: "First SSH login, ms p50/p95. Δ is the p50 against the release before it, booted interleaved with it " +
			"in the same run, and its [95% interval]: within noise when the interval includes 0.", Head: []string{"release"}}
	if slices.ContainsFunc(records, func(r Record) bool { return r.Against == nil }) {
		goal.Before += alone
	}
	diags := table{Title: "Diagnostics",
		Before: "Not targets: what moves behind the goal's numbers (AGENTS.md, \"The goal\"). p50.", Head: []string{"release"}}
	for _, n := range numbers {
		switch n.kind {
		case goalNumber:
			goal.Head = append(goal.Head, n.title, "Δ")
		case besideGoal:
			goal.Head = append(goal.Head, n.title)
		case diagnostic:
			diags.Head = append(diags.Head, n.title)
		}
	}
	where := table{Title: "Where each was measured",
		Head: []string{"release", "measured", "host", "shape", "boots of each probe", "against", "run"}}
	for _, r := range records {
		g, d := []entry{{Text: r.Release}}, []entry{{Text: r.Release}}
		for _, n := range numbers {
			v, ok := r.Numbers[n.key]
			if n.kind == diagnostic {
				d = append(d, entry{Text: p50(v, ok)})
				continue
			}
			g = append(g, entry{Text: pair(v, ok)})
			if n.kind == goalNumber {
				g = append(g, entry{Text: r.delta(n.key)})
			}
		}
		goal.Rows = append(goal.Rows, g)
		diags.Rows = append(diags.Rows, d)
		release := entry{Text: r.Release}
		if kept != "" {
			release.Link = kept + "/" + r.Release + ".json"
		}
		against := entry{}
		if r.Against != nil {
			against.Text = r.Against.Release
		}
		run := entry{}
		if r.Run != "" {
			run = entry{Text: r.Run[strings.LastIndex(r.Run, "/")+1:], Link: r.Run}
		}
		where.Rows = append(where.Rows, []entry{release, {Text: r.Measured.Format(time.DateOnly)}, {Text: r.Host},
			{Text: r.shape()}, {Text: itoa(r.Reps)}, against, run})
	}
	if len(records) > 0 {
		newest := records[len(records)-1]
		var gaps []string
		for _, n := range numbers {
			if v, ok := newest.Numbers[n.key]; ok && n.kind == goalNumber {
				gaps = append(gaps, fmt.Sprintf("%s %+.0f ms", n.key, v.P50-Goal))
			}
		}
		if len(gaps) > 0 {
			goal.After = newest.Release + " against the goal: " + strings.Join(gaps, ", ") + "."
		}
	}
	return []table{goal, diags, where}
}

// delta is r's Δ for the number key against the release measured with it, as a table cell: blank
// when r was measured alone or either release has no interval for it.
func (r Record) delta(key string) string {
	if r.Against == nil {
		return ""
	}
	if d, ok := r.Against.Delta[key]; ok {
		return d.String()
	}
	return ""
}

// markdown is tables as Markdown, and the dashes' reasons after them.
func markdown(ts []table, dashed []string) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## " + t.Title + "\n\n")
		if t.Before != "" {
			b.WriteString(t.Before + "\n\n")
		}
		b.WriteString("| " + strings.Join(t.Head, " | ") + " |\n|" + strings.Repeat("---|", len(t.Head)) + "\n")
		for _, row := range t.Rows {
			b.WriteString("|")
			for _, e := range row {
				if e.Link != "" {
					b.WriteString(" [" + e.Text + "](" + e.Link + ") |")
				} else {
					b.WriteString(" " + e.Text + " |")
				}
			}
			b.WriteString("\n")
		}
		if t.After != "" {
			b.WriteString("\n" + t.After + "\n")
		}
	}
	if len(dashed) > 0 {
		b.WriteString("\n" + dashesBefore + "\n\n- " + strings.Join(dashed, "\n- ") + "\n")
	}
	return b.String()
}

// pair is a number as a table cell, p50 / p95, or a dash when there is none.
func pair(v Number, ok bool) string {
	if !ok {
		return "-"
	}
	return num(v.P50) + " / " + num(v.P95)
}

// p50 is a number's p50 as a table cell, or a dash when there is none.
func p50(v Number, ok bool) string {
	if !ok {
		return "-"
	}
	return num(v.P50)
}

// dashesBefore introduces the dashes' reasons.
const dashesBefore = "A dash is a number that release has not:"

// dashes are why each record has not the numbers it has not, one line each.
func dashes(records []Record) []string {
	var lines []string
	for _, r := range records {
		for _, n := range numbers {
			if why, ok := r.Cannot[n.key]; ok {
				lines = append(lines, r.Release+", "+n.title+": "+why)
			}
		}
	}
	return lines
}

// header is where, when, in what shape and with what r was measured, as one line.
func (r Record) header() string {
	line := fmt.Sprintf("%s: measured %s on %s; %s; %d boots of each probe", r.Release, r.Measured.Format(time.DateOnly),
		r.Host, r.shape(), r.Reps)
	if r.Against != nil {
		line += ", interleaved with " + r.Against.Release
	}
	if r.Run != "" {
		line += " (" + r.Run + ")"
	}
	return line + "."
}

// Table is r's numbers: each p50 / p95, and why it has not the others.
func (r Record) Table() string {
	return r.header() + "\n\n" + side([]Record{r}, nil)
}

// Compare is two records side by side, the older first. When the newer was measured interleaved
// with the older, the two are that run's numbers, with the newer's Δ against the older's;
// otherwise each is its own run's, with no Δ, why not, and how to measure the two together.
func Compare(a, b Record) string {
	if b.Release < a.Release {
		a, b = b, a
	}
	if b.Against != nil && b.Against.Release == a.Release {
		with := Record{Release: a.Release, Numbers: b.Against.Numbers}
		return b.header() + "\n\n" + a.Release + "'s numbers are the ones booted with " + b.Release + " in its run, not its own " +
			"record's. Δ p50 [95% interval] is within noise when the interval includes 0.\n\n" +
			side([]Record{with, b}, b.Against.Delta)
	}
	return a.header() + "\n" + b.header() + "\n\n" + apart(a.Release, b.Release) + "\n\n" + side([]Record{a, b}, nil)
}

// side is records' numbers in columns, and a column of deltas when there are some.
func side(records []Record, deltas map[string]Delta) string {
	var b strings.Builder
	b.WriteString("| |")
	for _, r := range records {
		b.WriteString(" " + r.Release + " |")
	}
	if deltas != nil {
		b.WriteString(" Δ p50 |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(records)))
	if deltas != nil {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, n := range numbers {
		b.WriteString("| " + n.title + " |")
		for _, r := range records {
			v, ok := r.Numbers[n.key]
			b.WriteString(" " + pair(v, ok) + " |")
		}
		if deltas != nil {
			if d, ok := deltas[n.key]; ok {
				b.WriteString(" " + d.String() + " |")
			} else {
				b.WriteString(" |")
			}
		}
		b.WriteString("\n")
	}
	return b.String() + markdown(nil, dashes(records))
}
