// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// CPU is the CPU a guest is shown: who made it, the model it is presented as, and every feature
// it has. A guest asks CPUID once, at boot, and never again, so a checkpoint carries the CPU it
// was shown and may resume only where that CPU can be shown again.
type CPU struct {
	Vendor string `json:"vendor"`
	// Model is the named model the guest is shown, or host when it is shown the host's own.
	Model string `json:"model"`
	// Features are the names of the features the guest has, sorted.
	Features []string `json:"features,omitempty"`
}

// canShowCPU says whether a checkpoint of a guest shown saved may resume where here is shown,
// and why not when it may not.
//
// The same vendor, and every feature the guest was shown is one here can show: a guest that
// was told an instruction exists executes it, and on a CPU without it that is an invalid opcode
// long after the resume reported success. More is fine - the guest does not know about it.
//
// A named model is enough by its name: the machine runs one with enforce=on (Spec.Shape), so a
// host that cannot show every feature of it does not start the machine at all.
//
// This replaces the host's model name in the fingerprint, which was both too strict - two hosts
// of one model differ in nothing a guest sees, and a host whose microcode or kernel moved a flag
// was a new machine to the model's flags line - and too loose: two hosts of one model name with
// different features were one machine (spin-stack/spin#1348, 2026-11-19).
func canShowCPU(saved, here CPU) error {
	switch {
	case saved.Vendor != here.Vendor:
		return fmt.Errorf("the checkpoint's guest was shown a %s CPU, and this machine shows a %s one", saved.Vendor, here.Vendor)
	case saved.Model != here.Model:
		return fmt.Errorf("the checkpoint's guest was shown the CPU model %s, and this machine shows %s", saved.Model, here.Model)
	case saved.Model != "host":
		return nil
	}
	var missing []string
	for _, f := range saved.Features {
		if _, found := slices.BinarySearch(here.Features, f); !found {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the checkpoint's guest was shown CPU features this machine cannot show: %s", strings.Join(missing, " "))
	}
	return nil
}

// cpuSuffix is QEMU's type name for a vCPU of a model: <model>-x86_64-cpu.
const cpuSuffix = "-x86_64-cpu"

// CPU is the CPU this machine shows its guest: its first vCPU's model, expanded by QEMU into
// every property it has. Under model host that is the host's features less what QEMU cannot
// migrate (migratable=on, as Spec.Shape runs it).
func (m *QEMU) CPU(ctx context.Context) (CPU, error) {
	var cpus []struct {
		QOMPath string `json:"qom-path"`
	}
	if err := m.q.run(ctx, "query-cpus-fast", nil, &cpus); err != nil {
		return CPU{}, fmt.Errorf("listing the machine's vCPUs: %w", err)
	}
	if len(cpus) == 0 {
		return CPU{}, errors.New("the machine has no vCPU to ask its model of")
	}
	var typ string
	if err := m.q.run(ctx, "qom-get", map[string]any{"path": cpus[0].QOMPath, "property": "type"}, &typ); err != nil {
		return CPU{}, fmt.Errorf("reading the vCPUs' model: %w", err)
	}
	model, ok := strings.CutSuffix(typ, cpuSuffix)
	if !ok {
		return CPU{}, fmt.Errorf("a vCPU of type %s, which is not an x86_64 CPU model", typ)
	}
	asked := map[string]any{"name": model}
	if derivedFromHost(model) {
		asked["props"] = map[string]any{"migratable": true}
	}
	props, err := m.expand(ctx, "full", asked)
	if err != nil {
		return CPU{}, err
	}
	vendor, _ := props["vendor"].(string)
	return CPU{Vendor: vendor, Model: model, Features: features(props)}, nil
}

// expand is query-cpu-model-expansion of model: its properties as QEMU would give a vCPU of it.
func (m *QEMU) expand(ctx context.Context, how string, model map[string]any) (map[string]any, error) {
	var r struct {
		Model struct {
			Props map[string]any `json:"props"`
		} `json:"model"`
	}
	if err := m.q.run(ctx, "query-cpu-model-expansion", map[string]any{"type": how, "model": model}, &r); err != nil {
		return nil, fmt.Errorf("expanding the CPU model %v: %w", model["name"], err)
	}
	return r.Model.Props, nil
}

// features is the names of the properties that are on, sorted: a CPU's features, and the few
// switches QEMU expands beside them, which both sides of CanResume expand alike.
func features(props map[string]any) []string {
	var out []string
	for k, v := range props {
		if on, ok := v.(bool); ok && on {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Model is a named CPU model a host can run, and how many features it has.
type Model struct {
	Name     string `json:"name"`
	Features int    `json:"features"`
}

// Models are the named CPU models this host can run a machine of: the ones QEMU says it can
// run here with nothing missing (query-cpu-definitions' unavailable-features is empty, under the
// accelerator the machine runs), that are safe to migrate, are not an alias of another, and are
// not deprecated. host and max are the host's own, not a name every host shares.
//
// Each with its feature count, from its static expansion, which Baseline ranks them by.
func (m *QEMU) Models(ctx context.Context) ([]Model, error) {
	var defs []struct {
		Name          string   `json:"name"`
		Unavailable   []string `json:"unavailable-features"`
		MigrationSafe bool     `json:"migration-safe"`
		AliasOf       string   `json:"alias-of"`
		Deprecated    bool     `json:"deprecated"`
	}
	if err := m.q.run(ctx, "query-cpu-definitions", nil, &defs); err != nil {
		return nil, fmt.Errorf("listing the CPU models: %w", err)
	}
	var out []Model
	for _, d := range defs {
		if len(d.Unavailable) > 0 || !d.MigrationSafe || d.AliasOf != "" || d.Deprecated || derivedFromHost(d.Name) || d.Name == "base" {
			continue
		}
		props, err := m.expand(ctx, "static", map[string]any{"name": d.Name})
		if err != nil {
			return nil, err
		}
		out = append(out, Model{Name: d.Name, Features: len(features(props))})
	}
	slices.SortFunc(out, func(a, b Model) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Baseline is the model to run every machine of a fleet as: of the models every host can run,
// the newest.
//
// Newest is the one with the most features. QEMU's list carries no dates, and a later generation
// of a vendor's models is its predecessor's features and more (Skylake-Server, Icelake-Server,
// SapphireRapids), so it is the one the guest loses least under. Two with as many are told apart
// by name, the later one, so that every caller picks the same.
//
// The trade-off, which is the point of a baseline: on a fleet of mixed hosts every guest is shown
// only what the oldest can run - AVX-512 or AMX on the newer hosts are hidden from every workspace
// - and in return a checkpoint resumes on any host of the fleet.
func Baseline(hosts [][]Model) (Model, error) {
	if len(hosts) == 0 {
		return Model{}, errors.New("no hosts to pick a CPU model all of them run")
	}
	var best Model
	for _, m := range hosts[0] {
		everywhere := true
		for _, h := range hosts[1:] {
			everywhere = everywhere && slices.ContainsFunc(h, func(o Model) bool { return o.Name == m.Name })
		}
		if everywhere && (m.Features > best.Features || (m.Features == best.Features && m.Name > best.Name)) {
			best = m
		}
	}
	if best.Name == "" {
		return Model{}, errors.New("no CPU model every host runs: the fleet has hosts of more than one vendor, or one that runs none")
	}
	return best, nil
}

// derivedFromHost reports whether a CPU model takes its feature set from the
// silicon it runs on, which is what makes a checkpoint saved with it unusable on
// another machine — and what migratable=on applies to.
func derivedFromHost(model string) bool {
	return model == "host" || model == "max"
}
