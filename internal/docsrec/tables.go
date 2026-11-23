// SPDX-License-Identifier: Apache-2.0

package docsrec

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Record is one row of docs/boot-time.md's tables, docs/experiments/[<issue>-]<slug>.json: a change
// the machine carries, one measured and rejected, or one still open. Its table's columns say which
// of the text fields it has; each holds its cell as the table shows it.
type Record struct {
	// Decision is carried, rejected or open, and with Section names the record's table.
	Decision string `json:"decision"`
	// Section is a rejected change's part of the machine: kernel, firmware, image, login, restore,
	// qmp or memory.
	Section string `json:"section,omitempty"`
	// Order places the row in its table, lowest first; rows with the same order follow their file
	// names. The split left gaps of 10, so a row goes between two without moving either.
	Order int `json:"order"`
	// Date is when a rejected change was decided, YYYY-MM-DD.
	Date string `json:"date,omitempty"`

	Change    string `json:"change"`
	Where     string `json:"where,omitempty"`
	Result    string `json:"result,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	Shape     string `json:"shape,omitempty"`
	Source    string `json:"source,omitempty"`
	Number    string `json:"number,omitempty"`
	Phase     string `json:"phase,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	State     string `json:"state,omitempty"`

	Issues []int `json:"issues,omitempty"`
	PRs    []int `json:"prs,omitempty"`
	Runs   []int `json:"runs,omitempty"`
}

// cells is each text field by its JSON name. Date's cell is its month and day, as the tables have
// always shown it.
func (r Record) cells() map[string]string {
	date := r.Date
	if len(date) == len(dateLayout) {
		date = date[5:]
	}
	return map[string]string{
		"change": r.Change, "where": r.Where, "result": r.Result, "evidence": r.Evidence,
		"shape": r.Shape, "source": r.Source, "date": date, "number": r.Number, "phase": r.Phase,
		"decided_by": r.DecidedBy, "state": r.State,
	}
}

// column is one column of a table: its heading and the record's field it shows.
type column struct{ heading, field string }

var (
	carriedColumns = []column{
		{"Item", "change"}, {"Where", "where"}, {"Earns (Δp50 of taking it back)", "result"},
		{"Evidence", "evidence"}, {"Run", "source"},
	}
	rejectedColumns = []column{
		{"Idea", "change"}, {"Result", "result"}, {"Shape", "shape"}, {"Source", "source"}, {"Date", "date"},
	}
	openColumns = []column{
		{"Experiment", "change"}, {"Number", "number"}, {"Phase, p50, ceiling", "phase"},
		{"Decided by", "decided_by"}, {"State", "state"},
	}
)

// Table is one of docs/boot-time.md's tables, under the heading it has there.
type Table struct {
	Decision, Section string
	// Level and Heading are the heading's level (2 or 3) and text.
	Level   int
	Heading string
	columns []column
}

// Tables are the tables in the order boot-time.md has them. "Measured and rejected" is a heading of
// its own above its sections.
var Tables = []Table{
	{"carried", "", 2, "What the machine carries", carriedColumns},
	{"rejected", "kernel", 3, "Kernel and its command line", rejectedColumns},
	{"rejected", "firmware", 3, "Firmware, QEMU and the machine", rejectedColumns},
	{"rejected", "image", 3, "systemd and the image", rejectedColumns},
	{"rejected", "login", 3, "The login", rejectedColumns},
	{"rejected", "restore", 3, "The restore and checkpoints", rejectedColumns},
	{"rejected", "qmp", 3, "QMP and vsock", rejectedColumns},
	{"rejected", "memory", 3, "Memory and the host", rejectedColumns},
	{"open", "", 2, "Open experiments", openColumns},
}

// table is the table r belongs in.
func (r Record) table() (Table, error) {
	for _, t := range Tables {
		if t.Decision == r.Decision && t.Section == r.Section {
			return t, nil
		}
	}
	if !slices.Contains([]string{"carried", "rejected", "open"}, r.Decision) {
		return Table{}, fmt.Errorf("decision %q is not carried, rejected or open", r.Decision)
	}
	if r.Decision == "rejected" {
		return Table{}, fmt.Errorf("section %q is not kernel, firmware, image, login, restore, qmp or memory", r.Section)
	}
	return Table{}, fmt.Errorf("section: a %s record has none", r.Decision)
}

// ParseRecord reads one record, named name, and refuses one whose table is unknown, which lacks a
// cell its table shows or has one it does not, or whose lists do not agree with its text.
func ParseRecord(name string, data []byte) (Record, error) {
	r, err := parseRecord(name, data)
	if err != nil {
		return Record{}, fmt.Errorf("%s: %w", name, err)
	}
	return r, nil
}

func parseRecord(name string, data []byte) (Record, error) {
	var r Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Record{}, err
	}
	if base, ok := strings.CutSuffix(name, ".json"); !ok || !slug.MatchString(base) {
		return Record{}, errors.New("the file is not named [<issue>-]<slug>.json, a slug of a-z, 0-9 and -")
	}
	t, err := r.table()
	if err != nil {
		return Record{}, err
	}
	if r.Order < 1 {
		return Record{}, errors.New("order: required, the row's place in its table, from 1")
	}
	if r.Date != "" {
		if _, err := parseDate(r.Date); err != nil {
			return Record{}, err
		}
	}
	cells := r.cells()
	for _, c := range t.columns {
		if strings.TrimSpace(cells[c.field]) == "" {
			return Record{}, fmt.Errorf("%s: required, the table's %q", c.field, c.heading)
		}
		if strings.Contains(cells[c.field], "\n") {
			return Record{}, fmt.Errorf("%s: a cell is one line", c.field)
		}
		delete(cells, c.field)
	}
	for _, field := range slices.Sorted(maps.Keys(cells)) {
		if cells[field] != "" {
			return Record{}, fmt.Errorf("%s: %q has no such column", field, t.Heading)
		}
	}
	var text strings.Builder
	all := r.cells()
	for _, field := range slices.Sorted(maps.Keys(all)) {
		text.WriteString(all[field] + "\n")
	}
	if err := checkRefs(text.String(), r.Issues, r.PRs, r.Runs); err != nil {
		return Record{}, err
	}
	return r, nil
}

// ReadRecords reads every record in dir, in the order their tables show them.
func ReadRecords(dir string) ([]Record, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the experiments: %w", err)
	}
	var records []Record
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading the experiments: %w", err)
		}
		r, err := ParseRecord(f.Name(), data)
		if err != nil {
			return nil, fmt.Errorf("reading the experiments: %s: %w", dir, err)
		}
		records = append(records, r)
	}
	// Stable: the directory is read in file-name order, which breaks a tie.
	slices.SortStableFunc(records, func(a, b Record) int { return cmp.Compare(a.Order, b.Order) })
	return records, nil
}

// Markdown is t's rows among records, under its header row.
func (t Table) Markdown(records []Record) string {
	var b strings.Builder
	row := func(cells []string) { b.WriteString("| " + strings.Join(cells, " | ") + " |\n") }
	var headings, rule []string
	for _, c := range t.columns {
		headings, rule = append(headings, c.heading), append(rule, "---")
	}
	row(headings)
	b.WriteString("|" + strings.Join(rule, "|") + "|\n")
	for _, r := range records {
		if r.Decision != t.Decision || r.Section != t.Section {
			continue
		}
		cells := r.cells()
		var line []string
		for _, c := range t.columns {
			line = append(line, cells[c.field])
		}
		row(line)
	}
	return b.String()
}

// TablesMarkdown is every table as one page, each under its heading in boot-time.md.
func TablesMarkdown(records []Record) string {
	var b strings.Builder
	b.WriteString("# Experiments\n\nThe tables of docs/boot-time.md, rendered from docs/experiments by " +
		"`spin-machine-dev docs tables`. docs/boot-time.md says how each is read.\n")
	for _, t := range Tables {
		if t.Decision == "rejected" && t.Section == Tables[1].Section {
			b.WriteString("\n## Measured and rejected\n")
		}
		b.WriteString("\n" + strings.Repeat("#", t.Level) + " " + t.Heading + "\n\n" + t.Markdown(records))
	}
	return b.String()
}
