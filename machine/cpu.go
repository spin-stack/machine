// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// CPU is the CPU a guest is shown: who made it, the model it is presented as, every feature it
// has, and the vCPU's properties that shape what CPUID answers besides its features. A guest's
// kernel reads CPUID at boot and keeps what it read, and a process started after a resume reads
// it again, from the machine it resumed into, so a checkpoint carries the CPU it was shown and
// resumes only where the same CPU is shown.
type CPU struct {
	Vendor string `json:"vendor"`
	// Model is the named model the guest is shown, or host when it is shown the host's own.
	Model string `json:"model"`
	// Features are the names of the features the guest has, sorted.
	Features []string `json:"features,omitempty"`
	// Properties are the vCPU's properties in cpuidProperties, by name, each as QEMU reads it back
	// from the running vCPU. A record from before they were held has none (#569).
	Properties map[string]string `json:"properties,omitempty"`
}

// cpuidProperties are the vCPU's properties that shape CPUID and are not a feature bit, which
// the record holds: the ones -cpu sets (cpuArg), and the ones the host model takes from the host
// (host-cpu.c's host_cpu_max_instance_init, kvm-cpu.c's kvm_cpu_max_instance_init), with the
// cache switches x-consistent-cache works with. Read in QEMU 11.2.0-rc0's target/i386 (#569):
//
//   - x-consistent-cache, x-legacy-cpuid2-cache, l3-cache, legacy-cache, host-cache-info: which
//     cache model leaves 2, 4 and 0x80000005-6 describe. Under host, legacy-cache is on and
//     x-consistent-cache=off alone makes leaf 2 describe legacy_intel_cpuid2_cache_info (a 2 MiB
//     8-way L2) where leaf 4 describes legacy_intel_cache_info (4 MiB 16-way), so a process glibc
//     starts reads another L2 from either side of each switch. x-legacy-cpuid2-cache is
//     qemu/patches/0006's (#586). host-cache-info is only host's and max's, and a named model's
//     vCPU has none to read.
//   - family, model, stepping, model-id: leaf 1 and the brand string, the host's under host.
//   - level, xlevel: the highest leaves, KVM's under host.
//   - phys-bits, guest-phys-bits: leaf 0x80000008, the host's under host (host-phys-bits).
//   - pmu: leaf 0xA, which KVM's host model turns on.
//
// Left out: migratable, enforce, check and x-force-features decide which features are on, which
// Features holds; a vCPU's topology ids are its slot, and the topology is the machine's
// (Hardware's machine smp); the other switches (hv-*, kvm, vmware-cpuid-freq, cpuid-0xb, the
// x-vendor-cpuid-only pair and the rest of x86_cpu_properties) are at the machine type's
// defaults, which QEMU keeps for pc-q35-11.1 across versions, until -cpu sets one, and
// TestTheRecordHoldsWhatTheCPUIsGiven fails a -cpu option that is neither a feature nor here. MSRs (lbr-fmt, ucode-rev,
// lmce) are not CPUID.
var cpuidProperties = []string{
	"family", "guest-phys-bits", "host-cache-info", "l3-cache", "legacy-cache", "level", "model",
	"model-id", "phys-bits", "pmu", "stepping", "x-consistent-cache", "x-legacy-cpuid2-cache", "xlevel",
}

// differences are the ways a guest shown saved would find here another CPU: its vendor, its model,
// its set of features and each of its properties, each the same. A property one side has and the
// other does not is a difference: a record from before the properties were held is refused by
// each, because nothing in it says which side of x-consistent-cache=off it was taken on (#569).
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
	names := slices.Collect(maps.Keys(saved.Properties))
	for n := range here.Properties {
		if _, ok := saved.Properties[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		was, ok := saved.Properties[n]
		is, has := here.Properties[n]
		if ok != has || was != is {
			ds = append(ds, Difference{Part: "cpu " + n, Checkpoint: was, Here: is, Rule: Same})
		}
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

var (
	queryCPUsFast          = qmpdef.Define[qmp.None, []vcpuInfo]("query-cpus-fast")
	queryCPUModelExpansion = qmpdef.Define[expansionArgs, expansion]("query-cpu-model-expansion")
	queryCPUDefinitions    = qmpdef.Define[qmp.None, []cpuDefinition]("query-cpu-definitions")
	queryHotpluggableCPUs  = qmpdef.Define[qmp.None, []cpuSlot]("query-hotpluggable-cpus")
)

type vcpuInfo struct {
	QOMPath string `json:"qom-path"`
}

type expansionArgs struct {
	Type  string       `json:"type"`
	Model cpuModelInfo `json:"model"`
}

type cpuModelInfo struct {
	Name string `json:"name"`
}

type expansion struct {
	Model struct {
		Props map[string]any `json:"props"`
	} `json:"model"`
}

type cpuDefinition struct {
	Name          string   `json:"name"`
	Unavailable   []string `json:"unavailable-features"`
	MigrationSafe bool     `json:"migration-safe"`
	AliasOf       string   `json:"alias-of"`
	Deprecated    bool     `json:"deprecated"`
}

// CPU is the CPU this machine shows its guest, read off its first vCPU: the features the vCPU has
// on, not the ones its model's name has, so that what the command line took away (Spec.CPUWithout,
// -vmx, and under host what migratable=on drops) is not reported as shown.
//
// A feature is a name QEMU's static expansion of the model lists as a flag: the CPUID features,
// one canonical name each. The vCPU and the full expansion also answer to aliases (sse4.1, sse4-1
// and sse4_1 are one feature) and to switches that are no CPUID bit (migratable, check, pmu,
// l3-cache), which the static expansion leaves out (QEMU 11.2.0-rc0, 2026-11-21). The switches
// that shape CPUID are read off the same vCPU into Properties.
func (m *QEMU) CPU(ctx context.Context) (CPU, error) {
	cpus, err := qmp.Do(ctx, m.c, queryCPUsFast, qmp.None{})
	if err != nil {
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
	flags, err := m.expand(ctx, model)
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
	props := map[string]string{}
	for _, name := range cpuidProperties {
		if v := vcpu.value(name); v != nil {
			props[name] = qomValue(v)
		}
	}
	return CPU{Vendor: vcpu.text("vendor"), Model: model, Features: on, Properties: props}, nil
}

// expand is the static expansion of model: its properties as QEMU would give a vCPU of it.
func (m *QEMU) expand(ctx context.Context, model string) (map[string]any, error) {
	r, err := qmp.Do(ctx, m.c, queryCPUModelExpansion, expansionArgs{Type: "static", Model: cpuModelInfo{Name: model}})
	if err != nil {
		return nil, fmt.Errorf("expanding the CPU model %s: %w", model, err)
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

// CPUModel is a named CPU model a host can run, and how many features it has.
type CPUModel struct {
	Name     string `json:"name"`
	Features int    `json:"features"`
}

// CPUModels are the named CPU models this host can run a machine of: the ones QEMU says it can
// run here with nothing missing (query-cpu-definitions' unavailable-features is empty, under the
// accelerator the machine runs), that are safe to migrate, are not an alias of another, and are
// not deprecated. host and max are the host's own, not a name every host shares.
//
// Each with its feature count, from its static expansion, which Baseline ranks them by.
func (m *QEMU) CPUModels(ctx context.Context) ([]CPUModel, error) {
	defs, err := qmp.Do(ctx, m.c, queryCPUDefinitions, qmp.None{})
	if err != nil {
		return nil, fmt.Errorf("listing the CPU models: %w", err)
	}
	var out []CPUModel
	for _, d := range defs {
		if len(d.Unavailable) > 0 || !d.MigrationSafe || d.AliasOf != "" || d.Deprecated || derivedFromHost(d.Name) || d.Name == "base" {
			continue
		}
		props, err := m.expand(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, CPUModel{Name: d.Name, Features: len(features(props))})
	}
	slices.SortFunc(out, func(a, b CPUModel) int { return strings.Compare(a.Name, b.Name) })
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
func Baseline(hosts [][]CPUModel) (CPUModel, error) {
	if len(hosts) == 0 {
		return CPUModel{}, errors.New("no hosts to pick a CPU model all of them run")
	}
	var best CPUModel
	for _, m := range hosts[0] {
		everywhere := true
		for _, h := range hosts[1:] {
			everywhere = everywhere && slices.ContainsFunc(h, func(o CPUModel) bool { return o.Name == m.Name })
		}
		if everywhere && (m.Features > best.Features || (m.Features == best.Features && m.Name > best.Name)) {
			best = m
		}
	}
	if best.Name == "" {
		return CPUModel{}, errors.New("no CPU model every host runs: the fleet has hosts of more than one vendor, or one that runs none")
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

// cpuSlot is one place for a vCPU, as query-hotpluggable-cpus reports it.
type cpuSlot struct {
	Type    string         `json:"type"`
	QOMPath string         `json:"qom-path,omitempty"`
	Props   map[string]any `json:"props"`
}

// addedCPU is a vCPU added after exec, as a checkpoint records it to add it again before the
// state is loaded: the stream names each vCPU, and one that is not there fails the load.
type addedCPU struct {
	ID     string         `json:"id"`
	Driver string         `json:"driver"`
	Props  map[string]any `json:"props"`
}

// cpuTopology is every property a vCPU's slot is named by: all a checkpoint's vCPU may set.
var cpuTopology = []string{"node-id", "socket-id", "die-id", "cluster-id", "module-id", "core-id", "thread-id"}

// deviceArgs is the device_add that adds c again, refused unless it is a vCPU in a slot.
//
// The record is what the saved machine's QEMU answered, and device_add attaches whatever device
// and properties it is given: a QEMU the guest had taken over could have answered with another
// device, or a vCPU with properties of its choosing, for the machine that resumes it to add. So
// the driver has to be a vCPU's and every property a slot's coordinate, a number, and neither
// can name the driver or the id (spin-stack/spin#1367's review; spin's own client refused the
// same before it moved here).
func (c addedCPU) deviceArgs() (map[string]any, error) {
	if !strings.HasSuffix(c.Driver, cpuSuffix) || c.ID == "" {
		return nil, fmt.Errorf("the checkpoint records a vCPU %q of type %q, which is not a vCPU", c.ID, c.Driver)
	}
	args := map[string]any{"driver": c.Driver, "id": c.ID}
	for k, v := range c.Props {
		if _, number := v.(float64); !number || !slices.Contains(cpuTopology, k) {
			return nil, fmt.Errorf("the checkpoint gives vCPU %s the property %s=%v, which names no slot", c.ID, k, v)
		}
		args[k] = v
	}
	return args, nil
}

func (m *QEMU) cpuSlots(ctx context.Context) ([]cpuSlot, error) {
	slots, err := qmp.Do(ctx, m.c, queryHotpluggableCPUs, qmp.None{})
	if err != nil {
		return nil, fmt.Errorf("listing the machine's vCPU slots: %w", err)
	}
	// QEMU lists them highest first; added in this order, vCPU n is the n-th.
	slices.Reverse(slots)
	return slots, nil
}

// CPUs is how many vCPUs the machine has: the ones it was started with and the ones added since.
func (m *QEMU) CPUs(ctx context.Context) (int, error) {
	cpus, err := qmp.Do(ctx, m.c, queryCPUsFast, qmp.None{})
	if err != nil {
		return 0, fmt.Errorf("listing the machine's vCPUs: %w", err)
	}
	return len(cpus), nil
}

// SetCPUs adds vCPUs into the first free slots, or removes the last ones added, until there are
// n. A removal returns once QEMU has let the vCPU go, which waits on the guest offlining it.
func (m *QEMU) SetCPUs(ctx context.Context, n int) error {
	slots, err := m.cpuSlots(ctx)
	if err != nil {
		return err
	}
	if n < 1 || n > len(slots) {
		return fmt.Errorf("%d vCPUs, and the machine has slots for 1 to %d", n, len(slots))
	}
	have := 0
	for _, s := range slots {
		if s.QOMPath != "" {
			have++
		}
	}
	for i, s := range slots {
		if have >= n {
			break
		}
		if s.QOMPath != "" {
			continue
		}
		args := map[string]any{"driver": s.Type, "id": fmt.Sprintf("vcpu%d", i)}
		for k, v := range s.Props {
			args[k] = v
		}
		if _, err := qmp.Do(ctx, m.c, deviceAdd, args); err != nil {
			return fmt.Errorf("adding vCPU %d: %w", i, err)
		}
		have++
	}
	for _, s := range slices.Backward(slots) {
		if have <= n {
			break
		}
		id, ok := strings.CutPrefix(s.QOMPath, peripheral)
		if !ok {
			// Boot vCPUs are not devices anyone added, and cannot be removed.
			continue
		}
		if err := m.unplug(ctx, id); err != nil {
			return fmt.Errorf("removing vCPU %s: %w", id, err)
		}
		have--
	}
	if have != n {
		return fmt.Errorf("%d vCPUs, and only %d can be removed: the rest are the ones the machine was started with", n, have)
	}
	return nil
}

// addedCPUs are the vCPUs added since exec: QEMU puts a device with an id under
// /machine/peripheral, and the boot vCPUs under /machine/unattached.
func (m *QEMU) addedCPUs(ctx context.Context) ([]addedCPU, error) {
	slots, err := m.cpuSlots(ctx)
	if err != nil {
		return nil, err
	}
	var out []addedCPU
	for _, s := range slots {
		if id, ok := strings.CutPrefix(s.QOMPath, peripheral); ok {
			out = append(out, addedCPU{ID: id, Driver: s.Type, Props: s.Props})
		}
	}
	return out, nil
}
