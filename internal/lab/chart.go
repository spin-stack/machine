// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// The chart's canvas, its plot's left and right, and each goal number's panel's top and bottom, in
// px, the first goal number's panel on top. Fixed, so that the same records are the same bytes and
// docs/releases.svg changes only when a record does.
const (
	chartWidth, chartHeight = 800, 470
	plotLeft, plotRight     = 64.0, 776.0
	panelTop, panelHeight   = 84.0, 140.0
	panelGap                = 38.0 // room above a panel for its name
)

// seriesColours are the goal numbers' colours, in the registry's order: never cycled, so a goal
// number added to the registry needs a colour of its own here. One SVG is shown on GitHub's light
// page and on its dark one, and an image cannot follow the theme, so each is a mid-saturation hue
// that clears 3:1 on both #ffffff and #0d1117 and that a colour-blind reader tells from the other
// (dataviz's palette checks, 2026-11-20).
var seriesColours = []string{"#2a78d6", "#d95926"}

// ink is the text's and the axes' grey: 3.9:1 on GitHub's light page, 4.8:1 on its dark one.
const ink = "#808080"

// labelRoom is the room a release needs for each of its marks to carry its label: closer than
// this and the labels run into each other, so only a panel's newest mark is labelled; the tooltips
// have every value. A release's name under the plot is rotated below twice that.
const labelRoom = 40.0

// chartCaption says what a mark is, above the plot.
const chartCaption = "Δ p50, ms: a release less the release before it, both booted in the same run. " +
	"Levels are in the table, as runs differ."

// paired is one release's Δ for a goal number against the release before it on the chart, both
// booted interleaved in the same run: the only difference between two releases the lab reads.
type paired struct {
	at      int    // the record's index
	against string // the release before it, which the run booted beside it
	delta   Delta
}

// pairs are the releases with a Δ for key against the release before them on the chart: not the
// first release, one measured alone, one measured against another release (the one before a
// withdrawn one), nor one whose run has no Δ for key.
func pairs(records []Record, key string) []paired {
	var out []paired
	for i := 1; i < len(records); i++ {
		a := records[i].Against
		if a == nil || a.Release != records[i-1].Release {
			continue
		}
		if d, ok := a.Delta[key]; ok {
			out = append(out, paired{at: i, against: a.Release, delta: d})
		}
	}
	return out
}

// Chart is the goal's numbers release by release as an SVG, drawn from what was measured: a Δ is
// read only between two releases booted in the same run (about). Each goal number is a panel,
// cold above restore, around a zero line and on its own axis, as cold's Δs run to tens of ms and
// restore's to a few. A mark is a release's p50 less the release before it, both booted
// interleaved in the same run, its whisker the Δ's 95% interval: below 0 the release is faster.
// A Δ whose interval excludes 0, a change, is a large dot, and one within noise a small one. The
// marks are not joined nor added up: no run measured a level across runs, and a sum of Δs adds up
// their errors, so a line through them drew a level that no run read. A release without a Δ
// against the release before it on the chart has no mark (pairs). The levels are in the page's
// tables: a line joining each release's p50 from its own run read v20261124.01's 376 ms cold
// against v20261123.08's 344 as a regression where its same-run Δ was +0.0 [-5.6, +8.2]
// (docs/log/2026-11-25-a-false-regression-and-a-chart-of-paired-deltas.md).
//
// A dashed rule says where the shape the releases were booted in changes: v20261120.03, the first
// in spin's shape, read 473 ms cold against v20261120.02's 328 in the shape before, and 38 ms
// faster than v20261120.02 booted beside it in its own (lab 44805222647). Its Δ is a mark, as that
// run booted both releases in one shape.
func Chart(records []Record) string {
	slot := (plotRight - plotLeft) / float64(max(len(records), 1)) // mutate-exempt: with no records nothing is placed in a slot; any divisor but 0 draws the same chart
	x := func(i int) float64 { return plotLeft + slot*(float64(i)+0.5) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" `+
		`font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif" font-size="12" fill="%s">`+"\n",
		chartWidth, chartHeight, chartWidth, chartHeight, ink)
	fmt.Fprintf(&b, "<title>%s</title>\n", chartTitle)
	fmt.Fprintf(&b, `<text x="%.1f" y="24">%s</text>`+"\n", plotLeft, chartCaption)
	// The key to the marks, in the text's grey: they are the same marks in either panel's colour.
	kx := plotLeft
	for _, k := range []struct{ mark, text string }{
		{"change", "a change: its 95% interval excludes 0"},
		{"noise", "within noise"},
		{"whisker", "the Δ's 95% interval"},
		{"", "below 0 is faster"},
	} {
		cx := kx + 6
		switch k.mark {
		case "whisker":
			fmt.Fprintf(&b, `<line x1="%.1f" y1="40" x2="%.1f" y2="52" stroke="%s" stroke-width="1.5"/>`, cx, cx, ink)
		case "":
			kx -= 16
		default:
			b.WriteString(point(k.mark, cx, 46, ink, ""))
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="50">%s</text>`+"\n", kx+16, k.text)
		kx += 16 + 6*float64(len([]rune(k.text))) + 20
	}

	var tops []float64
	top := panelTop
	for s, n := range goalNumbers() {
		panel(&b, records, n, pairs(records, n.key), seriesColours[s], x, top, top+panelHeight, slot >= labelRoom)
		tops = append(tops, top)
		top += panelHeight + panelGap
	}
	bottom := top - panelGap

	for i, r := range records {
		name := html.EscapeString(r.Release)
		if slot < 2*labelRoom {
			// Its end just right of the slot's middle, below the plot, so the name reads up to its marks.
			fmt.Fprintf(&b, `<text transform="translate(%.1f %.1f) rotate(-40)" text-anchor="end">%s</text>`+"\n", x(i)+4, bottom+16, name) // mutate-exempt: px offsets from the plot's foot; a px either way is the same chart, and TestACrowdedChartLabelsTheNewestAndTurnsTheNames holds the names below the plot
		} else {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle">%s</text>`+"\n", x(i), bottom+20, name) // mutate-exempt: px below the plot for a release's name; a px either way is the same chart
		}
	}

	for i := 1; i < len(records); i++ {
		if records[i].shape() == records[i-1].shape() {
			continue
		}
		// A rule in each panel and none across the room between them, where it crossed a panel's name.
		mx := (x(i-1) + x(i)) / 2
		b.WriteString(`<g class="shape">`)
		for _, t := range tops {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-dasharray="2 4"/>`, mx, t, mx, t+panelHeight, ink)
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end">shape changes</text><title>from here: %s</title></g>`+"\n",
			mx-4, panelTop+12, html.EscapeString(records[i].shape()))
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// goalNumbers are the registry's goal numbers, in its order.
func goalNumbers() []number {
	var out []number
	for _, n := range numbers {
		if n.kind == goalNumber {
			out = append(out, n)
		}
	}
	return out
}

// point is one mark, its tooltip title when there is one. The transparent stroke is a mark's hit
// area, wider than the mark, for the tooltip. A change is a large dot and a Δ within noise a small
// one, so that the eye finds the changes first.
func point(class string, cx, cy float64, colour, title string) string {
	r := "2.5"
	if class == "change" {
		r = "5"
	}
	mark := fmt.Sprintf(`<circle class="%s" cx="%.1f" cy="%.1f" r="%s" fill="%s" stroke="%s" stroke-opacity="0" stroke-width="12"`,
		class, cx, cy, r, colour, colour)
	if title == "" {
		return mark + "/>"
	}
	return fmt.Sprintf("%s><title>%s</title></circle>\n", mark, html.EscapeString(title))
}

// axis is a panel's y axis: it runs from -top to top, a tenth beyond the farthest Δ or interval
// end drawn, rounded up to the first step of 1, 2, 5, 10, 20, 50, 100, 200, 500 or 1000 ms that
// takes four lines or fewer on each side of 0. A panel with no Δ runs from -2 to 2.
func axis(ps []paired) (top, tick float64) {
	hi := 1.0
	for _, p := range ps {
		hi = max(hi, math.Abs(p.delta.Lo), math.Abs(p.delta.Hi), math.Abs(p.delta.P50))
	}
	hi *= 1.1
	for _, tick = range []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000} {
		if math.Ceil(hi/tick) <= 4 {
			break
		}
	}
	return math.Ceil(hi/tick) * tick, tick
}

// panel is goal number n between top and bottom: its name above, its axis around 0, and each of
// its Δs as a whisker and a mark with a tooltip and, when labelled, its Δ beside it. The newest
// mark is always labelled, so the newest release's Δ is on the chart however many releases there
// are.
func panel(b *strings.Builder, records []Record, n number, ps []paired, colour string, x func(int) float64, top, bottom float64, labelled bool) {
	lim, tick := axis(ps)
	y := func(v float64) float64 { return (top+bottom)/2 - (bottom-top)/2*v/lim }

	fmt.Fprintf(b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2"/>`, plotLeft, top-14, plotLeft+20, top-14, colour)
	fmt.Fprintf(b, `<text x="%.1f" y="%.1f">%s, Δ ms</text>`+"\n", plotLeft+26, top-10, html.EscapeString(n.title))
	for v := -lim; v <= lim; v += tick {
		opacity, label := "0.25", fmt.Sprintf("%+.0f", v)
		if v == 0 {
			opacity, label = "0.8", "0"
		}
		fmt.Fprintf(b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-opacity="%s"/>`+"\n",
			plotLeft, y(v), plotRight, y(v), ink, opacity)
		fmt.Fprintf(b, `<text x="%.1f" y="%.1f" text-anchor="end">%s</text>`+"\n", plotLeft-8, y(v)+4, label)
	}

	fmt.Fprintf(b, `<g class="panel" data-number="%s">`+"\n", n.key)
	for k, p := range ps {
		fmt.Fprintf(b, `<line class="whisker" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5"/>`+"\n",
			x(p.at), y(p.delta.Lo), x(p.at), y(p.delta.Hi), colour)
		class := "noise"
		if !p.delta.WithinNoise() {
			class = "change"
		}
		b.WriteString(point(class, x(p.at), y(p.delta.P50), colour,
			fmt.Sprintf("%s %s: Δ %s against %s, booted in the same run", records[p.at].Release, n.key, p.delta, p.against)))
		if labelled || k == len(ps)-1 {
			fmt.Fprintf(b, `<text x="%.1f" y="%.1f">%+.1f</text>`+"\n", x(p.at)+7, y(p.delta.P50)+4, p.delta.P50) // mutate-exempt: px from the mark to its label; a px either way is the same chart
		}
	}
	b.WriteString("</g>\n")
}
