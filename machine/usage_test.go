// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"reflect"
	"strings"
	"testing"
)

// blockstatsAnswers is the places root and home's counters, from a QEMU that answers x-flat with
// refuse when it is not empty, as one without qemu/patches' 0004 does.
func blockstatsAnswers(refuse string) func(c call) string {
	places := machineAnswers("running", "root", "home")
	return func(c call) string {
		if c.Command != "query-blockstats" {
			return places(c)
		}
		if _, flat := c.Args["x-flat"]; flat && refuse != "" {
			return refused(refuse)
		}
		return ret(`[{"qdev": "/machine/peripheral/disk0/virtio-backend", "stats": {"rd_bytes": 1, "wr_bytes": 2, "rd_operations": 3, "wr_operations": 4, "flush_operations": 5}},
			{"qdev": "/machine/peripheral/hd2", "stats": {"rd_bytes": 6, "wr_bytes": 7, "rd_operations": 8, "wr_operations": 9, "flush_operations": 10}}]`)
	}
}

var wantDisks = []DiskUsage{
	{Device: "disk0", Read: 1, Written: 2, Reads: 3, Writes: 4, Flushes: 5},
	{Device: "hd2", Read: 6, Written: 7, Reads: 8, Writes: 9, Flushes: 10},
}

// Each device's counters are asked for without its backing chain's, by every DiskUsage.
func TestDiskUsageAsksForEachDevicesOwnCounters(t *testing.T) {
	f := &fakeQEMU{answer: blockstatsAnswers("")}
	m := f.attach(t)
	for range 2 {
		got, err := m.DiskUsage(ctx(t))
		if err != nil || !reflect.DeepEqual(got, wantDisks) {
			t.Fatalf("DiskUsage = %+v, %v, want %+v", got, err, wantDisks)
		}
	}
	want := []map[string]any{{"x-flat": true}, {"x-flat": true}}
	if got := f.sent("query-blockstats"); !reflect.DeepEqual(got, want) {
		t.Errorf("query-blockstats was sent %v, want %v", got, want)
	}
}

// A QEMU without the member, as a machine an earlier release started runs beside an upgraded
// library, is asked again without it, and from then on only without it: one round trip more in
// its life.
func TestDiskUsageAsksAQEMUWithoutXFlatForItsWholeChain(t *testing.T) {
	f := &fakeQEMU{answer: blockstatsAnswers("Parameter 'x-flat' is unexpected")}
	m := f.attach(t)
	for range 2 {
		got, err := m.DiskUsage(ctx(t))
		if err != nil || !reflect.DeepEqual(got, wantDisks) {
			t.Fatalf("DiskUsage = %+v, %v, want %+v", got, err, wantDisks)
		}
	}
	want := []map[string]any{{"x-flat": true}, {}, {}}
	if got := f.sent("query-blockstats"); !reflect.DeepEqual(got, want) {
		t.Errorf("query-blockstats was sent %v, want %v", got, want)
	}
}

// Any other refusal is DiskUsage's error, and the next DiskUsage asks with x-flat again.
func TestDiskUsageReturnsAnyOtherRefusal(t *testing.T) {
	f := &fakeQEMU{answer: blockstatsAnswers("Graph lock held")}
	m := f.attach(t)
	for range 2 {
		if _, err := m.DiskUsage(ctx(t)); err == nil || !strings.Contains(err.Error(), "reading the machine's disk counters") || !strings.Contains(err.Error(), "Graph lock held") {
			t.Fatalf("DiskUsage = %v, want the refusal", err)
		}
	}
	want := []map[string]any{{"x-flat": true}, {"x-flat": true}}
	if got := f.sent("query-blockstats"); !reflect.DeepEqual(got, want) {
		t.Errorf("query-blockstats was sent %v, want %v", got, want)
	}
}
