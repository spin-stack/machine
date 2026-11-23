// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// A daily check that failed on main is one issue a check and a day, named for both and pointing at
// the run; a day that has one, open or closed, opens no other.
func TestFailedOpensOneIssueACheckADay(t *testing.T) {
	checkout(t)
	calls := fakes(t, map[string]string{"gh": `case "$1 $2" in
"issue list") echo '[{"title":"CodeQL failed on main 2026-11-23","url":"https://x/issues/1"}]';;
"issue create") echo https://x/issues/9;;
esac`})
	if _, stderr, status := verb(t, "failed", "CodeQL", "--run", "https://x/runs/5", "--day", "2026-11-24"); status != 0 ||
		!strings.Contains(stderr, "CodeQL failed on main 2026-11-24: https://x/issues/9") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh issue create -R spin-stack/machine --title CodeQL failed on main 2026-11-24 --body The daily CodeQL run on main failed: https://x/runs/5.") {
		t.Errorf("gh ran:\n%s", got)
	}

	calls = fakes(t, map[string]string{"gh": `[ "$1 $2" = "issue list" ] && echo '[{"title":"CodeQL failed on main 2026-11-24","url":"https://x/issues/9"}]'; exit 0`})
	if _, stderr, status := verb(t, "failed", "CodeQL", "--run", "https://x/runs/6", "--day", "2026-11-24"); status != 0 ||
		strings.Contains(read(t, calls), "issue create") || !strings.Contains(stderr, "https://x/issues/9") {
		t.Errorf("opened again: exit %d, %q, gh ran %q", status, stderr, read(t, calls))
	}

	for name, gh := range map[string]string{
		"no issues listed": "exit 1",
		"no issue opened":  `[ "$1 $2" = "issue list" ] && echo '[]' && exit 0; exit 1`,
	} {
		fakes(t, map[string]string{"gh": gh})
		if _, _, status := verb(t, "failed", "CodeQL", "--run", "https://x/runs/5"); status != 2 {
			t.Errorf("%s: exit %d", name, status)
		}
	}
	if _, _, status := verb(t, "failed", "CodeQL"); status == 0 {
		t.Error("no --run: exit 0")
	}
	if _, _, status := verb(t, "failed", "--run", "https://x/runs/5"); status == 0 {
		t.Error("no check named: exit 0")
	}
}
