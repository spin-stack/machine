// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"slices"
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

// Every option -cpu gives a vCPU is a feature, which CPU.Features holds, a filter of features
// (migratable, enforce), whose outcome Features holds too, or a property the record holds
// (cpuidProperties). x-consistent-cache went onto -cpu with nothing in the record moving, and a
// checkpoint resumed across the change to leaf 2 (#569); this fails the next one where it is made.
func TestTheRecordHoldsWhatTheCPUIsGiven(t *testing.T) {
	filters := []string{"migratable", "enforce"}
	for _, s := range []Spec{{}, {Accel: "tcg"}, {CPU: "Skylake-Server-v4"}, {CPUWithout: []string{"avx512f"}}} {
		model, opts, _ := strings.Cut(s.cpuArg(), ",")
		for _, o := range strings.Split(opts, ",") {
			if strings.HasPrefix(o, "-") || strings.HasPrefix(o, "+") {
				continue
			}
			name, _, _ := strings.Cut(o, "=")
			if !slices.Contains(filters, name) && !slices.Contains(cpuidProperties, name) {
				t.Errorf("-cpu %s gives the vCPU %s, which the record does not hold: add it to cpuidProperties, or say here why the record need not", model, o)
			}
		}
	}
}
