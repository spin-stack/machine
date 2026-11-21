// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Result is what an experiment recorded, written as result.json: every sample as it was taken,
// so that a summary read later is the samples' and not the table's.
type Result struct {
	Probe    string     `json:"probe"`
	About    string     `json:"about"`
	Options  Options    `json:"options"`
	Reps     int        `json:"reps"`
	Rows     []string   `json:"rows"`
	Metrics  []Metric   `json:"metrics"`
	Trees    []Manifest `json:"trees"`
	Host     Host       `json:"host"`
	Started  time.Time  `json:"started"`
	Finished time.Time  `json:"finished"`
	Samples  []Sample   `json:"samples"`
}

// Sample is one measurement: one repetition of one row of one tree.
type Sample struct {
	Tree   string             `json:"tree"`
	Row    string             `json:"row"`
	Rep    int                `json:"rep"`
	Values map[string]float64 `json:"values"`
	Detail map[string]float64 `json:"detail,omitempty"`
	Missed string             `json:"missed,omitempty"`
}

// ReadResult reads a result.json.
func ReadResult(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &r, nil
}

// Write writes r as JSON at path.
func (r *Result) Write(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Values are metric's values over the samples of tree and row that were not lost.
func (r *Result) Values(tree, row, metric string) []float64 {
	var vs []float64
	for _, s := range r.Samples {
		if s.Tree != tree || s.Row != row || s.Missed != "" {
			continue
		}
		if v, ok := s.Values[metric]; ok {
			vs = append(vs, v)
		}
	}
	return vs
}

// Missed is how many measurements of tree and row were lost.
func (r *Result) Missed(tree, row string) int {
	n := 0
	for _, s := range r.Samples {
		if s.Tree == tree && s.Row == row && s.Missed != "" {
			n++
		}
	}
	return n
}

// label is a tree and row as a table names them.
func (r *Result) label(tree, row string) string {
	if len(r.Rows) == 1 {
		return tree
	}
	return tree + ", " + row
}

// Table is r's summary: the shape it booted, a line per tree and row, p50/p95 of each metric, how
// many were lost, and under it each tree's p50s against the first tree's with their intervals.
func (r *Result) Table() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", r.Probe, r.About)
	host := r.Host.Name
	if r.Host.Instance != "" {
		host += " on " + r.Host.Instance
	}
	opts := ""
	if o := optionsLine(r.Options); o != "" {
		opts = o + "; "
	}
	fmt.Fprintf(&b, "p50/p95 over %d of each, interleaved, in %s; %s%s (%s, %d CPUs, nested %v), scratch on %s\n\n",
		r.Reps, shapeOf(r.Options), opts, host, r.Host.CPU, r.Host.CPUs, r.Host.Nested, r.Host.ScratchDevice)
	width := 10
	for _, t := range r.Trees {
		for _, row := range r.Rows {
			width = max(width, len(r.label(t.Name, row)))
		}
	}
	cols := make([]int, len(r.Metrics))
	fmt.Fprintf(&b, "  %-*s", width, "")
	for i, m := range r.Metrics {
		h := strings.ToUpper(m.Name) + " " + m.Unit
		cols[i] = max(13, len(h))
		fmt.Fprintf(&b, " %*s", cols[i], h)
	}
	fmt.Fprintf(&b, " %6s\n", "LOST")
	for _, t := range r.Trees {
		for _, row := range r.Rows {
			fmt.Fprintf(&b, "  %-*s", width, r.label(t.Name, row))
			for i, m := range r.Metrics {
				vs := r.Values(t.Name, row, m.Name)
				p50, ok := Percentile(vs, .5)
				p95, _ := Percentile(vs, .95)
				cell := "-"
				if ok {
					cell = num(p50) + "/" + num(p95)
				}
				fmt.Fprintf(&b, " %*s", cols[i], cell)
			}
			fmt.Fprintf(&b, " %6d\n", r.Missed(t.Name, row))
		}
	}
	if len(r.Trees) > 1 {
		first := r.Trees[0].Name
		fmt.Fprintf(&b, "\n  Δ p50 against %s [95%% interval]: a change only when the interval excludes 0\n", first)
		var metric int
		for _, m := range r.Metrics {
			metric = max(metric, len(m.Name+" "+m.Unit))
		}
		for _, t := range r.Trees[1:] {
			for _, row := range r.Rows {
				for _, m := range r.Metrics {
					fmt.Fprintf(&b, "  %-*s %-*s %s\n", width, r.label(t.Name, row), metric, m.Name+" "+m.Unit,
						against(r.Values(first, row, m.Name), r.Values(t.Name, row, m.Name)))
				}
			}
		}
	}
	b.WriteString("\n  trees:\n")
	for _, t := range r.Trees {
		fmt.Fprintf(&b, "    %s:", t.Name)
		for _, name := range []string{"kernel", "qemu", "image", "spin-machine"} {
			fmt.Fprintf(&b, " %s %s;", name, t.Parts[name].Source)
		}
		for _, e := range t.Edits {
			fmt.Fprintf(&b, " edit %s;", e.Name)
		}
		if t.Append != "" {
			fmt.Fprintf(&b, " append %q;", t.Append)
		}
		if len(t.Boot) > 0 {
			fmt.Fprintf(&b, " boot %q;", strings.Join(t.Boot, " "))
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

// against is b's p50 against a's as a cell: the Δ and its interval, the Δ alone when a side has a
// single sample, or a dash when one has none.
func against(a, b []float64) string {
	if d, ok := DeltaP50(a, b); ok {
		return d.String()
	}
	pa, okA := Percentile(a, .5)
	pb, okB := Percentile(b, .5)
	if !okA || !okB {
		return "-"
	}
	return fmt.Sprintf("%+.1f, no interval from one boot", pb-pa)
}

// optionsLine is o's options but the shape's, which the table says first.
func optionsLine(o Options) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(o)) {
		if o[k] != "" && !slices.ContainsFunc(shape, func(s Option) bool { return s.Name == k }) {
			parts = append(parts, k+"="+o[k])
		}
	}
	return strings.Join(parts, " ")
}

// num is a value as the table prints it: whole numbers whole, the rest to one place.
func num(v float64) string {
	if v == float64(int64(v)) || v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func itoa(n int) string { return strconv.Itoa(n) }
