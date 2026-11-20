// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
)

// Goal is the first login a cold boot and a restore are measured against (AGENTS.md, "The goal").
const Goal = 100.0

// Ladder is releases measured together: each goal probe over every release that can run it,
// interleaved on one host. It is how the machine's evolution is read - a release's numbers
// against another's taken the same hour on the same instance - where comparing two reports taken
// on different days said v20261119.02 accepted SSH 7.3% later than v20261119.01 and the bench,
// interleaved, said it did not (labs 44583379559, 44600043625).
type Ladder struct {
	// Releases are oldest first, as the table reads.
	Releases []string  `json:"releases"`
	Results  []*Result `json:"results"`
	// Cannot is, by probe, the releases whose spin-machine cannot run it and why: a release from
	// before a probe's commands is a cell that says so, never a row dropped.
	Cannot map[string]map[string]string `json:"cannot,omitempty"`
}

// ladderProbes are what a ladder runs, in order: the two goal numbers and the prestarted ones
// (prestart), and the kernel's time for the diagnostics.
var ladderProbes = []string{"ssh-first", "restore-first", "prestart", "initcalls"}

// RunLadder measures trees, oldest release first, with each ladder probe over those whose
// spin-machine can run it.
func RunLadder(ctx context.Context, trees []*Tree, reps int, scratch string, log io.Writer) (*Ladder, error) {
	l := &Ladder{Cannot: map[string]map[string]string{}}
	for _, t := range trees {
		l.Releases = append(l.Releases, t.Name())
	}
	for _, name := range ladderProbes {
		p := Probes[name]
		opts, err := settle(p, nil)
		if err != nil {
			return nil, err
		}
		var can []*Tree
		for _, t := range trees {
			if err := CanRun(p, opts, t); err != nil {
				if l.Cannot[name] == nil {
					l.Cannot[name] = map[string]string{}
				}
				l.Cannot[name][t.Name()] = err.Error()
				continue
			}
			can = append(can, t)
		}
		if len(can) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(log, "ladder: %s over %d of %d releases\n", name, len(can), len(trees))
		r, err := Experiment{Probe: p, Trees: can, Reps: reps, Scratch: scratch, Log: log}.Run(ctx)
		if err != nil {
			return nil, fmt.Errorf("the ladder's %s: %w", name, err)
		}
		l.Results = append(l.Results, r)
	}
	return l, nil
}

// LadderReleases are the n newest of tags (newest first, as gh lists them) up to and including
// newest - all of them when newest is empty - oldest first.
func LadderReleases(tags []string, newest string, n int) ([]string, error) {
	if newest != "" {
		i := slices.Index(tags, newest)
		if i < 0 {
			return nil, fmt.Errorf("no published release %s among the newest %d", newest, len(tags))
		}
		tags = tags[i:]
	}
	tags = slices.Clone(tags[:min(n, len(tags))])
	slices.Reverse(tags)
	return tags, nil
}

// cell is what the ladder says of one release and one number: what a probe recorded, or why the
// release could not run the probe.
type cell struct {
	probe, row, metric string
}

func (l *Ladder) result(probe string) *Result {
	for _, r := range l.Results {
		if r.Probe == probe {
			return r
		}
	}
	return nil
}

// value is the p50 and p95 of c for release, or why there is none.
func (l *Ladder) value(release string, c cell) (p50, p95 float64, why string) {
	if reason, ok := l.Cannot[c.probe][release]; ok {
		why = reason
		return
	}
	r := l.result(c.probe)
	if r == nil {
		why = "not measured"
		return
	}
	vs := r.Values(release, c.row, c.metric)
	p50, ok := Pct(vs, .5)
	if !ok {
		why = fmt.Sprintf("lost all %d", r.Missed(release, c.row))
		return
	}
	p95, _ = Pct(vs, .95)
	return
}

// host is where the ladder ran, as the first result says.
func (l *Ladder) host() string {
	if len(l.Results) == 0 {
		return "no host"
	}
	h := l.Results[0].Host
	name := cmp.Or(h.Instance, h.Name)
	if h.Nested {
		return name + ", nested"
	}
	return name
}

func (l *Ladder) reps() int {
	if len(l.Results) == 0 {
		return 0
	}
	return l.Results[0].Reps
}

// Write writes the ladder as JSON at path.
func (l *Ladder) Write(path string) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// ReadLadder reads a ladder.json.
func ReadLadder(path string) (*Ladder, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var l Ladder
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &l, nil
}
