// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"path/filepath"
	"time"
)

// prestart is whether a QEMU started ahead of the workspace moves either of the goal's numbers.
// QEMU's own start - exec, the machine's creation, the reset that copies the kernel into memory -
// is ~30 ms nested, and a cold boot and a restore both pay it. A machine started --held or
// --restore pays it before anyone asks, and `spin-machine start` or `restore` is the request: it
// binds the workspace's overlay to its place and runs the machine, or loads a state into it.
//
// Four rows, to the first SSH login: a cold boot from before QEMU's exec; a prestarted boot from
// before `start`, its QEMU settled first; a state restored into a QEMU exec'd for it; and one
// restored into a QEMU started ahead with --restore. Every state is made before any restore, for
// Probe.Phases' reason. On the bench fleet: 320 against 296 ms booted, 145 against 126 restored
// (lab 44584824586).
//
// The tree's spin-machine has to have --held, which a release from before 2026-11-19 does not:
// make the tree with --spin-machine here.
type prestart struct{}

const (
	rowCold          = "cold boot"
	rowPrestarted    = "prestarted boot"
	rowRestored      = "restored"
	rowRestoredAhead = "restored into prestarted"
)

func (prestart) Name() string { return "prestart" }
func (prestart) About() string {
	return "ms to the first SSH login from before QEMU's exec, or for a prestarted QEMU from before the request"
}
func (prestart) Options() []Option { return append(shape, channelsOption) }
func (prestart) Rows(o Options) []string {
	return append([]string{rowCold, rowPrestarted}, withChannels(o, rowRestored, rowRestoredAhead)...)
}
func (prestart) Phases() int { return 2 }

func (prestart) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "held"}, Need{"boot", "restore"},
		Need{"save", "to"}, Need{"start", "disk"}, Need{"restore", "disk"}), append(channelsNeeds(o), plugNeeds(o)...)...)
}

func (prestart) Metrics() []Metric {
	return []Metric{
		{"running", "ms", "`start` or `restore` returned; nothing for a cold boot"},
		{"login", "ms", "the first SSH login"},
	}
}

// mutate-lane: TestEveryProbeMeasuresATree
func (prestart) Measure(_ context.Context, m *Measurement) error {
	way, channels, err := rowChannels(m.Row)
	if err != nil {
		return err
	}
	switch {
	case m.Phase == 0 && (way == rowRestored || way == rowRestoredAhead):
		return coldSave(m, channels...)
	case m.Phase == 1 && way == rowRestored:
		return restore(m, false, false, channels...)
	case m.Phase == 1 && way == rowRestoredAhead:
		return restoreAhead(m, channels...)
	case m.Phase == 0:
		return bootAhead(m)
	}
	return nil
}

// bootAhead is a cold or a prestarted boot to the first login.
// mutate-lane: TestEveryProbeMeasuresATree
func bootAhead(m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	c := &checkpoint{overlay: overlay, cid: NewCID()}
	qmp := filepath.Join(m.Socks, "m.qmp")
	if m.Row == rowCold {
		t0 := time.Now()
		mc, err := m.Launch(nil, m.BootArgs(overlay, c.bootFlags(m, qmp)...)...)
		if err != nil {
			return err
		}
		defer mc.Stop()
		plugged := m.Plug(qmp)
		login, err := m.Key.LoggedIn(mc, c.cid, t0)
		if err != nil {
			return err
		}
		if err := plugged(); err != nil {
			return err
		}
		m.RecordMs("login", login)
		return nil
	}
	mc, err := m.Launch(nil, m.BootArgs(overlay, c.bootFlags(m, qmp, "--held")...)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	if err := settled(qmp, "prelaunch"); err != nil {
		return err
	}
	t0 := time.Now()
	if err := m.SpinMachine("start", "--release", m.Tree.Dir, "--qmp", qmp, "--disk", overlay); err != nil {
		return err
	}
	m.RecordMs("running", time.Since(t0))
	plugged := m.Plug(qmp)
	login, err := m.Key.LoggedIn(mc, c.cid, t0)
	if err != nil {
		return err
	}
	if err := plugged(); err != nil {
		return err
	}
	m.RecordMs("login", login)
	return nil
}

// restoreAhead loads the saved state into a QEMU started ahead with --restore, with restore's
// --channels when the row names them.
// mutate-lane: TestEveryProbeMeasuresATree
func restoreAhead(m *Measurement, channels ...string) error {
	c := m.State.(*checkpoint)
	if err := evict(c.state); err != nil {
		return err
	}
	qmp := filepath.Join(m.Socks, "prestarted.qmp")
	mc, err := m.Launch(nil, m.BootArgs(c.overlay, c.bootFlags(m, qmp, "--restore")...)...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	if err := settled(qmp, "inmigrate"); err != nil {
		return err
	}
	t0 := time.Now()
	if err := m.SpinMachine(append([]string{"restore", "--release", m.Tree.Dir, "--qmp", qmp, "--disk", c.overlay, "--from", c.state}, channels...)...); err != nil {
		return err
	}
	m.RecordMs("running", time.Since(t0))
	login, err := m.Key.LoggedIn(mc, c.cid, t0)
	if err != nil {
		return err
	}
	m.RecordMs("login", login)
	return nil
}
