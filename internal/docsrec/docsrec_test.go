// SPDX-License-Identifier: Apache-2.0

package docsrec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheLogIsItsHeadEntriesByDateUnderTheirWeeksAndRework(t *testing.T) {
	l, err := ReadLog("testdata/log")
	if err != nil {
		t.Fatal(err)
	}
	want := `# Log

The head.

### Week 10: 2026-11-09 to 11-13

**11-13 · kernel · the last of week ten (#5, lab 45000000001).** One paragraph.

And another, with a list:
- one;
- two.

### Week 11: 2026-11-14 to 11-20

**11-14/15 · image/lab · spans two days (#7).**
- its prose starts on the line after the head.

**11-14 · lab · also twenty.** Its order is the next's, and its file name comes first.

**11-14 · lab · second of the day.** It follows the first, whatever its name.

## 2. Rework cycles

The narrative.
`
	if got := l.Markdown(); got != want {
		t.Errorf("the log:\n%s\nwant:\n%s", got, want)
	}
}

func TestWeeks(t *testing.T) {
	for day, want := range map[string]string{
		"2026-09-07": "### Week 1: 2026-09-07 to 09-13",
		"2026-09-13": "### Week 1: 2026-09-07 to 09-13",
		"2026-09-30": "### Week 4: 2026-09-28 to 10-04",
		"2026-11-09": "### Week 10: 2026-11-09 to 11-13",
		"2026-11-13": "### Week 10: 2026-11-09 to 11-13",
		"2026-11-20": "### Week 11: 2026-11-14 to 11-20",
		"2026-11-21": "### Week 12: 2026-11-21 to 11-27",
		"2026-12-31": "### Week 17: 2026-12-26 to 2027-01-01",
	} {
		d, err := time.Parse(dateLayout, day)
		if err != nil {
			t.Fatal(err)
		}
		if got := weekHeading(d); got != want {
			t.Errorf("%s: %q, want %q", day, got, want)
		}
	}
}

func TestAnEntryIsRefusedNamingItsFileAndField(t *testing.T) {
	entry := func(front, body string) string { return "---\n" + front + "---\n" + body + "\n" }
	good := "date: \"2026-11-14\"\norder: 1\narea: lab\ntitle: a title (#7, lab 45000000001)\nprs: [7]\nruns: [45000000001]\n"
	if _, err := ParseEntry("2026-11-14-a-title.md", []byte(entry(good, "Prose."))); err != nil {
		t.Fatalf("a good entry: %v", err)
	}
	for _, c := range []struct{ name, data, want string }{
		{"2026-11-14-a.md", "date: 2026-11-14\n", "does not open with front matter"},
		{"2026-11-14-a.md", "---\ndate: \"2026-11-14\"\n", "not closed"},
		{"2026-11-14-a.md", entry("date: \"2026-11-14\"\nwhen: today\n", "Prose."), "field when not found"},
		{"2026-11-14-a.md", entry(strings.Replace(good, "2026-11-14", "2026-11-31", 1), "Prose."), `date "2026-11-31" is not YYYY-MM-DD`},
		{"2026-11-15-a.md", entry(good, "Prose."), "not named 2026-11-14-<slug>.md"},
		{"2026-11-14-A_b.md", entry(good, "Prose."), "not named 2026-11-14-<slug>.md"},
		{"2026-11-14-a.md", entry(good+"days: 11-15/16\n", "Prose."), `days "11-15/16" does not start with the date's 11-14/`},
		{"2026-11-14-a.md", entry(strings.Replace(good, "order: 1", "order: 0", 1), "Prose."), "order: required"},
		{"2026-11-14-a.md", entry(strings.Replace(good, "area: lab", "area: \"\"", 1), "Prose."), "area: required"},
		{"2026-11-14-a.md", entry(good, "  "), "the prose: required"},
		{"2026-11-14-a.md", entry(good+"issues: [8]\n", "Prose."), "#8 is listed but the text does not cite it"},
		{"2026-11-14-a.md", entry(good, "Prose, lab 45000000002."), "cites run 45000000002, which runs does not list"},
		{"2026-11-14-a.md", entry(strings.Replace(good, "runs: [45000000001]", "runs: [45000000001, 45000000003]", 1), "Prose."), "45000000003 is listed but the text does not cite it"},
		{"2026-11-14-a.md", entry(strings.Replace(good, "runs: [45000000001]", "runs: [lab]", 1), "Prose."), "front matter"},
	} {
		_, err := ParseEntry(c.name, []byte(c.data))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), c.name+": ") {
			t.Errorf("%s %q: %v, want %s: ...%s...", c.name, c.data, err, c.name, c.want)
		}
	}
}

func TestTheLogRefusesAFileThatIsNotAnEntry(t *testing.T) {
	dir := t.TempDir()
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(logHead, "# Log\n")
	if _, err := ReadLog(dir); err == nil || !strings.Contains(err.Error(), "needs README.md and rework.md") {
		t.Errorf("a log without rework.md: %v", err)
	}
	put(logRework, "## 2.\n")
	put("notes.txt", "a note")
	if _, err := ReadLog(dir); err == nil || !strings.Contains(err.Error(), "notes.txt: does not open with front matter") {
		t.Errorf("a log with notes.txt: %v", err)
	}
	if _, err := ReadLog(filepath.Join(dir, "none")); err == nil {
		t.Error("a log that is not there was read")
	}
}

func TestTheTablesAreTheRecordsInOrder(t *testing.T) {
	records, err := ReadRecords("testdata/experiments")
	if err != nil {
		t.Fatal(err)
	}
	got := TablesMarkdown(records)
	for _, want := range []string{
		"# Experiments\n",
		"\n## What the machine carries\n\n| Item | Where | Earns (Δp50 of taking it back) | Evidence | Run |\n|---|---|---|---|---|\n" +
			"| Kernel patch 0001 (#5) | kernel/patches | cold −7.0 [−9.0, −5.0] | measured | lab 45000000001 |\n",
		"\n## Measured and rejected\n\n### Kernel and its command line\n\n| Idea | Result | Shape | Source | Date |\n|---|---|---|---|---|\n" +
			"| An earlier idea (#7) | 154 µs | runner | `c03ed89` | 09-10 |\n| A later idea | nothing | spin | the same | 11-20 |\n\n### Firmware, QEMU and the machine\n",
		"\n### Memory and the host\n\n| Idea | Result | Shape | Source | Date |\n|---|---|---|---|---|\n\n## Open experiments\n",
		"| An open experiment | cold | ~15 ms | `ssh-first`, 40 a side | open |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the tables do not hold\n%s\nin\n%s", want, got)
		}
	}
	if strings.Count(got, "## Measured and rejected") != 1 {
		t.Errorf("one heading over the rejected tables:\n%s", got)
	}
}

func TestARecordIsRefusedNamingItsFileAndField(t *testing.T) {
	good := `{"decision": "rejected", "section": "kernel", "order": 1, "date": "2026-09-10", "change": "An idea (#7, lab 45000000001)", "result": "nothing", "shape": "spin", "source": "the same", "issues": [7], "runs": [45000000001]}`
	if _, err := ParseRecord("7-an-idea.json", []byte(good)); err != nil {
		t.Fatalf("a good record: %v", err)
	}
	edit := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	for _, c := range []struct{ name, data, want string }{
		{"a.json", `{"decision": "rejected",}`, "invalid character"},
		{"a.json", edit(`"order"`, `"rank": 1, "order"`), `unknown field "rank"`},
		{"An_idea.json", good, "not named [<issue>-]<slug>.json"},
		{"a.yaml", good, "not named [<issue>-]<slug>.json"},
		{"a.json", edit(`"rejected"`, `"kept"`), `decision "kept" is not carried, rejected or open`},
		{"a.json", edit(`"kernel"`, `"network"`), `section "network" is not kernel`},
		{"a.json", edit(`"decision": "rejected"`, `"decision": "open"`), "section: a open record has none"},
		{"a.json", edit(`"order": 1`, `"order": 0`), "order: required"},
		{"a.json", edit(`"2026-09-10"`, `"09-10"`), `date "09-10" is not YYYY-MM-DD`},
		{"a.json", edit(`"shape": "spin"`, `"shape": " "`), `shape: required, the table's "Shape"`},
		{"a.json", edit(`"date": "2026-09-10", `, ""), `date: required, the table's "Date"`},
		{"a.json", edit(`"result": "nothing"`, `"result": "two\nlines"`), "result: a cell is one line"},
		{"a.json", edit(`"shape": "spin"`, `"shape": "spin", "where": "machine"`), `where: "Kernel and its command line" has no such column`},
		{"a.json", edit(`"issues": [7]`, `"issues": [7, 8]`), "#8 is listed but the text does not cite it"},
		{"a.json", edit(`"source": "the same"`, `"source": "lab 45000000002"`), "cites run 45000000002"},
		{"a.json", edit(`"runs": [45000000001]`, `"runs": ["45000000001"]`), "cannot unmarshal string"},
	} {
		_, err := ParseRecord(c.name, []byte(c.data))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), c.name+": ") {
			t.Errorf("%s %s: %v, want %s: ...%s...", c.name, c.data, err, c.name, c.want)
		}
	}
}

func TestTheExperimentsRefuseARecordThatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"decision": "kept"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecords(dir); err == nil || !strings.Contains(err.Error(), "a.json: decision") {
		t.Errorf("a directory with a bad record: %v", err)
	}
	if _, err := ReadRecords(filepath.Join(dir, "none")); err == nil {
		t.Error("experiments that are not there were read")
	}
}

func TestAPageIsTheMarkdownAsHTML(t *testing.T) {
	got, err := HTML("Experiments", "# Experiments\n\n| a | b |\n|---|---|\n| `x<y` | 2 |\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<title>Experiments - spin-machine</title>",
		`<h1 id="experiments">Experiments</h1>`,
		"<table>",
		"<td><code>x&lt;y</code></td>",
		`<a href="log.html">Log</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page has no %q:\n%s", want, got)
		}
	}
}
