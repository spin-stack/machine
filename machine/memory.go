// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

var (
	queryMemorySizeSummary = qmpdef.Define[qmp.None, memorySize]("query-memory-size-summary")
	// qom-get answers any, and qom-set takes any: what a property holds is its reader's to know.
	qomGet = qmpdef.Define[qomGetArgs, json.RawMessage]("qom-get")
	qomSet = qmpdef.Define[qomSetArgs, qmp.None]("qom-set")
)

type memorySize struct {
	Base    int64 `json:"base-memory"`
	Plugged int64 `json:"plugged-memory"`
}

type qomGetArgs struct {
	Path     string `json:"path"`
	Property string `json:"property"`
}

type qomSetArgs struct {
	Path     string `json:"path"`
	Property string `json:"property"`
	Value    any    `json:"value"`
}

// readProperty is the value of the property of the object at path, as a T.
func readProperty[T any](ctx context.Context, m *QEMU, path, property string) (T, error) {
	var v T
	raw, err := do(ctx, m.q, qomGet, qomGetArgs{Path: path, Property: property})
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("reading %s's %s: %w", path, property, err)
	}
	return v, nil
}

const memPath = peripheral + VirtioMemID

// mib is a MiB in bytes, which QEMU counts memory in.
const mib = 1 << 20

// SetMemory asks virtio-mem for what is above the memory the machine booted with.
func (m *QEMU) SetMemory(ctx context.Context, mb int) error {
	summary, err := do(ctx, m.q, queryMemorySizeSummary, qmp.None{})
	if err != nil {
		return fmt.Errorf("reading the machine's memory: %w", err)
	}
	// The backend's size, which is the ceiling: the device's own size is what it has plugged.
	ceiling, err := readProperty[int64](ctx, m, "/objects/"+memGrowthID, "size")
	if err != nil {
		return fmt.Errorf("reading how far the machine's memory may grow: %w", err)
	}
	want := int64(mb)*mib - summary.Base
	if want < 0 || want > ceiling {
		return fmt.Errorf("%d MiB, and the machine has %d MiB to %d MiB", mb, summary.Base/mib, (summary.Base+ceiling)/mib)
	}
	if _, err := do(ctx, m.q, qomSet, qomSetArgs{Path: memPath, Property: "requested-size", Value: want}); err != nil {
		return fmt.Errorf("asking for %d MiB: %w", mb, err)
	}
	return nil
}

// Memory is the memory the guest boots with and what virtio-mem has plugged since.
func (m *QEMU) Memory(ctx context.Context) (int, error) {
	summary, err := do(ctx, m.q, queryMemorySizeSummary, qmp.None{})
	if err != nil {
		return 0, fmt.Errorf("reading the machine's memory: %w", err)
	}
	return int((summary.Base + summary.Plugged) / mib), nil
}
