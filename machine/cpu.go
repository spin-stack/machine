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
// was shown and resumes only where the same CPU is shown.
type CPU struct {
	Vendor string `json:"vendor"`
	// Model is the named model the guest is shown, or host when it is shown the host's own.
	Model string `json:"model"`
	// Features are the names of the features the guest has, sorted.
	Features []string `json:"features,omitempty"`
}

// differences are the ways a guest shown saved would find here another CPU: its vendor, its model
// and its set of features, each the same.
//
// The same set, and not a subset of here's: a guest that was told an instruction exists executes
// it, and on a CPU without it that is an invalid opcode long after the resume reported success;
// and a CPU with more shows a process features (AVX-512, AMX) whose register state the guest's
// kernel never enabled, because CPUID is not in the migration stream. One difference lists the
// features that differ, each side's own.
//
// One rule for every model: under enforce=on a named model's features are its name's on every
// host, so the sets agree whenever the names do, and under host - or host less what a pool does
// not share (Spec.CPUWithout) - the set is what decides.
func (saved CPU) differences(here CPU) []Difference {
	var ds []Difference
	if saved.Vendor != here.Vendor {
		ds = append(ds, Difference{Part: "cpu vendor", Checkpoint: saved.Vendor, Here: here.Vendor, Rule: Same})
	}
	if saved.Model != here.Model {
		ds = append(ds, Difference{Part: "cpu model", Checkpoint: saved.Model, Here: here.Model, Rule: Same})
	}
	gone, added := without(saved.Features, here.Features), without(here.Features, saved.Features)
	if len(gone) > 0 || len(added) > 0 {
		ds = append(ds, Difference{Part: "cpu features", Checkpoint: strings.Join(gone, " "), Here: strings.Join(added, " "), Rule: Same})
	}
	return ds
}

// without is the names in a that b does not have, sorted.
func without(a, b []string) []string {
	var out []string
	for _, f := range a {
		if !slices.Contains(b, f) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// cpuSuffix is QEMU's type name for a vCPU of a model: <model>-x86_64-cpu.
const cpuSuffix = "-x86_64-cpu"

// CPU is the CPU this machine shows its guest, read off its first vCPU: the features the vCPU has
// on, not the ones its model's name has, so that what the command line took away (Spec.CPUWithout,
// -vmx, and under host what migratable=on drops) is not reported as shown.
//
// A feature is a name QEMU's static expansion of the model lists as a flag: the CPUID features,
// one canonical name each. The vCPU and the full expansion also answer to aliases (sse4.1, sse4-1
// and sse4_1 are one feature) and to switches that are no CPUID bit (migratable, check, pmu,
// l3-cache), which the static expansion leaves out (QEMU 11.2.0-rc0, 2026-11-21).
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
	vcpus, err := m.qomObjects(ctx, cpus[0].QOMPath)
	if err != nil {
		return CPU{}, fmt.Errorf("reading the machine's vCPU: %w", err)
	}
	vcpu := vcpus[0]
	model, ok := strings.CutSuffix(vcpu.text("type"), cpuSuffix)
	if !ok {
		return CPU{}, fmt.Errorf("a vCPU of type %q, which is not an x86_64 CPU model", vcpu.text("type"))
	}
	flags, err := m.expand(ctx, "static", map[string]any{"name": model})
	if err != nil {
		return CPU{}, err
	}
	var on []string
	for name, v := range flags {
		if _, flag := v.(bool); flag && string(vcpu.value(name)) == "true" {
			on = append(on, name)
		}
	}
	slices.Sort(on)
	return CPU{Vendor: vcpu.text("vendor"), Model: model, Features: on}, nil
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

// features is the names of the properties that are on, sorted.
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

// Unshared are the features some host of a pool shows and another does not: the Spec.CPUWithout
// that has every host of the pool show the features they share, so that a checkpoint of one
// resumes on any (spin-stack/spin#1417). Each host's CPU is what its machine shows under host
// with nothing taken away (Target.CPU).
//
// Where Baseline names the newest model every host runs, this keeps the host's own model and
// takes away only what is missing somewhere: a named model cost the cold first login 27 ms on the
// bench against host (lab 44855890737, 2026-11-21). Hosts of more than one vendor or model are an
// error, which no list of features takes away.
func Unshared(hosts []CPU) ([]string, error) {
	if len(hosts) == 0 {
		return nil, errors.New("no hosts to find the features they share")
	}
	shows := map[string]int{}
	for _, h := range hosts {
		if h.Vendor != hosts[0].Vendor || h.Model != hosts[0].Model {
			return nil, fmt.Errorf("a pool of %s %s and %s %s CPUs: only hosts of one vendor and model share features by taking some away",
				hosts[0].Vendor, hosts[0].Model, h.Vendor, h.Model)
		}
		for _, f := range slices.Compact(slices.Sorted(slices.Values(h.Features))) {
			shows[f]++
		}
	}
	var out []string
	for f, n := range shows {
		if n < len(hosts) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out, nil
}

// derivedFromHost reports whether a CPU model takes its feature set from the
// silicon it runs on, which is what makes a checkpoint saved with it unusable on
// another machine — and what migratable=on applies to.
func derivedFromHost(model string) bool {
	return model == "host" || model == "max"
}
