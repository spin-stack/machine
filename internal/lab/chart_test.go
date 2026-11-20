// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// drawn is what a chart shows, read back from its SVG as a browser would parse it.
type drawn struct {
	Title string `xml:"title"`
	Lines []struct {
		Class string `xml:"class,attr"`
		Dash  string `xml:"stroke-dasharray,attr"`
	} `xml:"line"`
	Texts []struct {
		Y         float64 `xml:"y,attr"`
		Transform string  `xml:"transform,attr"`
		Text      string  `xml:",chardata"`
	} `xml:"text"`
	Runs []struct {
		Number string `xml:"data-number,attr"`
		Bands  []struct {
			Points string `xml:"points,attr"`
		} `xml:"polygon"`
		Polylines []struct{} `xml:"polyline"`
		Points    []struct {
			X     float64 `xml:"cx,attr"`
			Title string  `xml:"title"`
		} `xml:"circle"`
		Labels []string `xml:"text"`
	} `xml:"g"`
}

func draw(t *testing.T, records []Record) drawn {
	t.Helper()
	var d drawn
	if err := xml.Unmarshal([]byte(Chart(records)), &d); err != nil {
		t.Fatalf("the chart is not an SVG a browser reads: %v", err)
	}
	return d
}

// texts are the chart's text outside its lines: the axes, the releases, the legend.
func (d drawn) texts() []string {
	var out []string
	for _, x := range d.Texts {
		out = append(out, x.Text)
	}
	return out
}

// lines are each run of number, as the tooltips of its points.
func (d drawn) lines(number string) [][]string {
	var out [][]string
	for _, r := range d.Runs {
		if r.Number != number {
			continue
		}
		var titles []string
		for _, p := range r.Points {
			titles = append(titles, p.Title)
		}
		out = append(out, titles)
	}
	return out
}

// goal is whether the chart draws the goal as a dashed line, labelled.
func (d drawn) goal() bool {
	for _, l := range d.Lines {
		if l.Class == "goal" && l.Dash != "" {
			return slices.Contains(d.texts(), "goal 100 ms")
		}
	}
	return false
}

func rec(release string, numbers map[string]Number) Record {
	return Record{Release: release, Numbers: numbers}
}

// Each goal number is a line of its p50 over a band to its p95, a tooltip and a label on every
// point; a release without it breaks its line rather than drawing a zero, and the axis runs from
// 0 above the highest p95.
func TestTheChartDrawsEachGoalNumberAndBreaksItWhereAReleaseHasNone(t *testing.T) {
	both := map[string]Number{"cold": {340, 352.4}, "restore": {152, 161}, "save-ms": {900, 900}}
	d := draw(t, []Record{rec("v1", both), rec("v2", map[string]Number{"cold": {321, 330}}), rec("v3", both)})
	if d.Title != chartTitle || !d.goal() {
		t.Errorf("title %q, goal drawn %v", d.Title, d.goal())
	}
	if got, want := d.lines("cold"), [][]string{{"v1 cold: p50 340, p95 352 ms", "v2 cold: p50 321, p95 330 ms",
		"v3 cold: p50 340, p95 352 ms"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("cold's lines %q", got)
	}
	if got, want := d.lines("restore"), [][]string{{"v1 restore: p50 152, p95 161 ms"}, {"v3 restore: p50 152, p95 161 ms"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("restore's lines %q: v2 has none, so v1's and v3's are not one line", got)
	}
	if got := d.lines("save-ms"); got != nil {
		t.Errorf("a diagnostic is charted: %q", got)
	}
	for _, r := range d.Runs {
		polylines := 0
		if len(r.Points) > 1 {
			polylines = 1
		}
		if len(r.Bands) != 1 || len(r.Polylines) != polylines || len(r.Labels) != len(r.Points) {
			t.Errorf("%s's run of %d: %d bands, %d lines, labels %q", r.Number, len(r.Points), len(r.Bands), len(r.Polylines), r.Labels)
		}
	}
	if got := d.Runs[0].Labels; !slices.Equal(got, []string{"340", "321", "340"}) {
		t.Errorf("cold's labels %q", got)
	}
	texts := d.texts()
	for _, want := range []string{"v1", "v2", "v3", "cold boot to first login", "restore to first login", "ms"} {
		if !slices.Contains(texts, want) {
			t.Errorf("no %q in %q", want, texts)
		}
	}
	// The save's 900 is not a goal number, so it does not stretch the axis.
	if got := d.axis(); !slices.Equal(got, []string{"0", "50", "100", "150", "200", "250", "300", "350", "400"}) {
		t.Errorf("the axis %q", got)
	}
}

// axis is the chart's y axis, as its labels: every number among its texts outside the lines.
func (d drawn) axis() []string {
	var out []string
	for _, s := range d.texts() {
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// The axis runs from 0 to a round number above the highest p95, or the goal, in eight steps or
// fewer of 25, 50, 100, 200, 500 or 1000 ms.
func TestTheChartsAxisIsRoundAndAboveEveryP95(t *testing.T) {
	for p95, want := range map[float64]string{
		0:     "0 25 50 75 100 125",
		352.4: "0 50 100 150 200 250 300 350 400",
		400:   "0 100 200 300 400 500",
		600:   "0 100 200 300 400 500 600 700",
		1000:  "0 200 400 600 800 1000 1200",
		1700:  "0 500 1000 1500 2000",
		5000:  "0 1000 2000 3000 4000 5000 6000",
	} {
		var records []Record
		if p95 > 0 {
			records = []Record{rec("v1", map[string]Number{"cold": {p95 / 2, p95}})}
		}
		if got := strings.Join(draw(t, records).axis(), " "); got != want {
			t.Errorf("a p95 of %v: the axis is %s, want %s", p95, got, want)
		}
	}
}

// Too many releases for every point to carry its label: the tooltips have each value, the newest
// is labelled, and the releases' names are turned to fit.
func TestACrowdedChartLabelsTheNewestAndTurnsTheNames(t *testing.T) {
	var records []Record
	for i := range 30 {
		records = append(records, rec(fmt.Sprintf("v%02d", i), map[string]Number{"cold": {float64(300 + i), 310}}))
	}
	d := draw(t, records)
	if len(d.Runs) != 1 || len(d.Runs[0].Points) != 30 || !slices.Equal(d.Runs[0].Labels, []string{"329"}) {
		t.Errorf("runs %+v", d.Runs)
	}
	zero := 0.0
	for _, x := range d.Texts {
		if x.Text == "0" {
			zero = x.Y
		}
	}
	for _, x := range d.Texts {
		if !strings.HasPrefix(x.Text, "v") {
			continue
		}
		var px, py float64
		if _, err := fmt.Sscanf(x.Transform, "translate(%g %g) rotate(-40)", &px, &py); err != nil || py <= zero {
			t.Errorf("%s is not turned below the axis: %q", x.Text, x.Transform)
		}
	}
	// Seven releases' names fit level, and each point keeps its label.
	if d := draw(t, records[:7]); len(d.Runs[0].Labels) != 7 || strings.Contains(Chart(records[:7]), "rotate(-40)") {
		t.Errorf("seven releases: labels %q", d.Runs[0].Labels)
	}
}

// No records is an empty chart with its goal; one is a point with its band; a release's name is
// text, whatever it holds.
func TestAChartOfNoneOrOneRelease(t *testing.T) {
	if d := draw(t, nil); len(d.Runs) != 0 || !d.goal() {
		t.Errorf("no records: %+v", d)
	}
	d := draw(t, []Record{rec("<v1 & co>", map[string]Number{"restore": {1500, 1700}})})
	if got := d.lines("restore"); len(got) != 1 || !slices.Equal(got[0], []string{"<v1 & co> restore: p50 1500, p95 1700 ms"}) {
		t.Errorf("one record: %q", got)
	}
	r := d.Runs[0]
	if len(r.Bands) != 1 || len(r.Polylines) != 0 || !slices.Equal(r.Labels, []string{"1500"}) {
		t.Fatalf("one record's run %+v", r)
	}
	// Its band has a width, about its point, from its p50 to its p95.
	xs, ys := map[bool]int{}, map[float64]bool{}
	for _, p := range strings.Fields(r.Bands[0].Points) {
		var px, py float64
		if _, err := fmt.Sscanf(p, "%g,%g", &px, &py); err != nil {
			t.Fatal(err)
		}
		xs[px < r.Points[0].X]++
		ys[py] = true
	}
	if xs[true] != 2 || xs[false] != 2 || len(ys) != 2 {
		t.Errorf("one release's band is %q about a point at %v", r.Bands[0].Points, r.Points[0].X)
	}
}
