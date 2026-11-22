// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

var queryHotpluggable = qmpdef.Define[qmp.None, []map[string]any]("query-hotpluggable-cpus")

// TestAWorkspaceCheckpointedAndContinuedGrows is a workspace snapshotted with memory and continued,
// grown at once as spin grows one: spin's shape booted to systemd at 2048 MiB and logged into over
// SSH, checkpointed and continued, then given every vCPU, 8192 MiB and four disks. Each of
// SPIN_CPUPLUG_RUNS guests (default 10) has all three within growWithin of the plug.
//
// The vCPUs go through QEMU's ACPI CPU hotplug and the guest's GPE handler, the memory through
// virtio-mem and the disks through virtio-scsi, each right after the migration's cont (#433).
//
//	SPIN_CPUPLUG_TEST=1     it starts machines (task boot:cpu-plug)
//	SPIN_CPUPLUG_RUNS=N     guests, default 10
func TestAWorkspaceCheckpointedAndContinuedGrows(t *testing.T) {
	if os.Getenv("SPIN_CPUPLUG_TEST") == "" {
		t.Skip("set SPIN_CPUPLUG_TEST=1: this starts machines")
	}
	runs := envInt(t, "SPIN_CPUPLUG_RUNS", 10)
	x := newRollback(t)
	short := 0
	for i := range runs {
		if !x.grows(t, i) {
			short++
		}
	}
	if short > 0 {
		t.Errorf("%d of %d guests checkpointed and continued did not grow", short, runs)
	}
}

// growWithin is how long a guest is given to have what it was grown by. A guest had all of it
// within 1 s of the plug in 1100 checkpoints, and with 5% of one host CPU within 6 s (laptop,
// 2026-11-21).
const growWithin = 20 * time.Second

// grows boots a workspace, checkpoints and continues it, grows it and says whether its guest has
// every vCPU, all the memory and the four disks within growWithin.
func (x *rollback) grows(t *testing.T, i int) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Removed each run, not at the test's end: a run's state is the guest's RAM.
	dir, err := os.MkdirTemp(x.dir, "grow")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	w := x.r.workspace(t, x.signer, false)
	defer w.Kill()
	top := filepath.Join(dir, "top.qcow2")
	mustRun(t, filepath.Join(x.r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", "-b", x.frozen, "-F", "qcow2", top)
	w.boot(ctx, t, top, []string{x.frozen, x.raw})

	state, err := os.Create(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	// Closed once the guest has grown, never between the Continue and the plug. ext4 writes back
	// a state QEMU truncated on its first close, which took up to 75 s on a busy host. The plug
	// held behind that close reached the guest after its 60 s wait was over, which is what #433
	// saw (overhead lane, /var/tmp on ext4, 2026-11-21).
	defer func() { _ = state.Close() }()
	if _, _, err := w.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("checkpointing: %v", err)
	}
	if err := w.Continue(ctx); err != nil {
		t.Fatal(err)
	}

	plugged := time.Now()
	if err := w.SetCPUs(ctx, 16); err != nil {
		t.Fatal(err)
	}
	if err := w.SetMemory(ctx, 8192); err != nil {
		t.Fatal(err)
	}
	for d := range 4 {
		p := filepath.Join(dir, fmt.Sprintf("d%d.raw", d))
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, 1<<20); err != nil {
			t.Fatal(err)
		}
		disk, err := machine.OpenDisk(machine.DiskPath{Serial: fmt.Sprintf("d%d", d), Path: p, Format: "raw"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = disk.Close() }()
		if err := w.AttachDisk(ctx, disk); err != nil {
			t.Fatal(err)
		}
	}
	const has = `c=$(ls -d /sys/devices/system/cpu/cpu[0-9]* | wc -l)
m=$(awk '$1 == "MemTotal:" { print int($2 / 1024) }' /proc/meminfo)
d=$(ls /dev/sd? 2>/dev/null | wc -l)
echo "$c vCPUs, $m MiB, $d disks"
[ "$c" -eq 16 ] && [ "$m" -ge 8000 ] && [ "$d" -eq 4 ] && echo grown || true`
	got := ""
	for deadline := plugged.Add(growWithin); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if got = w.sh(t, has); strings.HasSuffix(got, "grown") {
			break
		}
	}
	t.Logf("#%d: %s %v after the plug", i, strings.ReplaceAll(got, "\n", ", "), time.Since(plugged).Round(time.Millisecond))
	if strings.HasSuffix(got, "grown") {
		return true
	}
	vcpus, _ := w.CPUs(ctx)
	slots, _ := qmp.Do(ctx, w.mon, queryHotpluggable, qmp.None{})
	taken := 0
	for _, s := range slots {
		if s["qom-path"] != nil {
			taken++
		}
	}
	t.Logf("#%d: QEMU has %d vCPUs in %d of %d slots; the guest:\n%s", i, vcpus, taken, len(slots),
		strings.TrimSpace(w.sh(t, "grep . /sys/firmware/acpi/interrupts/sci /sys/firmware/acpi/interrupts/gpe02 /sys/firmware/acpi/interrupts/error; grep -i acpi /proc/interrupts; dmesg | tail -40")))
	return false
}
