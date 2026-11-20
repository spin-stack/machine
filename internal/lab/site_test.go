// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"strings"
	"testing"
)

// The site says what the page does: why a Δ is read with care, the chart inline with its
// tooltips, the tables, the gap to the goal, each run and each record linked, and the dashes'
// reasons.
func TestTheSiteIsThePageAsHTML(t *testing.T) {
	page, err := Site(docRecords())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<!doctype html>",
		"prefers-color-scheme: dark",
		"7.3% later",
		"<code>spin-machine-dev releases compare A B</code>",
		"<svg xmlns=\"http://www.w3.org/2000/svg\"",
		"<title>v2 cold: p50 321, p95 321 ms</title>",
		"<th>cold boot to first login</th><th>Δ</th>",
		"<tr><td>v2</td><td>321 / 321</td><td>-19.0</td><td>152 / 152</td><td></td><td>296 / 296</td><td>-</td></tr>",
		// html/template writes a + as &#43;, which a browser shows as +.
		"<p>v2 against the goal: cold &#43;221 ms, restore &#43;52 ms.</p>",
		"<th>kernel to &#34;Freeing&#34; ms</th>",
		"<td><a href=\"releases/v2.json\">v2</a></td>",
		"<td><a href=\"https://x/actions/runs/7\">7</a></td>",
		"<li>v2, restore, QEMU started ahead: lost all 1</li>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in:\n%s", want, page)
		}
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "src=") || strings.Contains(page, "stylesheet") {
		t.Errorf("the page runs or fetches something:\n%s", page)
	}
}

// Whatever a record holds is text on the page, never markup.
func TestTheSiteEscapesWhatARecordHolds(t *testing.T) {
	r := docRecords()[1]
	r.Release, r.Host, r.Cannot = "<i>v2</i>", `"a" & <b>`, map[string]string{"kernel-ms": "<script>alert(1)</script>"}
	page, err := Site([]Record{r})
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"<i>", "<b>", "<script>"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("%s is markup on the page:\n%s", unwanted, page)
		}
	}
	for _, want := range []string{"&lt;i&gt;v2&lt;/i&gt;", "&#34;a&#34; &amp; &lt;b&gt;", "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in:\n%s", want, page)
		}
	}
	// With nothing recorded the page is still the page: the chart and the tables' heads.
	empty, err := Site(nil)
	if err != nil || !strings.Contains(empty, "<svg") || !strings.Contains(empty, "<h2>Where each was measured</h2>") ||
		strings.Contains(empty, "A dash") {
		t.Errorf("no records: %v\n%s", err, empty)
	}
}
