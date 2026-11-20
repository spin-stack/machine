// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"encoding/json"
	"fmt"
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
	Reps     int       `json:"reps"`
	// Run is the workflow run whose artifact keeps every sample, when one did.
	Run     string            `json:"run,omitempty"`
	Numbers map[string]Number `json:"numbers"`
	Cannot  map[string]string `json:"cannot,omitempty"`
}

// Records are the ladder's releases as records, oldest first, each with the ladder's run.
func (l *Ladder) Records() []Record {
	var measured time.Time
	for _, r := range l.Results {
		if measured.IsZero() || r.Started.Before(measured) {
			measured = r.Started
		}
	}
	var out []Record
	for _, rel := range l.Releases {
		r := Record{Release: rel, Measured: measured.UTC().Truncate(time.Second), Host: l.host(), Reps: l.reps(),
			Numbers: map[string]Number{}}
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

// apart is why two records' numbers are read with care, and how to measure a, older, and b
// together instead.
func apart(a, b string) string {
	return "Each release is measured in a run of its own, and the bench moves between runs by as much as a " +
		"release does: two reports taken on different days said v20261119.02 accepted SSH 7.3% later than " +
		"v20261119.01, and the two interleaved on one host said it did not (labs 44583379559, 44600043625; " +
		"docs/log.md). A Δ of that size says nothing; to decide on one, measure the two together: " +
		"`spin-machine-dev lab ladder --newest " + b + " --releases N`, N the releases from " + a + " to " + b + "."
}

// about is what the page and the site say first: where the records come from, the goal, and why a
// Δ between two of them is read with care.
func about() string {
	return fmt.Sprintf("Each release is measured on the bench fleet once it is published (report.yml), booted by the "+
		"spin-machine of its own commit; a release from before that by `spin-machine-dev releases measure`. "+
		"The goal is %.0f ms (AGENTS.md). ", Goal) +
		apart("A", "B") + " `spin-machine-dev releases compare A B` puts two side by side."
}

// chartTitle is what the chart shows, and its alternative text where it is an image.
const chartTitle = "First SSH login, p50 and p95, release by release"

// Doc is docs/releases.md: the goal's two numbers release by release, as a chart and as tables, the
// diagnostics, and the run each release was measured in. kept is the records' directory beside
// the page: the chart is kept.svg (Chart, which releases doc writes) and each record is linked as
// kept/RELEASE.json.
func Doc(records []Record, kept string) string {
	return "<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json: edit those, never this. -->\n\n" +
		"# Releases, measured\n\n" + about() + "\n\n" +
		"![" + chartTitle + "](" + kept + ".svg)\n\n" +
		markdown(tables(records, kept), dashes(records))
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

// tables are records' tables in the order they are read: the goal's numbers, each goal number's Δ
// against the row above and the newest's gap to the goal; the diagnostics; and where each was
// measured, each release linked to its record at kept/RELEASE.json when kept is not empty.
func tables(records []Record, kept string) []table {
	goal := table{Title: "The goal, release by release",
		Before: "First SSH login, ms p50/p95. Δ is the p50 against the row above.", Head: []string{"release"}}
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
	where := table{Title: "Where each was measured", Head: []string{"release", "measured", "host", "boots of each probe", "run"}}
	for i, r := range records {
		g, d := []entry{{Text: r.Release}}, []entry{{Text: r.Release}}
		for _, n := range numbers {
			v, ok := r.Numbers[n.key]
			if n.kind == diagnostic {
				d = append(d, entry{Text: p50(v, ok)})
				continue
			}
			g = append(g, entry{Text: pair(v, ok)})
			if n.kind != goalNumber {
				continue
			}
			prev, had := Number{}, false
			if i > 0 {
				prev, had = records[i-1].Numbers[n.key]
			}
			delta := ""
			if ok && had {
				delta = fmt.Sprintf("%+.1f", v.P50-prev.P50)
			}
			g = append(g, entry{Text: delta})
		}
		goal.Rows = append(goal.Rows, g)
		diags.Rows = append(diags.Rows, d)
		release := entry{Text: r.Release}
		if kept != "" {
			release.Link = kept + "/" + r.Release + ".json"
		}
		run := entry{}
		if r.Run != "" {
			run = entry{Text: r.Run[strings.LastIndex(r.Run, "/")+1:], Link: r.Run}
		}
		where.Rows = append(where.Rows, []entry{release, {Text: r.Measured.Format(time.DateOnly)}, {Text: r.Host},
			{Text: itoa(r.Reps)}, run})
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

// header is where and when r was measured, as one line.
func (r Record) header() string {
	line := fmt.Sprintf("%s: measured %s on %s, %d boots of each probe", r.Release, r.Measured.Format(time.DateOnly), r.Host, r.Reps)
	if r.Run != "" {
		line += " (" + r.Run + ")"
	}
	return line + "."
}

// Table is r's numbers: each p50 / p95, and why it has not the others.
func (r Record) Table() string {
	return r.header() + "\n\n" + side([]Record{r})
}

// Compare is two records side by side, the older first, each with where and when it was measured
// and why a Δ between them is read with care.
func Compare(a, b Record) string {
	if b.Release < a.Release {
		a, b = b, a
	}
	return a.header() + "\n" + b.header() + "\n\n" + apart(a.Release, b.Release) + "\n\n" + side([]Record{a, b})
}

// side is records' numbers in columns, with the last's Δ p50 against the first's when there are
// two.
func side(records []Record) string {
	var b strings.Builder
	b.WriteString("| |")
	for _, r := range records {
		b.WriteString(" " + r.Release + " |")
	}
	delta := len(records) == 2
	if delta {
		b.WriteString(" Δ p50 |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(records)))
	if delta {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, n := range numbers {
		b.WriteString("| " + n.title + " |")
		for _, r := range records {
			v, ok := r.Numbers[n.key]
			b.WriteString(" " + pair(v, ok) + " |")
		}
		if delta {
			first, ok1 := records[0].Numbers[n.key]
			last, ok2 := records[1].Numbers[n.key]
			if ok1 && ok2 {
				fmt.Fprintf(&b, " %+.1f |", last.P50-first.P50)
			} else {
				b.WriteString(" |")
			}
		}
		b.WriteString("\n")
	}
	return b.String() + markdown(nil, dashes(records))
}
