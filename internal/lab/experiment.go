// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Experiment is a probe run over trees, repetitions of each, interleaved.
type Experiment struct {
	Probe   Probe
	Trees   []*Tree
	Reps    int
	Options Options
	// Scratch is where overlays and states go: a disk, never a tmpfs that holds them in RAM.
	Scratch string
	// Keep is where a probe keeps files a person reads beside the result, such as a chart of a
	// boot: the result's own directory.
	Keep string
	Log  io.Writer
}

// Run measures every tree, interleaved - repetition after repetition, each tree and row in turn,
// the only comparison a host whose speed drifts from one minute to the next leaves honest - and
// returns what it recorded. A lost measurement is in the result as a miss; an error is an
// experiment that could not go on.
func (e Experiment) Run(ctx context.Context) (*Result, error) {
	if len(e.Trees) == 0 {
		return nil, errNoTrees
	}
	if e.Reps < 1 {
		return nil, fmt.Errorf("an experiment repeats each measurement at least once, not %d times", e.Reps)
	}
	opts, err := settle(e.Probe, e.Options)
	if err != nil {
		return nil, err
	}
	treeOpts := map[*Tree]Options{}
	for _, t := range e.Trees {
		o, err := optionsOf(e.Probe, opts, t)
		if err != nil {
			return nil, err
		}
		if err := CanRun(e.Probe, o, t); err != nil {
			return nil, fmt.Errorf("tree %s cannot run %s: %w", t.Name(), e.Probe.Name(), err)
		}
		treeOpts[t] = o
	}
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	switch opts["login"] {
	case "", "lane":
	case "spin":
		key.SpinsPath = true
	default:
		return nil, fmt.Errorf("%s's option login is lane or spin, not %q", e.Probe.Name(), opts["login"])
	}
	r := &Result{Probe: e.Probe.Name(), About: e.Probe.About(), Options: opts, Reps: e.Reps,
		Rows: e.Probe.Rows(opts), Metrics: e.Probe.Metrics(), Host: ReadHost(e.Scratch), Started: time.Now().UTC()}
	for _, t := range e.Trees {
		r.Trees = append(r.Trees, t.Manifest)
	}

	// made is every measurement, cleaned up at the end; ms are the ones the phases measure.
	var made, ms []*Measurement
	defer func() {
		for _, m := range made {
			for i := len(m.cleanup) - 1; i >= 0; i-- {
				m.cleanup[i]()
			}
		}
	}()
	measurement := func(t *Tree, row string, rep int) (*Measurement, error) {
		dir, err := os.MkdirTemp(e.Scratch, "m")
		if err != nil {
			return nil, fmt.Errorf("making a measurement's directory under %s: %w", e.Scratch, err)
		}
		socks, err := shortDir()
		if err != nil {
			return nil, err
		}
		m := &Measurement{Tree: t, Row: row, Rep: rep, Options: treeOpts[t], Dir: dir, Socks: socks, Keep: e.Keep, Key: key,
			values: map[string]float64{}, detail: map[string]float64{}, log: e.Log}
		m.Cleanup(removeAll(dir))
		m.Cleanup(removeAll(socks))
		made = append(made, m)
		return m, nil
	}
	for rep := range e.Reps {
		for _, t := range e.Trees {
			for _, row := range r.Rows {
				m, err := measurement(t, row, rep)
				if err != nil {
					return nil, err
				}
				ms = append(ms, m)
			}
		}
	}
	// Every measurement of a tree still to come is lost with a refusal of it, said once.
	refuse := func(m *Measurement, why string) {
		for _, o := range ms {
			if o.Tree == m.Tree && o.missed == "" {
				o.missed = "refused: " + why
			}
		}
		m.Logf("refused, and its measurements still to come with it: %s", why)
	}
	// A probe that restores hands every restore its tree's record (Measurement.Target), read once
	// for each tree before anything is measured, as spin reads its host's when its runner starts.
	if slices.ContainsFunc(e.Probe.Needs(opts), func(n Need) bool { return n.Command == "restore" }) {
		for _, t := range e.Trees {
			m, err := measurement(t, "before measuring", 0)
			if err != nil {
				return nil, err
			}
			target, err := readTarget(m)
			var refused *RefusedError
			switch {
			case errors.As(err, &refused):
				refuse(m, refused.Why)
			case err != nil:
				return nil, fmt.Errorf("reading tree %s's record: %w", t.Name(), err)
			case target == "":
				m.Logf("its spin-machine cannot print the machine's record or take one: each restore asks the machine it restores into")
			default:
				m.Logf("the machine's record read, and handed to every restore")
			}
			for _, o := range ms {
				if o.Tree == t {
					o.Target = target
				}
			}
		}
	}
	for phase := range e.Probe.Phases() {
		for _, m := range ms {
			if m.missed != "" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			m.Phase = phase
			err := e.Probe.Measure(ctx, m)
			var miss *MissError
			var refused *RefusedError
			switch {
			case errors.As(err, &miss):
				m.missed = miss.Why
				m.Logf("lost: %s", miss.Why)
			case errors.As(err, &refused):
				refuse(m, refused.Why)
			case err != nil:
				return nil, fmt.Errorf("%s, %s, repetition %d: %w", m.Tree.Name(), m.Row, m.Rep+1, err)
			}
		}
	}
	for _, m := range ms {
		r.Samples = append(r.Samples, Sample{Tree: m.Tree.Name(), Row: m.Row, Rep: m.Rep + 1,
			Values: m.values, Detail: m.detail, Missed: m.missed})
	}
	r.Finished = time.Now().UTC()
	return r, nil
}

// optionsOf is the options tree t boots with: the run's, with the tree's own shape options in their
// place. Only the shape: the rows and the login are the run's, one for every tree, since a table
// compares the trees row by row and one key logs into every machine.
func optionsOf(p Probe, run Options, t *Tree) (Options, error) {
	if len(t.Manifest.Options) == 0 {
		return run, nil
	}
	o := maps.Clone(run)
	for _, name := range slices.Sorted(maps.Keys(t.Manifest.Options)) {
		if name == "login" || !slices.ContainsFunc(shape, func(s Option) bool { return s.Name == name }) {
			return nil, fmt.Errorf("tree %s: %s is not a shape option a tree can set; a tree sets %s", t.Name(), name, treeOptionNames())
		}
		o[name] = t.Manifest.Options[name]
	}
	o, err := settle(p, o)
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", t.Name(), err)
	}
	return o, nil
}

// treeOptionNames are the options a tree can set: the shape's, but the login.
func treeOptionNames() string {
	var names []string
	for _, s := range shape {
		if s.Name != "login" {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, ", ")
}

// Scratch is where an experiment's overlays and states go, and the device that is: TMPDIR when it
// is on a disk, else the host's instance store on a bench runner, else dir.
func Scratch(dir string) (string, error) {
	if t := os.Getenv("TMPDIR"); t != "" && !isTmpfs(t) {
		return t, nil
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" { // mutate-exempt: off a runner with an instance store, runnerScratch is this same directory
		return runnerScratch(dir)
	}
	return labScratch(dir)
}

// runnerScratch is a bench runner's instance store, or the lab's own directory where it has none.
//
// mutate-lane: TestTheScratchOnABenchRunnerIsItsInstanceStore
func runnerScratch(dir string) (string, error) {
	if d, err := instanceStore(); err != nil || d != "" {
		return d, err
	}
	return labScratch(dir)
}

// labScratch is dir's scratch directory, made.
func labScratch(dir string) (string, error) {
	d := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", fmt.Errorf("making %s: %w", d, err)
	}
	return d, nil
}
