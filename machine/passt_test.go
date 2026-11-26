// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// passt's profile attaches to the one file passt is, by the path AppArmor matches at exec: its
// symlinks resolved, never a pattern. A path a profile would not read as itself is refused.
func TestPasstsProfileAttachesToTheOneFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "azAZ09._+-", "passt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin")
	if err := os.Symlink(filepath.Dir(file), link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, passt := range []string{file, filepath.Join(link, "passt"), "bin/passt"} {
		got, err := PasstAppArmor(passt)
		if err != nil {
			t.Fatalf("PasstAppArmor(%q): %v", passt, err)
		}
		if want := "\nprofile spin-machine-passt \"" + file + "\" flags=(attach_disconnected) {\n"; !strings.Contains(got, want) {
			t.Errorf("PasstAppArmor(%q) attaches otherwise than %q:\n%s", passt, want, got)
		}
	}

	for _, name := range []string{"a b", "*", "?", "[x]", "{x}", "^", "@{HOME}", `"`, `\`, "ü", "`", ":"} {
		p := filepath.Join(dir, name, "passt")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		if got, err := PasstAppArmor(p); err == nil || !strings.Contains(err.Error(), "unpack the release at a path") {
			t.Errorf("PasstAppArmor(%q) = %q, %v; want it refused", p, got, err)
		}
	}
	if _, err := PasstAppArmor(filepath.Join(dir, "none", "passt")); err == nil || !strings.Contains(err.Error(), "resolving passt's path") {
		t.Errorf("PasstAppArmor of no file = %v, want it refused", err)
	}
}
