// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/spin-machine/internal/lab"
)

func TestATreeSpecIsANameAndLabTreesFlags(t *testing.T) {
	s, err := parseTreeSpec(` kaslr = --kernel run:44584704610 --append "quiet x=1" --edit lane --edit lane --spin-machine here`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "kaslr" || s.Kernel != "run:44584704610" || s.Append != "quiet x=1" || !slices.Equal(s.Edits, []string{"lane", "lane"}) ||
		s.SpinMachine != "here" || s.From != "release" {
		t.Errorf("parsed %+v", s)
	}
	for bad, says := range map[string]string{
		"no-equals":              "a tree is NAME=FLAGS",
		"=--from release":        "a tree is NAME=FLAGS",
		"t=--nope":               "unknown flag",
		"t=--from release stray": `"stray" is not a flag`,
		`t=--append "open`:       "an unclosed",
	} {
		if _, err := parseTreeSpec(bad); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("parseTreeSpec(%q) = %v, want an error saying %q", bad, err, says)
		}
	}
}

func TestWordsSplitAsAShellWould(t *testing.T) {
	for in, want := range map[string][]string{
		`a b  c`:           {"a", "b", "c"},
		`--append 'x y' z`: {"--append", "x y", "z"},
		`"a 'b' c" d`:      {"a 'b' c", "d"},
		`''`:               {""},
		"  \t\n":           nil,
		`x"y z"`:           {"xy z"},
	} {
		got, err := splitWords(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("splitWords(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := splitWords(`"a`); err == nil {
		t.Error("an unclosed quote was taken")
	}
}

func TestAListValueCarriesSeveralSeparatedBySemicolons(t *testing.T) {
	if got := splitList([]string{"a=1; b=2;", " ;c=3", ""}); !slices.Equal(got, []string{"a=1", "b=2", "c=3"}) {
		t.Errorf("splitList = %q", got)
	}
	o, err := options([]string{"cpus = 1; flags=--max-cpus 16"})
	if err != nil || o["cpus"] != "1" || o["flags"] != "--max-cpus 16" || len(o) != 2 {
		t.Errorf("options = %v, %v", o, err)
	}
	if _, err := options([]string{"cpus"}); err == nil {
		t.Error("an option with no value was taken")
	}
}

// counter is a probe that boots nothing: one measurement, its repetition as its number, and the
// second tree's lost.
type counter struct{}

func (counter) Name() string  { return "counter" }
func (counter) About() string { return "counts" }
func (counter) Options() []lab.Option {
	return []lab.Option{{Name: "cpus", Default: "2"}, {Name: "memory", Default: "2048"}, {Name: "flags"}}
}
func (counter) Metrics() []lab.Metric     { return []lab.Metric{{Name: "n", Unit: "count"}} }
func (counter) Rows(lab.Options) []string { return []string{"one"} }
func (counter) Phases() int               { return 1 }
func (counter) Measure(_ context.Context, m *lab.Measurement) error {
	if m.Tree.Name() == "lost" {
		return lab.Miss("gone")
	}
	m.Record("n", float64(m.Rep+1))
	return nil
}

// fakeRelease is a directory laid out as a release, every file a few bytes.
func fakeRelease(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	kernel := make([]byte, 0x210)
	copy(kernel[0x202:], "HdrS")
	for p, b := range map[string][]byte{
		"bin/qemu-system-x86_64": []byte("qemu"), "bin/qemu-img": []byte("img"), "bin/spin-machine": []byte("sm"),
		"qemu/qboot.bin": []byte("qboot"), "qemu/linuxboot_dma.bin": []byte("rom"), "image/rootfs.qcow2": []byte("base"),
		"kernel/bzImage": kernel,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runLab(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	// A command of its own each time: root's verbs are made once, at init, and a flag set in one
	// run would still be set in the next.
	c := labCommand()
	c.SetArgs(args)
	c.SetOut(&out)
	c.SetErr(&errOut)
	err := c.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

func TestLabRunMakesItsTreesMeasuresThemAndWritesTheResult(t *testing.T) {
	lab.Probes["counter"] = counter{}
	t.Cleanup(func() { delete(lab.Probes, "counter") })
	base := fakeRelease(t)
	t.Setenv("TMPDIR", "")
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "summary"))
	if err := os.WriteFile(os.Getenv("GITHUB_STEP_SUMMARY"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	trees := filepath.Join("..", "..", "_output", "trees")
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(trees, "labtest-a"))
		_ = os.RemoveAll(filepath.Join(trees, "lost"))
	})
	out := t.TempDir()

	stdout, _, err := runLab(t, "run", "counter", "--tree", "labtest-a=--from "+base, "--reps", "2", "--opt", "cpus=1", "--out", out)
	if err != nil {
		t.Fatal(err)
	}
	r, err := lab.ReadResult(filepath.Join(out, "result.json"))
	if err != nil || len(r.Samples) != 2 || r.Options["cpus"] != "1" || r.Trees[0].Parts["kernel"].Source != base {
		t.Fatalf("result %+v, %v", r, err)
	}
	if !strings.Contains(stdout, "labtest-a") || !strings.Contains(stdout, " 1/2 ") {
		t.Errorf("the table:\n%s", stdout)
	}
	if b, _ := os.ReadFile(os.Getenv("GITHUB_STEP_SUMMARY")); !strings.Contains(string(b), "```\ncounter: counts") {
		t.Errorf("the summary: %q", b)
	}

	// The tree made above, by name; a second tree whose measurement is lost: exit 1.
	_, _, err = runLab(t, "run", "counter", "labtest-a", "--tree", "lost=--from "+base, "--reps", "1", "--out", t.TempDir())
	var e exitError
	if !errors.As(err, &e) || e.code != 1 {
		t.Errorf("a lost measurement exits %v, want 1", err)
	}
	// show reads it back from the directory.
	if stdout, _, err := runLab(t, "show", out); err != nil || !strings.Contains(stdout, "labtest-a") {
		t.Errorf("show: %v\n%s", err, stdout)
	}

	// With no --out, the result goes under _output/lab/results, named for when and what.
	results := filepath.Join("..", "..", "_output", "lab", "results")
	before, _ := filepath.Glob(filepath.Join(results, "*-counter"))
	if _, _, err := runLab(t, "run", "counter", "labtest-a", "--reps", "1"); err != nil {
		t.Fatal(err)
	}
	after, _ := filepath.Glob(filepath.Join(results, "*-counter"))
	for _, d := range after {
		if !slices.Contains(before, d) {
			t.Cleanup(func() { _ = os.RemoveAll(d) })
			if _, err := lab.ReadResult(filepath.Join(d, "result.json")); err != nil {
				t.Error(err)
			}
		}
	}
	if len(after) != len(before)+1 {
		t.Errorf("results named *-counter: %d before, %d after", len(before), len(after))
	}
	if _, _, err := runLab(t, "run", "nope", "labtest-a"); err == nil || !strings.Contains(err.Error(), `no probe "nope"`) {
		t.Errorf("an unknown probe: %v", err)
	}

	for _, args := range [][]string{
		{"run", "nope", "labtest-a"},
		{"run", "counter", "no-such-tree"},
		{"run", "counter", "--tree", "bad"},
		{"run", "counter", "labtest-a", "--opt", "cpus"},
		{"show", filepath.Join(t.TempDir(), "empty")},
		{"tree", "x", "--from", filepath.Join(t.TempDir(), "nothing")},
		{"dispatch", "nope"},
		{"dispatch", "counter"},
		{"dispatch", "counter", "--tree", "bad"},
		{"dispatch", "counter", "--tree", "a=--from release", "--opt", "x"},
	} {
		_, _, err := runLab(t, args...)
		if !errors.As(err, &e) || e.code != 2 {
			t.Errorf("lab %v exits %v, want 2", args, err)
		}
	}
}

func TestLabTreeMakesANamedTreeAndSaysWhere(t *testing.T) {
	base := fakeRelease(t)
	dir := filepath.Join("..", "..", "_output", "trees", "labtest-tree")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	stdout, _, err := runLab(t, "tree", "labtest-tree", "--from", base, "--append", "quiet")
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(dir)
	if strings.TrimSpace(stdout) != abs {
		t.Errorf("tree printed %q, want %q", stdout, abs)
	}
	tr, err := lab.OpenTree(abs)
	if err != nil || tr.Manifest.Append != "quiet" {
		t.Errorf("the tree: %+v, %v", tr, err)
	}
}

func TestAResultIsFoundByDirectoryOrRun(t *testing.T) {
	if p, err := resultOf("some/dir"); err != nil || p != "some/dir/result.json" {
		t.Errorf("resultOf(dir) = %q, %v", p, err)
	}
	if _, err := resultOf("123"); err == nil {
		t.Error("a run's result was found outside a checkout")
	}
	if g := github(); g.Repo != "spin-stack/spin-machine" {
		t.Errorf("repo %q", g.Repo)
	}
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	if g := github(); g.Repo != "o/r" {
		t.Errorf("repo %q", g.Repo)
	}
}

func TestLabsHelpNamesEveryProbe(t *testing.T) {
	c := labCommand()
	for _, p := range lab.ProbeNames() {
		if p != "counter" && !strings.Contains(c.Long, p) {
			t.Errorf("lab --help does not name %s", p)
		}
	}
	var names []string
	for _, sub := range c.Commands() {
		names = append(names, sub.Name())
	}
	if !slices.Equal(names, []string{"check", "dispatch", "run", "show", "tree"}) {
		t.Errorf("lab's verbs: %v", names)
	}
}

// TestLabDispatchMeasuresOnTheBench is the bench lane: a real lab.yml run of ssh-first over the
// newest release, 1 repetition, dispatched, waited for and its result read back
// (SPIN_LAB_DISPATCH=<ref>, gh logged in). It costs a bench run.
func TestLabDispatchMeasuresOnTheBench(t *testing.T) {
	ref := os.Getenv("SPIN_LAB_DISPATCH")
	if ref == "" {
		t.Skip("set SPIN_LAB_DISPATCH to a branch: dispatches a lab run on the bench fleet")
	}
	stdout, _, err := runLab(t, "dispatch", "ssh-first", "--tree", "release=--from release", "--reps", "1", "--ref", ref)
	if err != nil || !strings.Contains(stdout, "SSHD_ACCEPTS") {
		t.Errorf("dispatch: %v\n%s", err, stdout)
	}
}

// TestLabCheckRunsABootTarget is the VM lane's check: boot:lockdown on a tree made from a built
// release (SPIN_LAB_LANE, _output), which needs KVM or TCG and nothing else.
func TestLabCheckRunsABootTarget(t *testing.T) {
	from := os.Getenv("SPIN_LAB_LANE")
	if from == "" {
		t.Skip("set SPIN_LAB_LANE to a built release (_output): boots a VM")
	}
	dir := filepath.Join("..", "..", "_output", "trees", "labtest-check")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if _, _, err := runLab(t, "check", "lockdown", "--tree", "labtest-check=--from "+from); err != nil {
		t.Error(err)
	}
	var e exitError
	if _, _, err := runLab(t, "check", "no-such-target", "--tree", "labtest-check=--from "+from); !errors.As(err, &e) || e.code != 1 {
		t.Errorf("a target that is not there exits %v, want 1", err)
	}
}
