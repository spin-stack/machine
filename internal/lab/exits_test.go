// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/json"
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
