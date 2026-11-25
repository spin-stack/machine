// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// The chart's canvas and its plot, in px. Fixed, so that the same records are the same bytes and
// docs/releases.svg changes only when a record does.
const (
	chartWidth, chartHeight = 800, 400
	plotLeft, plotRight     = 64.0, 776.0
	plotTop, plotBottom     = 88.0, 330.0
)

// seriesColours are the goal numbers' colours, in the registry's order: never cycled, so a goal
// number added to the registry needs a colour of its own here. One SVG is shown on GitHub's light
// page and on its dark one, and an image cannot follow the theme, so each is a mid-saturation hue
// that clears 3:1 on both #ffffff and #0d1117 and that a colour-blind reader tells from the other
// (dataviz's palette checks, 2026-11-20).
var seriesColours = []string{"#2a78d6", "#d95926"}

// ink is the text's and the axes' grey: 3.9:1 on GitHub's light page, 4.8:1 on its dark one.
const ink = "#808080"

// labelRoom is the room a release needs for each of its points to carry its label: closer than
// this and the labels run into each other, so only a line's last point is labelled; the tooltips
// have every value. A release's name under its point is rotated below twice that.
const labelRoom = 40.0

// chartCaption says what the line is, above the plot.
const chartCaption = "First SSH login, ms. The line adds up each release's Δ p50 against the one before it, " +
	"booted in the same run, from the first of its stretch."

// step is one release's point on a goal number's line. The line's first step stands at the
// release's own p50; every later one at the step before it plus delta, the release's Δ p50 against
// that step's release, booted interleaved with it in the same run.
type step struct {
	at    int     // the record's index
	value float64 // where the line is at this release
	delta *Delta  // nil on a line's first step: there it moved by nothing measured
	own   Number  // the release's p50 and p95 from its own run
}

// measured is whether s moved the line by a Δ whose interval excludes 0: a change, under the
// lab's one acceptance rule (Delta).
func (s step) measured() bool { return s.delta != nil && !s.delta.WithinNoise() }

// high is the highest of s's marks: its point, its run's own p50, and its whisker's top.
func (s step) high() float64 {
	hi := max(s.value, s.own.P50)
	if s.delta != nil {
		hi = max(hi, s.value-s.delta.P50+s.delta.Hi)
	}
	return hi
}

// chains are each line of the number key: the records' runs (runs), each cut again wherever a
// release has no Δ for key against the release before it on the chart, booted in the same run.
// A line moves only by what one run measured; where nothing did, a new one starts at the
// release's own p50.
func chains(records []Record, key string) [][]step {
	var out [][]step
	for _, run := range runs(records, key) {
		var line []step
		for k, i := range run {
			r := records[i]
			s := step{at: i, value: r.Numbers[key].P50, own: r.Numbers[key]}
			if k > 0 && r.Against != nil && r.Against.Release == records[run[k-1]].Release {
				if d, ok := r.Against.Delta[key]; ok {
					s.delta, s.value = &d, line[len(line)-1].value+d.P50
				}
			}
			if s.delta == nil && line != nil {
				out = append(out, line)
				line = nil
			}
			line = append(line, s)
		}
		out = append(out, line)
	}
	return out
}

// Chart is the goal's numbers release by release as an SVG, drawn from what was measured: a Δ is
// read only between two releases booted in the same run (about), so each goal number's line moves
// only by those. The line is each release's Δ p50 against the release before it in its run, added
// up from the first release of its stretch, which stands at its own p50; a whisker is each Δ's 95%
// interval, and a point whose interval excludes 0, a change, is drawn larger than one within
// noise. Each release's p50 from its own run is a hollow point beside the line, joined to none, so
// that how far the bench moved between runs shows as that and not as a release's change. A line
// joining those p50s read v20261124.01's 376 ms cold against v20261123.08's 344 as a regression
// where its same-run Δ was +0.0 [-5.6, +8.2] (docs/log/2026-11-25-a-false-regression-and-a-chart-of-paired-deltas.md).
// No p95 is drawn, as it has no Δ and a band across runs carries the same drift; the tooltips have
// every p95.
//
// A line breaks where its chain does (chains): at a release without the number, where a zero would
// draw a release that was never measured; at one measured with no release before it, or against
// another than the one before it on the chart; and at one measured in another shape than the one
// before it: v20261120.03, the first in spin's shape, read 473 ms cold against v20261120.02's 328
// in the shape before, and 38 ms faster than v20261120.02 booted beside it in its own (lab
// 44805222647). A dashed rule between the two says where the shape changed.
func Chart(records []Record) string {
	var goals []number
	var lines [][][]step
	for _, n := range numbers {
		if n.kind == goalNumber {
			goals = append(goals, n)
			lines = append(lines, chains(records, n.key))
		}
	}
	top, tick := axis(lines)
	slot := (plotRight - plotLeft) / float64(max(len(records), 1)) // mutate-exempt: with no records nothing is placed in a slot; any divisor but 0 draws the same chart
	x := func(i int) float64 { return plotLeft + slot*(float64(i)+0.5) }
	y := func(v float64) float64 { return plotBottom - (plotBottom-plotTop)*v/top }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" `+
		`font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif" font-size="12" fill="%s">`+"\n",
		chartWidth, chartHeight, chartWidth, chartHeight, ink)
	fmt.Fprintf(&b, "<title>%s</title>\n", chartTitle)

	for v := 0.0; v <= top; v += tick {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-opacity="0.3"/>`+"\n",
			plotLeft, y(v), plotRight, y(v), ink)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end">%.0f</text>`+"\n", plotLeft-8, y(v)+4, v)
	}
	fmt.Fprintf(&b, `<text transform="translate(16 %.1f) rotate(-90)" text-anchor="middle">ms</text>`+"\n", (plotTop+plotBottom)/2)
	for i, r := range records {
		name := html.EscapeString(r.Release)
		if slot < 2*labelRoom {
			// Its end just right of the point's foot, below the axis, so the name reads up to its point.
			fmt.Fprintf(&b, `<text transform="translate(%.1f %.1f) rotate(-40)" text-anchor="end">%s</text>`+"\n", x(i)+4, plotBottom+16, name) // mutate-exempt: px offsets from the point's foot; a px either way is the same chart, and TestACrowdedChartLabelsTheNewestAndTurnsTheNames holds the names below the axis
		} else {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle">%s</text>`+"\n", x(i), plotBottom+20, name) // mutate-exempt: px below the axis for a release's name; a px either way is the same chart
		}
	}

	for i := 1; i < len(records); i++ {
		if records[i].shape() == records[i-1].shape() {
			continue
		}
		mx := (x(i-1) + x(i)) / 2
		fmt.Fprintf(&b, `<g class="shape"><line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-dasharray="2 4"/>`+
			`<text x="%.1f" y="%.1f" text-anchor="end">shape changes</text><title>from here: %s</title></g>`+"\n",
			mx, plotTop, mx, plotBottom, ink, mx-4, plotTop+12, html.EscapeString(records[i].shape()))
	}

	fmt.Fprintf(&b, `<line class="goal" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5" stroke-dasharray="6 4"/>`+"\n",
		plotLeft, y(Goal), plotRight, y(Goal), ink)

	for s, n := range goals {
		for _, line := range lines[s] {
			series(&b, records, n, line, seriesColours[s], x, y, slot >= labelRoom)
		}
	}

	// The legend, along the top: a swatch of each line, then the goal's with its value. The value
	// is not written on the goal's line: at its right end it sat on the newest release's points and
	// their labels, the place a reader looks first.
	lx := plotLeft
	for s, n := range goals {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="30" x2="%.1f" y2="30" stroke="%s" stroke-width="2"/>`, lx, lx+20, seriesColours[s])
		fmt.Fprintf(&b, `<text x="%.1f" y="34">%s</text>`+"\n", lx+26, html.EscapeString(n.title))
		lx += 26 + 7*float64(len(n.title)) + 24
	}
	fmt.Fprintf(&b, `<line x1="%.1f" y1="30" x2="%.1f" y2="30" stroke="%s" stroke-width="1.5" stroke-dasharray="6 4"/>`, lx, lx+20, ink)
	fmt.Fprintf(&b, `<text x="%.1f" y="34">goal %.0f ms</text>`+"\n", lx+26, Goal)
	fmt.Fprintf(&b, `<text x="%.1f" y="56">%s</text>`+"\n", plotLeft, chartCaption)
	// The key to the marks, in the text's grey: they are the same marks in either line's colour.
	kx := plotLeft
	for _, k := range []struct{ mark, text string }{
		{"change", "a change: its 95% interval excludes 0"},
		{"noise", "within noise"},
		{"whisker", "the Δ's 95% interval"},
		{"own", "a run's own p50"},
	} {
		cx := kx + 6
		if k.mark == "whisker" {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="68" x2="%.1f" y2="80" stroke="%s" stroke-width="1.5"/>`, cx, cx, ink)
		} else {
			b.WriteString(point(k.mark, cx, 74, ink, ""))
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="78">%s</text>`+"\n", kx+16, k.text)
		kx += 16 + 6.5*float64(len([]rune(k.text))) + 20
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// point is one mark of a line, its tooltip title when there is one. The transparent stroke is a
// point's hit area, wider than its mark, for the tooltip. A change is a large dot and a step within
// noise, or a line's start, a small one, so that the eye finds the changes first; a run's own p50
// is a faint ring that the line's dots never look like, its zero-opacity fill there to be hovered.
func point(class string, cx, cy float64, colour, title string) string {
	var mark string
	switch class {
	case "change":
		mark = fmt.Sprintf(`r="5" fill="%s"`, colour)
	case "own":
		mark = fmt.Sprintf(`r="4" fill="%s" fill-opacity="0" stroke="%s" stroke-opacity="0.6" stroke-width="1.5"`, colour, colour)
	default:
		mark = fmt.Sprintf(`r="2.5" fill="%s"`, colour)
	}
	hit := ""
	if class != "own" {
		hit = fmt.Sprintf(` stroke="%s" stroke-opacity="0" stroke-width="12"`, colour)
	}
	if title == "" {
		return fmt.Sprintf(`<circle class="%s" cx="%.1f" cy="%.1f" %s%s/>`, class, cx, cy, mark, hit)
	}
	return fmt.Sprintf(`<circle class="%s" cx="%.1f" cy="%.1f" %s%s><title>%s</title></circle>`+"\n",
		class, cx, cy, mark, hit, html.EscapeString(title))
}

// axis is the y axis' top and the step between its lines: a tenth above the highest value drawn
// (a line's point, the top of its whisker, a run's own p50), or the goal when that is higher,
// rounded up to the first step of 25, 50, 100, 200, 500 or 1000 ms that takes eight lines or fewer.
func axis(lines [][][]step) (top, tick float64) {
	hi := Goal
	for _, number := range lines {
		for _, line := range number {
			for _, s := range line {
				hi = max(hi, s.high())
			}
		}
	}
	hi *= 1.1
	for _, tick = range []float64{25, 50, 100, 200, 500, 1000} {
		if math.Ceil(hi/tick) <= 8 {
			break
		}
	}
	return math.Ceil(hi/tick) * tick, tick
}

// runs are the stretches of consecutive records in one shape that have the number key, each as the
// records' indexes: no line joins two releases across one without the number, nor two machines.
func runs(records []Record, key string) [][]int {
	var out [][]int
	var run []int
	for i, r := range records {
		if run != nil && r.shape() != records[run[len(run)-1]].shape() {
			out = append(out, run)
			run = nil
		}
		if _, ok := r.Numbers[key]; ok {
			run = append(run, i)
			continue
		}
		if run != nil {
			out = append(out, run)
			run = nil
		}
	}
	if run != nil {
		out = append(out, run)
	}
	return out
}

// series is one line of n as a group: each release's own p50 as a ring, the line, each step's
// whisker, and a point on each release with a tooltip and, when labelled, its Δ above it, or its
// p50 where the line starts. A line's last point is always labelled, so the newest release's
// change is on the chart however many releases there are.
func series(b *strings.Builder, records []Record, n number, line []step, colour string, x func(int) float64, y func(float64) float64, labelled bool) {
	fmt.Fprintf(b, `<g class="run" data-number="%s">`+"\n", n.key)
	for _, s := range line {
		b.WriteString(point("own", x(s.at), y(s.own.P50), colour,
			fmt.Sprintf("%s %s, its own run: p50 %s, p95 %s ms", records[s.at].Release, n.key, num(s.own.P50), num(s.own.P95))))
	}
	if len(line) > 1 {
		var pts []string
		for _, s := range line {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(s.at), y(s.value)))
		}
		fmt.Fprintf(b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2"/>`+"\n", strings.Join(pts, " "), colour)
	}
	for _, s := range line {
		if s.delta == nil {
			continue
		}
		from := s.value - s.delta.P50
		fmt.Fprintf(b, `<line class="whisker" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5"/>`+"\n",
			x(s.at), y(from+s.delta.Lo), x(s.at), y(from+s.delta.Hi), colour)
	}
	for k, s := range line {
		r := records[s.at]
		class, title, label := "start", fmt.Sprintf("%s %s: the line starts at its run's p50 %s, p95 %s ms",
			r.Release, n.key, num(s.own.P50), num(s.own.P95)), num(s.own.P50)
		if s.delta != nil {
			class, title, label = "noise", fmt.Sprintf("%s %s: Δ %s against %s; its run's p50 %s, p95 %s ms",
				r.Release, n.key, s.delta, r.Against.Release, num(s.own.P50), num(s.own.P95)), fmt.Sprintf("%+.1f", s.delta.P50)
			if s.measured() {
				class = "change"
			}
		}
		b.WriteString(point(class, x(s.at), y(s.value), colour, title))
		if labelled || k == len(line)-1 {
			// Above every mark at the release, its ring and its whisker too, which a label on the
			// line's point ran into.
			fmt.Fprintf(b, `<text x="%.1f" y="%.1f" text-anchor="middle">%s</text>`+"\n", x(s.at), y(s.high())-10, label)
		}
	}
	b.WriteString("</g>\n")
}
