// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// An experiment (#514, exp-batch3, never merged): a guest in spin's shape, 1 vCPU, 128 MiB sized to
// 2048 right after its start, runs docker builds for 10 minutes and says on its console how much of
// its volatile journal it kept.
func TestExpJournalOfABusyWorkspace(t *testing.T) {
	if os.Getenv("SPIN_EXP_JOURNAL") != "1" {
		t.Skip("set SPIN_EXP_JOURNAL=1")
	}
	out := releaseTree(t)
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}
	root := newRawRoot(t, out, base)
	oneshotAtBoot(t, root, "journal-check", "exp-journal-check.sh", "TimeoutStartSec=infinity")
	checkTimeout = 20 * time.Minute
	plugged := false
	t.Log(checkGuest(t, rel, root, "JOURNAL_CHECK_OK", "JOURNAL_CHECK_FAILED",
		func(s *machine.Spec) { s.BootCPUs, s.Memory.BaseMB, s.Memory.CeilingMB = 1, 128, 8192 },
		func(m *machine.QEMU, _ string) {
			if plugged {
				return
			}
			plugged = true
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.SetMemory(ctx, 2048); err != nil {
				t.Fatalf("sizing the guest to 2048 MiB: %v", err)
			}
		}))
}
