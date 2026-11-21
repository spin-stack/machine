// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// QEMU is a machine running under QEMU, reached through one of its monitors. Like the monitor, it
// is one caller's at a time.
type QEMU struct {
	q *qmpClient
	// record is what Target read, once it has.
	record *Target
}

var _ VM = (*QEMU)(nil)

// Attach takes over a QMP monitor of a machine started from a Spec's Args, and returns the
// machine. The connection is a Unix socket because disks and states are handed to QEMU as
// descriptors over it.
//
// It takes no Spec: what it needs - which places for disks the machine has and what each is,
// whether it is held for a boot or for a checkpoint, its memory and vCPU slots - it asks QEMU,
// which cannot disagree with the command line it was started from the way a second copy of the
// Spec could.
func Attach(conn *net.UnixConn) (*QEMU, error) {
	q, err := newQMP(conn)
	if err != nil {
		return nil, err
	}
	return &QEMU{q: q}, nil
}

// Close lets the monitor go. The machine runs on: a monitor's client leaving is nothing to QEMU.
func (m *QEMU) Close() error { return m.q.c.Close() }

var (
	quit        = qmpdef.Define[qmp.None, qmp.None]("quit")
	queryStatus = qmpdef.Define[qmp.None, runStatus]("query-status")
	// deviceAdd's arguments are a device's driver, its id and its own properties, which QEMU's
	// schema does not list: device_add takes any property the device has. So a map is what they
	// are, and Check holds the command, not the properties, to QEMU.
	deviceAdd = qmpdef.Define[map[string]any, qmp.None]("device_add")
	deviceDel = qmpdef.Define[deviceID, qmp.None]("device_del")
)

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
	_, err := do(ctx, m.q, quit, qmp.None{})
	if err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "EOF") {
		return fmt.Errorf("quitting: %w", err)
	}
	return nil
}

func (m *QEMU) status(ctx context.Context) (runState, error) {
	st, err := do(ctx, m.q, queryStatus, qmp.None{})
	return st.Status, err
}

// unplug removes the device id and returns once QEMU has reported it gone. The event may arrive
// with device_del's own reply, so it is looked for there first.
func (m *QEMU) unplug(ctx context.Context, id string) error {
	m.q.removing, m.q.removed = id, false
	if _, err := do(ctx, m.q, deviceDel, deviceID{id}); err != nil {
		return err
	}
	for !m.q.removed {
		// A query to read what QEMU has sent meanwhile, events included.
		if _, err := m.status(ctx); err != nil {
			return fmt.Errorf("waiting for %s to go: %w", id, err)
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return fmt.Errorf("waiting for %s to go: %w", id, err)
		}
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
	if _, err := do(ctx, m.q, qmp.Cont, qmp.None{}); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	return nil
}

// Continue runs a machine a checkpoint left stopped.
func (m *QEMU) Continue(ctx context.Context) error {
	if _, err := do(ctx, m.q, qmp.Cont, qmp.None{}); err != nil {
		return fmt.Errorf("continuing the machine: %w", err)
	}
	return nil
}

const peripheral = "/machine/peripheral/"
