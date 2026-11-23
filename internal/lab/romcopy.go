// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// romCopy is an experiment's probe (#698): how long QEMU's reset takes to copy the kernel's ELF
// segments from their ROM blobs into guest RAM, on a cold boot with QEMU traced. The copy is
// timed from the legacy reset handler that does it (the last resettable_phase_hold_exec before the
// first ELF segment's loader_write_rom) to the last ELF segment's loader_write_rom.
type romCopy struct{}

func (romCopy) Name() string { return "romcopy" }
func (romCopy) About() string {
	return "QEMU's reset copying the kernel's ELF segments into guest RAM, traced, on a cold boot from before QEMU's exec"
}
func (romCopy) Options() []Option     { return shape }
func (romCopy) Rows(Options) []string { return []string{"boot"} }
func (romCopy) Phases() int           { return 1 }

func (romCopy) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "qemu"}), plugNeeds(o)...)
}

func (romCopy) Metrics() []Metric {
	return []Metric{
		{"first_login", "ms", "the traced cold boot's first login, from before the exec"},
		{"rom_start", "ms", "from before the exec to the reset handler that copies the ELF segments"},
		{"rom_copy", "ms", "that handler to the last ELF segment written"},
		{"rom_seg0", "ms", "of rom_copy, the first ELF segment (text and rodata)"},
		{"ready", "ms", "from before the exec to QEMU's main loop taking its first QMP command"},
		{"cont", "ms", "from before the exec to cont taken"},
	}
}

const romCopyEvents = qmpTraceEvents + "loader_write_rom\nresettable_phase_hold_exec\n"

// mutate-lane: TestEveryProbeMeasuresATree
func (romCopy) Measure(_ context.Context, m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	events := filepath.Join(m.Dir, "rom-events")
	if err := os.WriteFile(events, []byte(romCopyEvents), 0o600); err != nil {
		return fmt.Errorf("writing the trace's events: %w", err)
	}
	qemu, err := filepath.EvalSymlinks(filepath.Join(m.Tree.Dir, "bin", "qemu-system-x86_64"))
	if err != nil {
		return fmt.Errorf("finding the tree's QEMU: %w", err)
	}
	wrapper := filepath.Join(m.Dir, "qemu-rom")
	script := fmt.Sprintf("#!/bin/sh\nexec %s -msg timestamp=on -D %s/qemu-$$.log -trace events=%s \"$@\"\n", qemu, m.Dir, events)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { // #nosec G306 -- a script this process runs
		return fmt.Errorf("writing the traced QEMU: %w", err)
	}
	cid := NewCID()
	sock := filepath.Join(m.Socks, "rom.qmp")
	args := []string{"--vsock-cid", fmt.Sprint(cid), "--qmp", sock, "--console", "file:" + sock + ".console",
		"--append", m.Tree.Cmdline(m.Key.Init()), "--qemu=" + wrapper}
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	plugged := m.Plug(sock)
	login, err := m.Key.LoggedIn(mc, cid, t0)
	if err != nil {
		return err
	}
	if err := plugged(); err != nil {
		return err
	}
	m.RecordMs("first_login", login)
	b, err := os.ReadFile(filepath.Join(m.Dir, fmt.Sprintf("qemu-%d.log", mc.PID())))
	if err != nil {
		return fmt.Errorf("reading QEMU's trace: %w", err)
	}
	var hold, start, seg0, last time.Time
	for line := range strings.Lines(string(b)) {
		stamp, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		at, err := time.Parse("2006-01-02T15:04:05.000000Z", stamp)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "resettable_phase_hold_exec") && start.IsZero():
			hold = at
		case strings.HasPrefix(rest, "loader_write_rom") && strings.Contains(rest, "ELF program header segment"):
			if start.IsZero() {
				start, seg0 = hold, at
			}
			last = at
		}
	}
	if start.IsZero() {
		return Miss("QEMU's trace holds no ELF segment written")
	}
	m.RecordMs("rom_start", start.Sub(t0))
	m.RecordMs("rom_copy", last.Sub(start))
	m.RecordMs("rom_seg0", seg0.Sub(start))
	t := parseQMPTrace(string(b))
	if len(t.Commands) > 0 {
		m.RecordMs("ready", t.Commands[0].Taken.Sub(t0))
		for _, c := range t.Commands {
			if c.Name == "cont" {
				m.RecordMs("cont", c.Taken.Sub(t0))
				break
			}
		}
	}
	return nil
}
