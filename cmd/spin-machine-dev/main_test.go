// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestTheExitStatusIsTheVerbsOrTwo(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
		says string
	}{
		{"done", nil, 0, ""},
		{"something to act on, already said", exitError{code: 1}, 1, ""},
		{"could not, and why", couldNot(errors.New("no network")), 2, "spin-machine-dev: no network\n"},
		{"an error no verb classified", errors.New("boom"), 2, "spin-machine-dev: boom\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w bytes.Buffer
			if got := exit(tc.err, &w); got != tc.want || w.String() != tc.says {
				t.Errorf("exit(%v) = %d, said %q; want %d, %q", tc.err, got, w.String(), tc.want, tc.says)
			}
		})
	}
}
