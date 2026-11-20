// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
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
//
// A cached row reads every state into the page cache instead of evicting it, restore-first's
// cached row: against the evicted row at the same number at once, it is what reading the states
// from the disk costs the burst. A row under a THP defrag policy sets the host's
// (/sys/kernel/mm/transparent_hugepage/defrag) for its burst alone: QEMU asks for huge pages for
// guest RAM (MADV_HUGEPAGE), and under the kernel's default, madvise, each fault that finds no free
// 2 MiB block compacts and reclaims before it returns. On a laptop (2026-11-21, a lead), eight
// restores at once from the page cache took 440 ms with QEMU's huge pages and 133 without them,
// against ~110 for one alone, with ~3000 failed compactions a burst.
type restoreTogether struct{}

func init() { register(restoreTogether{}) }

func (restoreTogether) Name() string { return "restore-together" }
func (restoreTogether) About() string {
	return "ms from `spin-machine restore` into a QEMU started ahead to the first SSH login, with the row's number of restores let go at once on one host"
}

func (restoreTogether) Options() []Option {
	return append(shape,
		Option{"together", "1 4 8", "the numbers of restores let go at once, a row each, space-separated"},
		Option{"states", "evicted", "where each restore reads its state from: evicted (the disk) or cached (the page cache), space-separated, each the rows over again"},
		Option{"defrag", "", "the host's THP defrag policies to restore under (always, defer, defer+madvise, madvise, never), space-separated, each the rows over again; none leaves the host's as it is"},
		Option{channelsOption.Name, "1 8 16", "spin-machine save and restore --channels, space-separated, each the rows over again; none is the rows as the tree's spin-machine runs them"})
}

// togetherRow is the row of n restores at once, before withChannels names its count.
func togetherRow(n string) string { return n + " at once" }

// cachedRow is what a row restoring from the page cache adds to togetherRow, and defragRow what
// one under a THP defrag policy adds before the policy.
const (
	cachedRow = " from the cache"
	defragRow = " under THP defrag "
)

func (restoreTogether) Rows(o Options) []string {
	defrags := o.Fields("defrag")
	if len(defrags) == 0 {
		defrags = []string{""}
	}
	var rows []string
	for _, defrag := range defrags {
		for _, states := range o.Fields("states") {
			for _, n := range o.Fields("together") {
				row := togetherRow(n)
				switch states {
				case "evicted":
				case "cached":
					row += cachedRow
				default: // its first measurement refuses it (rowTogether), and stops the experiment
					row += " " + states
				}
				if defrag != "" {
					row += defragRow + defrag
				}
				rows = append(rows, row)
			}
		}
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

// burst is a row of restoreTogether: how many restores it lets go at once, whether their states
// are read from the page cache, the host's THP defrag policy during it (none leaves the host's),
// and spin-machine's --channels flag for them.
type burst struct {
	n        int
	cached   bool
	defrag   string
	channels []string
}

// rowTogether reads a row Rows named.
func rowTogether(row string) (burst, error) {
	way, channels, err := rowChannels(row)
	if err != nil {
		return burst{}, err
	}
	way, defrag, _ := strings.Cut(way, defragRow)
	way, cached := strings.CutSuffix(way, cachedRow)
	n, err := strconv.Atoi(strings.TrimSuffix(way, togetherRow("")))
	if err != nil || n < 1 || !strings.HasSuffix(way, togetherRow("")) {
		return burst{}, fmt.Errorf("row %q: %q is not a number of restores at once", row, way)
	}
	return burst{n: n, cached: cached, defrag: defrag, channels: channels}, nil
}

// mutate-lane: TestEveryProbeMeasuresATree
func (restoreTogether) Measure(_ context.Context, m *Measurement) error {
	b, err := rowTogether(m.Row)
	if err != nil {
		return err
	}
	if m.Phase == 0 {
		cs := make([]*checkpoint, b.n)
		for i := range cs {
			c, err := saveCold(m, fmt.Sprintf("w%d-", i), b.channels...)
			if err != nil {
				return err
			}
			cs[i] = c.checkpoint
		}
		m.State = cs
		m.Logf("%d checkpoints saved", b.n)
		return nil
	}
	return restoreAll(m, m.State.([]*checkpoint), b)
}

// restoreAll starts a QEMU ahead for each of cs, then restores them all at once as b says, and
// records their first logins.
// mutate-lane: TestEveryProbeMeasuresATree
func restoreAll(m *Measurement, cs []*checkpoint, b burst) (err error) {
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
		if b.cached {
			if _, _, err := readData(c.state); err != nil {
				return err
			}
		}
	}

	if b.defrag != "" {
		restoreDefrag, setErr := setDefrag(b.defrag)
		if setErr != nil {
			return setErr
		}
		defer func() { err = errors.Join(err, restoreDefrag()) }()
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
			args := append([]string{"restore", "--release", m.Tree.Dir, "--qmp", qmps[i], "--disk", c.overlay, "--from", c.state}, b.channels...)
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
	// What the host waited on while they ran (hostCounters), and the memory it had going in.
	mem, before := memInfo(), readHostCounters()
	close(start)
	wg.Wait()
	for k, v := range readHostCounters().since(before) {
		m.Detail(hostDetail+k, v)
	}
	for k, v := range mem {
		m.Detail(hostDetail+k, v)
	}

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
		m.Detail("running\x00"+strconv.Itoa(i), ms(r.running))
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

// hostDetail prefixes a measurement's host counters in its detail.
const hostDetail = "host\x00"

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
	fmt.Fprintf(&b, "\n  the host during each burst, p50 over the measurements: what the restores waited on\n")
	for _, t := range r.Trees {
		for _, row := range r.Rows {
			by := map[string][]float64{}
			for _, s := range r.Samples {
				if s.Tree != t.Name || s.Row != row || s.Missed != "" {
					continue
				}
				for k, v := range s.Detail {
					if name, ok := strings.CutPrefix(k, hostDetail); ok {
						by[name] = append(by[name], v)
					}
				}
			}
			var parts []string
			for _, name := range slices.Sorted(maps.Keys(by)) {
				p50, _ := Pct(by[name], .5)
				parts = append(parts, fmt.Sprintf("%s %s", name, num(p50)))
			}
			if len(parts) > 0 {
				fmt.Fprintf(&b, "  %-32s %s\n", r.label(t.Name, row), strings.Join(parts, ", "))
			}
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

// thpDefrag is the host's policy for a huge page fault that finds no free 2 MiB block.
const thpDefrag = "/sys/kernel/mm/transparent_hugepage/defrag"

// setDefrag makes policy the host's THP defrag policy, and returns what puts the one before back.
// mutate-lane: TestEveryProbeMeasuresATree
func setDefrag(policy string) (restore func() error, err error) {
	was, err := defragPolicy()
	if err != nil {
		return nil, err
	}
	set := func(p string) error {
		cmd := exec.Command("sudo", "tee", thpDefrag)
		cmd.Stdin = strings.NewReader(p)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("setting %s to %s: %w: %s", thpDefrag, p, err, strings.TrimSpace(string(out)))
		}
		// tee says nothing of a policy the kernel took as another: ask.
		if now, err := defragPolicy(); err != nil || now != p {
			return fmt.Errorf("setting %s to %s: it reads %q (%v)", thpDefrag, p, now, err)
		}
		return nil
	}
	if err := set(policy); err != nil {
		return nil, err
	}
	return func() error { return set(was) }, nil
}

// defragPolicy is the host's THP defrag policy: the one of the file's choices in brackets.
func defragPolicy() (string, error) {
	b, err := os.ReadFile(thpDefrag)
	if err != nil {
		return "", fmt.Errorf("reading the host's THP defrag policy: %w", err)
	}
	for _, f := range strings.Fields(string(b)) {
		if p, ok := strings.CutPrefix(f, "["); ok {
			return strings.TrimSuffix(p, "]"), nil
		}
	}
	return "", fmt.Errorf("%s names no policy in brackets: %q", thpDefrag, b)
}
