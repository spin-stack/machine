// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// recorder is a probe that boots nothing: it records which measurement it was asked for, in which
// phase, and what the test tells it to.
type recorder struct {
	phases  int
	rows    []string
	calls   []string
	cleaned []string
	miss    string // tree/row/rep/phase whose measurement is lost
	fail    string // tree/row/rep/phase whose measurement stops the experiment
}

func (r *recorder) Name() string  { return "recorder" }
func (r *recorder) About() string { return "what it was asked" }
func (r *recorder) Options() []Option {
	return append(shape, Option{"n", "3", "a number"}, Option{"on", "false", "a switch"})
}
func (r *recorder) Metrics() []Metric { return []Metric{{"a", "ms", ""}, {"b", "count", ""}} }
func (r *recorder) Rows(o Options) []string {
	if o.Bool("on") {
		return append(slices.Clone(r.rows), "extra")
	}
	return r.rows
}
func (r *recorder) Phases() int { return r.phases }

func (r *recorder) Measure(_ context.Context, m *Measurement) error {
	at := fmt.Sprintf("%s/%s/%d/%d", m.Tree.Name(), m.Row, m.Rep, m.Phase)
	r.calls = append(r.calls, at)
	switch at {
	case r.miss:
		return Miss("never answered: %d", 7)
	case r.fail:
		return errors.New("broke")
	}
	if fi, err := os.Stat(m.Dir); err != nil || !fi.IsDir() || len(m.Socks) > 40 || m.Key == nil {
		return fmt.Errorf("the measurement's own directory %s, sockets %s, key %v", m.Dir, m.Socks, m.Key)
	}
	if m.Phase == 0 {
		m.State = at
		m.Cleanup(func() { r.cleaned = append(r.cleaned, at+" first") })
		m.Cleanup(func() { r.cleaned = append(r.cleaned, at+" second") })
	} else if m.State != strings.TrimSuffix(at, "1")+"0" {
		return fmt.Errorf("phase 1 got state %v", m.State)
	}
	m.RecordMs("a", time.Duration(m.Rep+1)*time.Millisecond)
	m.Record("b", float64(m.Options.Int("n")))
	m.Detail("k", 1)
	m.Logf("measured %s", at)
	return nil
}

func trees(names ...string) []*Tree {
	var ts []*Tree
	for _, n := range names {
		ts = append(ts, &Tree{Dir: "/" + n, Manifest: Manifest{Name: n, Parts: map[string]Part{"kernel": {Source: "release v1"}}}})
	}
	return ts
}

func TestAnExperimentInterleavesAndFinishesEachPhaseBeforeTheNext(t *testing.T) {
	p := &recorder{phases: 2, rows: []string{"x", "y"}}
	var log bytes.Buffer
	scratch := t.TempDir()
	r, err := Experiment{Probe: p, Trees: trees("A", "B"), Reps: 2, Options: Options{"n": "5"}, Scratch: scratch, Log: &log}.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for phase := range 2 {
		for rep := range 2 {
			for _, tree := range []string{"A", "B"} {
				for _, row := range []string{"x", "y"} {
					want = append(want, fmt.Sprintf("%s/%s/%d/%d", tree, row, rep, phase))
				}
			}
		}
	}
	if !slices.Equal(p.calls, want) {
		t.Errorf("measured in the order\n%v\nwant\n%v", p.calls, want)
	}
	if len(r.Samples) != 8 || r.Samples[0].Rep != 1 || r.Samples[7].Rep != 2 || r.Samples[7].Tree != "B" {
		t.Errorf("samples: %+v", r.Samples)
	}
	if r.Options["n"] != "5" || r.Options["cpus"] != "2" || r.Options["on"] != "false" || r.Reps != 2 || r.Probe != "recorder" ||
		!slices.Equal(r.Rows, []string{"x", "y"}) || len(r.Trees) != 2 || r.Finished.Before(r.Started) {
		t.Errorf("result: %+v", r)
	}
	if got := r.Values("B", "y", "b"); !slices.Equal(got, []float64{5, 5}) {
		t.Errorf("B y b = %v", got)
	}
	if !strings.Contains(log.String(), "A, x, repetition 1: measured A/x/0/0") {
		t.Errorf("log: %s", log.String())
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("the measurements' directories outlived the experiment: %v", left)
	}
	// Last registered, first run: a machine is stopped before the directory under it goes.
	if len(p.cleaned) != 16 || p.cleaned[0] != "A/x/0/0 second" || p.cleaned[1] != "A/x/0/0 first" {
		t.Errorf("cleaned up in the order %v", p.cleaned)
	}
}

func TestALostMeasurementIsCountedAndAFailureStops(t *testing.T) {
	p := &recorder{phases: 2, rows: []string{"x"}, miss: "A/x/0/0"}
	r, err := Experiment{Probe: p, Trees: trees("A"), Reps: 2, Scratch: t.TempDir(), Log: io.Discard}.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.calls, "A/x/0/1") {
		t.Error("a measurement lost in its first phase was measured in its second")
	}
	if r.Missed("A", "x") != 1 || r.Samples[0].Missed != "never answered: 7" || len(r.Values("A", "x", "a")) != 1 {
		t.Errorf("samples: %+v", r.Samples)
	}

	p = &recorder{phases: 1, rows: []string{"x"}, fail: "B/x/0/0"}
	_, err = Experiment{Probe: p, Trees: trees("A", "B", "C"), Reps: 1, Scratch: t.TempDir(), Log: io.Discard}.Run(t.Context())
	if err == nil || err.Error() != "B, x, repetition 1: broke" || slices.Contains(p.calls, "C/x/0/0") {
		t.Errorf("err %v after %v", err, p.calls)
	}
	for _, e := range []Experiment{
		{Probe: p, Reps: 1},
		{Probe: p, Trees: trees("A"), Reps: 0},
		{Probe: p, Trees: trees("A"), Reps: 1, Options: Options{"nope": "1"}},
		{Probe: p, Trees: trees("A"), Reps: 1, Scratch: "/nonexistent/scratch"},
	} {
		if _, err := e.Run(t.Context()); err == nil {
			t.Errorf("%+v ran", e)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Experiment{Probe: p, Trees: trees("A"), Reps: 1, Scratch: t.TempDir(), Log: io.Discard}).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled experiment: %v", err)
	}
}

func TestAnOptionIsOneTheProbeTakesAndOfItsKind(t *testing.T) {
	p := &recorder{}
	for _, tc := range []struct {
		given Options
		want  string
	}{
		{Options{"nope": "1"}, `takes no option "nope"; it takes: cpus, flags, memory, n, on`},
		{Options{"n": "many"}, "is a number"},
		{Options{"n": "-1"}, "is a number"},
		{Options{"on": "maybe"}, "true or false"},
	} {
		if _, err := settle(p, tc.given); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("settle(%v) = %v, want %q", tc.given, err, tc.want)
		}
	}
	o, err := settle(p, Options{"n": "4", "on": "true", "flags": "--a b"})
	if err != nil || o.Int("n") != 4 || !o.Bool("on") || !slices.Equal(o.Fields("flags"), []string{"--a", "b"}) || o["memory"] != "2048" {
		t.Errorf("settle = %v, %v", o, err)
	}
	if !slices.Equal(p.Rows(o), []string{"extra"}) {
		t.Errorf("rows %v", p.Rows(o))
	}
}

func TestBootArgsAreTheTreesInTheExperimentsShape(t *testing.T) {
	m := &Measurement{Tree: trees("A")[0], Options: Options{"cpus": "1", "memory": "512", "flags": "--max-cpus 16"}}
	got := m.BootArgs("/o.qcow2", "--vsock-cid", "9")
	want := []string{"boot", "--release", "/A", "--disk", "/o.qcow2", "--memory", "512", "--cpus", "1", "--vsock-cid", "9", "--max-cpus", "16"}
	if !slices.Equal(got, want) {
		t.Errorf("BootArgs = %v", got)
	}
	c := &checkpoint{cid: 42}
	m.Tree.Manifest.Append = "quiet"
	if got := c.bootFlags(m, "/s/q", "--held"); !slices.Equal(got, []string{"--vsock-cid", "42", "--qmp", "/s/q", "--console", "file:/s/q.console", "--append", "init=/sbin/init quiet", "--held"}) {
		t.Errorf("bootFlags = %v", got)
	}
}

func TestTheTableIsEachTreesP50AndP95AndItsDistanceFromTheFirst(t *testing.T) {
	r := &Result{Probe: "p", About: "about", Reps: 3, Rows: []string{"x"}, Options: Options{"cpus": "2", "flags": ""},
		Metrics: []Metric{{"a", "ms", ""}, {"curve25519_in", "count", ""}},
		Trees:   []Manifest{{Name: "release", Parts: map[string]Part{"kernel": {Source: "release v1"}}, Edits: []NamedEdit{{Name: "lane"}}, Append: "q"}, {Name: "kaslr"}},
		Host:    Host{Name: "h", Instance: "c8id.4xlarge", CPU: "Xeon", CPUs: 16, Nested: true, ScratchDevice: "/dev/nvme1n1"}}
	for i, v := range []float64{100, 110, 300} {
		r.Samples = append(r.Samples, Sample{Tree: "release", Row: "x", Rep: i + 1, Values: map[string]float64{"a": v, "curve25519_in": 0}})
		r.Samples = append(r.Samples, Sample{Tree: "kaslr", Row: "x", Rep: i + 1, Values: map[string]float64{"a": v - 10.5}})
	}
	r.Samples = append(r.Samples, Sample{Tree: "kaslr", Row: "x", Rep: 4, Missed: "gone", Values: map[string]float64{"a": 1}})
	got := r.Table()
	for _, want := range []string{
		"p: about\n",
		"p50/p95 over 3 of each, interleaved; cpus=2; h on c8id.4xlarge (Xeon, 16 CPUs, nested true), scratch on /dev/nvme1n1",
		"  release          110/300                 0/0      0\n",
		"  kaslr           99.5/290                   -      1\n",
		"  p50 against release\n",
		"  kaslr              -10.5                   -\n",
		"    release: kernel release v1; qemu ; image ; spin-machine ; edit lane; append \"q\";\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the table has no %q:\n%s", want, got)
		}
	}
	r.Rows = []string{"x", "y"}
	if !strings.Contains(r.Table(), "release, x") {
		t.Error("with two rows a line is not named by its row")
	}
	path := filepath.Join(t.TempDir(), "result.json")
	if err := r.Write(path); err != nil {
		t.Fatal(err)
	}
	back, err := ReadResult(path)
	if err != nil || len(back.Samples) != 7 || back.Host.Instance != "c8id.4xlarge" || back.Samples[6].Missed != "gone" {
		t.Errorf("read back: %+v, %v", back, err)
	}
	if _, err := ReadResult(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("read a result that is not there")
	}
	write(t, path, []byte("{"))
	if _, err := ReadResult(path); err == nil {
		t.Error("read a result that is not JSON")
	}
	if err := r.Write(filepath.Join(t.TempDir(), "no/such/dir/result.json")); err == nil {
		t.Error("wrote into a directory that is not there")
	}
	for v, want := range map[float64]string{7: "7", 7.25: "7.2", 123.4: "123", 100.4: "100", 99.4: "99.4", 0.06: "0.1"} {
		if got := num(v); got != want {
			t.Errorf("num(%v) = %q, want %q", v, got, want)
		}
	}
}

const ringBufferSample = `[    0.000000] Linux version 7.3.5
[    0.010000] calling  acpi_init+0x0/0x420 @ 1
[    0.014000] initcall acpi_init+0x0/0x420 returned 0 after 4000 usecs
[    0.020000] initcall inet_init+0x0/0x2e0 returned 0 after 1500 usecs
[    0.080000] Freeing unused kernel image (initmem) memory: 2628K
[    0.090000] systemd-journald[62]: Collecting audit messages is disabled.
[    0.050000] a reprinted line going backwards
`

func TestTheRingBufferIsItsInitcallsItsGapsAndWhenTheKernelWasDone(t *testing.T) {
	p, err := parseRingBuffer(ringBufferSample)
	if err != nil {
		t.Fatal(err)
	}
	if p.total != 5500 || p.calls["acpi_init+0x0/0x420"] != 4000 || p.calls["inet_init+0x0/0x2e0"] != 1500 || p.freeing != 0.08 {
		t.Errorf("parsed %+v", p)
	}
	gaps := map[string]gap{}
	for _, g := range p.gaps {
		gaps[g.before] = g
	}
	journald := gaps["systemd-journald[]: Collecting audit messages is disabled."]
	if len(p.gaps) != 5 || journald.after != "Freeing unused kernel image (initmem) memory: 2628K" || journald.ms < 9.995 || journald.ms > 10.005 {
		t.Errorf("gaps %+v", p.gaps)
	}
	if _, err := parseRingBuffer("[    0.1] [    0.2] twice"); err == nil {
		t.Log("not a double stamp at six digits")
	}
	if _, err := parseRingBuffer("[    0.100000] sh[1]: [    0.000100] re-stamped"); err == nil || !strings.Contains(err.Error(), "two kernel stamps") {
		t.Errorf("a re-stamped dump: %v", err)
	}
	if _, err := parseRingBuffer("[    0.100000] no initcalls here"); err == nil || !strings.Contains(err.Error(), "no initcall lines") {
		t.Errorf("no initcalls: %v", err)
	}
	if clip(strings.Repeat("x", 100)) != strings.Repeat("x", 88) || clip("short") != "short" || tail("abcdef", 2) != "ef" || tail("ab", 5) != "ab" {
		t.Error("clip or tail")
	}
}

func TestInitcallsDetailRanksEachTreesInitcallsAndGaps(t *testing.T) {
	r := &Result{Options: Options{"top": "2"}, Trees: []Manifest{{Name: "A"}, {Name: "B"}}}
	for i, tr := range []string{"A", "B"} {
		r.Samples = append(r.Samples, Sample{Tree: tr, Detail: map[string]float64{
			"initcall\x00acpi_init+0x0/0x420": 4 + float64(i), "initcall\x00slow_init+0x0/0x10": 9, "initcall\x00fast_init+0x0/0x10": 1,
			"gap\x00before it\x00after it": 30, "gap\x00x\x00y": 2,
		}})
	}
	// B's own ranking: its acpi_init, 5, not A's 4 with it.
	r.Samples = append(r.Samples, Sample{Tree: "B", Detail: map[string]float64{"initcall\x00b_only+0x0/0x10": 50}})
	got := initcalls{}.Detail(r)
	if !strings.Contains(got, "  B\n   GAP P50      P95  BETWEEN (ms)\n     30.00    30.00  after:  before it") ||
		!strings.Contains(got, "     50.00    50.00  b_only+0x0/0x10\n      9.00     9.00  slow_init") {
		t.Errorf("B's own ranking is not its own:\n%s", got)
	}
	if strings.Count(got, "b_only") != 1 {
		t.Errorf("B's initcall was listed under A:\n%s", got)
	}
	// With no top, thirty: all three of A's are listed.
	r.Options = Options{}
	if all := (initcalls{}).Detail(r); !strings.Contains(all, "fast_init") {
		t.Errorf("with no top, an initcall is missing:\n%s", all)
	}
	r.Options = Options{"top": "2"}
	for _, want := range []string{
		"  acpi_init                            4.00           5.00\n",
		"  inet_init                               -              -\n",
		"  A\n   GAP P50      P95  BETWEEN (ms)\n     30.00    30.00  after:  before it\n                     before: after it\n       P50      P95  INITCALL\n      9.00     9.00  slow_init+0x0/0x10\n      4.00     4.00  acpi_init+0x0/0x420\n",
		"     30.00    30.00  after:  before it\n                     before: after it\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the detail has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fast_init") || strings.Contains(got, "after:  x\n") {
		t.Errorf("top=2 listed more than two initcalls or one gap:\n%s", got)
	}
}

func TestReadUntilReturnsOnceTheMarkerIsThere(t *testing.T) {
	if got, err := readUntil(strings.NewReader("a b END c"), "END", time.Second); err != nil || got != "a b END c" {
		t.Errorf("readUntil = %q, %v", got, err)
	}
	if got, err := readUntil(strings.NewReader("no marker"), "END", time.Second); err == nil || !strings.Contains(err.Error(), "ended before") || got != "no marker" {
		t.Errorf("at EOF: %q, %v", got, err)
	}
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	if _, err := readUntil(r, "END", 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "never saw") {
		t.Errorf("a console that says nothing: %v", err)
	}
}

func TestALoginKeyGoesIntoTheOverlayOwnedBySpin(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	e := k.Edit()
	if !strings.Contains(e.Setup, "printf '%s' '"+string(k.public)+"'") || !strings.Contains(e.Setup, "chmod 600") ||
		!strings.Contains(e.Files["/etc/systemd/system/serial-getty@ttyS0.service.d/zz-bench.conf"], "SPIN-READY-login") ||
		!strings.Contains(e.Files["/etc/systemd/system/sshd@.service.d/keygen.conf"], "After=spin-machine-sshd-keygen.service") {
		t.Errorf("the key's edit: %+v", e)
	}
	for range 50 {
		if c := NewCID(); c < 100000 || c >= 100000+1<<20 {
			t.Fatalf("cid %d out of its range", c)
		}
	}
	d, err := shortDir()
	if err != nil || !strings.HasPrefix(d, "/tmp/lab") || len(d) > 30 {
		t.Errorf("shortDir = %q, %v", d, err)
	}
	_ = os.RemoveAll(d)
	var c vsockConn
	if c.LocalAddr().Network() != "vsock" || c.RemoteAddr().String() != "vsock" {
		t.Error("vsock addresses")
	}
	if (&MissError{Why: "w"}).Error() != "w" {
		t.Error("MissError")
	}
}

func TestAStateIsReadWholeAndASocketWaitedFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	write(t, path, bytes.Repeat([]byte("s"), 3<<20))
	if d, err := readWhole(path); err != nil || d <= 0 {
		t.Errorf("readWhole = %v, %v", d, err)
	}
	if _, err := readWhole(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("read a state that is not there")
	}
	dir, err := shortDir()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "q")
	if err := waitSocket(sock, time.Now().Add(10*time.Millisecond)); err == nil {
		t.Error("waited for a socket nothing listens on")
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if err := waitSocket(sock, time.Now().Add(time.Second)); err != nil {
		t.Errorf("waitSocket: %v", err)
	}
}

func TestTheProbesAreTheGoalsAndTheKernels(t *testing.T) {
	if got := ProbeNames(); !slices.Equal(got, []string{"initcalls", "prestart", "restore-first", "ssh-first"}) {
		t.Errorf("probes %v", got)
	}
	for _, name := range ProbeNames() {
		p := Probes[name]
		o, err := settle(p, nil)
		if err != nil || p.About() == "" || len(p.Metrics()) == 0 || len(p.Rows(o)) == 0 || p.Phases() < 1 {
			t.Errorf("%s: %v", name, err)
		}
	}
	rf := Probes["restore-first"]
	if !slices.Equal(rf.Rows(Options{"fast": "true"}), []string{"evicted", "cached", "fast"}) || rf.Phases() != 2 {
		t.Error("restore-first's rows")
	}
	if Probes["prestart"].Phases() != 2 || Probes["ssh-first"].Phases() != 1 || Probes["initcalls"].Phases() != 1 {
		t.Error("phases")
	}
}

func TestTheScratchIsTMPDIROnADiskOrTheLabsOwn(t *testing.T) {
	disk, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_ACTIONS", "")
	if isTmpfs(disk) {
		t.Skip("the home directory is on a tmpfs")
	}
	t.Setenv("TMPDIR", disk)
	if got, err := Scratch(t.TempDir()); err != nil || got != disk {
		t.Errorf("Scratch with TMPDIR on a disk = %s, %v", got, err)
	}
	t.Setenv("TMPDIR", "")
	lab := filepath.Join(t.TempDir(), "lab")
	if got, err := Scratch(lab); err != nil || got != filepath.Join(lab, "scratch") || !fileExists(got) {
		t.Errorf("Scratch with no TMPDIR = %s, %v", got, err)
	}
	if isTmpfs("/proc") || isTmpfs("/nonexistent") {
		t.Error("procfs, or nothing, taken for a tmpfs")
	}
	mounts, _ := os.ReadFile("/proc/mounts")
	if strings.Contains(string(mounts), " /dev/shm tmpfs ") && !isTmpfs("/dev/shm") {
		t.Error("/dev/shm is a tmpfs and was not taken for one")
	}
}

func TestDispatchWaitsForABusyLabAndFindsItsOwnRun(t *testing.T) {
	now := time.Now()
	runs := []Run{{ID: 1, Status: "completed", CreatedAt: now.Add(-time.Hour)}, {ID: 2, Status: "queued", URL: "u2", CreatedAt: now}}
	if busy(runs) != "u2" || busy(runs[:1]) != "" {
		t.Error("busy")
	}
	if r, ok := started(runs, now.Add(-time.Minute)); !ok || r.ID != 2 {
		t.Errorf("started = %+v, %v", r, ok)
	}
	if _, ok := started(runs, now.Add(time.Minute)); ok {
		t.Error("a run from before the dispatch was taken for its own")
	}
	got := dispatchArgs("o/r", "main", map[string]string{"trees": "a=b", "probe": "ssh-first"})
	if !slices.Equal(got, []string{"workflow", "run", "lab.yml", "-R", "o/r", "--ref", "main", "-f", "probe=ssh-first", "-f", "trees=a=b"}) {
		t.Errorf("dispatchArgs = %v", got)
	}
}
