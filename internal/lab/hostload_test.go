// SPDX-License-Identifier: Apache-2.0

package lab

import "testing"

// A burst's counters are what each counted while it ran: the CPUs' busy share of all their time,
// and the KiB pgpgin counts as MiB read. A counter one side lacks is left out rather than read
// as zero.
func TestHostCountersAreWhatTheBurstCounted(t *testing.T) {
	before := hostCounters{"cpu jiffies": 1000, "cpu busy jiffies": 200, "pgpgin": 1024, "compact_stall": 5, "psi memory full ms": 10}
	after := hostCounters{"cpu jiffies": 1400, "cpu busy jiffies": 500, "pgpgin": 1024 + 2048*1024, "compact_stall": 9, "psi memory full ms": 70, "allocstall": 3}
	got := after.since(before)
	want := hostCounters{"cpu busy %": 75, "read MiB": 2048, "compact_stall": 4, "psi memory full ms": 60}
	if len(got) != len(want) {
		t.Errorf("since = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("since[%q] = %v, want %v", k, got[k], v)
		}
	}
	// A burst inside one clock tick counted no CPU time, and has no share of it.
	if busy, ok := before.since(before)["cpu busy %"]; ok {
		t.Errorf("a burst that counted no jiffies reads %v%% busy", busy)
	}
}

// The counters are read from the kernel's own formats: PSI's totals in µs as ms, every
// allocstall_ zone summed, and the CPUs' jiffies without guest time, which user and nice already
// count. A file the host lacks leaves its counters out.
func TestHostCountersAreReadFromTheKernelsFiles(t *testing.T) {
	files := map[string]string{
		"/proc/pressure/memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=2500\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1500\n",
		"/proc/pressure/io":     "some avg10=0.00\n",
		"/proc/vmstat":          "pgpgin 4096\nallocstall_dma 1\nallocstall_normal 2\ncompact_stall 3\nnr_free_pages 99\n",
		// user nice system idle iowait irq softirq steal guest guest_nice
		"/proc/stat": "cpu  10 20 30 400 50 6 7 8 900 900\ncpu0 1 1 1 1 1 1 1 1 1 1\n",
	}
	got := hostCountersFrom(func(path string) string { return files[path] })
	want := hostCounters{"psi memory some ms": 2.5, "psi memory full ms": 1.5, "pgpgin": 4096, "allocstall": 3,
		"compact_stall": 3, "cpu jiffies": 531, "cpu busy jiffies": 81}
	if len(got) != len(want) {
		t.Errorf("hostCountersFrom = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("hostCountersFrom[%q] = %v, want %v", k, got[k], v)
		}
	}
	// A kernel before guest time counted only to steal.
	old := hostCountersFrom(func(path string) string {
		if path == "/proc/stat" {
			return "cpu  10 20 30 400 50 6 7 8\n"
		}
		return ""
	})
	if old["cpu jiffies"] != 531 || old["cpu busy jiffies"] != 81 {
		t.Errorf("hostCountersFrom with eight CPU columns = %v", old)
	}
	if none := hostCountersFrom(func(string) string { return "" }); len(none) != 0 {
		t.Errorf("a host with none of the files counts %v", none)
	}
}

// The memory going in is /proc/meminfo's three lines that say what is free and cached, in MiB.
func TestMemInfoIsWhatIsFreeAndCached(t *testing.T) {
	got := memInfoFrom("MemTotal:       32768000 kB\nMemFree:         2048 kB\nMemAvailable:    4096 kB\nBuffers: 1024 kB\nCached:          3072 kB\n")
	want := hostCounters{"MemFree MiB": 2, "MemAvailable MiB": 4, "Cached MiB": 3}
	if len(got) != len(want) || got["MemFree MiB"] != 2 || got["MemAvailable MiB"] != 4 || got["Cached MiB"] != 3 {
		t.Errorf("memInfoFrom = %v, want %v", got, want)
	}
}
