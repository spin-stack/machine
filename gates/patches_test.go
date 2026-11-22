// SPDX-License-Identifier: Apache-2.0

package gates_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every kernel and QEMU patch is git format-patch output against the pinned tarball, with a plain
// "[PATCH]" subject: `git format-patch -N` from a git tree of the tarball with the series applied,
// less the mbox "From <commit>" line, a commit of nobody's repository that the pin gate would read
// as a pin. A patch written by hand applies with `patch` and is read less well by `git am` and
// checkpatch (#487), and a "[PATCH 7/11]" outlives the series it counted: three of the kernel's
// were deleted and the numbers stayed.
func TestPatchesAreFormatPatchOutput(t *testing.T) {
	header := regexp.MustCompile(`\AFrom: .+\nDate: .+\nSubject: \[PATCH\] \S`)
	for _, dir := range []string{"kernel/patches", "qemu/patches"} {
		patches, err := filepath.Glob(filepath.Join(root, dir, "*.patch"))
		if err != nil {
			t.Fatal(err)
		}
		if len(patches) == 0 {
			t.Errorf("no patches in %s: the check reads nothing", dir)
		}
		for _, p := range patches {
			b, err := os.ReadFile(p) // #nosec G304 -- a patch in this repository
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if !header.MatchString(s) {
				t.Errorf("%s: does not open as git format-patch -N writes it (From:, Date:, Subject: [PATCH])", p)
			}
			if !strings.Contains(s, "\ndiff --git a/") {
				t.Errorf("%s: has no diff --git: not made by git format-patch", p)
			}
		}
	}
}
