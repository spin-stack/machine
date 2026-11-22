// SPDX-License-Identifier: Apache-2.0

package gates_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every way the image differs from what its packages installed has a row in docs/image.md.
// image/build.sh derives the list from the image it built and refuses one that is not
// image/deviations.txt, so a new mask, drop-in, edit or deletion arrives with a line here, and this
// test with the row that says why.
func TestEveryDeviationOfTheImageIsExplained(t *testing.T) {
	inventory, err := os.ReadFile(filepath.Join(root, "image", "deviations.txt"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "image.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(inventory)), "\n")
	if len(lines) < 50 {
		t.Fatalf("image/deviations.txt has %d lines: the check reads almost nothing", len(lines))
	}
	for _, line := range lines {
		kind, path, ok := strings.Cut(line, " ")
		if !ok || path == "" {
			t.Errorf("image/deviations.txt: %q is not KIND PATH", line)
			continue
		}
		if name := deviationName(path); !strings.Contains(string(doc), "`"+name+"`") {
			t.Errorf("docs/image.md does not name `%s` (%s %s)", name, kind, path)
		}
	}
}

// deviationName is how docs/image.md names a path: a unit, a generator or a tmpfiles rule by its
// own name, anything else by its path.
func deviationName(path string) string {
	for _, dir := range []string{"/etc/systemd/system/", "/usr/lib/systemd/system/", "/etc/systemd/system-generators/", "/etc/tmpfiles.d/"} {
		if name, ok := strings.CutPrefix(path, dir); ok {
			return name
		}
	}
	return path
}
