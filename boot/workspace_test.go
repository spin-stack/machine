// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// A correctness test, including under TCG: what a workspace expects of the image (the journal,
// timers, apt, locales, time zones, docker, a disk attached and formatted) works in a booted guest, and what docs/image.md says the image
// gives up is still given up. It edits a private raw copy, without mounts or NBD.
func TestAWorkspaceHasWhatItExpects(t *testing.T) {
	if os.Getenv("SPIN_WORKSPACE_TEST") != "1" {
		t.Skip("set SPIN_WORKSPACE_TEST=1 to check a workspace's expectations in a built image")
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
	oneshotAtBoot(t, root, "workspace-check", "workspace-check.sh", "")
	empty := filepath.Join(t.TempDir(), "ws.raw")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(empty, 64<<20); err != nil {
		t.Fatal(err)
	}
	attached := false
	t.Log(checkGuest(t, rel, root, "WORKSPACE_CHECK_OK", "WORKSPACE_CHECK_FAILED",
		func(s *machine.Spec) { s.HotplugDisks = 1 },
		func(m *machine.QEMU, console string) {
			if attached || !strings.Contains(console, "WS waiting for a disk") {
				return
			}
			attached = true
			d, err := machine.OpenDisk(machine.DiskPath{Serial: "ws", Path: empty, Format: "raw"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := m.AttachDisk(ctx, d); err != nil {
				t.Fatalf("attaching the empty disk: %v", err)
			}
		}))
}
