// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"sync"
	"time"

	"github.com/spin-stack/machine/qmp"
)

// Phase is a step of a checkpoint or a resume, told as it happens to SaveOptions.Phases or
// LoadOptions.Phases. A checkpoint is SaveStarted, RAMWritten while the guest runs, GuestStopped,
// Sealed, SaveCompleted, then GuestRunning from Continue or GuestHeld from Hold. A resume is
// LoadStarted, StateLoaded and GuestRunning; a fast one runs the guest first, and Loaded tells
// StateLoaded. docs/machine.md, "Phases", says what the gaps between them are.
type Phase interface {
	// When is when the host learned of it.
	When() time.Time
	phase()
}

// SaveStarted is QEMU having taken the migration: it writes the guest's RAM while the guest runs.
type SaveStarted struct{ At time.Time }

// RAMWritten is how much of the guest's RAM QEMU has written, and the RAM of the machine's blocks
// at their ceilings, most of which a guest never touches. A page the guest dirties again is
// written again. It is told at most every progressEvery while the guest runs.
type RAMWritten struct {
	At           time.Time
	Bytes, Total int64
}

// GuestStopped is the guest stopped and its disks quiet, before the RAM it dirtied last and its
// device state are written: QEMU's pre-switchover, where the seal runs.
type GuestStopped struct{ At time.Time }

// Sealed is the caller's seal having returned, after Took.
type Sealed struct {
	At   time.Time
	Took time.Duration
}

// SaveCompleted is QEMU having written the whole state, At when it said so, and what it cost.
type SaveCompleted struct {
	At   time.Time
	Cost Cost
}

// GuestRunning is the guest running again: cont returned after a checkpoint, or the resumed guest
// runs.
type GuestRunning struct{ At time.Time }

// GuestHeld is a checkpointed machine's disks taken back with the guest left stopped (Hold).
type GuestHeld struct{ At time.Time }

// LoadStarted is QEMU having taken the state to load.
type LoadStarted struct{ At time.Time }

// StateLoaded is the whole state read.
type StateLoaded struct{ At time.Time }

func (p SaveStarted) When() time.Time   { return p.At }
func (p RAMWritten) When() time.Time    { return p.At }
func (p GuestStopped) When() time.Time  { return p.At }
func (p Sealed) When() time.Time        { return p.At }
func (p SaveCompleted) When() time.Time { return p.At }
func (p GuestRunning) When() time.Time  { return p.At }
func (p GuestHeld) When() time.Time     { return p.At }
func (p LoadStarted) When() time.Time   { return p.At }
func (p StateLoaded) When() time.Time   { return p.At }
func (SaveStarted) phase()              {}
func (RAMWritten) phase()               {}
func (GuestStopped) phase()             {}
func (Sealed) phase()                   {}
func (SaveCompleted) phase()            {}
func (GuestRunning) phase()             {}
func (GuestHeld) phase()                {}
func (LoadStarted) phase()              {}
func (StateLoaded) phase()              {}

// progressEvery bounds how often a checkpoint asks QEMU how much RAM it has written, which it
// asks only for a caller that takes the phases.
const progressEvery = 100 * time.Millisecond

// tell tells p of a phase, when the caller takes them.
func tell(p func(Phase), ph Phase) {
	if p != nil {
		p(ph)
	}
}

// progress tells p how much RAM the save has written, every progressEvery, until the returned
// stop, which returns once nothing more will be told. With no p it asks QEMU nothing.
func (m *QEMU) progress(ctx context.Context, p func(Phase)) (stop func()) {
	if p == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var done sync.WaitGroup
	done.Go(func() {
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			info, err := qmp.Do(ctx, m.c, queryMigrate, qmp.None{})
			if err != nil || ctx.Err() != nil {
				continue
			}
			p(RAMWritten{At: time.Now(), Bytes: info.RAM.Transferred, Total: info.RAM.Total})
		}
	})
	return func() {
		cancel()
		done.Wait()
	}
}
