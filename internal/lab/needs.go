// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Need is a command of a tree's spin-machine, and one of its flags, that a probe runs.
type Need struct{ Command, Flag string }

// bootNeeds are the boot flags every probe's boots pass (Measurement.BootArgs).
var bootNeeds = []Need{{"boot", "release"}, {"boot", "disk"}, {"boot", "memory"}, {"boot", "cpus"},
	{"boot", "max-memory"}, {"boot", "max-cpus"}, {"boot", "hotplug-disks"}}

// CanRun says why tree t's spin-machine cannot run probe p with options o, or nil when it can,
// before anything boots: a release older than what the probe asks of it is a row that says so,
// not an experiment that stops halfway.
func CanRun(p Probe, o Options, t *Tree) error { return lacks(t, p.Needs(o)) }

// lacks says which of needs tree t's spin-machine has not, or nil when it has them all.
func lacks(t *Tree, needs []Need) error {
	help := map[string]string{}
	for _, need := range needs {
		out, seen := help[need.Command]
		if !seen {
			// A command it has prints its usage on -h; one it has not prints the command's.
			b, _ := exec.Command(t.SpinMachine(), need.Command, "-h").CombinedOutput()
			out = string(b)
			help[need.Command] = out
		}
		if !strings.Contains(out, "Usage of "+need.Command+":") {
			return fmt.Errorf("its spin-machine has no %s command", need.Command)
		}
		if need.Flag != "" && !regexp.MustCompile(`(?m)^\s+-`+regexp.QuoteMeta(need.Flag)+`\b`).MatchString(out) {
			return fmt.Errorf("its spin-machine's %s has no --%s", need.Command, need.Flag)
		}
	}
	return nil
}
