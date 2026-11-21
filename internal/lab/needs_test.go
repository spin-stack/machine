// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// usageOf is a spin-machine that prints, for each command it has, Go's flag usage with the flags
// given; for any other command it prints the command list, as spin-machine does.
func usageOf(commands map[string][]string) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\ncase \"$1\" in\n")
	for cmd, flags := range commands {
		b.WriteString(cmd + ") echo 'Usage of " + cmd + ":'\n")
		for _, f := range flags {
			b.WriteString("  echo '  -" + f + " string'\n  echo '    \tits help'\n")
		}
		b.WriteString("  exit 0 ;;\n")
	}
	b.WriteString("*) echo 'spin-machine - the machine this repository builds'; exit 1 ;;\nesac\n")
	return []byte(b.String())
}

func treeWith(t *testing.T, commands map[string][]string) *Tree {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bin", "spin-machine"), usageOf(commands))
	return &Tree{Dir: dir, Manifest: Manifest{Name: "t"}}
}

// A release's spin-machine runs a probe only when it has every command and flag the probe asks of
// it: v20261119.01's has boot --incoming and no --restore, --held or start.
func TestAProbeRunsOnlyWhereItsSpinMachineHasWhatItNeeds(t *testing.T) {
	boot := []string{"release", "disk", "memory", "cpus", "max-memory", "max-cpus", "hotplug-disks", "vsock-cid", "console",
		"append", "qmp", "profile"}
	old := treeWith(t, map[string][]string{"boot": append(boot, "incoming"), "save": {"qmp", "to"}, "restore": {"qmp", "from"}})
	// Every probe boots spin's shape: a spin-machine that cannot is refused before it boots.
	if err := CanRun(Probes["ssh-first"], nil, treeWith(t, map[string][]string{"boot": boot[:4]})); err == nil ||
		!strings.Contains(err.Error(), "boot has no --max-memory") {
		t.Errorf("CanRun of a spin-machine with no ceilings = %v", err)
	}
	now := treeWith(t, map[string][]string{"boot": append(boot, "restore", "held"), "save": {"qmp", "to"},
		"restore": {"qmp", "from", "disk"}, "start": {"qmp", "disk"}})
	for _, tc := range []struct {
		probe string
		opts  Options
		tree  *Tree
		want  string // empty when it can
	}{
		{"ssh-first", nil, old, ""},
		{"initcalls", nil, old, ""},
		{"restore-first", nil, old, "boot has no --restore"},
		{"prestart", nil, old, "boot has no --held"},
		{"restore-first", nil, now, ""},
		{"restore-first", Options{"fast": "true"}, now, "restore has no --fast"},
		{"restore-first", Options{"channels": "1 4"}, now, "save has no --channels"},
		{"prestart", Options{"channels": "4"}, treeWith(t, map[string][]string{"boot": append(boot, "restore", "held"), "save": {"to", "channels"},
			"restore": {"disk"}, "start": {"disk"}}), "restore has no --channels"},
		{"prestart", Options{"channels": "4"}, treeWith(t, map[string][]string{"boot": append(boot, "restore", "held"), "save": {"to", "channels"},
			"restore": {"disk", "channels"}, "start": {"disk"}}), ""},
		{"prestart", nil, now, ""},
		{"prestart", nil, treeWith(t, map[string][]string{"boot": append(boot, "restore", "held"), "save": {"to"}, "restore": {"disk"}}),
			"has no start command"},
	} {
		err := CanRun(Probes[tc.probe], tc.opts, tc.tree)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("CanRun(%s, %v) = %v, want %q", tc.probe, tc.opts, err, tc.want)
		}
	}
	// A probe that runs nothing particular runs anywhere.
	if err := CanRun(&recorder{}, nil, old); err != nil {
		t.Errorf("a probe with no needs: %v", err)
	}
}

// An experiment refuses a tree that cannot run its probe before anything boots.
func TestAnExperimentRefusesATreeThatCannotRunItsProbe(t *testing.T) {
	old := treeWith(t, map[string][]string{"boot": {"release", "disk", "memory", "cpus"}})
	_, err := Experiment{Probe: Probes["prestart"], Trees: []*Tree{old}, Reps: 1, Scratch: t.TempDir()}.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "tree t cannot run prestart") {
		t.Errorf("Run = %v", err)
	}
}

// A release's spin-machine is built from the commit its machine.env names, whatever module path
// that commit declares; a tree that names none has no spin-machine to build.
func TestAReleasesSpinMachineIsItsCommits(t *testing.T) {
	s := src(t)
	to := filepath.Join(t.TempDir(), "spin-machine")
	if _, err := s.releaseSpinMachine(t.TempDir(), to); err == nil || !strings.Contains(err.Error(), "machine.env") {
		t.Errorf("no machine.env: %v", err)
	}
	base := t.TempDir()
	write(t, filepath.Join(base, "machine.env"), []byte("version=v1\n"))
	if _, err := s.releaseSpinMachine(base, to); err == nil || !strings.Contains(err.Error(), "names no source_commit") {
		t.Errorf("no source_commit: %v", err)
	}
	write(t, filepath.Join(base, "machine.env"), []byte("version=v1\nsource_commit=0000000\n"))
	env, err := readEnv(filepath.Join(base, "machine.env"))
	if err != nil || env["version"] != "v1" || env["source_commit"] != "0000000" || len(env) != 2 {
		t.Errorf("readEnv = %v, %v", env, err)
	}
}

// A release's spin-machine is fetched by its commit from where the checkout's origin points, so
// neither the module path the commit declares nor the repository's name decides what is built.
func TestAReleasesSpinMachineIsFetchedByItsCommit(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	upstream := t.TempDir()
	git(upstream, "init", "-q")
	commit := func(module, main string) string {
		write(t, filepath.Join(upstream, "go.mod"), []byte("module "+module+"\n\ngo 1.24\n"))
		write(t, filepath.Join(upstream, "cmd", "spin-machine", "main.go"), []byte(main))
		git(upstream, "add", "-A")
		git(upstream, "commit", "-q", "-m", module)
		return git(upstream, "rev-parse", "HEAD")
	}
	// net, so that a build with cgo would link the C library dynamically.
	program := func(says string) string {
		return "package main\n\nimport (\n\t\"fmt\"\n\t_ \"net\"\n)\n\nfunc main() { fmt.Print(\"" + says + "\") }\n"
	}
	old := commit("github.com/spin-stack/spin-machine", program("old"))
	renamed := commit("github.com/spin-stack/machine", program("renamed"))
	broken := commit("github.com/spin-stack/machine", "package main\n\nfunc main() { undefined() }\n")

	checkout := t.TempDir()
	git(checkout, "init", "-q")
	git(checkout, "remote", "add", "origin", upstream)
	s := Sources{Checkout: checkout}
	built := t.TempDir()
	to := filepath.Join(built, "spin-machine")
	// A tree's spin-machine is a link to the tree it starts from, and a build must not write
	// through it.
	linked := filepath.Join(t.TempDir(), "spin-machine")
	write(t, linked, []byte("the base's"))
	if err := os.Symlink(linked, to); err != nil {
		t.Fatal(err)
	}
	build := func(commit string) (string, error) {
		t.Helper()
		base := t.TempDir()
		write(t, filepath.Join(base, "machine.env"), []byte("version=v1\nsource_commit="+commit+"\n"))
		return s.releaseSpinMachine(base, to)
	}
	for _, c := range []struct{ commit, says string }{{old, "old"}, {renamed, "renamed"}} {
		source, err := build(c.commit)
		if err != nil || source != "built from "+c.commit+", v1's" {
			t.Fatalf("releaseSpinMachine(%s) = %q, %v", c.says, source, err)
		}
		if out, err := exec.Command(to).Output(); err != nil || string(out) != c.says {
			t.Errorf("the %s commit's spin-machine says %q, %v", c.says, out, err)
		}
	}
	if b, err := os.ReadFile(linked); err != nil || string(b) != "the base's" {
		t.Errorf("the base's spin-machine is %q, %v; the build wrote through the link", b, err)
	}
	f, err := elf.Open(to)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Error("the release's spin-machine is dynamically linked; a release's is static")
		}
	}
	if left, _ := filepath.Glob(filepath.Join(built, ".source*")); len(left) != 0 {
		t.Errorf("the source it was built from is left behind: %v", left)
	}

	if _, err := build(broken); err == nil || !strings.Contains(err.Error(), "building v1's spin-machine at "+broken) {
		t.Errorf("a commit that does not build: %v", err)
	}
	missing := strings.Repeat("0", 40)
	if _, err := build(missing); err == nil || !strings.Contains(err.Error(), "checking out v1's spin-machine at "+missing) {
		t.Errorf("a commit origin does not have: %v", err)
	}
	s.Checkout = t.TempDir()
	if _, err := build(old); err == nil || !strings.Contains(err.Error(), "finding "+s.Checkout+"'s origin") {
		t.Errorf("a checkout with no origin: %v", err)
	}
}

func TestQEMUsRSSIsVmRSSInMiB(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   float64
		ok     bool
	}{
		{"Name:\tqemu\nVmPeak:\t 9000 kB\nVmRSS:\t  204800 kB\nThreads:\t5\n", 200, true},
		{"Name:\tqemu\n", 0, false},
		{"VmRSS:\t204800 MB\n", 0, false},
		{"VmRSS:\tlots kB\n", 0, false},
	} {
		got, ok := rssMiB(tc.status)
		if got != tc.want || ok != tc.ok {
			t.Errorf("rssMiB(%q) = %v, %v; want %v, %v", tc.status, got, ok, tc.want, tc.ok)
		}
	}
}
