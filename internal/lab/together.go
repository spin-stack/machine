// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// restoreTogether is whether a checkpoint's channels (multifd, spin-machine restore --channels)
// keep their gain when a host restores several workspaces at once. One restore into a QEMU
// started ahead reached the first login at 150 ms with 1 channel, 103 with 8 and 93 with 16 (lab
// 44789688221, spin's shape and login), but each channel is a thread of QEMU's, and 8 restores of
// 16 channels are 128 threads on a host of 16 vCPUs.
//
// Each row is a number of restores at once and a number of channels. A measurement makes that
// many checkpoints, each of its own overlay, booted cold, logged into and saved with the row's
// channels; every checkpoint is made before any restore, for Probe.Phases' reason. Then it starts
// a QEMU ahead for each with --restore, waits for every one to settle, evicts every state from the
// page cache - restore-first's evicted row, the goal's number: a checkpoint kept for later is read
// from the disk - and lets all the restores go at once, each `spin-machine restore` and its login
// in a goroutine of its own, held until the last of them is ready. Each restore's first login is
// timed from its own start, as prestart's restored-into-prestarted row times one.
type restoreTogether struct{}

func init() { register(restoreTogether{}) }

func (restoreTogether) Name() string { return "restore-together" }
func (restoreTogether) About() string {
	return "ms from `spin-machine restore` into a QEMU started ahead to the first SSH login, with the row's number of restores let go at once on one host"
}

func (restoreTogether) Options() []Option {
	return append(shape,
		Option{"together", "1 4 8", "the numbers of restores let go at once, a row each, space-separated"},
		Option{channelsOption.Name, "1 8 16", "spin-machine save and restore --channels, space-separated, each the rows over again; none is the rows as the tree's spin-machine runs them"})
}

// togetherRow is the row of n restores at once, before withChannels names its count.
func togetherRow(n string) string { return n + " at once" }

func (restoreTogether) Rows(o Options) []string {
	var rows []string
	for _, n := range o.Fields("together") {
		rows = append(rows, togetherRow(n))
	}
	return withChannels(o, rows...)
}

func (restoreTogether) Phases() int { return 2 }

func (restoreTogether) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "restore"},
		Need{"save", "to"}, Need{"restore", "disk"}), append(channelsNeeds(o), plugNeeds(o)...)...)
}

func (restoreTogether) Metrics() []Metric {
	return []Metric{
		{"login", "ms", "the median first login of the restores let go together (each restore's is in the detail, pooled below)"},
		{"slowest", "ms", "the slowest first login of the restores let go together"},
		{"running", "ms", "the last `spin-machine restore` to return: every guest running"},
	}
}

// rowTogether splits a row into how many restores it lets go at once and spin-machine's
// --channels flag for them.
func rowTogether(row string) (int, []string, error) {
	way, channels, err := rowChannels(row)
	if err != nil {
		return 0, nil, err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(way, togetherRow("")))
	if err != nil || n < 1 || !strings.HasSuffix(way, togetherRow("")) {
		return 0, nil, fmt.Errorf("row %q: %q is not a number of restores at once", row, way)
	}
	return n, channels, nil
}

// mutate-lane: TestEveryProbeMeasuresATree
func (restoreTogether) Measure(_ context.Context, m *Measurement) error {
	n, channels, err := rowTogether(m.Row)
	if err != nil {
		return err
	}
	if m.Phase == 0 {
		cs := make([]*checkpoint, n)
		for i := range cs {
			c, err := saveCold(m, fmt.Sprintf("w%d-", i), channels...)
			if err != nil {
				return err
			}
			cs[i] = c.checkpoint
		}
		m.State = cs
		m.Logf("%d checkpoints saved", n)
		return nil
	}
	return restoreAll(m, m.State.([]*checkpoint), channels...)
}

// restoreAll starts a QEMU ahead for each of cs, then restores them all at once with restore's
// --channels when the row names them, and records their first logins.
// mutate-lane: TestEveryProbeMeasuresATree
func restoreAll(m *Measurement, cs []*checkpoint, channels ...string) error {
	machines := make([]*Machine, len(cs))
	qmps := make([]string, len(cs))
	defer func() { // restored guests left running would be what the restores after them measured
		for _, mc := range machines {
			if mc != nil {
				mc.Stop()
			}
		}
	}()
	for i, c := range cs {
		qmps[i] = filepath.Join(m.Socks, fmt.Sprintf("w%d-ahead.qmp", i))
		mc, err := m.Launch(nil, m.BootArgs(c.overlay, c.bootFlags(m, qmps[i], "--restore")...)...)
		if err != nil {
			return err
		}
		machines[i] = mc
	}
	for _, qmp := range qmps {
		if err := settled(qmp, "inmigrate"); err != nil {
			return err
		}
	}
	// After every QEMU's start, which reads the release's files and not the states.
	for _, c := range cs {
		if err := evict(c.state); err != nil {
			return err
		}
	}

	type restored struct {
		running, login time.Duration
		err            error
	}
	out := make([]restored, len(cs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Go(func() {
			<-start
			t0 := time.Now()
			args := append([]string{"restore", "--release", m.Tree.Dir, "--qmp", qmps[i], "--disk", c.overlay, "--from", c.state}, channels...)
			if err := m.SpinMachine(args...); err != nil {
				out[i].err = fmt.Errorf("restore %d of %d: %w", i+1, len(cs), err)
				return
			}
			out[i].running = time.Since(t0)
			login, err := m.Key.LoggedIn(machines[i], c.cid, t0)
			if err != nil {
				out[i].err = fmt.Errorf("restore %d of %d: %w", i+1, len(cs), err)
				return
			}
			out[i].login = login
		})
	}
	close(start)
	wg.Wait()

	var errs []error
	var logins, running []float64
	for i, r := range out {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
		logins, running = append(logins, ms(r.login)), append(running, ms(r.running))
		m.Detail("login\x00"+strconv.Itoa(i), ms(r.login))
	}
	// A restore lost loses the measurement: the others ran beside one that did not, which is not
	// the row's load.
	if err := errors.Join(errs...); err != nil {
		var miss *MissError
		if errors.As(err, &miss) {
			return Miss("%v", err)
		}
		return err
	}
	median, _ := Pct(logins, .5)
	m.Record("login", median)
	m.Record("slowest", slices.Max(logins))
	m.Record("running", slices.Max(running))
	m.Logf("%d restored together: login median %.0f ms, slowest %.0f", len(cs), median, slices.Max(logins))
	return nil
}

// Detail is each row's first logins pooled, every restore of every repetition one sample, and
// each row's p50 against the same number at once with the first count of channels. The restores
// let go together share a host and a moment, so they are not independent samples: the interval
// is narrower than the truth, and a Δ near its edge wants a run of its own.
func (restoreTogether) Detail(r *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  first logins pooled, each restore one sample\n")
	for _, t := range r.Trees {
		for _, row := range r.Rows {
			vs := pooledLogins(r, t.Name, row)
			p50, ok := Pct(vs, .5)
			if !ok {
				fmt.Fprintf(&b, "  %-32s -\n", r.label(t.Name, row))
				continue
			}
			p95, _ := Pct(vs, .95)
			line := fmt.Sprintf("  %-32s %s/%s over %d", r.label(t.Name, row), num(p50), num(p95), len(vs))
			if base := firstAtOnce(r.Rows, row); base != row {
				line += fmt.Sprintf(", Δ p50 against %q %s", base, against(pooledLogins(r, t.Name, base), vs))
			}
			fmt.Fprintln(&b, line)
		}
	}
	return b.String()
}

// pooledLogins are every restore's first login in tree's row, over the measurements not lost.
func pooledLogins(r *Result, tree, row string) []float64 {
	var vs []float64
	for _, s := range r.Samples {
		if s.Tree != tree || s.Row != row || s.Missed != "" {
			continue
		}
		for k, v := range s.Detail {
			if strings.HasPrefix(k, "login\x00") {
				vs = append(vs, v)
			}
		}
	}
	return vs
}

// firstAtOnce is the first of rows with as many restores at once as row: the same load with the
// first count of channels the run compares.
func firstAtOnce(rows []string, row string) string {
	way, _, _ := strings.Cut(row, ", ")
	for _, r := range rows {
		if w, _, _ := strings.Cut(r, ", "); w == way {
			return r
		}
	}
	return row
}
