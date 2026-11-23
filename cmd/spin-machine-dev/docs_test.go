// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docsCheckout is a repository with a log of one entry and one record.
func docsCheckout(t *testing.T) string {
	dir := checkout(t)
	put(t, filepath.Join(dir, "docs/log/README.md"), "# Log\n", 0o644)
	put(t, filepath.Join(dir, "docs/log/rework.md"), "## 2. Rework cycles\n", 0o644)
	put(t, filepath.Join(dir, "docs/log/2026-11-23-an-entry.md"),
		"---\ndate: \"2026-11-23\"\norder: 10\narea: lab\ntitle: an entry\n---\nIts prose.\n", 0o644)
	put(t, filepath.Join(dir, "docs/experiments/an-idea.json"),
		`{"decision": "rejected", "section": "login", "order": 10, "date": "2026-11-23", "change": "An idea", "result": "nothing", "shape": "spin", "source": "-"}`, 0o644)
	return dir
}

func TestDocsRendersTheLogAndTheTables(t *testing.T) {
	dir := docsCheckout(t)
	stdout, stderr, status := verb(t, "docs", "log")
	if status != 0 || !strings.Contains(stdout, "### Week 12: 2026-11-21 to 11-27\n\n**11-23 · lab · an entry.** Its prose.\n") {
		t.Errorf("docs log: %d, %s%s", status, stdout, stderr)
	}
	out := filepath.Join(t.TempDir(), "tables.md")
	if _, stderr, status := verb(t, "docs", "tables", "--out", out); status != 0 {
		t.Fatalf("docs tables: %d, %s", status, stderr)
	}
	if got := read(t, out); !strings.Contains(got, "### The login\n\n| Idea | Result | Shape | Source | Date |\n|---|---|---|---|---|\n| An idea | nothing | spin | - | 11-23 |\n") {
		t.Errorf("docs tables wrote:\n%s", got)
	}
	site := filepath.Join(t.TempDir(), "site")
	if _, stderr, status := verb(t, "docs", "site", "--out", site); status != 0 {
		t.Fatalf("docs site: %d, %s", status, stderr)
	}
	if log := read(t, filepath.Join(site, "log.html")); !strings.Contains(log, "<strong>11-23 · lab · an entry.</strong> Its prose.") {
		t.Errorf("log.html:\n%s", log)
	}
	if exp := read(t, filepath.Join(site, "experiments.html")); !strings.Contains(exp, "<td>An idea</td>") {
		t.Errorf("experiments.html:\n%s", exp)
	}
	if err := os.Remove(filepath.Join(dir, "docs/log/rework.md")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, status := verb(t, "docs", "log"); status != 2 || !strings.Contains(stderr, "rework.md") {
		t.Errorf("docs log without rework.md: %d, %s", status, stderr)
	}
	if _, stderr, status := verb(t, "docs", "site", "--out", site); status != 2 || !strings.Contains(stderr, "rework.md") {
		t.Errorf("docs site without rework.md: %d, %s", status, stderr)
	}
}

func TestDocsRefusesARecordThatDoesNotParse(t *testing.T) {
	dir := docsCheckout(t)
	put(t, filepath.Join(dir, "docs/experiments/kept.json"), `{"decision": "kept"}`, 0o644)
	for _, args := range [][]string{{"docs", "tables"}, {"docs", "site", "--out", t.TempDir()}} {
		if _, stderr, status := verb(t, args...); status != 2 || !strings.Contains(stderr, `kept.json: decision "kept"`) {
			t.Errorf("%v: %d, %s", args, status, stderr)
		}
	}
	if _, stderr, status := verb(t, "docs", "tables", "--out", filepath.Join(dir, "none", "t.md")); status != 2 {
		t.Errorf("docs tables to a directory that is not there: %d, %s", status, stderr)
	}
}
