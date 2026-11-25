// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/xml"
	"fmt"
	"html"
	"math"
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

// whisker is one line a panel draws through a mark.
type whisker struct {
	X  float64 `xml:"x1,attr"`
	Y1 float64 `xml:"y1,attr"`
	Y2 float64 `xml:"y2,attr"`
}

// drawn is what a chart shows, read back from its SVG as a browser would parse it.
type drawn struct {
	Title string `xml:"title"`
	Lines []struct {
		Y1 float64 `xml:"y1,attr"`
		Y2 float64 `xml:"y2,attr"`
	} `xml:"line"`
	Polylines []struct{} `xml:"polyline"`
	Texts     []struct {
		Y         float64 `xml:"y,attr"`
		Transform string  `xml:"transform,attr"`
		Text      string  `xml:",chardata"`
	} `xml:"text"`
	Panels []struct {
		Class     string     `xml:"class,attr"`
		Number    string     `xml:"data-number,attr"`
		Polylines []struct{} `xml:"polyline"`
		Whiskers  []whisker  `xml:"line"`
		Marks     []mark     `xml:"circle"`
		Labels    []string   `xml:"text"`
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

// texts are the chart's text outside its panels' marks: the axes, the releases, the legend.
func (d drawn) texts() []string {
	var out []string
	for _, x := range d.Texts {
		out = append(out, x.Text)
	}
	return out
}

// panel is number's marks, whiskers and labels; ok is false when the chart has no panel for it.
func (d drawn) panel(number string) (marks []mark, whiskers []whisker, labels []string, ok bool) {
	for _, p := range d.Panels {
		if p.Class == "panel" && p.Number == number {
			return p.Marks, p.Whiskers, p.Labels, true
		}
	}
	return nil, nil, nil, false
}

// tick is one label of a panel's y axis and the height it is written at.
type tick struct{ v, y float64 }

// axes are each panel's y axis, top panel first, read from the numbers written left of the plot:
// each axis is written from its bottom up, so a value lower than the one before starts the next.
func (d drawn) axes() [][]tick {
	var out [][]tick
	for _, x := range d.Texts {
		v, err := strconv.ParseFloat(x.Text, 64)
		if err != nil {
			continue
		}
		if len(out) == 0 || v < out[len(out)-1][len(out[len(out)-1])-1].v {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], tick{v, x.Y - 4})
	}
	return out
}

// at is the height panel k's axis draws the value v at.
func (d drawn) at(k int, v float64) float64 {
	a := d.axes()[k]
	lo, hi := a[0], a[len(a)-1]
	return lo.y + (hi.y-lo.y)*(v-lo.v)/(hi.v-lo.v)
}

// onePx is whether two heights are one, as the SVG writes them to a tenth of a px.
func onePx(a, b float64) bool { return math.Abs(a-b) < 0.11 }

func rec(release string, numbers map[string]Number) Record {
	return Record{Release: release, Numbers: numbers}
}

// paired is release with its cold number, measured against before in the same run with the Δ d.
func pairedRec(release string, cold Number, before string, d Delta) Record {
	r := rec(release, map[string]Number{"cold": cold})
	r.Against = &Against{Release: before, Delta: map[string]Delta{"cold": d}}
	return r
}

// A mark stands at its release's own Δ, its whisker from the interval's low end to its high one,
// and nothing adds up: two releases each +10 against the one before are two marks at +10, not
// the second at +20. Neither release's own p50 is drawn, nor any line through the marks.
func TestEachMarkIsItsReleasesOwnDeltaNotASum(t *testing.T) {
	records := []Record{
		rec("v1", map[string]Number{"cold": {340, 352}}),
		pairedRec("v2", Number{400, 410}, "v1", Delta{10, 4, 16}),
		pairedRec("v3", Number{300, 310}, "v2", Delta{10, 4, 16}),
	}
	d := draw(t, records)
	marks, whiskers, labels, ok := d.panel("cold")
	if !ok || len(marks) != 2 || len(whiskers) != 2 {
		t.Fatalf("cold: marks %v, whiskers %v", marks, whiskers)
	}
	for k, m := range marks {
		if !onePx(m.Y, d.at(0, 10)) {
			t.Errorf("%s at %v, not at +10 (%v)", m.Title, m.Y, d.at(0, 10))
		}
		w := whiskers[k]
		if w.X != m.X || !onePx(w.Y1, d.at(0, 4)) || !onePx(w.Y2, d.at(0, 16)) {
			t.Errorf("%s's whisker %+v, not from +4 to +16 at x %v", m.Title, w, m.X)
		}
	}
	if !slices.Equal(labels, []string{"+10.0", "+10.0"}) {
		t.Errorf("labels %q", labels)
	}
	if len(d.Polylines) != 0 || strings.Contains(Chart(records), "polyline") {
		t.Error("the marks are joined")
	}
	for _, s := range d.texts() {
		if s == "400" || s == "340" || s == "300" {
			t.Errorf("a release's own p50 %s is on the chart", s)
		}
	}
	if Chart(records) != Chart(records) {
		t.Error("the same records draw two charts")
	}
}

// A Δ whose interval excludes 0 is a change, a larger mark than one within noise; a gain is below
// the zero line and a loss above it.
func TestAChangeIsLargerAndAGainIsBelowZero(t *testing.T) {
	records := []Record{
		rec("v1", map[string]Number{"cold": {344, 360}}),
		pairedRec("v2", Number{376, 390}, "v1", Delta{0, -5.6, 8.2}),
		pairedRec("v3", Number{360, 380}, "v2", Delta{-20, -28, -12}),
		pairedRec("v4", Number{380, 390}, "v3", Delta{15, 6, 22}),
	}
	d := draw(t, records)
	marks, _, _, _ := d.panel("cold")
	if len(marks) != 3 {
		t.Fatalf("marks %v", marks)
	}
	noise, gain, loss, zero := marks[0], marks[1], marks[2], d.at(0, 0)
	if noise.Class != "noise" || gain.Class != "change" || loss.Class != "change" {
		t.Errorf("classes %s %s %s", noise.Class, gain.Class, loss.Class)
	}
	if gain.R <= noise.R || loss.R != gain.R {
		t.Errorf("radii: noise %v, gain %v, loss %v", noise.R, gain.R, loss.R)
	}
	if !onePx(noise.Y, zero) || gain.Y <= zero || loss.Y >= zero {
		t.Errorf("zero at %v: noise %v, gain %v, loss %v", zero, noise.Y, gain.Y, loss.Y)
	}
}

// Only a release with a Δ for the number against the release before it on the chart has a mark:
// not the first release, one measured alone, one measured against another release (the one
// before a withdrawn one), nor one whose run has no Δ for the number.
func TestOnlyASameRunDeltaAgainstTheReleaseBeforeIsMarked(t *testing.T) {
	d := Delta{-10, -14, -6}
	alone := rec("v2", map[string]Number{"cold": {320, 330}})
	noCold := pairedRec("v4", Number{300, 310}, "v3", d)
	noCold.Against.Delta = map[string]Delta{"restore": d}
	records := []Record{
		rec("v1", map[string]Number{"cold": {340, 350}}),
		alone,
		pairedRec("v3", Number{310, 320}, "v1", d),
		noCold,
		pairedRec("v5", Number{290, 300}, "v4", d),
	}
	var got []string
	marks, _, _, _ := draw(t, records).panel("cold")
	for _, m := range marks {
		got = append(got, m.Title)
	}
	if want := []string{"v5 cold: Δ -10.0 [-14.0, -6.0] against v4, booted in the same run"}; !slices.Equal(got, want) {
		t.Errorf("cold's marks %q", got)
	}
	if got := pairs(records, "restore"); len(got) != 1 || got[0].at != 3 {
		t.Errorf("restore's pairs %+v", got)
	}
}

// Each goal number is a panel of its own, cold above restore, each on its own axis; a diagnostic
// is none. Each mark's tooltip is its Δ as the lab prints it, against the release it was booted
// beside.
func TestTheChartDrawsEachGoalNumberInAPanelOfItsOwn(t *testing.T) {
	v1 := rec("v1", map[string]Number{"cold": {340, 352.4}, "restore": {152, 161}, "save-ms": {900, 900}})
	v2 := rec("v2", map[string]Number{"cold": {321, 330}, "restore": {150, 160}, "save-ms": {800, 800}})
	v2.Against = &Against{Release: "v1", Delta: map[string]Delta{"cold": {-19, -25, -12}, "restore": {-2, -5, 1}, "save-ms": {-100, -110, -90}}}
	d := draw(t, []Record{v1, v2})
	if d.Title != chartTitle {
		t.Errorf("title %q", d.Title)
	}
	var numbers []string
	for _, p := range d.Panels {
		if p.Class == "panel" {
			numbers = append(numbers, p.Number)
		}
	}
	if !slices.Equal(numbers, []string{"cold", "restore"}) {
		t.Errorf("panels %q", numbers)
	}
	cold, _, _, _ := d.panel("cold")
	restore, _, _, _ := d.panel("restore")
	if len(cold) != 1 || cold[0].Title != "v2 cold: Δ -19.0 [-25.0, -12.0] against v1, booted in the same run" ||
		len(restore) != 1 || restore[0].Title != "v2 restore: Δ -2.0 [-5.0, +1.0] within noise against v1, booted in the same run" {
		t.Errorf("tooltips %v %v", cold, restore)
	}
	if cold[0].Y >= d.at(1, 0) || restore[0].Y <= d.at(0, 0) {
		t.Errorf("cold's mark %v is not above restore's panel, or restore's %v not below cold's", cold[0].Y, restore[0].Y)
	}
	axes := d.axes()
	if len(axes) != 2 || axes[0][len(axes[0])-1].v != 30 || axes[1][len(axes[1])-1].v != 6 {
		t.Errorf("axes %v", axes)
	}
	texts := d.texts()
	for _, want := range []string{"v1", "v2", "cold boot to first login, Δ ms", "restore to first login, Δ ms", chartCaption, "below 0 is faster"} {
		if !slices.Contains(texts, want) {
			t.Errorf("no %q in %q", want, texts)
		}
	}
}

// A panel's axis is symmetric about 0 and round: it reaches a tenth past the farthest Δ or
// interval end, in four steps or fewer each side of 0 of 1, 2, 5, 10, 20, 50, 100, 200, 500 or
// 1000 ms.
func TestAPanelsAxisIsSymmetricRoundAndBeyondEveryMark(t *testing.T) {
	for _, c := range []struct {
		d    Delta
		want string
	}{
		{Delta{0, -0.5, 0.5}, "-2 -1 0 +1 +2"},
		{Delta{1, 0.1, 2.6}, "-3 -2 -1 0 +1 +2 +3"},
		{Delta{-46.7, -48.2, -43.5}, "-60 -40 -20 0 +20 +40 +60"},
		{Delta{-57.4, -65.4, -46.7}, "-80 -60 -40 -20 0 +20 +40 +60 +80"},
		{Delta{100, 90, 110}, "-150 -100 -50 0 +50 +100 +150"},
		{Delta{-3000, -3500, -2500}, "-4000 -3000 -2000 -1000 0 +1000 +2000 +3000 +4000"},
	} {
		records := []Record{rec("v1", nil), pairedRec("v2", Number{}, "v1", c.d)}
		var got []string
		for _, k := range draw(t, records).axes()[0] {
			got = append(got, strconv.FormatFloat(k.v, 'f', -1, 64))
		}
		if want := strings.ReplaceAll(c.want, "+", ""); strings.Join(got, " ") != want {
			t.Errorf("a Δ of %v: the axis is %s, want %s", c.d, strings.Join(got, " "), c.want)
		}
	}
}

// A dashed rule in each panel says where the shape the releases were booted in changes. The first
// release in the new shape keeps its mark: its run booted it and the release before it in one shape.
func TestTheChartMarksWhereTheShapeChanges(t *testing.T) {
	n := Number{330, 345}
	spins := pairedRec("v3", Number{473, 487}, "v2", Delta{-38, -45, -30})
	spins.Shape = "1 of 16 vCPUs, sized to 2048 MiB, spin's login"
	newer := pairedRec("v4", Number{473, 487}, "v3", Delta{-1, -6, 4})
	newer.Shape = spins.Shape
	records := []Record{rec("v1", map[string]Number{"cold": n}), pairedRec("v2", n, "v1", Delta{0, -5, 5}), spins, newer}
	svg := Chart(records)
	if strings.Count(svg, `class="shape"`) != 1 || !strings.Contains(svg, "from here: "+html.EscapeString(spins.Shape)) {
		t.Errorf("the shape's change is not marked once, between v2 and v3:\n%s", svg)
	}
	d := draw(t, records)
	marks, _, _, _ := d.panel("cold")
	if len(marks) != 3 || !strings.HasPrefix(marks[1].Title, "v3 cold: Δ -38.0") {
		t.Errorf("cold's marks %v", marks)
	}
	for _, p := range d.Panels {
		if p.Class != "shape" {
			continue
		}
		if w := p.Whiskers; len(w) != 2 || w[0].Y1 > d.at(0, 0) || w[0].Y2 < d.at(0, 0) || w[1].Y1 > d.at(1, 0) || w[1].Y2 < d.at(1, 0) {
			t.Errorf("the rule %+v is not in both panels", w)
		}
	}
	if got := Chart(records[1:]); strings.Count(got, `class="shape"`) != 1 {
		t.Errorf("a change after the first release is not marked once:\n%s", got)
	}
	if got := Chart(records[:2]); strings.Contains(got, `class="shape"`) {
		t.Errorf("one shape is marked as a change:\n%s", got)
	}
}

// Too many releases for every mark to carry its label: the tooltips have each value, the newest
// is labelled, and the releases' names are turned to fit below the plot.
func TestACrowdedChartLabelsTheNewestAndTurnsTheNames(t *testing.T) {
	records := []Record{rec("v00", map[string]Number{"cold": {300, 310}})}
	for i := 1; i < 30; i++ {
		records = append(records, pairedRec(fmt.Sprintf("v%02d", i), Number{float64(300 + i), 310}, fmt.Sprintf("v%02d", i-1), Delta{float64(i), -3, 30}))
	}
	d := draw(t, records)
	marks, _, labels, _ := d.panel("cold")
	if len(marks) != 29 || !slices.Equal(labels, []string{"+29.0"}) {
		t.Errorf("marks %d, labels %q", len(marks), labels)
	}
	bottom := d.at(1, -2)
	for _, x := range d.Texts {
		if !strings.HasPrefix(x.Text, "v") {
			continue
		}
		var px, py float64
		if _, err := fmt.Sscanf(x.Transform, "translate(%g %g) rotate(-40)", &px, &py); err != nil || py <= bottom {
			t.Errorf("%s is not turned below the plot: %q", x.Text, x.Transform)
		}
	}
	// Seven releases' names fit level, and each mark keeps its label.
	if _, _, labels, _ := draw(t, records[:7]).panel("cold"); len(labels) != 6 || strings.Contains(Chart(records[:7]), "rotate(-40)") {
		t.Errorf("seven releases: labels %q", labels)
	}
}

// No records, or one, is two empty panels about 0; a release's name is text, whatever it holds.
func TestAChartOfNoneOrOneRelease(t *testing.T) {
	for _, records := range [][]Record{nil, {rec("<v1 & co>", map[string]Number{"restore": {1500, 1700}})}} {
		d := draw(t, records)
		for _, n := range []string{"cold", "restore"} {
			if marks, _, _, ok := d.panel(n); !ok || len(marks) != 0 {
				t.Errorf("%d records, %s: marks %v, panel %v", len(records), n, marks, ok)
			}
		}
		if got := d.axes(); len(got) != 2 {
			t.Errorf("%d records: axes %v", len(records), got)
		}
	}
	if !slices.Contains(draw(t, []Record{rec("<v1 & co>", nil)}).texts(), "<v1 & co>") {
		t.Error("the release's name is not its text")
	}
}
