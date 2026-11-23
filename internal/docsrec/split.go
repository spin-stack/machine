// SPDX-License-Identifier: Apache-2.0

package docsrec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Kinds says whether each number of this repository is an issue or a pull request.
type Kinds map[int]string

// localRef is a #N of this repository: not owner/repo#N, and not after the name of another
// project, as in "go-tools #131" or "upstream #42858".
var localRef = regexp.MustCompile(`(?:^|([A-Za-z-]+) |[^\w/#\s])#(\d+)\b`)

var otherProjects = []string{"upstream", "go-tools", "spin", "Firecracker", "linux-loader"}

// refs are the issues, pull requests and runs text cites.
func (k Kinds) refs(text string) (issues, prs, runs []int) {
	for _, m := range localRef.FindAllStringSubmatch(text, -1) {
		if slices.Contains(otherProjects, m[1]) {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		switch k[n] {
		case "issue":
			issues = append(issues, n)
		case "pr":
			prs = append(prs, n)
		}
	}
	for _, id := range runID.FindAllString(text, -1) {
		n, _ := strconv.Atoi(id)
		runs = append(runs, n)
	}
	for _, s := range []*[]int{&issues, &prs, &runs} {
		slices.Sort(*s)
		*s = slices.Compact(*s)
	}
	return issues, prs, runs
}

// first is the first issue or pull request text cites, or 0.
func (k Kinds) first(text string) int {
	for _, m := range localRef.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[2])
		if !slices.Contains(otherProjects, m[1]) && k[n] != "" {
			return n
		}
	}
	return 0
}

var (
	parenthetical = regexp.MustCompile(`\([^()]*\)`)
	notSlug       = regexp.MustCompile(`[^a-z0-9]+`)
)

// slugOf is a file name's slug for text: its first words outside parentheses.
func slugOf(text string, words int) string {
	text = strings.NewReplacer("'", "", "’", "").Replace(text)
	s := strings.ToLower(parenthetical.ReplaceAllString(text, ""))
	if strings.Trim(notSlug.ReplaceAllString(s, ""), "-") == "" {
		s = strings.ToLower(text)
	}
	parts := strings.Split(strings.Trim(notSlug.ReplaceAllString(s, "-"), "-"), "-")
	if len(parts) > words {
		parts = parts[:words]
	}
	return strings.Join(parts, "-")
}

// unique makes name unique among taken by a numbered suffix.
func unique(taken map[string]bool, name, ext string) string {
	n := name + ext
	for i := 2; taken[n]; i++ {
		n = fmt.Sprintf("%s-%d%s", name, i, ext)
	}
	taken[n] = true
	return n
}

var (
	entryHead = regexp.MustCompile(`^\*\*(\d\d)-(\d\d)(/[\d-]+)? · (.+?) · (.+?)\.\*\*( .*)?$`)
	weekLine  = regexp.MustCompile(`^### Week \d+: (\d{4})-(\d\d)-`)
)

// Split turns docs/log.md and the tables of docs/boot-time.md into the files that replace them,
// keyed by their paths in the repository. It is the migration of spin-stack/machine#709, run once.
//
// The log's entries carry their month and day only. Each takes its year from the week heading it
// is under, which names it, a year later when its month comes before the heading's.
func Split(log, bootTime string, k Kinds) (map[string][]byte, error) {
	files := map[string][]byte{}
	if err := k.splitLog(log, files); err != nil {
		return nil, err
	}
	if err := k.splitTables(bootTime, files); err != nil {
		return nil, err
	}
	return files, nil
}

func (k Kinds) splitLog(log string, files map[string][]byte) error {
	lines := strings.Split(log, "\n")
	s1 := slices.Index(lines, "## 1. Chronological log")
	s2 := slices.Index(lines, "## 2. Rework cycles")
	if s1 < 0 || s2 < s1 {
		return errors.New("docs/log.md: no sections 1 and 2")
	}
	files[LogDir+"/"+logHead] = []byte(strings.Join(lines[:s1+3], "\n") + "\n")
	files[LogDir+"/"+logRework] = []byte(strings.Join(lines[s2:], "\n"))

	type raw struct {
		e     Entry
		lines []string
	}
	var entries []*raw
	var year int
	var month time.Month
	for i, l := range lines[s1+3 : s2] {
		if m := weekLine.FindStringSubmatch(l); m != nil {
			year, _ = strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			month = time.Month(mo)
			continue
		}
		m := entryHead.FindStringSubmatch(l)
		if m == nil {
			if len(entries) > 0 {
				entries[len(entries)-1].lines = append(entries[len(entries)-1].lines, l)
			} else if strings.TrimSpace(l) != "" {
				return fmt.Errorf("docs/log.md:%d: text before the first entry", s1+4+i)
			}
			continue
		}
		mo, _ := strconv.Atoi(m[1])
		day, _ := strconv.Atoi(m[2])
		y := year
		if time.Month(mo) < month {
			y++
		}
		e := Entry{
			Date:  time.Date(y, time.Month(mo), day, 0, 0, 0, 0, time.UTC).Format(dateLayout),
			Area:  m[4],
			Title: m[5],
		}
		if m[3] != "" {
			e.Days = m[1] + "-" + m[2] + m[3]
		}
		first := strings.TrimPrefix(m[6], " ")
		entries = append(entries, &raw{e: e, lines: []string{first}})
	}
	taken := map[string]bool{}
	orders := map[string]int{}
	for _, r := range entries {
		e := r.e
		e.Body = strings.TrimRight(strings.Join(r.lines, "\n"), " \n")
		orders[e.Date] += 10
		e.Order = orders[e.Date]
		e.Issues, e.PRs, e.Runs = k.refs(e.Title + "\n" + e.Body)
		var front bytes.Buffer
		enc := yaml.NewEncoder(&front)
		enc.SetIndent(2)
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("writing the entry %s %s: %w", e.Date, e.Title, err)
		}
		name := unique(taken, e.Date+"-"+slugOf(e.Title, 8), ".md")
		files[LogDir+"/"+name] = []byte("---\n" + front.String() + "---\n" + e.Body + "\n")
	}
	return nil
}

func (k Kinds) splitTables(bootTime string, files map[string][]byte) error {
	lines := strings.Split(bootTime, "\n")
	taken := map[string]bool{}
	for _, t := range Tables {
		at := slices.Index(lines, strings.Repeat("#", t.Level)+" "+t.Heading)
		if at < 0 {
			return fmt.Errorf("docs/boot-time.md: no heading %q", t.Heading)
		}
		header := strings.SplitN(t.Markdown(nil), "\n", 2)[0]
		for at < len(lines) && lines[at] != header {
			at++
		}
		if at == len(lines) {
			return fmt.Errorf("docs/boot-time.md: %q has no table", t.Heading)
		}
		for i, l := range lines[at+2:] {
			if !strings.HasPrefix(l, "|") {
				break
			}
			cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(l, "| "), " |"), " | ")
			if len(cells) != len(t.columns) {
				return fmt.Errorf("docs/boot-time.md:%d: %d cells, %q has %d columns", at+3+i, len(cells), t.Heading, len(t.columns))
			}
			byField := map[string]string{}
			for j, c := range t.columns {
				byField[c.field] = cells[j]
			}
			r := Record{
				Decision: t.Decision, Section: t.Section, Order: 10 * (i + 1),
				Change: byField["change"], Where: byField["where"], Result: byField["result"],
				Evidence: byField["evidence"], Shape: byField["shape"], Source: byField["source"],
				Number: byField["number"], Phase: byField["phase"], DecidedBy: byField["decided_by"],
				State: byField["state"],
			}
			if d := byField["date"]; d != "" {
				// The tables began with the repository on 2026-09-07 and have not seen a new year.
				r.Date = "2026-" + d
			}
			r.Issues, r.PRs, r.Runs = k.refs(l)
			name := slugOf(r.Change, 6)
			if n := k.first(r.Change); n > 0 {
				name = strconv.Itoa(n) + "-" + name
			}
			var data bytes.Buffer
			enc := json.NewEncoder(&data)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(r); err != nil {
				return fmt.Errorf("writing the record %q: %w", r.Change, err)
			}
			files[ExperimentsDir+"/"+unique(taken, name, ".json")] = data.Bytes()
		}
	}
	return nil
}
