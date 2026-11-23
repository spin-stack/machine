// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Target is a machine as its VMM reports it running (VM.Target): what a checkpoint of it records
// (Saved.From), and what a checkpoint resumed into it is held to (CanResume).
//
// It is asked of the running VMM, the same way on both sides of a resume, and not predicted from a
// Spec: the two records cannot disagree in how they were made, and neither can disagree with what
// the VMM runs. Every list in it is sorted and holds no name twice, so two equal Targets marshal
// to equal bytes, which is what lets a caller store one as text and compare it as text.
type Target struct {
	// VMM is the program and version, "qemu 11.2.0".
	VMM string `json:"vmm"`
	// Formats are the state formats the machine reads, sorted.
	Formats []string `json:"formats"`
	// CPU is the CPU the guest is shown, as the running vCPU has it.
	CPU CPU `json:"cpu"`
	// Hardware is the devices, -machine options and RAM blocks the guest is given.
	Hardware Hardware `json:"machine"`
}

// Hardware is what a guest is given, as named facts, each with a rule given by where it
// sits. That is the one place a VMM says which differences a resume may cross:
//
//   - an exact fact is one the machine a checkpoint resumes into has, with the same value, and
//     no more of them: one only the machine resumed into has is a difference too, because QEMU
//     loads a checkpoint into a machine with an extra device and the guest never finds it;
//   - an at-least fact is a count the machine resumed into may have more of: the guest uses what
//     it was given and the rest waits. One the checkpoint's machine had and this one lacks, or
//     has less of, is a difference; one only this one has is not.
//
// A fact named "a b" belongs to the one named "a": a difference in "a" - a device missing, or of
// another type - is reported alone, not with each of its properties.
//
// Hardware is made by its VMM's Target through newHardware, and read from JSON through the same,
// so its lists are always sorted by name with no name twice, and it marshals to the same bytes
// however it was made.
type Hardware struct {
	exact   []part
	atLeast []count
}

// part is an exact fact: a name and its value.
type part struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// count is an at-least fact: a name and how many.
type count struct {
	Name string `json:"name"`
	N    int64  `json:"n"`
}

// newHardware sorts both lists by name, and refuses a name given twice or given in both lists: a
// fact has one value and one rule. It refuses an empty name too: a difference in a fact with none
// would name nothing (FuzzCanResume, #653).
func newHardware(exact []part, atLeast []count) (Hardware, error) {
	exact, atLeast = slices.Clone(exact), slices.Clone(atLeast)
	slices.SortFunc(exact, func(a, b part) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(atLeast, func(a, b count) int { return strings.Compare(a.Name, b.Name) })
	seen := map[string]bool{}
	var names []string
	for _, p := range exact {
		names = append(names, p.Name)
	}
	for _, c := range atLeast {
		names = append(names, c.Name)
	}
	for _, n := range names {
		if n == "" {
			return Hardware{}, errors.New("the machine names a fact with no name")
		}
		if seen[n] {
			return Hardware{}, fmt.Errorf("the machine names %q twice", n)
		}
		seen[n] = true
	}
	return Hardware{exact: exact, atLeast: atLeast}, nil
}

// hardwareJSON is Hardware as it is written.
type hardwareJSON struct {
	Exact   []part  `json:"exact"`
	AtLeast []count `json:"at_least,omitempty"`
}

// MarshalJSON writes both lists, sorted. Hardware with no exact fact writes an empty list and
// not null, so that it has one form.
func (m Hardware) MarshalJSON() ([]byte, error) {
	exact := m.exact
	if exact == nil {
		exact = []part{}
	}
	return json.Marshal(hardwareJSON{Exact: exact, AtLeast: m.atLeast})
}

// UnmarshalJSON reads what MarshalJSON wrote, or the same lists in another order, through
// newHardware: a record with a name twice is refused, as one made that way would have been.
func (m *Hardware) UnmarshalJSON(b []byte) error {
	var j hardwareJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return fmt.Errorf("reading a machine record: %w", err)
	}
	read, err := newHardware(j.Exact, j.AtLeast)
	if err != nil {
		return fmt.Errorf("reading a machine record: %w", err)
	}
	*m = read
	return nil
}

// Rule is what a part of a record is held to by a resume.
type Rule string

const (
	// Same is a part the machine resumed into has with the checkpoint's value, and the
	// checkpoint's machine had: an exact fact of the Hardware, and the CPU.
	Same Rule = "same"
	// AtLeast is a count the machine resumed into has as much of or more: an at-least fact.
	AtLeast Rule = "at least"
	// AsNew is the VMM: the same program, at the checkpoint's version or newer. A refusal whose
	// only rule is this one is a host that has not been updated yet, not a machine that changed.
	AsNew Rule = "as new"
	// Reads is the format, and what the checkpoint says was added again (Saved.Added): what the
	// machine resumed into reads.
	Reads Rule = "reads"
)

// Difference is one part a resume would cross: its name, its value on each side - empty where
// that side has none - and the rule it breaks.
type Difference struct {
	// Part names it: "vmm", "format", "cpu vendor", "cpu model", "cpu features", "device 02.0",
	// "device 10.0 host_mtu", "ram pc.bios", "machine smp.maxcpus".
	Part       string `json:"part"`
	Checkpoint string `json:"checkpoint"`
	Here       string `json:"here"`
	Rule       Rule   `json:"rule"`
}

// String is the difference on one line: "ram pc.bios: checkpoint 65536, here 262144 (same)".
func (d Difference) String() string {
	return fmt.Sprintf("%s: checkpoint %s, here %s (%s)", d.Part, cmp.Or(d.Checkpoint, "none"), cmp.Or(d.Here, "none"), d.Rule)
}

// Refusal is why a checkpoint cannot resume into a machine: every difference found, so that
// whoever fixes a host fixes it in one pass. It is the one error that says the checkpoint will
// not resume into a machine like this one, so a retry here fails the same way; Resume returns
// every other failure as a plain error (docs/machine.md, "What a failed resume says").
type Refusal struct {
	Differences []Difference `json:"differences"`
}

func (r *Refusal) Error() string {
	ds := make([]string, len(r.Differences))
	for i, d := range r.Differences {
		ds[i] = d.String()
	}
	return "the checkpoint's machine is not this one: " + strings.Join(ds, "; ")
}

// CanResume says whether a checkpoint may be resumed into a machine of this Target: nil, or a
// *Refusal naming every difference. It is the one rule: a caller placing checkpoints, and Resume
// before it reads a byte, ask it of the same two records, so a host placement chose does not
// refuse.
//
//   - the format is one the machine reads;
//   - the VMM is the same program, at the version that wrote the state or newer: QEMU loads what
//     an older QEMU of the same machine type wrote, never the other way, so a rolling update does
//     not send a checkpoint back to a host that has not been updated yet;
//   - the CPU is the same one (CPU's differences);
//   - the machine is the same one, but for the counts it may have more of (Hardware).
//
// A format the machine does not read, or a record of another VMM, is refused alone: two VMMs'
// records name nothing in common, and listing every part of both says no more. Nothing is
// skipped for being empty: a record without a VMM, a CPU or a machine - one from before records,
// or one nobody filled - is refused by name, not waved through.
func CanResume(saved Saved, here Target) error {
	if !slices.Contains(here.Formats, saved.Format) {
		return &Refusal{[]Difference{{Part: "format", Checkpoint: saved.Format, Here: strings.Join(here.Formats, " "), Rule: Reads}}}
	}
	from := saved.From
	vmm := Difference{Part: "vmm", Checkpoint: from.VMM, Here: here.VMM, Rule: AsNew}
	sp, sv, sok := vmmVersion(from.VMM)
	hp, hv, hok := vmmVersion(here.VMM)
	if !sok || !hok || sp != hp {
		return &Refusal{[]Difference{vmm}}
	}
	var ds []Difference
	if slices.Compare(hv, sv) < 0 {
		ds = append(ds, vmm)
	}
	ds = append(ds, from.CPU.differences(here.CPU)...)
	ds = append(ds, from.Hardware.differences(here.Hardware)...)
	if len(ds) == 0 {
		return nil
	}
	return &Refusal{ds}
}

// vmmVersion splits "qemu 11.2.0" into its program and version numbers, and says whether it is
// both: "qemu" is a program with no version, which no checkpoint is held to.
func vmmVersion(v string) (string, []int, bool) {
	program, version, _ := strings.Cut(v, " ")
	var nums []int
	for _, p := range strings.Split(version, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return program, nil, false
		}
		nums = append(nums, n)
	}
	return program, nums, program != ""
}

// differences are the facts of here a checkpoint of saved would cross, by name: indexed, so a
// Hardware compares the same whatever order its lists are in.
//
// A name that moved from one list to the other between releases comes out as missing from the
// exact side, which is refused: the safe direction.
func (saved Hardware) differences(here Hardware) []Difference {
	was := make(map[string]string, len(saved.exact))
	for _, p := range saved.exact {
		was[p.Name] = p.Value
	}
	is := make(map[string]string, len(here.exact))
	for _, p := range here.exact {
		is[p.Name] = p.Value
	}
	var ds []Difference
	for _, p := range saved.exact {
		if v, ok := is[p.Name]; !ok || v != p.Value {
			ds = append(ds, Difference{Part: p.Name, Checkpoint: p.Value, Here: v, Rule: Same})
		}
	}
	for _, p := range here.exact {
		if _, ok := was[p.Name]; !ok {
			ds = append(ds, Difference{Part: p.Name, Here: p.Value, Rule: Same})
		}
	}
	has := make(map[string]int64, len(here.atLeast))
	for _, c := range here.atLeast {
		has[c.Name] = c.N
	}
	for _, c := range saved.atLeast {
		n, ok := has[c.Name]
		if ok && n >= c.N {
			continue
		}
		d := Difference{Part: c.Name, Checkpoint: strconv.FormatInt(c.N, 10), Rule: AtLeast}
		if ok {
			d.Here = strconv.FormatInt(n, 10)
		}
		ds = append(ds, d)
	}
	named := make(map[string]bool, len(ds))
	for _, d := range ds {
		named[d.Part] = true
	}
	ds = slices.DeleteFunc(ds, func(d Difference) bool { return underADifference(d.Part, named) })
	slices.SortFunc(ds, func(a, b Difference) int { return strings.Compare(a.Part, b.Part) })
	return ds
}

// underADifference is whether a part "a b" belongs to a part "a" that is itself a difference.
func underADifference(name string, named map[string]bool) bool {
	for i := range len(name) {
		if name[i] == ' ' && named[name[:i]] {
			return true
		}
	}
	return false
}
