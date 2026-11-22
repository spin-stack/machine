// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The host's histograms read back as each reason's count and time, from the synthetic event's
// lines alone: the MSR histogram kept beside it shares its file and is not an exit reason.
func TestTheHostsExitHistogramReadsAsCountAndTimeByReason(t *testing.T) {
	text := `== exit reasons
# event histogram
#
# trigger info: hist:keys=reason:vals=hitcount,lat:sort=reason:size=2048 [active]
#

{ reason:          1 } hitcount:          1  lat:       2658
{ reason:         12 } hitcount:          6  lat:  884624510
{ reason:         32 } hitcount:         12  lat:      37936
{ reason:         33 } hitcount:          x  lat:          1

Totals:
    Hits: 19
== MSRs (ecx, write)
{ ecx:       1760, write:          1 } hitcount:         12
`
	got := parseExitHist(text)
	want := map[uint32]exitCost{1: {1, 2658}, 12: {6, 884624510}, 32: {12, 37936}}
	if len(got) != len(want) {
		t.Fatalf("read %v, want %v", got, want)
	}
	for r, c := range want {
		if got[r] != c {
			t.Errorf("reason %d (%s): %v, want %v", r, vmxReason(r), got[r], c)
		}
	}
	if vmxReason(32) != "MSR_WRITE" || vmxReason(0x80000021) != "reason 2147483681" {
		t.Errorf("names: %q, %q", vmxReason(32), vmxReason(0x80000021))
	}
}

// KVM's counters are summed over the vCPUs, and a histogram among them is left out.
func TestKVMsCountersAreSummedOverTheVCPUs(t *testing.T) {
	var res []allStatsResult
	if err := json.Unmarshal([]byte(`[
		{"provider":"kvm","qom-path":"/machine/unattached/device[0]","stats":[{"name":"exits","value":100},{"name":"halt_poll_fail_hist","value":[1,2,3]}]},
		{"provider":"kvm","qom-path":"/machine/peripheral/cpu1","stats":[{"name":"exits","value":20}]},
		{"provider":"cryptodev","stats":[{"name":"exits","value":5}]}]`), &res); err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	sumStats(got, "vcpu", res)
	if len(got) != 1 || got["vcpu/exits"] != 120 {
		t.Errorf("summed %v, want vcpu/exits 120 alone", got)
	}
}

// Disarming removes every trigger arming put in, in the reverse order, so that a synthetic event
// is removed only once nothing refers to it; and arming stops at the first write the host refuses.
func TestDisarmingUndoesEveryTriggerArmingAdds(t *testing.T) {
	armed := strings.Split(strings.TrimSpace(exitsScript(false)), "\n")
	disarmed := strings.Split(strings.TrimSpace(exitsScript(true)), "\n")
	if !slices.Contains(armed, "set -e") {
		t.Errorf("arming goes on past a refused write:\n%s", exitsScript(false))
	}
	var adds, removes []string
	for _, l := range armed {
		if strings.HasPrefix(l, "echo '") {
			adds = append(adds, l)
		}
	}
	for _, l := range disarmed {
		if strings.HasPrefix(l, "echo '!") {
			removes = append(removes, strings.Replace(strings.TrimSuffix(l, " 2>/dev/null"), "echo '!", "echo '", 1))
		}
	}
	slices.Reverse(removes)
	if len(adds) != len(exitsTriggers) || !slices.Equal(adds, removes) {
		t.Errorf("armed:\n%s\ndisarmed, reversed:\n%s", strings.Join(adds, "\n"), strings.Join(removes, "\n"))
	}
}

// The detail lists each reason's count, time and time an exit for every tree, the counters that
// are not zero, and the files kept for the boots that were not lost.
func TestTheExitsDetailIsEachReasonByTree(t *testing.T) {
	r := &Result{Trees: []Manifest{{Name: "release"}, {Name: "arm"}}, Samples: []Sample{
		{Tree: "release", Rep: 1, Detail: map[string]float64{"exit\x00CPUID": 3800, "exit_us\x00CPUID": 7600, "kvm\x00vcpu/exits": 10000, "kvm\x00vcpu/nmi_injections": 0}},
		{Tree: "release", Rep: 2, Detail: map[string]float64{"exit\x00HLT": 300, "exit_us\x00HLT": 9000}},
		{Tree: "arm", Rep: 1, Detail: map[string]float64{"exit\x00CPUID": 2200, "exit_us\x00CPUID": 4400, "kvm\x00vcpu/exits": 8500, "kvm\x00vcpu/nmi_injections": 0}},
		{Tree: "arm", Rep: 2, Missed: "never logged into"},
	}}
	d := exitsProbe{}.Detail(r)
	if !strings.Contains(d, "HLT                          300       9000   30.00                            -") {
		t.Errorf("a reason one tree did not make is not shown as such:\n%s", d)
	}
	for _, want := range []string{"CPUID", "3800       7600    2.00", "2200       4400    2.00", "vcpu/exits", "release-1.exits.txt", "arm-1.exits.txt"} {
		if !strings.Contains(d, want) {
			t.Errorf("no %q in:\n%s", want, d)
		}
	}
	for _, unwanted := range []string{"nmi_injections", "arm-2.exits.txt"} {
		if strings.Contains(d, unwanted) {
			t.Errorf("%q in:\n%s", unwanted, d)
		}
	}
}
