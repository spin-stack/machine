// SPDX-License-Identifier: Apache-2.0

package lab

import (
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

// A release's spin-machine is built from the commit its machine.env names; a tree that names none
// has no spin-machine to build, and a commit the proxy cannot give is said.
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
	t.Setenv("GOPROXY", "off")
	if _, err := s.releaseSpinMachine(base, to); err == nil || !strings.Contains(err.Error(), "building v1's spin-machine at 0000000") {
		t.Errorf("a commit the proxy cannot give: %v", err)
	}
	env, err := readEnv(filepath.Join(base, "machine.env"))
	if err != nil || env["version"] != "v1" || env["source_commit"] != "0000000" || len(env) != 2 {
		t.Errorf("readEnv = %v, %v", env, err)
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
