// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Probe is one measurement of a tree. The experiment runs it over every tree and repetition,
// interleaved, and writes down what it recorded.
type Probe interface {
	Name() string
	// About is what the probe measures and from when, said once above its table.
	About() string
	Options() []Option
	Metrics() []Metric
	// Rows are the ways one repetition measures a tree: one, or several when the probe compares
	// ways of doing the same thing to it (a state restored evicted, cached, fast).
	Rows(o Options) []string
	// Phases is how many passes the experiment makes. Each pass runs every repetition, tree and
	// row, interleaved, before the next starts: a restore probe makes every state before it
	// restores any, because a cold boot after a restored guest had run was ~1.8x slower on the
	// bench fleet (2026-11-13) and would have measured that.
	Phases() int
	// Measure does one pass of one measurement, recording into m. An error made by Miss loses the
	// measurement and the experiment goes on; any other stops it.
	Measure(ctx context.Context, m *Measurement) error
}

// Detailer is a probe with more to say than its metrics' p50s: the initcalls behind a kernel's
// total, the gaps between its lines.
type Detailer interface {
	Detail(r *Result) string
}

// Metric is one number a probe records for each measurement.
type Metric struct {
	Name  string `json:"name"`
	Unit  string `json:"unit"` // ms, MiB, count
	About string `json:"about,omitempty"`
}

// Option is a setting a probe takes, with its default.
type Option struct {
	Name, Default, About string
}

// Options are the settings an experiment was run with, every probe option present.
type Options map[string]string

// Int is option name as a number; settle checked it is one.
func (o Options) Int(name string) int {
	n, _ := strconv.Atoi(o[name])
	return n
}

// Bool is option name as a boolean; settle checked it is one.
func (o Options) Bool(name string) bool {
	b, _ := strconv.ParseBool(o[name])
	return b
}

// Fields is option name split on spaces: spin-machine boot flags.
func (o Options) Fields(name string) []string { return strings.Fields(o[name]) }

// shape is the machine every probe boots unless told otherwise: what the probes this replaced
// booted, so the numbers compare with theirs.
var shape = []Option{
	{"cpus", "2", "vCPUs"},
	{"memory", "2048", "MiB of memory"},
	{"flags", "", "more spin-machine boot flags for every boot, e.g. the ceilings a workspace boots with: --max-cpus 16 --max-memory 8192 --hotplug-disks 1"},
}

// settle is given with every option p takes filled in, refusing a name p does not take or a value
// that is not what its default is: a typo fails here rather than being ignored.
func settle(p Probe, given Options) (Options, error) {
	out := Options{}
	known := map[string]Option{}
	for _, o := range p.Options() {
		known[o.Name], out[o.Name] = o, o.Default
	}
	for _, name := range slices.Sorted(maps.Keys(given)) {
		o, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("%s takes no option %q; it takes: %s", p.Name(), name, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
		}
		v := given[name]
		if _, err := strconv.Atoi(o.Default); err == nil {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return nil, fmt.Errorf("%s's option %s is a number, not %q", p.Name(), name, v)
			}
		}
		if _, err := strconv.ParseBool(o.Default); err == nil {
			if _, err := strconv.ParseBool(v); err != nil {
				return nil, fmt.Errorf("%s's option %s is true or false, not %q", p.Name(), name, v)
			}
		}
		out[name] = v
	}
	return out, nil
}

// Measurement is one repetition of one row of one tree, carried through every phase.
type Measurement struct {
	Tree    *Tree
	Row     string
	Rep     int
	Phase   int
	Options Options
	// Dir is the measurement's own, on the experiment's scratch disk: overlays and states.
	Dir string
	// Socks is the measurement's own directory for Unix sockets, short enough for sun_path.
	Socks string
	// Key logs into every machine the experiment boots.
	Key *Key
	// State is the probe's, from one phase to the next.
	State any

	values  map[string]float64
	detail  map[string]float64
	missed  string
	cleanup []func()
	log     io.Writer
}

// Record notes metric's value for this measurement.
func (m *Measurement) Record(metric string, v float64) { m.values[metric] = v }

// RecordMs notes a duration in milliseconds.
func (m *Measurement) RecordMs(metric string, d time.Duration) {
	m.Record(metric, float64(d)/float64(time.Millisecond))
}

// Detail notes something a Detailer reads back, keyed as the probe likes.
func (m *Measurement) Detail(key string, v float64) { m.detail[key] = v }

// Cleanup runs f when the experiment is over, whatever happened: a machine left running would be
// what the measurements after it measured.
func (m *Measurement) Cleanup(f func()) { m.cleanup = append(m.cleanup, f) }

// Logf says what happened, on the experiment's log.
func (m *Measurement) Logf(format string, args ...any) {
	_, _ = fmt.Fprintf(m.log, "%s, %s, repetition %d: %s\n", m.Tree.Name(), m.Row, m.Rep+1, fmt.Sprintf(format, args...))
}

// Overlay makes a fresh overlay of the tree at m.Dir/name with the tree's edits and those given,
// and returns its path.
// mutate-lane: TestEveryProbeMeasuresATree
func (m *Measurement) Overlay(name string, edits ...Edit) (string, error) {
	path := m.Dir + "/" + name
	if err := NewOverlay(m.Tree, path); err != nil {
		return "", err
	}
	for _, e := range m.Tree.Manifest.Edits {
		edits = append(edits, e.Edit)
	}
	if len(edits) > 0 {
		if err := EditOverlay(path, edits...); err != nil {
			return "", err
		}
	}
	return path, nil
}

// BootArgs are `spin-machine boot`'s flags for the tree in the experiment's shape, the given
// ones after them.
func (m *Measurement) BootArgs(overlay string, extra ...string) []string {
	args := []string{"boot", "--release", m.Tree.Dir, "--disk", overlay,
		"--memory", m.Options["memory"], "--cpus", m.Options["cpus"]}
	args = append(args, extra...)
	return append(args, m.Options.Fields("flags")...)
}

// MissError is a measurement lost - a machine that never answered - and not an experiment that
// cannot go on: it is counted beside the table.
type MissError struct{ Why string }

func (e *MissError) Error() string { return e.Why }

// Miss is a lost measurement and why.
func Miss(format string, args ...any) error { return &MissError{Why: fmt.Sprintf(format, args...)} }

// Machine is one `spin-machine boot`, in a process group of its own so that QEMU goes with it.
type Machine struct {
	cmd    *exec.Cmd
	done   chan struct{}
	Stderr *bytes.Buffer
}

// Launch starts the tree's spin-machine with args. The machine is killed when m is cleaned up if
// nothing has stopped it.
// mutate-lane: TestEveryProbeMeasuresATree
func (m *Measurement) Launch(stdout io.Writer, args ...string) (*Machine, error) {
	cmd := exec.Command(m.Tree.SpinMachine(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if stdout == nil {
		cmd.Stdout = io.Discard
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launching %s %s: %w", m.Tree.SpinMachine(), strings.Join(args, " "), err)
	}
	mc := &Machine{cmd: cmd, done: make(chan struct{}), Stderr: stderr}
	go func() { _ = cmd.Wait(); close(mc.done) }()
	m.Cleanup(mc.Stop)
	return mc, nil
}

// PID is the machine's process: QEMU's, which spin-machine boot execs in its place.
// mutate-lane: TestEveryProbeMeasuresATree
func (mc *Machine) PID() int { return mc.cmd.Process.Pid }

// rssMiB is VmRSS in a /proc/PID/status, in MiB.
func rssMiB(status string) (float64, bool) {
	for line := range strings.Lines(status) {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) != 2 || f[1] != "kB" {
				return 0, false
			}
			kb, err := strconv.Atoi(f[0])
			return float64(kb) / 1024, err == nil
		}
	}
	return 0, false
}

// Done is closed once the machine's process has exited.
// mutate-lane: TestEveryProbeMeasuresATree
func (mc *Machine) Done() <-chan struct{} { return mc.done }

// Stop kills the machine's process group and waits for it.
// mutate-lane: TestEveryProbeMeasuresATree
func (mc *Machine) Stop() {
	select {
	case <-mc.done:
	default:
		_ = syscall.Kill(-mc.cmd.Process.Pid, syscall.SIGKILL)
		<-mc.done
	}
}

// SpinMachine runs the tree's spin-machine with args to completion.
// mutate-lane: TestEveryProbeMeasuresATree
func (m *Measurement) SpinMachine(args ...string) error {
	return run(m.Tree.SpinMachine(), args...)
}

// Probes are the probes there are, by name.
var Probes = map[string]Probe{}

func register(p Probe) { Probes[p.Name()] = p }

// ProbeNames are the probes' names, sorted.
func ProbeNames() []string { return slices.Sorted(maps.Keys(Probes)) }

func removeAll(dir string) func() { return func() { _ = os.RemoveAll(dir) } }

var errNoTrees = errors.New("an experiment measures at least one tree")
