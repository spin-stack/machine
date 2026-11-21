// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"fmt"
)

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
