// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Log     io.Writer
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
	for _, t := range e.Trees {
		if err := CanRun(e.Probe, opts, t); err != nil {
			return nil, fmt.Errorf("tree %s cannot run %s: %w", t.Name(), e.Probe.Name(), err)
		}
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

	var ms []*Measurement
	defer func() {
		for _, m := range ms {
			for i := len(m.cleanup) - 1; i >= 0; i-- {
				m.cleanup[i]()
			}
		}
	}()
	for rep := range e.Reps {
		for _, t := range e.Trees {
			for _, row := range r.Rows {
				dir, err := os.MkdirTemp(e.Scratch, "m")
				if err != nil {
					return nil, fmt.Errorf("making a measurement's directory under %s: %w", e.Scratch, err)
				}
				socks, err := shortDir()
				if err != nil {
					return nil, err
				}
				m := &Measurement{Tree: t, Row: row, Rep: rep, Options: opts, Dir: dir, Socks: socks, Key: key,
					values: map[string]float64{}, detail: map[string]float64{}, log: e.Log}
				m.Cleanup(removeAll(dir))
				m.Cleanup(removeAll(socks))
				ms = append(ms, m)
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
				// Every measurement of the tree still to come is lost with this one, said once.
				for _, o := range ms {
					if o.Tree == m.Tree && o.missed == "" {
						o.missed = "refused: " + refused.Why
					}
				}
				m.Logf("refused, and its measurements still to come with it: %s", refused.Why)
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
