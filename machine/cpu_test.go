// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"strings"
	"testing"
)

func TestBaseline(t *testing.T) {
	sky, ice, spr := CPUModel{"Skylake-Server-v4", 100}, CPUModel{"Icelake-Server-v6", 120}, CPUModel{"SapphireRapids-v3", 140}
	epyc := CPUModel{"EPYC-Genoa-v1", 130}
	for _, tc := range []struct {
		name  string
		hosts [][]CPUModel
		want  CPUModel
		err   string
	}{
		{"the newest every host runs", [][]CPUModel{{sky, ice, spr}, {sky, ice}, {ice, sky}}, ice, ""},
		{"one host", [][]CPUModel{{sky, spr}}, spr, ""},
		{"as many features, the later name", [][]CPUModel{{{"A-v1", 5}, {"A-v2", 5}}}, CPUModel{"A-v2", 5}, ""},
		{"fewer features, a later name", [][]CPUModel{{ice, sky}}, ice, ""},
		{"two vendors share no model", [][]CPUModel{{sky, ice}, {epyc}}, CPUModel{}, "more than one vendor"},
		{"no hosts", nil, CPUModel{}, "no hosts"},
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
