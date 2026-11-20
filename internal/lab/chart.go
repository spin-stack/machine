// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"html"
	"math"
	"slices"
	"strings"
)

// The chart's canvas and its plot, in px. Fixed, so that the same records are the same bytes and
// docs/releases.svg changes only when a record does.
const (
	chartWidth, chartHeight = 800, 400
	plotLeft, plotRight     = 64.0, 776.0
	plotTop, plotBottom     = 72.0, 330.0
)

// seriesColours are the goal numbers' colours, in the registry's order: never cycled, so a goal
// number added to the registry needs a colour of its own here. One SVG is shown on GitHub's light
// page and on its dark one, and an image cannot follow the theme, so each is a mid-saturation hue
// that clears 3:1 on both #ffffff and #0d1117 and that a colour-blind reader tells from the other
// (dataviz's palette checks, 2026-11-20).
var seriesColours = []string{"#2a78d6", "#d95926"}

// ink is the text's and the axes' grey: 3.9:1 on GitHub's light page, 4.8:1 on its dark one.
const ink = "#808080"

// labelRoom is the room a release needs for each of its points to carry its p50: closer than this
// and the labels run into each other, so only a line's last point is labelled; the tooltips have
// every value. A release's name under its point is rotated below twice that.
const labelRoom = 40.0

// Chart is the goal's numbers release by release as an SVG: each goal number's p50 as a line over
// a band up to its p95, against the goal, with a tooltip on every point. A release without a
// number breaks that number's line, where a zero would draw a release that was never measured,
// and so does a release measured in another shape than the one before it: v20261120.03, the first
// in spin's shape, read 473 ms cold against v20261120.02's 328 in the shape before, and 38 ms
// faster than v20261120.02 booted beside it in its own (lab 44805222647). A dashed rule between
// the two says where the shape changed.
func Chart(records []Record) string {
	var goals []number
	for _, n := range numbers {
		if n.kind == goalNumber {
			goals = append(goals, n)
		}
	}
	top, step := axis(records, goals)
	slot := (plotRight - plotLeft) / float64(max(len(records), 1)) // mutate-exempt: with no records nothing is placed in a slot; any divisor but 0 draws the same chart
	x := func(i int) float64 { return plotLeft + slot*(float64(i)+0.5) }
	y := func(v float64) float64 { return plotBottom - (plotBottom-plotTop)*v/top }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" `+
		`font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif" font-size="12" fill="%s">`+"\n",
		chartWidth, chartHeight, chartWidth, chartHeight, ink)
	fmt.Fprintf(&b, "<title>%s</title>\n", chartTitle)

	for v := 0.0; v <= top; v += step {
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
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end">goal %.0f ms</text>`+"\n", plotRight, y(Goal)-6, Goal)

	for s, n := range goals {
		for _, run := range runs(records, n.key) {
			series(&b, records, n, run, seriesColours[s], x, y, slot >= labelRoom)
		}
	}

	// The legend, along the top: a swatch of each line and its band, then the goal's.
	lx := plotLeft
	for s, n := range goals {
		fmt.Fprintf(&b, `<rect x="%.1f" y="24" width="20" height="12" fill="%s" fill-opacity="0.25"/>`, lx, seriesColours[s])
		fmt.Fprintf(&b, `<line x1="%.1f" y1="30" x2="%.1f" y2="30" stroke="%s" stroke-width="2"/>`, lx, lx+20, seriesColours[s])
		fmt.Fprintf(&b, `<text x="%.1f" y="34">%s</text>`+"\n", lx+26, html.EscapeString(n.title))
		lx += 26 + 7*float64(len(n.title)) + 24
	}
	fmt.Fprintf(&b, `<line x1="%.1f" y1="30" x2="%.1f" y2="30" stroke="%s" stroke-width="1.5" stroke-dasharray="6 4"/>`, lx, lx+20, ink)
	fmt.Fprintf(&b, `<text x="%.1f" y="34">goal</text>`+"\n", lx+26)
	fmt.Fprintf(&b, `<text x="%.1f" y="56">First SSH login, ms: the line is the p50, the band up to the p95.</text>`+"\n", plotLeft)
	b.WriteString("</svg>\n")
	return b.String()
}

// axis is the y axis' top and the step between its lines: a tenth above the highest p95 drawn, or
// the goal when that is higher, rounded up to the first step of 25, 50, 100, 200, 500 or 1000 ms
// that takes eight lines or fewer.
func axis(records []Record, goals []number) (top, step float64) {
	hi := Goal
	for _, r := range records {
		for _, n := range goals {
			if v, ok := r.Numbers[n.key]; ok {
				hi = max(hi, v.P95)
			}
		}
	}
	hi *= 1.1
	for _, step = range []float64{25, 50, 100, 200, 500, 1000} {
		if math.Ceil(hi/step) <= 8 {
			break
		}
	}
	return math.Ceil(hi/step) * step, step
}

// runs are the stretches of consecutive records in one shape that have the number key, each as the
// records' indexes: one line each, so that no line joins two releases across one without the
// number, nor two machines.
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

// series is one run of n as a group: its band from p50 up to p95, its p50 line, and a point on
// each release with a tooltip and, when labelled, its p50 above it; a run's last point is always
// labelled, so the newest value is on the chart however many releases there are.
func series(b *strings.Builder, records []Record, n number, run []int, colour string, x func(int) float64, y func(float64) float64, labelled bool) {
	xy := func(px, py float64) string { return fmt.Sprintf("%.1f,%.1f", px, py) }
	var upper, lower []string
	for _, i := range run {
		v := records[i].Numbers[n.key]
		upper = append(upper, xy(x(i), y(v.P95)))
		lower = append(lower, xy(x(i), y(v.P50)))
	}
	line := slices.Clone(lower)
	if len(run) == 1 {
		// A band over one x has no width: one release's is as wide as its point's hit area.
		const half = 8.0 // mutate-exempt: half the width of one release's band; any width shows it
		v, cx := records[run[0]].Numbers[n.key], x(run[0])
		upper = []string{xy(cx-half, y(v.P95)), xy(cx+half, y(v.P95))}
		lower = []string{xy(cx-half, y(v.P50)), xy(cx+half, y(v.P50))}
	}
	slices.Reverse(lower)
	fmt.Fprintf(b, `<g class="run" data-number="%s">`+"\n", n.key)
	fmt.Fprintf(b, `<polygon class="band" points="%s" fill="%s" fill-opacity="0.25"/>`+"\n", strings.Join(append(upper, lower...), " "), colour)
	if len(run) > 1 {
		fmt.Fprintf(b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2"/>`+"\n", strings.Join(line, " "), colour)
	}
	for k, i := range run {
		r, cx := records[i], x(i)
		v := r.Numbers[n.key]
		// The transparent stroke is the point's hit area, wider than its mark, for the tooltip.
		fmt.Fprintf(b, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s" stroke="%s" stroke-opacity="0" stroke-width="12">`+
			`<title>%s %s: p50 %s, p95 %s ms</title></circle>`+"\n",
			cx, y(v.P50), colour, colour, html.EscapeString(r.Release), n.key, num(v.P50), num(v.P95))
		if labelled || k == len(run)-1 {
			fmt.Fprintf(b, `<text x="%.1f" y="%.1f" text-anchor="middle">%s</text>`+"\n", cx, y(v.P50)-10, num(v.P50))
		}
	}
	b.WriteString("</g>\n")
}
