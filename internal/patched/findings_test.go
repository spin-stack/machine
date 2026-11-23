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

func sarifOf(results ...string) string {
	return `{"version":"2.1.0","$schema":"s","runs":[{"tool":{"driver":{"name":"CodeQL"}},"results":[` + strings.Join(results, ",") + `]}]}`
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
	if want := []string{"changed", "added", "spans-into-added", "upstreams", "upstreams-after", "new-file", "absolute"}; !slices.Equal(rules, want) {
		t.Errorf("scoped results = %v, want %v", rules, want)
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
