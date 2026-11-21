// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// QEMU is a machine running under QEMU, reached through one of its monitors. It is one caller's
// at a time: Target keeps what it read without a lock, and nothing orders what two callers would
// do to the machine at once.
type QEMU struct {
	c *qmp.Client
	// record is what Target read, once it has.
	record *Target
}

var _ VM = (*QEMU)(nil)

// Attach takes over a QMP client of a machine started from a Spec's Args, and returns the
// machine. It asks QEMU nothing. The client's connection has to pass descriptors, as qmp.Unix
// does, because disks and states are handed to QEMU as descriptors over it.
//
// It takes no Spec: what it needs - which places for disks the machine has and what each is,
// whether it is held for a boot or for a checkpoint, its memory and vCPU slots - it asks QEMU,
// which cannot disagree with the command line it was started from the way a second copy of the
// Spec could.
func Attach(c *qmp.Client) *QEMU { return &QEMU{c: c} }

// Close lets the monitor go. The machine runs on: a monitor's client leaving is nothing to QEMU.
func (m *QEMU) Close() error { return m.c.Close() }

var (
	quit        = qmpdef.Define[qmp.None, qmp.None]("quit")
	queryStatus = qmpdef.Define[qmp.None, runStatus]("query-status")
	// deviceAdd's arguments are a device's driver, its id and its own properties, which QEMU's
	// schema does not list: device_add takes any property the device has. So a map is what they
	// are, and Check holds the command, not the properties, to QEMU.
	deviceAdd     = qmpdef.Define[map[string]any, qmp.None]("device_add")
	deviceDel     = qmpdef.Define[deviceID, qmp.None]("device_del")
	deviceDeleted = qmpdef.DefineEvent[deviceGone]("DEVICE_DELETED")
)

// deviceGone is DEVICE_DELETED's data: the id of the device gone, which a device added without one
// has none of.
type deviceGone struct {
	Device string `json:"device"`
}

type runStatus struct {
	Status runState `json:"status"`
}

// runState is QEMU's run state, of the ones the machine tells apart: prelaunch for a machine held
// for a boot (-S), inmigrate for one held for a checkpoint (-incoming defer), and paused for one
// stopped.
type runState string

const (
	prelaunch runState = "prelaunch"
	inmigrate runState = "inmigrate"
	paused    runState = "paused"
)

func init() { qmpdef.Enum(prelaunch, inmigrate, paused) }

type deviceID struct {
	ID string `json:"id"`
}

// Quit ends QEMU. Its reply may never arrive, because QEMU can close the monitor before sending
// it, so a connection that ends instead is the quit having worked.
func (m *QEMU) Quit(ctx context.Context) error {
	if _, err := qmp.Do(ctx, m.c, quit, qmp.None{}); err != nil && !errors.Is(err, qmp.ErrClosed) {
		return fmt.Errorf("quitting: %w", err)
	}
	return nil
}

func (m *QEMU) status(ctx context.Context) (runState, error) {
	st, err := qmp.Do(ctx, m.c, queryStatus, qmp.None{})
	return st.Status, err
}

// unplug removes the device id and returns once QEMU has reported it gone. The wait is armed
// before device_del, whose answer the event may come with, and sees no DEVICE_DELETED of an
// earlier device with the same id.
func (m *QEMU) unplug(ctx context.Context, id string) error {
	gone := qmp.Expect(m.c, deviceDeleted, func(d deviceGone) bool { return d.Device == id })
	defer gone.Stop()
	if _, err := qmp.Do(ctx, m.c, deviceDel, deviceID{id}); err != nil {
		return err
	}
	if _, err := gone.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for %s to go: %w", id, err)
	}
	return nil
}

// Boot binds the disks to their places and lets the vCPUs go.
func (m *QEMU) Boot(ctx context.Context, disks ...Disk) error {
	st, err := m.status(ctx)
	if err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if st != prelaunch {
		// inmigrate takes a cont and stays inmigrate: QEMU only notes that the machine is to
		// run once a migration has arrived. A machine for a boot is started without
		// Spec.Restore.
		return fmt.Errorf("booting a machine that is %s: only one started held for a boot boots", st)
	}
	if err := m.bindAll(ctx, disks, nil); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if _, err := qmp.Do(ctx, m.c, qmp.Cont, qmp.None{}); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	return nil
}

// Continue runs a machine a checkpoint left stopped.
func (m *QEMU) Continue(ctx context.Context) error {
	if _, err := qmp.Do(ctx, m.c, qmp.Cont, qmp.None{}); err != nil {
		return fmt.Errorf("continuing the machine: %w", err)
	}
	return nil
}

const peripheral = "/machine/peripheral/"
