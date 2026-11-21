// SPDX-License-Identifier: Apache-2.0

// The versioned q35 the machine is pinned to (machine.MachineType), watched against the QEMU it runs
// on.
//
// checkMachineType reads a built QEMU's machine types: it fails when the pinned one is missing or
// deprecated - QEMU deprecates a versioned type after three years and removes it after six, so
// this fires years before a QEMU stops loading the checkpoints it saved - and says, without
// failing, when QEMU offers a newer one, for the build log and the release's notes.
//
// MachineTypeCompat returns what the newer types change, from QEMU's own source: the properties
// hw_compat_X_Y (hw/core/machine.c) and pc_compat_X_Y (hw/i386/pc.c) switch back off for the
// pinned X.Y. That list is what moving the pin would change under every guest, and spin-machine-dev bump
// NAME=qemu prints it from the tarball the pin names.

package build

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spin-stack/spin-machine/machine"
)

// MachineType is what qemu says of the machine type every checkpoint is saved on: an error when it
// is missing or deprecated, and a notice when the binary offers a newer one.
func MachineType(qemu string) (string, error) {
	help, err := machineHelp(qemu)
	if err != nil {
		return "", err
	}
	notice, err := checkMachineType(help, machine.MachineType)
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(qemu), err)
	}
	return notice, nil
}

// machineHelp is a QEMU binary's `-machine help`.
func machineHelp(qemu string) (string, error) {
	out, err := exec.Command(qemu, "-machine", "help").CombinedOutput() //nolint:gosec // the QEMU this build made
	if err != nil {
		return "", fmt.Errorf("%s -machine help: %w: %s", qemu, err, out)
	}
	return string(out), nil
}

// q35 is one line of `-machine help` that names a versioned q35: its X.Y and whether it is
// deprecated.
var q35 = regexp.MustCompile(`^pc-q35-(\d+)\.(\d+)\s.*?(\(deprecated\))?\s*$`)

// q35Version is a pc-q35-X.Y's X and Y.
type q35Version struct{ major, minor int }

func parseQ35(name string) (q35Version, bool) {
	m := regexp.MustCompile(`^pc-q35-(\d+)\.(\d+)$`).FindStringSubmatch(name)
	if m == nil {
		return q35Version{}, false
	}
	x, _ := strconv.Atoi(m[1])
	y, _ := strconv.Atoi(m[2])
	return q35Version{x, y}, true
}

func (v q35Version) newer(than q35Version) bool {
	return v.major > than.major || v.major == than.major && v.minor > than.minor
}

// checkMachineType is what `-machine help` says of pinned: an error when it is not offered or is
// deprecated, and a notice when a newer pc-q35 is.
func checkMachineType(help, pinned string) (notice string, err error) {
	want, ok := parseQ35(pinned)
	if !ok {
		return "", fmt.Errorf("%q is not a pc-q35-X.Y", pinned)
	}
	var newest q35Version
	found := false
	for _, line := range strings.Split(help, "\n") {
		m := q35.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		v := q35Version{x, y}
		if v == want {
			if m[3] != "" {
				return "", fmt.Errorf("this QEMU deprecates %s, the machine type every checkpoint is saved on: move machine.MachineType, a change of hardware under every guest, before QEMU removes it", pinned)
			}
			found = true
		}
		if v.newer(newest) {
			newest = v
		}
	}
	if !found {
		return "", fmt.Errorf("this QEMU offers no %s, the machine type every checkpoint is saved on", pinned)
	}
	if newest.newer(want) {
		return fmt.Sprintf("QEMU offers pc-q35-%d.%d; this machine stays on %s (machine.MachineType), and moving it is a new machine: spin-machine-dev bump qemu says what would change.",
			newest.major, newest.minor, pinned), nil
	}
	return "", nil
}

// MachineTypeCompat is QEMU's source's hw_compat_X_Y and pc_compat_X_Y for pinned's X.Y, each array as the
// source writes it: what every type newer than pinned turns on that pinned keeps off.
func MachineTypeCompat(src, pinned string) (string, error) {
	v, ok := parseQ35(pinned)
	if !ok {
		return "", fmt.Errorf("%q is not a pc-q35-X.Y", pinned)
	}
	var b strings.Builder
	for _, name := range []string{fmt.Sprintf("hw_compat_%d_%d", v.major, v.minor), fmt.Sprintf("pc_compat_%d_%d", v.major, v.minor)} {
		arr, err := array(src, name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s\n", arr)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// ErrNoCompatArray is a source in which QEMU has not yet given pinned's type a compat array: no newer
// type exists there to differ from it.
var ErrNoCompatArray = errors.New("no such array: this QEMU has no machine type newer than the pinned one")

// array is the declaration `GlobalProperty <name>[] = { ... };` in src, whole.
func array(src, name string) (string, error) {
	start := regexp.MustCompile(`GlobalProperty\s+` + regexp.QuoteMeta(name) + `\[\]\s*=\s*\{`).FindStringIndex(src)
	if start == nil {
		return "", fmt.Errorf("%s: %w", name, ErrNoCompatArray)
	}
	end := strings.Index(src[start[0]:], "};")
	if end < 0 { // mutate-exempt: the match starts with "GlobalProperty", so "};" is never at its offset 0
		return "", fmt.Errorf("%s has no end in the source", name)
	}
	return src[start[0] : start[0]+end+2], nil
}
