// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// facts is a record's machine as two maps, for a test to edit.
type facts struct {
	exact   map[string]string
	atLeast map[string]int64
}

// spinsMachine is a machine of spin's shape as QEMU 11.2.0-rc0 recorded it (2026-11-21), cut to a
// fact of each kind a resume is held to.
func spinsMachine() facts {
	return facts{
		exact: map[string]string{
			"accel":                       "kvm",
			"machine type":                "pc-q35-11.1",
			"machine smp.cpus":            "1",
			"machine smp.maxcpus":         "16",
			"machine memory.size":         "134217728",
			"device 02.0":                 "vhost-vsock-pci",
			"device 02.0 disable-legacy":  "on",
			"device 04.0":                 "virtio-balloon-pci",
			"device 04.0 deflate-on-oom":  "true",
			"device 10.0":                 "virtio-net-pci",
			"device 10.0 host_mtu":        "1450",
			"device 1a.0":                 "virtio-scsi-pci",
			"device 1f.0":                 "ICH9-LPC",
			"device 1f.0 disable_s3":      "1",
			"device i8257/1":              "i8257",
			"ram pc.bios":                 "65536",
			"ram pc.ram":                  "134217728",
			"ram 0000:00:1e.0/mem.growth": "8455716864",
		},
		atLeast: map[string]int64{"device 1a.0 max_target": 15},
	}
}

func (f facts) machine(t *testing.T) Machine {
	t.Helper()
	var exact []part
	for n, v := range f.exact {
		exact = append(exact, part{n, v})
	}
	var atLeast []count
	for n, c := range f.atLeast {
		atLeast = append(atLeast, count{n, c})
	}
	m, err := newMachine(exact, atLeast)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var spinsCPU = CPU{Vendor: "GenuineIntel", Model: "host", Features: []string{"avx2", "sse4.1", "xsave"}}

// target is a QEMU 11.2.0 machine of spin's shape, changed by edit.
func target(t *testing.T, edit func(*Target, facts)) Target {
	t.Helper()
	f := spinsMachine()
	tg := Target{VMM: "qemu 11.2.0", Formats: []string{FormatMappedRAM, FormatStream}, CPU: spinsCPU}
	tg.CPU.Features = slices.Clone(spinsCPU.Features)
	if edit != nil {
		edit(&tg, f)
	}
	tg.Machine = f.machine(t)
	return tg
}

// CanResume, one checkpoint and one machine per row: a checkpoint taken on a machine of spin's
// shape changed by saved, resumed into one changed by here. The rows are what QEMU was measured to
// load silently (2026-11-21) - an extra device, a larger BIOS, more base RAM, a property the
// migration stream does not carry - which only the record refuses; what QEMU refuses by itself,
// which the record names first; and what may differ.
func TestCanResume(t *testing.T) {
	without := func(f facts, prefix string) {
		maps.DeleteFunc(f.exact, func(n, _ string) bool { return n == prefix || strings.HasPrefix(n, prefix+" ") })
	}
	for _, tc := range []struct {
		name        string
		saved, here func(*Target, facts)
		format      string
		want        []Difference
	}{
		{name: "the same machine"},
		{name: "a newer QEMU of the same machine", saved: func(t *Target, _ facts) { t.VMM = "qemu 11.1.3" }},

		// Loaded silently: the guest never finds the vsock, and SSH over it fails with no error.
		{name: "an extra device here", saved: func(_ *Target, f facts) { without(f, "device 02.0") },
			want: []Difference{{Part: "device 02.0", Here: "vhost-vsock-pci", Rule: Same}}},
		// Loaded silently: the firmware block is part one BIOS and part another at the next reset.
		{name: "a larger BIOS here", here: func(_ *Target, f facts) { f.exact["ram pc.bios"] = "262144" },
			want: []Difference{{Part: "ram pc.bios", Checkpoint: "65536", Here: "262144", Rule: Same}}},
		// Loaded silently: memory the host gives and the guest never sees.
		{name: "more base RAM here", here: func(_ *Target, f facts) {
			f.exact["ram pc.ram"], f.exact["machine memory.size"] = "268435456", "268435456"
		}, want: []Difference{
			{Part: "machine memory.size", Checkpoint: "134217728", Here: "268435456", Rule: Same},
			{Part: "ram pc.ram", Checkpoint: "134217728", Here: "268435456", Rule: Same},
		}},
		// Loaded silently: properties the migration stream does not carry.
		{name: "another MTU announced", here: func(_ *Target, f facts) { f.exact["device 10.0 host_mtu"] = "1500" },
			want: []Difference{{Part: "device 10.0 host_mtu", Checkpoint: "1450", Here: "1500", Rule: Same}}},
		{name: "suspend allowed", here: func(_ *Target, f facts) { f.exact["device 1f.0 disable_s3"] = "0" },
			want: []Difference{{Part: "device 1f.0 disable_s3", Checkpoint: "1", Here: "0", Rule: Same}}},

		// Refused by QEMU too, named here first.
		{name: "a device missing here", here: func(_ *Target, f facts) { without(f, "device 02.0") },
			want: []Difference{{Part: "device 02.0", Checkpoint: "vhost-vsock-pci", Rule: Same}}},
		{name: "another device in the slot, with its properties", here: func(_ *Target, f facts) {
			f.exact["device 02.0"], f.exact["device 02.0 disable-legacy"] = "virtio-rng-pci", "off"
		}, want: []Difference{{Part: "device 02.0", Checkpoint: "vhost-vsock-pci", Here: "virtio-rng-pci", Rule: Same}}},
		{name: "another vCPU ceiling", here: func(_ *Target, f facts) { f.exact["machine smp.maxcpus"] = "8" },
			want: []Difference{{Part: "machine smp.maxcpus", Checkpoint: "16", Here: "8", Rule: Same}}},

		// A controller with more targets than the guest scanned: it uses what it was given.
		{name: "more hotplug targets here", here: func(_ *Target, f facts) { f.atLeast["device 1a.0 max_target"] = 16 }},
		{name: "fewer hotplug targets here", here: func(_ *Target, f facts) { f.atLeast["device 1a.0 max_target"] = 10 },
			want: []Difference{{Part: "device 1a.0 max_target", Checkpoint: "15", Here: "10", Rule: AtLeast}}},
		{name: "no hotplug controller here", here: func(_ *Target, f facts) {
			without(f, "device 1a.0")
			delete(f.atLeast, "device 1a.0 max_target")
		}, want: []Difference{{Part: "device 1a.0", Checkpoint: "virtio-scsi-pci", Rule: Same}}},
		// A release that moved a fact from one rule to the other is refused, the safe way.
		{name: "a count that is exact here", here: func(_ *Target, f facts) {
			delete(f.atLeast, "device 1a.0 max_target")
			f.exact["device 1a.0 max_target"] = "15"
		}, want: []Difference{
			{Part: "device 1a.0 max_target", Here: "15", Rule: Same},
			{Part: "device 1a.0 max_target", Checkpoint: "15", Rule: AtLeast},
		}},

		{name: "an older QEMU here", here: func(t *Target, _ facts) { t.VMM = "qemu 11.1.3" },
			want: []Difference{{Part: "vmm", Checkpoint: "qemu 11.2.0", Here: "qemu 11.1.3", Rule: AsNew}}},
		{name: "an older minor, a newer major", saved: func(t *Target, _ facts) { t.VMM = "qemu 10.9.9" }},
		{name: "another VMM, which is all that is said", here: func(t *Target, f facts) {
			t.VMM, f.exact["ram pc.bios"] = "cloud-hypervisor 48.0", "1"
		}, want: []Difference{{Part: "vmm", Checkpoint: "qemu 11.2.0", Here: "cloud-hypervisor 48.0", Rule: AsNew}}},
		{name: "a VMM that is not a version", saved: func(t *Target, _ facts) { t.VMM = "qemu" },
			want: []Difference{{Part: "vmm", Checkpoint: "qemu", Here: "qemu 11.2.0", Rule: AsNew}}},
		{name: "a machine whose VMM is not a version", here: func(t *Target, _ facts) { t.VMM = "qemu 11.x" },
			want: []Difference{{Part: "vmm", Checkpoint: "qemu 11.2.0", Here: "qemu 11.x", Rule: AsNew}}},
		{name: "a version with no program", saved: func(t *Target, _ facts) { t.VMM = " 11.2.0" },
			want: []Difference{{Part: "vmm", Checkpoint: " 11.2.0", Here: "qemu 11.2.0", Rule: AsNew}}},
		// A Saved from before records decodes with no From at all.
		{name: "a checkpoint with no record", saved: func(t *Target, f facts) {
			*t = Target{}
			clear(f.exact)
			clear(f.atLeast)
		}, want: []Difference{{Part: "vmm", Here: "qemu 11.2.0", Rule: AsNew}}},
		{name: "a format this machine does not read", format: "firecracker",
			want: []Difference{{Part: "format", Checkpoint: "firecracker", Here: "qemu-mapped-ram qemu-stream", Rule: Reads}}},

		{name: "a feature the guest was shown is not here", here: func(t *Target, _ facts) { t.CPU.Features = []string{"avx2", "xsave"} },
			want: []Difference{{Part: "cpu features", Checkpoint: "sse4.1", Rule: Same}}},
		// CPUID is not in the stream: a process would find AVX-512 the guest's kernel never enabled.
		{name: "features here the guest was not shown", here: func(t *Target, _ facts) { t.CPU.Features = append(t.CPU.Features, "avx512f", "amx-tile") },
			want: []Difference{{Part: "cpu features", Here: "amx-tile avx512f", Rule: Same}}},
		{name: "one feature here the guest was not shown", here: func(t *Target, _ facts) { t.CPU.Features = append(t.CPU.Features, "avx512f") },
			want: []Difference{{Part: "cpu features", Here: "avx512f", Rule: Same}}},
		{name: "another vendor and model", here: func(t *Target, _ facts) { t.CPU.Vendor, t.CPU.Model = "AuthenticAMD", "EPYC-Genoa-v1" },
			want: []Difference{
				{Part: "cpu vendor", Checkpoint: "GenuineIntel", Here: "AuthenticAMD", Rule: Same},
				{Part: "cpu model", Checkpoint: "host", Here: "EPYC-Genoa-v1", Rule: Same},
			}},

		{name: "every difference, at once", here: func(t *Target, f facts) {
			t.VMM, t.CPU.Features = "qemu 11.1.0", nil
			f.exact["ram pc.bios"] = "262144"
			f.exact["device 03.0"] = "virtio-rng-pci"
		}, want: []Difference{
			{Part: "vmm", Checkpoint: "qemu 11.2.0", Here: "qemu 11.1.0", Rule: AsNew},
			{Part: "cpu features", Checkpoint: "avx2 sse4.1 xsave", Rule: Same},
			{Part: "device 03.0", Here: "virtio-rng-pci", Rule: Same},
			{Part: "ram pc.bios", Checkpoint: "65536", Here: "262144", Rule: Same},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := Saved{Format: cmpOr(tc.format, FormatMappedRAM), From: target(t, tc.saved)}
			err := CanResume(saved, target(t, tc.here))
			var r *Refusal
			switch {
			case tc.want == nil && err != nil:
				t.Fatalf("CanResume = %v, want nil", err)
			case tc.want == nil:
			case !errors.As(err, &r):
				t.Fatalf("CanResume = %v, want a *Refusal", err)
			case !reflect.DeepEqual(r.Differences, tc.want):
				t.Errorf("differences\n%+v\nwant\n%+v", r.Differences, tc.want)
			}
		})
	}
}

// A refusal reads as its differences, one after the other, a side with nothing as none.
func TestARefusalSaysEachDifference(t *testing.T) {
	r := &Refusal{[]Difference{
		{Part: "ram pc.bios", Checkpoint: "65536", Here: "262144", Rule: Same},
		{Part: "device 02.0", Here: "vhost-vsock-pci", Rule: Same},
	}}
	want := "the checkpoint's machine is not this one: ram pc.bios: checkpoint 65536, here 262144 (same); " +
		"device 02.0: checkpoint none, here vhost-vsock-pci (same)"
	if got := r.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A Machine is sorted by name whatever order its facts came in, and a name has one value and one
// rule.
func TestNewMachine(t *testing.T) {
	m, err := newMachine([]part{{"b", "2"}, {"a", "1"}}, []count{{"d", 4}, {"c", 3}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Machine{exact: []part{{"a", "1"}, {"b", "2"}}, atLeast: []count{{"c", 3}, {"d", 4}}}); !reflect.DeepEqual(m, want) {
		t.Errorf("newMachine = %+v, want %+v", m, want)
	}
	for _, tc := range []struct {
		name    string
		exact   []part
		atLeast []count
	}{
		{"a fact given twice", []part{{"a", "1"}, {"a", "2"}}, nil},
		{"a count given twice", nil, []count{{"c", 1}, {"c", 1}}},
		{"a name in both lists", []part{{"c", "1"}}, []count{{"c", 1}}},
	} {
		if _, err := newMachine(tc.exact, tc.atLeast); err == nil || !strings.Contains(err.Error(), `names "`) {
			t.Errorf("%s: newMachine = %v, want it refused by name", tc.name, err)
		}
	}
}

// Two equal records are equal bytes, however they were read: a caller stores them as text and
// tells them apart as text.
func TestARecordHasOneForm(t *testing.T) {
	m, err := newMachine([]part{{"b", "2"}, {"a", "1"}}, []count{{"c", 3}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"exact":[{"name":"a","value":"1"},{"name":"b","value":"2"}],"at_least":[{"name":"c","n":3}]}`; string(b) != want {
		t.Errorf("marshalled %s, want %s", b, want)
	}
	var read Machine
	if err := json.Unmarshal([]byte(`{"at_least":[{"name":"c","n":3}],"exact":[{"name":"b","value":"2"},{"name":"a","value":"1"}]}`), &read); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(read); string(again) != string(b) {
		t.Errorf("read out of order and written again: %s, want %s", again, b)
	}
	if empty, _ := json.Marshal(Machine{}); string(empty) != `{"exact":[]}` {
		t.Errorf("an empty machine is %s, want one form", empty)
	}
	for _, bad := range []string{`{"exact":[{"name":"a","value":"1"},{"name":"a","value":"2"}]}`, `{"exact":"x"}`} {
		if err := json.Unmarshal([]byte(bad), &read); err == nil || !strings.Contains(err.Error(), "machine record") {
			t.Errorf("read %s: %v, want it refused", bad, err)
		}
	}

	tg := target(t, nil)
	first, err := json.Marshal(tg)
	if err != nil {
		t.Fatal(err)
	}
	var back Target
	if err := json.Unmarshal(first, &back); err != nil {
		t.Fatal(err)
	}
	if second, _ := json.Marshal(back); string(second) != string(first) {
		t.Errorf("a Target read back and written again differs:\n%s\n%s", first, second)
	}
	if err := CanResume(Saved{Format: FormatMappedRAM, From: back}, tg); err != nil {
		t.Errorf("a Target read back does not resume into itself: %v", err)
	}
}

// What a pool takes away so that every host shows the same CPU: what one host lacks.
func TestUnshared(t *testing.T) {
	host := func(vendor string, features ...string) CPU {
		return CPU{Vendor: vendor, Model: "host", Features: features}
	}
	for _, tc := range []struct {
		name  string
		hosts []CPU
		want  []string
		err   string
	}{
		{"one host shares everything", []CPU{host("GenuineIntel", "avx2", "sse2")}, nil, ""},
		{"what one host lacks", []CPU{host("GenuineIntel", "avx512f", "avx2", "sse2"), host("GenuineIntel", "sse2", "avx2", "sse2"),
			host("GenuineIntel", "amx-tile", "avx2", "avx512f", "sse2")}, []string{"amx-tile", "avx512f"}, ""},
		{"two vendors", []CPU{host("GenuineIntel"), host("AuthenticAMD")}, nil, "a pool of GenuineIntel host and AuthenticAMD host CPUs"},
		{"two models", []CPU{host("GenuineIntel"), {Vendor: "GenuineIntel", Model: "Skylake-Server-v4"}}, nil, "a pool of GenuineIntel host and GenuineIntel Skylake-Server-v4 CPUs"},
		{"no hosts", nil, nil, "no hosts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Unshared(tc.hosts)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("Unshared = %v, %v, want %q", got, err, tc.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("Unshared = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}
