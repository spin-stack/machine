// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"math/rand/v2"
	"slices"
)

// Delta is one number's p50 in one tree less its p50 in another, both from the same run, and the
// bootstrap 95% confidence interval of that difference.
//
// The lab's one acceptance rule: a Δ is a change only when its interval excludes 0 - a gain when
// it is wholly below 0 for a time - and within noise when the interval includes 0, whatever its
// size. There is no threshold beside it. At 20 boots each, the interval of a release's cold first
// login against the release before it, interleaved, was 7 to 17 ms wide (labs 44641240861 and
// 44727451080, 2026-11-20), and changes of -4 and -5 ms had been accepted without one.
type Delta struct {
	P50 float64 `json:"p50"`
	Lo  float64 `json:"lo"`
	Hi  float64 `json:"hi"`
}

// WithinNoise is whether d's interval includes 0: a Δ that says nothing.
func (d Delta) WithinNoise() bool { return d.Lo <= 0 && d.Hi >= 0 }

// String is d as every comparison the lab prints it: the Δ, its interval, and "within noise"
// when the interval includes 0.
func (d Delta) String() string {
	s := fmt.Sprintf("%+.1f [%+.1f, %+.1f]", d.P50, d.Lo, d.Hi)
	if d.WithinNoise() {
		s += " within noise"
	}
	return s
}

// resamples is how many times the bootstrap draws each side again. Its seed is fixed, so that the
// same samples print the same interval wherever they are read.
const resamples = 2000

// DeltaP50 is b's p50 less a's, with the bootstrap 95% interval of that difference: each side
// drawn again with replacement, the p50s' difference taken each time, and its 2.5th and 97.5th
// percentiles kept. Every value is to a tenth, as a record keeps it, and the rule is read on
// those. There is none when either side has fewer than two samples: one boot has no spread to
// draw an interval from.
func DeltaP50(a, b []float64) (Delta, bool) {
	if len(a) < 2 || len(b) < 2 {
		return Delta{}, false
	}
	// Sorted, so the interval depends on the samples and not on the order they were taken in.
	a, b = slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))
	pa, _ := Percentile(a, .5)
	pb, _ := Percentile(b, .5)
	rng := rand.New(rand.NewPCG(1, 2)) // mutate-exempt: the seed; any seed draws an interval, and a fixed one the same interval every time
	ra, rb := make([]float64, len(a)), make([]float64, len(b))
	ds := make([]float64, resamples)
	for i := range ds {
		for j := range ra {
			ra[j] = a[rng.IntN(len(a))]
		}
		for j := range rb {
			rb[j] = b[rng.IntN(len(b))]
		}
		ma, _ := Percentile(ra, .5)
		mb, _ := Percentile(rb, .5)
		ds[i] = mb - ma
	}
	lo, _ := Percentile(ds, .025)
	hi, _ := Percentile(ds, .975)
	return Delta{P50: tenth(pb - pa), Lo: tenth(lo), Hi: tenth(hi)}, true
}
