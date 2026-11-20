// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"slices"
	"testing"
)

// A Δ is a change only when its interval excludes 0: two runs of one machine are within noise,
// and boots that are all faster, or all slower, are a change on the side they moved. The interval
// is the samples', whatever order they were taken in, and one boot on a side has none.
func TestADeltaIsAChangeOnlyWhenItsIntervalExcludesZero(t *testing.T) {
	one := []float64{330, 334, 328, 341, 336, 329, 333, 338, 331, 335}
	again := []float64{332, 329, 337, 330, 340, 333, 327, 336, 334, 331}
	noise, ok := DeltaP50(one, again)
	if !ok || !noise.WithinNoise() || noise.Lo >= 0 || noise.Hi <= 0 {
		t.Errorf("one machine twice: %+v, %v", noise, ok)
	}
	backwards := slices.Clone(one)
	slices.Reverse(backwards)
	if back, _ := DeltaP50(backwards, again); back != noise {
		t.Errorf("the same samples in another order: %+v, want %+v", back, noise)
	}
	var faster, slower []float64
	for _, v := range one {
		faster, slower = append(faster, v-20), append(slower, v+20)
	}
	// Every boot of one is within 13 ms of every other, so every draw of the Δ is within 13 of 20.
	if d, _ := DeltaP50(one, faster); d.P50 != -20 || d.Hi >= 0 || d.Lo < -33 || d.WithinNoise() {
		t.Errorf("20 ms faster: %+v", d)
	}
	if d, _ := DeltaP50(one, slower); d.P50 != 20 || d.Lo <= 0 || d.Hi > 33 || d.WithinNoise() {
		t.Errorf("20 ms slower: %+v", d)
	}
	for _, a := range [][]float64{nil, {330}} {
		if d, ok := DeltaP50(a, one); ok {
			t.Errorf("an interval from %v: %+v", a, d)
		}
		if d, ok := DeltaP50(one, a); ok {
			t.Errorf("an interval to %v: %+v", a, d)
		}
	}
}

// Every comparison prints a Δ the same way: the Δ, its interval, and within noise when the
// interval includes 0, an end of it at 0 included.
func TestADeltaSaysWhetherItIsWithinNoise(t *testing.T) {
	for d, want := range map[Delta]string{
		{P50: -2, Lo: -5, Hi: 1}:       "-2.0 [-5.0, +1.0] within noise",
		{P50: -20, Lo: -25.5, Hi: -15}: "-20.0 [-25.5, -15.0]",
		{P50: 3, Lo: 0, Hi: 5}:         "+3.0 [+0.0, +5.0] within noise",
		{P50: 3, Lo: 0.1, Hi: 5}:       "+3.0 [+0.1, +5.0]",
		{P50: 3, Lo: 0.5, Hi: 5}:       "+3.0 [+0.5, +5.0]",
		{P50: -3, Lo: -5, Hi: 0.5}:     "-3.0 [-5.0, +0.5] within noise",
		{P50: -3, Lo: -5, Hi: 0}:       "-3.0 [-5.0, +0.0] within noise",
		{P50: -3, Lo: -5, Hi: -0.1}:    "-3.0 [-5.0, -0.1]",
	} {
		if got := d.String(); got != want {
			t.Errorf("%+v is %q, want %q", d, got, want)
		}
	}
}
