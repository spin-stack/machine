// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spin-stack/spin-machine/machine"
)

// CompatRow is what one release's checkpoint does on this machine.
type CompatRow struct {
	Release string
	// Resumed is whether a checkpoint the release saved went on running here; Moved, that this
	// machine refused it (machine.CanResume), so none of the release's checkpoints is meant to.
	// NoBzImage is a release from before v20261119.02, which carries only the vmlinux the machine
	// entered through PVH then: nothing here boots it, so nothing checks it.
	Resumed, Moved, NoBzImage bool
	// Why is the failure, or the refusal: every difference between the two machines.
	Why string
}

// Compat is boot:compat over the releases, newest first, until one whose machine is not this one:
// a host that skipped releases resumes a checkpoint from any of them, not only the one before. A
// release's checkpoint is taken at spin's shape on its own QEMU, kernel and firmware, and this
// machine refuses it or resumes it (resume); the first it refuses, by its differences, is where
// the machine moved, and every older release is past it. It stops as well at the first release
// with no bzImage, which no spin-machine since v20261119.02 boots, and every older release has
// none either. For each release it also holds the other direction: a checkpoint written by here's
// QEMU is one CanResume refuses on a release whose QEMU is older (QEMU loads what an older version
// wrote, never the reverse).
//
// resume runs boot:compat's test with previous as the release before, and says the refusal when
// this machine refused the checkpoint, or the failure when it did not go on running.
func Compat(here string, tags []string, src Sources, resume func(previous string) (refused string, err error), log io.Writer) ([]CompatRow, error) {
	hereQEMU, err := qemuVersion(filepath.Join(here, "bin", "qemu-system-x86_64"))
	if err != nil {
		return nil, err
	}
	var rows []CompatRow
	for _, tag := range tags {
		dir, err := src.release(tag)
		if err != nil {
			return nil, err
		}
		if !fileExists(filepath.Join(dir, partFiles["kernel"])) {
			rows = append(rows, CompatRow{Release: tag, NoBzImage: true})
			break
		}
		relQEMU, err := qemuVersion(filepath.Join(dir, "bin", "qemu-system-x86_64"))
		if err != nil {
			return nil, err
		}
		if err := oneWay(hereQEMU, relQEMU); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		_, _ = fmt.Fprintf(log, "compat: a checkpoint of %s, resumed here\n", tag)
		refused, err := resume(dir)
		switch {
		case refused != "":
			rows = append(rows, CompatRow{Release: tag, Moved: true, Why: refused})
			return rows, nil
		case err != nil:
			rows = append(rows, CompatRow{Release: tag, Why: err.Error()})
		default:
			rows = append(rows, CompatRow{Release: tag, Resumed: true})
		}
	}
	return rows, nil
}

// oneWay holds CanResume to QEMU's promise between here's QEMU and a release's: a release's
// checkpoint resumes here, and a checkpoint of here's newer QEMU is refused on the release's. The
// two machines are otherwise one, so the VMM is all that differs.
func oneWay(here, release string) error {
	on := func(vmm string) machine.Target {
		return machine.Target{VMM: vmm, Formats: []string{machine.FormatMappedRAM, machine.FormatStream}}
	}
	if err := machine.CanResume(machine.Saved{Format: machine.FormatMappedRAM, From: on(release)}, on(here)); err != nil {
		return fmt.Errorf("a checkpoint of %s cannot resume on this machine's %s: %w", release, here, err)
	}
	back := machine.CanResume(machine.Saved{Format: machine.FormatMappedRAM, From: on(here)}, on(release))
	if here != release && back == nil {
		return fmt.Errorf("CanResume lets a checkpoint of %s onto the older %s", here, release)
	}
	return nil
}

// qemuVersion is the QEMU at path as a checkpoint records it (machine.Saved.From.VMM): "qemu 11.1.90",
// from its own --version, which is what query-version answers.
func qemuVersion(path string) (string, error) {
	b, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("asking %s its version: %w", path, err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	v, ok := strings.CutPrefix(strings.TrimSpace(first), "QEMU emulator version ")
	if !ok {
		return "", fmt.Errorf("%s says %q, not its version", path, first)
	}
	v, _, _ = strings.Cut(v, " ")
	return "qemu " + v, nil
}

// CompatTable is the rows as the run's summary carries them.
func CompatTable(rows []CompatRow) string {
	var b strings.Builder
	b.WriteString("| release | a checkpoint of it, resumed here |\n|---|---|\n")
	for _, r := range rows {
		switch {
		case r.Moved:
			fmt.Fprintf(&b, "| %s | not meant to: its machine is not this one (%s); older releases neither |\n", r.Release, r.Why)
		case r.NoBzImage:
			fmt.Fprintf(&b, "| %s | not checked: it carries no bzImage, the only kernel this machine boots; older releases neither |\n", r.Release)
		case r.Resumed:
			fmt.Fprintf(&b, "| %s | resumed |\n", r.Release)
		default:
			fmt.Fprintf(&b, "| %s | **failed**: %s |\n", r.Release, r.Why)
		}
	}
	return b.String()
}
