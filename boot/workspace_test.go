// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"os"
	"testing"

	"github.com/spin-stack/machine/machine"
)

// A correctness test, including under TCG: what a workspace expects of the image (the journal,
// timers, apt, locales, docker) works in a booted guest, and what docs/image.md says the image
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
	t.Log(checkOnConsole(t, rel, root, "WORKSPACE_CHECK_OK", "WORKSPACE_CHECK_FAILED"))
}
