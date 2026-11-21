// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
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

// Quit ends QEMU. Its reply may never arrive, because QEMU can close the monitor before sending
// it, so a connection that ends instead is the quit having worked.
func (m *QEMU) Quit(ctx context.Context) error {
	err := m.q.run(ctx, "quit", nil, nil)
	if err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "EOF") {
		return fmt.Errorf("quitting: %w", err)
	}
	return nil
}

// status is QEMU's run state: prelaunch for a machine held for a boot (-S), inmigrate for one
// held for a checkpoint (-incoming defer).
func (m *QEMU) status(ctx context.Context) (string, error) {
	var st struct {
		Status string `json:"status"`
	}
	if err := m.q.run(ctx, "query-status", nil, &st); err != nil {
		return "", err
	}
	return st.Status, nil
}

// Boot binds the disks to their places and lets the vCPUs go.
func (m *QEMU) Boot(ctx context.Context, disks ...Disk) error {
	st, err := m.status(ctx)
	if err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if st != "prelaunch" {
		// inmigrate takes a cont and stays inmigrate: QEMU only notes that the machine is to
		// run once a migration has arrived. A machine for a boot is started without
		// Spec.Restore.
		return fmt.Errorf("booting a machine that is %s: only one started held for a boot boots", st)
	}
	if err := m.bindAll(ctx, disks, nil); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if err := m.q.run(ctx, "cont", nil, nil); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	return nil
}

// Continue runs a machine a checkpoint left stopped.
func (m *QEMU) Continue(ctx context.Context) error {
	if err := m.q.run(ctx, "cont", nil, nil); err != nil {
		return fmt.Errorf("continuing the machine: %w", err)
	}
	return nil
}

const peripheral = "/machine/peripheral/"
