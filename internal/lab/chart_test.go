// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/xml"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// mark is one circle a chart draws.
type mark struct {
	Class string  `xml:"class,attr"`
	X     float64 `xml:"cx,attr"`
	Y     float64 `xml:"cy,attr"`
	R     float64 `xml:"r,attr"`
	Title string  `xml:"title"`
}

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
		Number    string     `xml:"data-number,attr"`
		Polygons  []struct{} `xml:"polygon"`
		Polylines []struct{} `xml:"polyline"`
		Whiskers  []struct {
			X  float64 `xml:"x1,attr"`
			Y1 float64 `xml:"y1,attr"`
			Y2 float64 `xml:"y2,attr"`
		} `xml:"line"`
		Marks  []mark   `xml:"circle"`
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

// onLine is whether m is one of a line's points rather than a run's own p50 beside it.
func (m mark) onLine() bool { return m.Class != "own" }

// lines are each line of number, as the tooltips of its points.
func (d drawn) lines(number string) [][]string {
	var out [][]string
	for _, r := range d.Runs {
		if r.Number != number {
			continue
		}
		var titles []string
		for _, m := range r.Marks {
			if m.onLine() {
				titles = append(titles, m.Title)
			}
		}
		out = append(out, titles)
	}
	return out
}

// marks are number's circles of class, across its lines.
func (d drawn) marks(number, class string) []mark {
	var out []mark
	for _, r := range d.Runs {
		if r.Number == number {
			for _, m := range r.Marks {
				if m.Class == class {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// goal is whether the chart draws the goal as a dashed line, with its value in the legend above
// the plot: written on the line, it sat on the newest release's points and their labels.
func (d drawn) goal() bool {
	for _, l := range d.Lines {
		if l.Class != "goal" || l.Dash == "" {
			continue
		}
		for _, x := range d.Texts {
			if x.Text == "goal 100 ms" && x.Y < plotTop {
				return true
			}
		}
	}
	return false
}

func rec(release string, numbers map[string]Number) Record {
	return Record{Release: release, Numbers: numbers}
}

// paired is release with its cold number, measured against before in the same run with the Δ d.
func paired(release string, cold Number, before string, d Delta) Record {
	r := rec(release, map[string]Number{"cold": cold})
	r.Against = &Against{Release: before, Delta: map[string]Delta{"cold": d}}
	return r
}

// values are each line's values, as chains has them.
func values(lines [][]step) [][]float64 {
	var out [][]float64
	for _, l := range lines {
		var vs []float64
		for _, s := range l {
			vs = append(vs, s.value)
		}
		out = append(out, vs)
	}
	return out
}

// A line starts at its first release's own p50 and moves by each next release's Δ against the one
// before it, whatever that release's own run read: v3's run read 400, 77 ms above where its Δ puts
// it.
func TestALineAddsUpTheSameRunDeltas(t *testing.T) {
	records := []Record{
		rec("v1", map[string]Number{"cold": {340, 352}}),
		paired("v2", Number{330, 345}, "v1", Delta{-19, -25, -12}),
		paired("v3", Number{400, 410}, "v2", Delta{2, -3, 6}),
	}
	lines := chains(records, "cold")
	if got := values(lines); !slices.EqualFunc(got, [][]float64{{340, 321, 323}}, slices.Equal) {
		t.Fatalf("the line %v", got)
	}
	l := lines[0]
	if l[0].delta != nil || *l[1].delta != (Delta{-19, -25, -12}) || l[2].own != (Number{400, 410}) {
		t.Errorf("the steps %+v", l)
	}
	if l[0].measured() || !l[1].measured() || l[2].measured() {
		t.Errorf("a change is the Δ whose interval excludes 0: %v %v %v", l[0].measured(), l[1].measured(), l[2].measured())
	}
}

// A release with no Δ against the release before it on the chart starts a line of its own at its
// own p50: one measured alone, one measured against another (the release before a withdrawn one),
// one whose Δ lacks the number, and one in another shape, which no run compared with the release
// before it.
func TestALineRestartsWhereNoSameRunDeltaJoinsTwoReleases(t *testing.T) {
	d := Delta{-10, -14, -6}
	alone := rec("v2", map[string]Number{"cold": {320, 330}})
	noCold := paired("v4", Number{300, 310}, "v3", d)
	noCold.Against.Delta = map[string]Delta{"restore": d}
	shaped := paired("v5", Number{450, 460}, "v4", d)
	shaped.Shape = "1 of 16 vCPUs, sized to 2048 MiB, spin's login"
	records := []Record{
		rec("v1", map[string]Number{"cold": {340, 350}}),
		alone,
		paired("v3", Number{310, 320}, "v1", d),
		noCold,
		shaped,
		func() Record { r := paired("v6", Number{470, 480}, "v5", d); r.Shape = shaped.Shape; return r }(),
	}
	if got := values(chains(records, "cold")); !slices.EqualFunc(got, [][]float64{{340}, {320}, {310}, {300}, {450, 440}}, slices.Equal) {
		t.Errorf("the lines %v", got)
	}
	if got := chains(records, "restore"); got != nil {
		t.Errorf("a number no release has is drawn: %v", got)
	}
}

// Each goal number is its lines, a tooltip on every point with its Δ, its interval and its run's
// p50 and p95, a whisker on every step, and every release's own p50 as a ring beside the line; a
// release without the number breaks its line rather than drawing a zero. No p95 band is drawn.
func TestTheChartDrawsEachGoalNumberAndBreaksItWhereAReleaseHasNone(t *testing.T) {
	v1 := rec("v1", map[string]Number{"cold": {340, 352.4}, "restore": {152, 161}, "save-ms": {900, 900}})
	v2 := paired("v2", Number{321, 330}, "v1", Delta{-19, -25, -12})
	v3 := rec("v3", map[string]Number{"cold": {340, 352.4}, "restore": {150, 160}})
	v3.Against = &Against{Release: "v2", Delta: map[string]Delta{"cold": {1, -4, 7}, "restore": {-2, -5, 1}}}
	d := draw(t, []Record{v1, v2, v3})
	if d.Title != chartTitle || !d.goal() {
		t.Errorf("title %q, goal drawn %v", d.Title, d.goal())
	}
	if got, want := d.lines("cold"), [][]string{{"v1 cold: the line starts at its run's p50 340, p95 352 ms",
		"v2 cold: Δ -19.0 [-25.0, -12.0] against v1; its run's p50 321, p95 330 ms",
		"v3 cold: Δ +1.0 [-4.0, +7.0] within noise against v2; its run's p50 340, p95 352 ms"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("cold's lines %q", got)
	}
	// v3's restore Δ is against v2, which has no restore: no run measured it against v1.
	if got, want := d.lines("restore"), [][]string{{"v1 restore: the line starts at its run's p50 152, p95 161 ms"},
		{"v3 restore: the line starts at its run's p50 150, p95 160 ms"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("restore's lines %q", got)
	}
	if got := d.lines("save-ms"); got != nil {
		t.Errorf("a diagnostic is charted: %q", got)
	}
	var own []string
	for _, m := range d.marks("cold", "own") {
		own = append(own, m.Title)
	}
	if want := []string{"v1 cold, its own run: p50 340, p95 352 ms", "v2 cold, its own run: p50 321, p95 330 ms",
		"v3 cold, its own run: p50 340, p95 352 ms"}; !slices.Equal(own, want) {
		t.Errorf("cold's own p50s %q", own)
	}
	for _, r := range d.Runs {
		if r.Number == "" {
			continue
		}
		points := 0
		for _, m := range r.Marks {
			if m.onLine() {
				points++
			}
		}
		polylines := 0
		if points > 1 {
			polylines = 1
		}
		if len(r.Polygons) != 0 || len(r.Polylines) != polylines || len(r.Whiskers) != points-1 || len(r.Labels) != points {
			t.Errorf("%s's line of %d: %d bands, %d lines, %d whiskers, labels %q", r.Number, points, len(r.Polygons), len(r.Polylines), len(r.Whiskers), r.Labels)
		}
	}
	if got := d.Runs[0].Labels; !slices.Equal(got, []string{"340", "-19.0", "+1.0"}) {
		t.Errorf("cold's labels %q", got)
	}
	texts := d.texts()
	for _, want := range []string{"v1", "v2", "v3", "cold boot to first login", "restore to first login", "ms", chartCaption} {
		if !slices.Contains(texts, want) {
			t.Errorf("no %q in %q", want, texts)
		}
	}
	// The save's 900 is not a goal number, nor is a p95 drawn, so neither stretches the axis.
	if got := d.axis(); !slices.Equal(got, []string{"0", "50", "100", "150", "200", "250", "300", "350", "400"}) {
		t.Errorf("the axis %q", got)
	}
}

// A release whose own run read far above the one before it, with a Δ of 0 between them, stays
// level on the line, its own p50 a ring above it; a Δ whose interval excludes 0 is a larger point
// than one within noise; a whisker spans the step's interval about the point before it.
func TestTheChartDrawsTheDriftBesideTheLineAndTheChangesLarger(t *testing.T) {
	records := []Record{
		rec("v1", map[string]Number{"cold": {344, 360}}),
		paired("v2", Number{376, 390}, "v1", Delta{0, -5.6, 8.2}),
		paired("v3", Number{360, 380}, "v2", Delta{-20, -28, -12}),
	}
	d := draw(t, records)
	start, noise, change := d.marks("cold", "start"), d.marks("cold", "noise"), d.marks("cold", "change")
	own := d.marks("cold", "own")
	if len(start) != 1 || len(noise) != 1 || len(change) != 1 || len(own) != 3 {
		t.Fatalf("start %v, noise %v, change %v, own %v", start, noise, change, own)
	}
	if noise[0].Y != start[0].Y || own[1].Y >= noise[0].Y {
		t.Errorf("v2's Δ of 0 is not level with v1 (%v, %v), or its run's 376 is not above it (%v)", start[0].Y, noise[0].Y, own[1].Y)
	}
	if change[0].Y <= noise[0].Y || change[0].R <= noise[0].R {
		t.Errorf("v3's -20 is not below v2 (%v, %v) or not larger (%v, %v)", change[0].Y, noise[0].Y, change[0].R, noise[0].R)
	}
	w := d.Runs[0].Whiskers
	if len(w) != 2 || w[0].X != noise[0].X || !(w[0].Y1 > start[0].Y && w[0].Y2 < start[0].Y) {
		t.Errorf("v2's whisker %+v does not span v1's point at %v", w, start[0].Y)
	}
	// v3's interval excludes 0: its whisker spans its point and stops short of v2's.
	if w[1].X != change[0].X || !(w[1].Y1 > change[0].Y && w[1].Y2 < change[0].Y && w[1].Y2 > noise[0].Y) {
		t.Errorf("v3's whisker %+v about its point at %v, v2's at %v", w[1], change[0].Y, noise[0].Y)
	}
	if svg := Chart(records); svg != Chart(records) {
		t.Error("the same records draw two charts")
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

// A release measured in another shape than the one before it starts its numbers' lines anew, and
// a rule says where the shape changed: two machines' numbers are not one line.
func TestTheChartBreaksEachLineWhereTheShapeChanges(t *testing.T) {
	n := Number{330, 345}
	spins := paired("v3", Number{473, 487}, "v2", Delta{-38, -45, -30})
	spins.Shape = "1 of 16 vCPUs, sized to 2048 MiB, spin's login"
	newer := paired("v4", Number{473, 487}, "v3", Delta{-1, -6, 4})
	newer.Shape = spins.Shape
	records := []Record{rec("v1", map[string]Number{"cold": n}), paired("v2", n, "v1", Delta{0, -5, 5}), spins, newer}
	svg := Chart(records)
	d := draw(t, records)
	if got := d.lines("cold"); len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 2 ||
		!strings.HasPrefix(got[1][0], "v3 cold: the line starts at its run's p50 473") {
		t.Errorf("cold's lines %q", got)
	}
	for _, r := range d.Runs {
		if r.Number != "" && len(r.Polylines) != 1 {
			t.Errorf("a line of two releases is drawn as %d lines", len(r.Polylines))
		}
	}
	if strings.Count(svg, `class="shape"`) != 1 || !strings.Contains(svg, "from here: "+html.EscapeString(spins.Shape)) {
		t.Errorf("the shape's change is not marked once, between v2 and v3:\n%s", svg)
	}
	if got := Chart(records[1:]); strings.Count(got, `class="shape"`) != 1 {
		t.Errorf("a change after the first release is not marked once:\n%s", got)
	}
	if got := Chart(records[:2]); strings.Contains(got, `class="shape"`) {
		t.Errorf("one shape is marked as a change:\n%s", got)
	}
}

// The axis runs from 0 to a round number above the highest mark drawn, a p50 or a whisker's top,
// or the goal, in eight steps or fewer of 25, 50, 100, 200, 500 or 1000 ms. A p95 does not raise it.
func TestTheChartsAxisIsRoundAndAboveEveryMark(t *testing.T) {
	for p50, want := range map[float64]string{
		0:    "0 25 50 75 100 125",
		320:  "0 50 100 150 200 250 300 350 400",
		400:  "0 100 200 300 400 500",
		600:  "0 100 200 300 400 500 600 700",
		1000: "0 200 400 600 800 1000 1200",
		1700: "0 500 1000 1500 2000",
		5000: "0 1000 2000 3000 4000 5000 6000",
	} {
		var records []Record
		if p50 > 0 {
			records = []Record{rec("v1", map[string]Number{"cold": {p50, 2 * p50}})}
		}
		if got := strings.Join(draw(t, records).axis(), " "); got != want {
			t.Errorf("a p50 of %v: the axis is %s, want %s", p50, got, want)
		}
	}
	// A whisker reaching above every point raises the axis: v2's point and its run's p50 are 300,
	// which alone top the axis at 350, and its interval's top is 360.
	whisker := []Record{rec("v1", map[string]Number{"cold": {300, 300}}), paired("v2", Number{300, 300}, "v1", Delta{0, -5, 60})}
	if got := strings.Join(draw(t, whisker).axis(), " "); got != "0 50 100 150 200 250 300 350 400" {
		t.Errorf("a whisker to 360: the axis is %s", got)
	}
}

// Too many releases for every point to carry its label: the tooltips have each value, the newest
// is labelled, and the releases' names are turned to fit.
func TestACrowdedChartLabelsTheNewestAndTurnsTheNames(t *testing.T) {
	records := []Record{rec("v00", map[string]Number{"cold": {300, 310}})}
	for i := 1; i < 30; i++ {
		records = append(records, paired(fmt.Sprintf("v%02d", i), Number{float64(300 + i), 310}, fmt.Sprintf("v%02d", i-1), Delta{1, -3, 5}))
	}
	d := draw(t, records)
	if len(d.Runs) != 1 || len(d.lines("cold")[0]) != 30 || !slices.Equal(d.Runs[0].Labels, []string{"+1.0"}) {
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

// No records is an empty chart with its goal; one is a point on its own p50 and its ring; a
// release's name is text, whatever it holds.
func TestAChartOfNoneOrOneRelease(t *testing.T) {
	if d := draw(t, nil); len(d.Runs) != 0 || !d.goal() {
		t.Errorf("no records: %+v", d)
	}
	d := draw(t, []Record{rec("<v1 & co>", map[string]Number{"restore": {1500, 1700}})})
	if got := d.lines("restore"); len(got) != 1 || !slices.Equal(got[0], []string{"<v1 & co> restore: the line starts at its run's p50 1500, p95 1700 ms"}) {
		t.Errorf("one record: %q", got)
	}
	r := d.Runs[0]
	if len(r.Polygons) != 0 || len(r.Polylines) != 0 || len(r.Whiskers) != 0 || !slices.Equal(r.Labels, []string{"1500"}) {
		t.Fatalf("one record's run %+v", r)
	}
	if own := d.marks("restore", "own"); len(own) != 1 || own[0].Y != r.Marks[1].Y {
		t.Errorf("one release's ring %v is not on its point %v", own, r.Marks)
	}
}
