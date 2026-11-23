// SPDX-License-Identifier: Apache-2.0

package docsrec

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSplitGivesTheTextBack is the migration's proof: the files Split makes from docs/log.md and
// docs/boot-time.md, as they stood before it, render the log and every table byte for byte.
func TestSplitGivesTheTextBack(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join("testdata/split", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	kinds := Kinds{}
	lines := strings.Split(strings.TrimSpace(read("kinds.txt")), "\n")
	for _, l := range lines {
		n, kind, _ := strings.Cut(l, " ")
		i, err := strconv.Atoi(n)
		if err != nil {
			t.Fatalf("kinds.txt: %q", l)
		}
		kinds[i] = kind
	}
	log, bootTime := read("log.md"), read("boot-time.md")
	files, err := Split(log, bootTime, kinds)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, data := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := ReadLog(filepath.Join(root, LogDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Markdown(); got != log {
		diff(t, "docs/log.md", log, got)
	}
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if entryHead.MatchString(line) {
			n++
		}
	}
	if len(l.Entries) != n {
		t.Errorf("%d entries from a log with %d heads", len(l.Entries), n)
	}
	records, err := ReadRecords(filepath.Join(root, ExperimentsDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range Tables {
		got := tb.Markdown(records)
		header := strings.SplitN(got, "\n", 2)[0] + "\n"
		at := strings.Index(bootTime, "#"+" "+tb.Heading+"\n")
		start := at + strings.Index(bootTime[at:], header)
		end := start + strings.Index(bootTime[start:], "\n\n") + 1
		if want := bootTime[start:end]; got != want {
			diff(t, tb.Heading, want, got)
		}
	}
	t.Logf("%d entries, %d records", len(l.Entries), len(records))
}

func diff(t *testing.T, what, want, got string) {
	t.Helper()
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range min(len(w), len(g)) {
		if w[i] != g[i] {
			t.Errorf("%s: line %d differs:\nwant %q\ngot  %q", what, i+1, w[i], g[i])
			return
		}
	}
	t.Errorf("%s: %d lines, want %d", what, len(g), len(w))
}
