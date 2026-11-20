// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// Each CPU's busy time is its ticks but idle and iowait, from its own line: the all-CPU line, a
// line short of steal and the lines that are not CPUs are left out.
func TestEachCPUsBusyTimeIsReadFromItsStatLine(t *testing.T) {
	stat := "cpu  10 20 30 400 50 6 7 8 900 900\n" +
		// user nice system idle iowait irq softirq steal guest guest_nice
		"cpu0 1 2 3 40 5 6 7 8 9 9\n" +
		"cpu1 0 0 1 100 0 0 0 0\n" +
		"cpu2 1 2 3\n" +
		"\n" +
		"intr 1 2 3 4 5 6 7 8 9\n"
	got := cpuTimes(stat)
	want := map[int][2]float64{0: {27, 72}, 1: {1, 101}}
	if !maps.Equal(got, want) {
		t.Errorf("cpuTimes = %v, want %v", got, want)
	}
}

// An interrupt line is its counts by CPU column, named by its label and the device or the last
// word of its description; lines with fewer counts than CPUs are left out.
func TestInterruptsAreCountedByLineAndCPU(t *testing.T) {
	text := "            CPU0       CPU1       CPU2\n" +
		"   9:      57134          0          0   IO-APIC   9-fasteoi   acpi\n" +
		" 125:          3        900          1   IR-PCI-MSIX-0000:00:04.0    1-edge      nvme0q1\n" +
		" LOC:       1000       2000       3000   Local timer interrupts\n" +
		"  77:          1          1          1   timer\n" +
		"  99:          4          5          6\n" +
		" ERR:          0\n" +
		" MIS:          0\n" +
		" XYZ:          1          x          2   not a count\n"
	got := interrupts(text)
	want := map[string][]float64{
		"9 acpi":         {57134, 0, 0},
		"125 nvme0q1":    {3, 900, 1},
		"LOC interrupts": {1000, 2000, 3000},
		"77 timer":       {1, 1, 1},
		"99":             {4, 5, 6},
	}
	if len(got) != len(want) {
		t.Errorf("interrupts = %v, want %v", got, want)
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("interrupts[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// A running thread's CPU is field 39 of its stat, counted after the command, which may hold
// spaces and parentheses; a thread in any state but R is on no CPU.
func TestARunningThreadsCPUIsField39(t *testing.T) {
	fields := func(state string, cpu string) string {
		f := make([]string, 37) // fields 3 to 39, the CPU the last of them
		for i := range f {
			f[i] = "0"
		}
		f[0], f[36] = state, cpu
		return strings.Join(f, " ")
	}
	for _, tc := range []struct {
		stat string
		cpu  int
		ok   bool
	}{
		{"4242 (qemu-system-x86) " + fields("R", "7"), 7, true},
		{"4243 (CPU 0/KVM) " + fields("S", "3"), -1, false},
		{"4244 (a) b (c)) " + fields("R", "12"), 12, true},
		{"4248 (ends in a space ) " + fields("R", "5"), 5, true},
		{"4245 (short) R 1 2", -1, false},
		{"4246 (x)" + fields("R", "1"), -1, false},
		{"4247 (x) " + fields("R", "y"), 0, false},
	} {
		if cpu, ok := runningOn(tc.stat); cpu != tc.cpu || ok != tc.ok {
			t.Errorf("runningOn(%.30q…) = %d, %v, want %d, %v", tc.stat, cpu, ok, tc.cpu, tc.ok)
		}
	}
}

// The CPUs a process may run on are its status's Cpus_allowed_list, ? when it has none.
func TestCPUsAllowedIsTheStatusList(t *testing.T) {
	if got := cpusAllowed("Name:\tqemu\nCpus_allowed:\tffff\nCpus_allowed_list:\t0-15\n"); got != "0-15" {
		t.Errorf("cpusAllowed = %q", got)
	}
	if got := cpusAllowed("Name:\tqemu\n"); got != "?" {
		t.Errorf("cpusAllowed of a status without the list = %q", got)
	}
}

// A burst's CPU detail is each CPU's busy ms (ticks of 10 ms) and interrupts since before, and
// each CPU's three busiest interrupt lines.
func TestABurstsCPUDetailIsWhatEachCPUCounted(t *testing.T) {
	before := cpuLoad{
		times: map[int][2]float64{0: {100, 1000}, 1: {50, 1000}},
		irqs:  map[string][]float64{"a": {10, 0}, "b": {0, 0}, "c": {0, 0}, "d": {0, 0}, "e": {5, 5}, "f": {1}},
	}
	after := cpuLoad{
		times: map[int][2]float64{0: {130, 1040}, 1: {52, 1040}, 2: {9, 9}},
		irqs:  map[string][]float64{"a": {50, 0}, "b": {30, 1}, "c": {20, 0}, "d": {10, 0}, "e": {5, 5}, "f": {500, 500}, "new": {99, 99}},
	}
	got := after.details(before)
	want := map[string]float64{
		cpuDetail + "busy ms\x000": 300, cpuDetail + "busy ms\x001": 20,
		cpuDetail + "irqs\x000": 100, cpuDetail + "irqs\x001": 1,
		cpuDetail + "irq\x000\x00a": 40, cpuDetail + "irq\x000\x00b": 30, cpuDetail + "irq\x000\x00c": 20,
		cpuDetail + "irq\x001\x00b": 1,
	}
	if !maps.Equal(got, want) {
		t.Errorf("details = %v, want %v", got, want)
	}
}

// A row's summary names the busiest CPU and its share, CPU 0 against the others per burst, the
// CPU taking most interrupts and its lines, where QEMU ran, and the affinities.
func TestTheCPUSummarySaysWhetherOneCPUTookTheBurst(t *testing.T) {
	burst := map[string]float64{
		cpuDetail + "busy ms\x000": 300, cpuDetail + "busy ms\x001": 50, cpuDetail + "busy ms\x002": 150,
		cpuDetail + "irqs\x000": 900, cpuDetail + "irqs\x001": 100,
		cpuDetail + "irq\x000\x00125 nvme0q1": 800, cpuDetail + "irq\x000\x00LOC interrupts": 100,
		cpuDetail + "qemu on\x000": 6, cpuDetail + "qemu on\x002": 2,
		cpuDetail + "qemu allowed\x000-15": 8, cpuDetail + "lab allowed\x000-15": 1, cpuDetail + "irqbalance": 0,
		"login\x000": 100,
	}
	got := cpuSummary([]Sample{{Detail: burst}, {Detail: burst}})
	want := "busiest cpu0 60% of busy, cpu0 300 ms a burst against the others' 100; " +
		"irqs cpu0 90% (125 nvme0q1, LOC interrupts); qemu running on 0:12 2:4; allowed lab 0-15, qemu 0-15; no irqbalance"
	if got != want {
		t.Errorf("cpuSummary =\n%s\nwant\n%s", got, want)
	}
	two := map[string]float64{
		cpuDetail + "busy ms\x000": 10, cpuDetail + "busy ms\x001": 30,
		cpuDetail + "irqs\x001": 1, cpuDetail + "irq\x001\x00LOC interrupts": 1,
		cpuDetail + "qemu on\x001": 1, cpuDetail + "irqbalance": 1, cpuDetail + "lab allowed\x000-1": 1,
	}
	got = cpuSummary([]Sample{{Detail: two}})
	want = "busiest cpu1 75% of busy, cpu0 10 ms a burst against the others' 30; irqs cpu1 100% (LOC interrupts); qemu running on 1:1; allowed lab 0-1; irqbalance runs"
	if got != want {
		t.Errorf("cpuSummary on two CPUs =\n%s\nwant\n%s", got, want)
	}
	if got := cpuSummary([]Sample{{Detail: map[string]float64{"login\x000": 1}}}); got != "" {
		t.Errorf("a row without CPU detail summarises as %q", got)
	}
}
