// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
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

// cpuSlot is one place for a vCPU, as query-hotpluggable-cpus reports it.
type cpuSlot struct {
	Type    string         `json:"type"`
	QOMPath string         `json:"qom-path,omitempty"`
	Props   map[string]any `json:"props"`
}

// addedCPU is a vCPU added after exec, as a checkpoint records it to add it again before the
// state is loaded: the stream names each vCPU, and one that is not there fails the load.
type addedCPU struct {
	ID     string         `json:"id"`
	Driver string         `json:"driver"`
	Props  map[string]any `json:"props"`
}

// cpuTopology is every property a vCPU's slot is named by: all a checkpoint's vCPU may set.
var cpuTopology = []string{"node-id", "socket-id", "die-id", "cluster-id", "module-id", "core-id", "thread-id"}

// deviceArgs is the device_add that adds c again, refused unless it is a vCPU in a slot.
//
// The record is what the saved machine's QEMU answered, and device_add attaches whatever device
// and properties it is given: a QEMU the guest had taken over could have answered with another
// device, or a vCPU with properties of its choosing, for the machine that resumes it to add. So
// the driver has to be a vCPU's and every property a slot's coordinate, a number, and neither
// can name the driver or the id (spin-stack/spin#1367's review; spin's own client refused the
// same before it moved here).
func (c addedCPU) deviceArgs() (map[string]any, error) {
	if !strings.HasSuffix(c.Driver, cpuSuffix) || c.ID == "" {
		return nil, fmt.Errorf("the checkpoint records a vCPU %q of type %q, which is not a vCPU", c.ID, c.Driver)
	}
	args := map[string]any{"driver": c.Driver, "id": c.ID}
	for k, v := range c.Props {
		if _, number := v.(float64); !number || !slices.Contains(cpuTopology, k) {
			return nil, fmt.Errorf("the checkpoint gives vCPU %s the property %s=%v, which names no slot", c.ID, k, v)
		}
		args[k] = v
	}
	return args, nil
}

func (m *QEMU) cpuSlots(ctx context.Context) ([]cpuSlot, error) {
	var slots []cpuSlot
	if err := m.q.run(ctx, "query-hotpluggable-cpus", nil, &slots); err != nil {
		return nil, fmt.Errorf("listing the machine's vCPU slots: %w", err)
	}
	// QEMU lists them highest first; added in this order, vCPU n is the n-th.
	slices.Reverse(slots)
	return slots, nil
}

// SetCPUs adds vCPUs into the first free slots, or removes the last ones added, until there are
// n. A removal returns once QEMU has let the vCPU go, which waits on the guest offlining it.
func (m *QEMU) SetCPUs(ctx context.Context, n int) error {
	slots, err := m.cpuSlots(ctx)
	if err != nil {
		return err
	}
	if n < 1 || n > len(slots) {
		return fmt.Errorf("%d vCPUs, and the machine has slots for 1 to %d", n, len(slots))
	}
	have := 0
	for _, s := range slots {
		if s.QOMPath != "" {
			have++
		}
	}
	for i, s := range slots {
		if have >= n {
			break
		}
		if s.QOMPath != "" {
			continue
		}
		args := map[string]any{"driver": s.Type, "id": fmt.Sprintf("vcpu%d", i)}
		for k, v := range s.Props {
			args[k] = v
		}
		if err := m.q.run(ctx, "device_add", args, nil); err != nil {
			return fmt.Errorf("adding vCPU %d: %w", i, err)
		}
		have++
	}
	for _, s := range slices.Backward(slots) {
		if have <= n {
			break
		}
		id, ok := strings.CutPrefix(s.QOMPath, peripheral)
		if !ok {
			// Boot vCPUs are not devices anyone added, and cannot be removed.
			continue
		}
		if err := m.q.run(ctx, "device_del", map[string]any{"id": id}, nil); err != nil {
			return fmt.Errorf("removing vCPU %s: %w", id, err)
		}
		if err := m.q.waitDeleted(ctx, id); err != nil {
			return fmt.Errorf("removing vCPU %s: %w", id, err)
		}
		have--
	}
	if have != n {
		return fmt.Errorf("%d vCPUs, and only %d can be removed: the rest are the ones the machine was started with", n, have)
	}
	return nil
}

// addedCPUs are the vCPUs added since exec: QEMU puts a device with an id under
// /machine/peripheral, and the boot vCPUs under /machine/unattached.
func (m *QEMU) addedCPUs(ctx context.Context) ([]addedCPU, error) {
	slots, err := m.cpuSlots(ctx)
	if err != nil {
		return nil, err
	}
	var out []addedCPU
	for _, s := range slots {
		if id, ok := strings.CutPrefix(s.QOMPath, peripheral); ok {
			out = append(out, addedCPU{ID: id, Driver: s.Type, Props: s.Props})
		}
	}
	return out, nil
}

const memPath = peripheral + VirtioMemID

// mib is a MiB in bytes, which QEMU counts memory in.
const mib = 1 << 20

// SetMemory asks virtio-mem for what is above the memory the machine booted with.
func (m *QEMU) SetMemory(ctx context.Context, mb int) error {
	var summary struct {
		Base int64 `json:"base-memory"`
	}
	if err := m.q.run(ctx, "query-memory-size-summary", nil, &summary); err != nil {
		return fmt.Errorf("reading the machine's memory: %w", err)
	}
	var ceiling int64
	// The backend's size, which is the ceiling: the device's own size is what it has plugged.
	if err := m.q.run(ctx, "qom-get", map[string]any{"path": "/objects/" + memGrowthID, "property": "size"}, &ceiling); err != nil {
		return fmt.Errorf("reading how far the machine's memory may grow: %w", err)
	}
	want := int64(mb)*mib - summary.Base
	if want < 0 || want > ceiling {
		return fmt.Errorf("%d MiB, and the machine has %d MiB to %d MiB", mb, summary.Base/mib, (summary.Base+ceiling)/mib)
	}
	if err := m.q.run(ctx, "qom-set", map[string]any{"path": memPath, "property": "requested-size", "value": want}, nil); err != nil {
		return fmt.Errorf("asking for %d MiB: %w", mb, err)
	}
	return nil
}

// Memory is the memory the guest boots with and what virtio-mem has plugged since.
func (m *QEMU) Memory(ctx context.Context) (int, error) {
	var summary struct {
		Base    int64 `json:"base-memory"`
		Plugged int64 `json:"plugged-memory"`
	}
	if err := m.q.run(ctx, "query-memory-size-summary", nil, &summary); err != nil {
		return 0, fmt.Errorf("reading the machine's memory: %w", err)
	}
	return int((summary.Base + summary.Plugged) / mib), nil
}
