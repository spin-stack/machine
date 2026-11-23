// SPDX-License-Identifier: Apache-2.0

package docsrec

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// The log's two hand-written files. README.md is the page's head, which GitHub also shows under the
// directory's listing; rework.md is section 2, read as a narrative.
const (
	logHead   = "README.md"
	logRework = "rework.md"
)

// Entry is one dated entry of the log, docs/log/<date>-<slug>.md: YAML front matter, then the
// prose. Rendered, it is one paragraph or more opening with **MM-DD · area · title.**
type Entry struct {
	// Date is the entry's day, YYYY-MM-DD, as its file name starts.
	Date string `yaml:"date"`
	// Days is the head's day when the entry spans more than one, such as 09-29/30. It starts with
	// Date's month and day.
	Days string `yaml:"days,omitempty"`
	// Order places the entry among its day's entries, lowest first; entries with the same order
	// follow their file names. An entry may say "the entry above".
	Order int `yaml:"order"`
	// Area is the part of the machine the entry is about: kernel, qemu, image, lab, machine, ...
	Area string `yaml:"area"`
	// Title is the head after the area, without its closing period.
	Title  string `yaml:"title"`
	Issues []int  `yaml:"issues,flow,omitempty"`
	PRs    []int  `yaml:"prs,flow,omitempty"`
	Runs   []int  `yaml:"runs,flow,omitempty"`

	// Body is the prose after the head: on the head's line, or from the next when it starts with a
	// newline.
	Body string `yaml:"-"`
	// File is the entry's file name.
	File string `yaml:"-"`

	day time.Time
}

// head is the entry's bold opening.
func (e Entry) head() string {
	day := e.Days
	if day == "" {
		day = e.day.Format("01-02")
	}
	return "**" + day + " · " + e.Area + " · " + e.Title + ".**"
}

// markdown is the entry as the log shows it.
func (e Entry) markdown() string {
	if strings.HasPrefix(e.Body, "\n") {
		return e.head() + e.Body
	}
	return e.head() + " " + e.Body
}

// ParseEntry reads one entry, named name, and refuses one a field of which is missing or does not
// agree with the file name or the prose.
func ParseEntry(name string, data []byte) (Entry, error) {
	e, err := parseEntry(name, data)
	if err != nil {
		return Entry{}, fmt.Errorf("%s: %w", name, err)
	}
	return e, nil
}

func parseEntry(name string, data []byte) (Entry, error) {
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return Entry{}, errors.New("does not open with front matter (---)")
	}
	front, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Entry{}, errors.New("its front matter is not closed (---)")
	}
	var e Entry
	dec := yaml.NewDecoder(bytes.NewReader(front))
	dec.KnownFields(true)
	if err := dec.Decode(&e); err != nil {
		return Entry{}, fmt.Errorf("front matter: %w", err)
	}
	e.File = name
	e.Body = strings.TrimRight(string(body), " \n")
	day, err := parseDate(e.Date)
	if err != nil {
		return Entry{}, err
	}
	e.day = day
	base, ok := strings.CutSuffix(name, ".md")
	if !ok || !strings.HasPrefix(base, e.Date+"-") || !slug.MatchString(strings.TrimPrefix(base, e.Date+"-")) {
		return Entry{}, fmt.Errorf("the file is not named %s-<slug>.md, a slug of a-z, 0-9 and -", e.Date)
	}
	if e.Days != "" && !strings.HasPrefix(e.Days, day.Format("01-02")+"/") {
		return Entry{}, fmt.Errorf("days %q does not start with the date's %s/", e.Days, day.Format("01-02"))
	}
	if e.Order < 1 {
		return Entry{}, errors.New("order: required, the entry's place among its day's, from 1")
	}
	for _, f := range []struct{ name, v string }{{"area", e.Area}, {"title", e.Title}, {"the prose", strings.TrimSpace(e.Body)}} {
		if f.v == "" {
			return Entry{}, fmt.Errorf("%s: required", f.name)
		}
	}
	if err := checkRefs(e.Title+"\n"+e.Body, e.Issues, e.PRs, e.Runs); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Log is the log as its files hold it.
type Log struct {
	// Head and Rework are README.md and rework.md as written.
	Head, Rework string
	// Entries are in the log's order.
	Entries []Entry
}

// ReadLog reads the log's directory. Every file in it is the head, section 2 or an entry; anything
// else is refused rather than left out of the page.
func ReadLog(dir string) (Log, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return Log{}, fmt.Errorf("reading the log: %w", err)
	}
	var l Log
	for _, f := range files {
		path := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return Log{}, fmt.Errorf("reading the log: %w", err)
		}
		switch f.Name() {
		case logHead:
			l.Head = string(data)
		case logRework:
			l.Rework = string(data)
		default:
			e, err := ParseEntry(f.Name(), data)
			if err != nil {
				return Log{}, fmt.Errorf("reading the log: %s: %w", dir, err)
			}
			l.Entries = append(l.Entries, e)
		}
	}
	if l.Head == "" || l.Rework == "" {
		return Log{}, fmt.Errorf("reading the log: %s needs %s and %s", dir, logHead, logRework)
	}
	slices.SortFunc(l.Entries, func(a, b Entry) int {
		return cmp.Or(a.day.Compare(b.day), cmp.Compare(a.Order, b.Order), strings.Compare(a.File, b.File))
	})
	return l, nil
}

// The log's weeks. They ran Monday to Sunday from the first commit, and the log was first written
// on 2026-11-13, which ends week 10; from then on a week runs Saturday to Friday.
var (
	firstWeek = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	weeksMove = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
)

// week is the number of the log's week that holds day, and its first and last days.
func week(day time.Time) (n int, first, last time.Time) {
	days := func(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }
	if day.Before(weeksMove) {
		k := days(firstWeek, day) / 7
		first = firstWeek.AddDate(0, 0, 7*k)
		last = first.AddDate(0, 0, 6)
		if !last.Before(weeksMove) {
			last = weeksMove.AddDate(0, 0, -1)
		}
		return k + 1, first, last
	}
	before := (days(firstWeek, weeksMove) + 6) / 7
	k := days(weeksMove, day) / 7
	first = weeksMove.AddDate(0, 0, 7*k)
	return before + 1 + k, first, first.AddDate(0, 0, 6)
}

// weekHeading is a week's heading, its last day without the year when the year is the first's.
func weekHeading(day time.Time) string {
	n, first, last := week(day)
	end := last.Format("01-02")
	if last.Year() != first.Year() {
		end = last.Format(dateLayout)
	}
	return fmt.Sprintf("### Week %d: %s to %s", n, first.Format(dateLayout), end)
}

// Markdown is the log as one page: the head, each week's heading and entries, and section 2.
func (l Log) Markdown() string {
	var b strings.Builder
	b.WriteString(l.Head)
	heading := ""
	for _, e := range l.Entries {
		if h := weekHeading(e.day); h != heading {
			heading = h
			b.WriteString("\n" + h + "\n")
		}
		b.WriteString("\n" + e.markdown() + "\n")
	}
	b.WriteString("\n" + l.Rework)
	return b.String()
}
