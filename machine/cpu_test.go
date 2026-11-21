// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"strings"
	"testing"
)

func TestBaseline(t *testing.T) {
	sky, ice, spr := Model{"Skylake-Server-v4", 100}, Model{"Icelake-Server-v6", 120}, Model{"SapphireRapids-v3", 140}
	epyc := Model{"EPYC-Genoa-v1", 130}
	for _, tc := range []struct {
		name  string
		hosts [][]Model
		want  Model
		err   string
	}{
		{"the newest every host runs", [][]Model{{sky, ice, spr}, {sky, ice}, {ice, sky}}, ice, ""},
		{"one host", [][]Model{{sky, spr}}, spr, ""},
		{"as many features, the later name", [][]Model{{{"A-v1", 5}, {"A-v2", 5}}}, Model{"A-v2", 5}, ""},
		{"fewer features, a later name", [][]Model{{ice, sky}}, ice, ""},
		{"two vendors share no model", [][]Model{{sky, ice}, {epyc}}, Model{}, "more than one vendor"},
		{"no hosts", nil, Model{}, "no hosts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Baseline(tc.hosts)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("Baseline = %v, %v, want %q", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Baseline = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}
