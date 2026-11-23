// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchedTreeIsMadeInTheOutput(t *testing.T) {
	files := t.TempDir()
	put(t, filepath.Join(files, "linux-7.3.5/lib/a.c"), "one\n", 0o644)
	if out, err := exec.Command("tar", "-C", files, "-cJf", filepath.Join(files, "linux-7.3.5.tar.xz"), "linux-7.3.5").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(files)))
	t.Cleanup(srv.Close)
	was := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = was })
	sum := sha256.Sum256([]byte(read(t, filepath.Join(files, "linux-7.3.5.tar.xz"))))

	root := checkout(t)
	put(t, filepath.Join(root, "versions.yaml"), "- name: kernel\n  kind: download\n  source: "+srv.URL+
		"/linux-{version}.tar.xz\n  version: 7.3.5\n  pin: "+hex.EncodeToString(sum[:])+"\n  track: releases https://k\n", 0o644)
	put(t, filepath.Join(root, "kernel/patches/1.patch"), "--- a/lib/a.c\n+++ b/lib/a.c\n@@ -1 +1 @@\n-one\n+ONE\n", 0o644)
	_, stderr, status := verb(t, "patched", "tree", "kernel")
	tree := filepath.Join(root, "_output", "patched", "kernel", "src")
	if status != 0 || !strings.Contains(stderr, "kernel: "+tree) {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if got := read(t, filepath.Join(tree, "lib/a.c")); got != "ONE\n" {
		t.Errorf("a.c = %q", got)
	}

	put(t, filepath.Join(root, "kernel/patches/1.patch"), "--- a/lib/a.c\n+++ b/lib/a.c\n@@ -1 +1 @@\n-zwei\n+ONE\n", 0o644)
	if _, stderr, status := verb(t, "patched", "tree", "kernel"); status != 2 || !strings.Contains(stderr, "does not apply") {
		t.Errorf("a patch that does not apply: status %d: %s", status, stderr)
	}
}

func TestPatchedScopeExitsOneOnAResultOnAnAddedLine(t *testing.T) {
	root := checkout(t)
	dir := filepath.Join(root, "_output", "patched", "openssh")
	put(t, filepath.Join(dir, "touched"), "auth2.c\n", 0o644)
	put(t, filepath.Join(dir, "upstream/auth2.c"), "a\nb\n", 0o644)
	put(t, filepath.Join(dir, "src/auth2.c"), "a\nB\n", 0o644)
	sarif := func(line string) string {
		return `{"runs":[{"invocations":[{"toolExecutionNotifications":[{"descriptor":{"id":"cpp/diagnostics/successfully-extracted-files"},` +
			`"locations":[{"physicalLocation":{"artifactLocation":{"uri":"auth2.c"}}}]}]}],` +
			`"results":[{"ruleId":"cpp/x","message":{"text":"bad"},"locations":[{"physicalLocation":` +
			`{"artifactLocation":{"uri":"auth2.c"},"region":{"startLine":` + line + `}}}]}]}]}`
	}
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), sarif("2"), 0o644)
	stdout, stderr, status := verb(t, "patched", "scope", "openssh")
	if status != 1 || stdout != "auth2.c:2: cpp/x: bad\n" || !strings.Contains(stderr, "openssh: 1 results on lines the patches added") {
		t.Errorf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), sarif("1"), 0o644)
	if _, stderr, status := verb(t, "patched", "scope", "openssh"); status != 0 || !strings.Contains(stderr, "openssh: no result on a line the patches added") {
		t.Errorf("upstream's result: status %d, %s", status, stderr)
	}
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), `{"runs":[{"results":[]}]}`, 0o644)
	if _, stderr, status := verb(t, "patched", "scope", "openssh"); status != 2 || !strings.Contains(stderr, "auth2.c (not extracted)") {
		t.Errorf("a SARIF that read nothing: status %d, %s", status, stderr)
	}
	if _, stderr, status := verb(t, "patched", "scope", "qemu"); status != 2 || !strings.Contains(stderr, "make the tree first") {
		t.Errorf("no tree: status %d, %s", status, stderr)
	}
}

func TestPatchedNamesItsParts(t *testing.T) {
	checkout(t)
	for _, v := range []string{"tree", "scope"} {
		if _, stderr, status := verb(t, "patched", v, "seabios"); status != 2 || !strings.Contains(stderr, "kernel, qemu, qboot, systemd, openssh") {
			t.Errorf("%s of an unknown part: status %d, %s", v, status, stderr)
		}
	}
	t.Chdir(t.TempDir())
	if _, _, status := verb(t, "patched", "tree", "kernel"); status != 2 {
		t.Errorf("outside a checkout: status %d", status)
	}
}

func TestPatchedSevereExitsOneOnAnUnsuppressedSevereResult(t *testing.T) {
	root := checkout(t)
	dir := filepath.Join(root, "sarif")
	sarif := func(suppressions string) string {
		return `{"runs":[{"tool":{"driver":{"name":"CodeQL"},"extensions":[{"rules":[{"id":"go/x","properties":{"security-severity":"7.5"}}]}]},` +
			`"results":[{"ruleId":"go/x","message":{"text":"bad"}` + suppressions + `,"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":3}}}]}]}]}`
	}
	put(t, filepath.Join(dir, "go.sarif"), sarif(""), 0o644)
	if stdout, stderr, status := verb(t, "patched", "severe", dir); status != 1 || stdout != "a.go:3: go/x: bad\n" {
		t.Errorf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	put(t, filepath.Join(dir, "go.sarif"), sarif(`,"suppressions":[{"kind":"inSource"}]`), 0o644)
	if _, stderr, status := verb(t, "patched", "severe", dir); status != 0 || !strings.Contains(stderr, "no result of medium") {
		t.Errorf("suppressed: status %d, %s", status, stderr)
	}
	if _, _, status := verb(t, "patched", "severe", filepath.Join(root, "none")); status != 2 {
		t.Errorf("no SARIF: status %d", status)
	}
}

func TestPatchedCompileRefusesAPartWithNothingToCompile(t *testing.T) {
	checkout(t)
	if _, stderr, status := verb(t, "patched", "compile", "qboot"); status != 2 || !strings.Contains(stderr, "without a build") {
		t.Errorf("status %d, %s", status, stderr)
	}
	if _, stderr, status := verb(t, "patched", "prepare", "qboot"); status != 2 || !strings.Contains(stderr, "nothing to prepare") {
		t.Errorf("prepare: status %d, %s", status, stderr)
	}
}

func TestPatchedCompileNamesThePartsItCompiles(t *testing.T) {
	checkout(t)
	if stdout, _, status := verb(t, "patched", "compile", "--help"); status != 0 || !strings.Contains(stdout, "Parts that have it: kernel, qemu.") {
		t.Errorf("status %d: %s", status, stdout)
	}
}

// A failed daily analysis is one issue a day, named for the day and pointing at the run; a day
// that has one, open or closed, opens no other.
func TestPatchedFailedOpensOneIssueADay(t *testing.T) {
	checkout(t)
	calls := fakes(t, map[string]string{"gh": `case "$1 $2" in
"issue list") echo '[{"title":"CodeQL failed on main 2026-11-23","url":"https://x/issues/1"}]';;
"issue create") echo https://x/issues/9;;
esac`})
	if _, stderr, status := verb(t, "patched", "failed", "--run", "https://x/runs/5", "--day", "2026-11-24"); status != 0 ||
		!strings.Contains(stderr, "CodeQL failed on main 2026-11-24: https://x/issues/9") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh issue create -R spin-stack/machine --title CodeQL failed on main 2026-11-24 --body The daily CodeQL run failed: https://x/runs/5.") {
		t.Errorf("gh ran:\n%s", got)
	}

	calls = fakes(t, map[string]string{"gh": `[ "$1 $2" = "issue list" ] && echo '[{"title":"CodeQL failed on main 2026-11-24","url":"https://x/issues/9"}]'; exit 0`})
	if _, stderr, status := verb(t, "patched", "failed", "--run", "https://x/runs/6", "--day", "2026-11-24"); status != 0 ||
		strings.Contains(read(t, calls), "issue create") || !strings.Contains(stderr, "https://x/issues/9") {
		t.Errorf("opened again: exit %d, %q, gh ran %q", status, stderr, read(t, calls))
	}

	for name, gh := range map[string]string{
		"no issues listed": "exit 1",
		"no issue opened":  `[ "$1 $2" = "issue list" ] && echo '[]' && exit 0; exit 1`,
	} {
		fakes(t, map[string]string{"gh": gh})
		if _, _, status := verb(t, "patched", "failed", "--run", "https://x/runs/5"); status != 2 {
			t.Errorf("%s: exit %d", name, status)
		}
	}
	if _, _, status := verb(t, "patched", "failed"); status == 0 {
		t.Error("no --run: exit 0")
	}
}
