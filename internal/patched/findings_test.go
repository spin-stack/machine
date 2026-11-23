// SPDX-License-Identifier: Apache-2.0

package patched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// analysed is a tree as Tree and the analysis leave it: a.c changed at line 2 and given line 4,
// new.c the patches', and a SARIF with a result on each kind of line.
func analysed(t *testing.T, sarif string) string {
	t.Helper()
	dir := t.TempDir()
	put(t, filepath.Join(dir, "touched"), "lib/a.c\nlib/new.c\n")
	put(t, filepath.Join(dir, "upstream/lib/a.c"), "one\ntwo\nthree\nfive\n")
	put(t, filepath.Join(dir, "src/lib/a.c"), "one\nTWO\nthree\nfour\nfive\n")
	put(t, filepath.Join(dir, "src/lib/new.c"), "new one\nnew two\n")
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), sarif)
	return dir
}

func res(rule, uri string, start, end int) string {
	region := `"startLine":` + itoa(start)
	if end > 0 {
		region += `,"endLine":` + itoa(end)
	}
	return `{"ruleId":"` + rule + `","message":{"text":"` + rule + ` said"},"partialFingerprints":{"x":"y"},` +
		`"locations":[{"physicalLocation":{"artifactLocation":{"uri":"` + uri + `","uriBaseId":"%SRCROOT%"},"region":{` + region + `}}}]}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// sarifOf is a SARIF with results, in which the extractor read both touched files.
func sarifOf(results ...string) string {
	return sarifRead([]string{"lib/a.c", "lib/new.c"}, nil, results...)
}

// sarifRead is a SARIF whose extractor reports the files extracted and the compilations failed
// as CodeQL's C/C++ extractor does (the shapes are those of codeql.yml's run 45214602230).
func sarifRead(extracted, failed []string, results ...string) string {
	var notes []string
	for _, f := range extracted {
		notes = append(notes, `{"locations":[{"physicalLocation":{"artifactLocation":{"uri":"`+f+`","uriBaseId":"%SRCROOT%","index":0}}}],`+
			`"message":{"text":"File successfully extracted."},"level":"none","descriptor":{"id":"cpp/diagnostics/successfully-extracted-files","index":0}}`)
	}
	var errs []string
	for _, f := range failed {
		errs = append(errs, `{"error_time_seconds":2.5,"error_args":["--mimic","/usr/bin/clang","-I/x/include","-c","`+f+`"],"failure_reason":"ExitCode: 4"}`)
	}
	notes = append(notes, `{"message":{"text":""},"level":"none","descriptor":{"id":"cpp/bmn/extraction-information","index":0},`+
		`"properties":{"attributes":{"extraction_errors":[`+strings.Join(errs, ",")+`]}}}`)
	return `{"version":"2.1.0","$schema":"s","runs":[{"tool":{"driver":{"name":"CodeQL"}},` +
		`"invocations":[{"toolExecutionNotifications":[` + strings.Join(notes, ",") + `]}],` +
		`"results":[` + strings.Join(results, ",") + `]}]}`
}

func TestScopeFailsOnATouchedFileTheAnalysisDidNotRead(t *testing.T) {
	dir := analysed(t, "")
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), sarifRead(nil, []string{filepath.Join(dir, "src/lib/a.c")}, res("added", "lib/a.c", 4, 0)))
	findings, err := Scope(context.Background(), dir)
	if err == nil {
		t.Fatal("a SARIF that read neither touched file passed")
	}
	for _, want := range []string{"lib/a.c (its compilation failed)", "lib/new.c (not extracted)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q in it", err, want)
		}
	}
	if len(findings) != 1 {
		t.Errorf("findings = %v: a result on an added line is still one", findings)
	}
	if _, err := os.Stat(filepath.Join(dir, "scoped.sarif")); err != nil {
		t.Errorf("no scoped.sarif for code scanning: %v", err)
	}
}

func TestScopeAsksOnlyForCFiles(t *testing.T) {
	dir := analysed(t, "")
	put(t, filepath.Join(dir, "touched"), "lib/a.c\nlib/new.c\nlib/Kconfig\nlib/a.h\n")
	put(t, filepath.Join(dir, "src/lib/Kconfig"), "config A\n")
	put(t, filepath.Join(dir, "src/lib/a.h"), "int a;\n")
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), sarifOf())
	if _, err := Scope(context.Background(), dir); err != nil {
		t.Errorf("a header or a Kconfig, which nothing compiles on its own, failed the scope: %v", err)
	}
}

func TestScopeKeepsTheTouchedFilesAndFindsWhatIsOnTheirAddedLines(t *testing.T) {
	dir := analysed(t, "")
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), sarifOf(
		res("changed", "lib/a.c", 2, 0),
		res("added", "lib/a.c", 4, 0),
		res("spans-into-added", "lib/a.c", 3, 4),
		res("upstreams", "lib/a.c", 1, 0),
		res("upstreams-after", "lib/a.c", 5, 0),
		res("new-file", "lib/new.c", 1, 0),
		res("absolute", "file://"+filepath.Join(dir, "src/lib/a.c"), 2, 0),
		res("untouched", "lib/b.c", 2, 0),
		`{"ruleId":"nowhere","message":{"text":"x"}}`,
	))
	findings, err := Scope(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.String())
	}
	want := []string{
		"lib/a.c:2: changed: changed said",
		"lib/a.c:4: added: added said",
		"lib/a.c:4: spans-into-added: spans-into-added said",
		"lib/new.c:1: new-file: new-file said",
		"lib/a.c:2: absolute: absolute said",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	var scoped struct {
		Schema string `json:"$schema"`
		Runs   []struct {
			Tool    json.RawMessage `json:"tool"`
			Results []struct {
				RuleID       string            `json:"ruleId"`
				Fingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "scoped.sarif"))), &scoped); err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, r := range scoped.Runs[0].Results {
		rules = append(rules, r.RuleID)
		if r.Fingerprints["x"] != "y" {
			t.Errorf("%s lost what it did not read", r.RuleID)
		}
	}
	if want := []string{"changed", "added", "spans-into-added", "new-file", "absolute"}; !slices.Equal(rules, want) {
		t.Errorf("scoped results = %v, want %v: only what the patches added goes to code scanning", rules, want)
	}
	if got, want := read(t, filepath.Join(dir, "upstream.txt")), "lib/a.c:1: upstreams: upstreams said\nlib/a.c:5: upstreams-after: upstreams-after said\n"; got != want {
		t.Errorf("upstream.txt = %q, want %q", got, want)
	}
	if scoped.Schema != "s" || len(scoped.Runs[0].Tool) == 0 {
		t.Errorf("the scoped SARIF lost its document: %+v", scoped)
	}
}

func TestScopeWithNothingOnAnAddedLineWritesAnEmptyRun(t *testing.T) {
	dir := analysed(t, sarifOf(res("untouched", "lib/b.c", 2, 0)))
	findings, err := Scope(context.Background(), dir)
	if err != nil || len(findings) != 0 {
		t.Fatalf("findings %v, %v", findings, err)
	}
	if got := read(t, filepath.Join(dir, "scoped.sarif")); !strings.Contains(got, `"results":[]`) {
		t.Errorf("scoped.sarif = %s", got)
	}
}

func TestScopeRefusesWhatItCannotRead(t *testing.T) {
	for name, c := range map[string]struct {
		prepare func(dir string)
		want    string
	}{
		"no tree":        {func(dir string) { _ = os.Remove(filepath.Join(dir, "touched")) }, "make the tree first"},
		"no SARIF":       {func(dir string) { _ = os.RemoveAll(filepath.Join(dir, "sarif")) }, "found 0"},
		"two SARIFs":     {func(dir string) { put(t, filepath.Join(dir, "sarif/b.sarif"), "{}") }, "found 2"},
		"not JSON":       {func(dir string) { put(t, filepath.Join(dir, "sarif/cpp.sarif"), "{") }, "reading"},
		"no run":         {func(dir string) { put(t, filepath.Join(dir, "sarif/cpp.sarif"), `{"runs":[]}`) }, "did not finish"},
		"bad results":    {func(dir string) { put(t, filepath.Join(dir, "sarif/cpp.sarif"), `{"runs":[{"results":{}}]}`) }, "results"},
		"a file deleted": {func(dir string) { _ = os.Remove(filepath.Join(dir, "src/lib/new.c")) }, "diff"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := analysed(t, sarifOf())
			c.prepare(dir)
			if _, err := Scope(context.Background(), dir); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q in it", err, c.want)
			}
		})
	}
}

func TestAddedLinesAreTheNewSideOfEachHunk(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "was"), "a\nb\nc\nd\ne\n")
	put(t, filepath.Join(dir, "now"), "a\nc\nD\nd\nE\n")
	got, err := addedLines(context.Background(), filepath.Join(dir, "was"), filepath.Join(dir, "now"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[3] || !got[5] {
		t.Errorf("added = %v, want 3 and 5: a deletion adds nothing", got)
	}
	same, err := addedLines(context.Background(), filepath.Join(dir, "was"), filepath.Join(dir, "was"))
	if err != nil || len(same) != 0 {
		t.Errorf("an unchanged file: %v, %v", same, err)
	}
}

func TestSevereIsWhatIsAtTheSeverityAndNotSuppressed(t *testing.T) {
	dir := t.TempDir()
	// The shape CodeQL writes: the rules in the query pack's extension, not the driver.
	put(t, filepath.Join(dir, "go.sarif"), `{"runs":[{"tool":{"driver":{"name":"CodeQL"},"extensions":[{"name":"codeql/go-queries","rules":[`+
		`{"id":"go/high","properties":{"security-severity":"8.2"}},{"id":"go/medium","properties":{"security-severity":"4.0"}},`+
		`{"id":"go/low","properties":{"security-severity":"3.9"}},{"id":"go/quality","properties":{}}]}]},"results":[`+
		res("go/high", "a.go", 3, 0)+","+res("go/medium", "b.go", 4, 0)+","+res("go/low", "c.go", 5, 0)+","+res("go/quality", "d.go", 6, 0)+","+
		`{"ruleId":"go/high","message":{"text":"why it stays"},"suppressions":[{"kind":"inSource"}],"locations":[{"physicalLocation":{"artifactLocation":{"uri":"e.go"},"region":{"startLine":7}}}]}`+
		`]}]}`)
	got, err := Severe(dir, Medium)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, f := range got {
		lines = append(lines, f.String())
	}
	if want := []string{"a.go:3: go/high: go/high said", "b.go:4: go/medium: go/medium said"}; !slices.Equal(lines, want) {
		t.Errorf("Severe = %q, want %q", lines, want)
	}
	if _, err := Severe(t.TempDir(), Medium); err == nil {
		t.Error("no SARIF gave no error")
	}
}

func TestScopeRefusesExtractionDiagnosticsItCannotRead(t *testing.T) {
	dir := analysed(t, `{"runs":[{"invocations":{},"results":[]}]}`)
	if _, err := Scope(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "cpp.sarif's extraction diagnostics") {
		t.Errorf("err = %v", err)
	}
}

func TestAFailedCompilationIsTheLastArgumentItWasGiven(t *testing.T) {
	dir := analysed(t, "")
	put(t, filepath.Join(dir, "sarif/cpp.sarif"), `{"runs":[{"invocations":[{"toolExecutionNotifications":[`+
		`{"descriptor":{"id":"cpp/diagnostics/successfully-extracted-files"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"lib/a.c"}}}]},`+
		`{"descriptor":{"id":"cpp/diagnostics/successfully-extracted-files"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"lib/new.c"}}}]},`+
		`{"descriptor":{"id":"cpp/bmn/extraction-information"},"properties":{"attributes":{"extraction_errors":[{"error_args":[]},{"error_args":["lib/new.c"]}]}}}`+
		`]}],"results":[]}]}`)
	if _, err := Scope(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "lib/new.c (its compilation failed)") || strings.Contains(err.Error(), "lib/a.c") {
		t.Errorf("err = %v", err)
	}
}
