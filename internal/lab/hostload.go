// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// hostCounters are the host kernel's running totals that say what a burst of restores waited on:
// the CPU, the disk, or memory. Eight restores let go at once took ~210 ms to the first login in
// the first three bursts of lab 44792429636 and ~330-430 in every burst after, at any count of
// channels, while one or four at once did not move: the probe recorded nothing that said which.
// A file the host does not have (PSI is off on some kernels) leaves its counters out.
type hostCounters map[string]float64

// vmstatCounters are /proc/vmstat's counters of a page allocation that had to wait: for reclaim
// (allocstall_*, pgscan_direct), for compaction (compact_stall), for a huge page it then went
// without (thp_fault_fallback), and the KiB read from block devices (pgpgin).
var vmstatCounters = []string{"pgpgin", "pgmajfault", "pgscan_direct", "pgsteal_direct", "compact_stall",
	"compact_fail", "thp_fault_alloc", "thp_fault_fallback"}

// readHostCounters reads the totals now.
func readHostCounters() hostCounters { return hostCountersFrom(readFileOrEmpty) }

// hostCountersFrom reads the totals from the files read returns, by path.
func hostCountersFrom(read func(path string) string) hostCounters {
	c := hostCounters{}
	for _, res := range []string{"cpu", "io", "memory"} {
		// "some avg10=0.00 avg60=0.00 avg300=0.00 total=1234" in µs, a line for some and full.
		for line := range strings.Lines(read("/proc/pressure/" + res)) {
			f := strings.Fields(line)
			if len(f) < 5 {
				continue
			}
			if v, ok := strings.CutPrefix(f[4], "total="); ok {
				if n, ok := counted(v); ok {
					c["psi "+res+" "+f[0]+" ms"] = n / 1000
				}
			}
		}
	}
	for line := range strings.Lines(read("/proc/vmstat")) {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		n, ok := counted(v)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(k, "allocstall_"):
			c["allocstall"] += n
		case slices.Contains(vmstatCounters, k):
			c[k] = n
		}
	}
	// The first line of /proc/stat is every CPU's jiffies: user nice system idle iowait irq
	// softirq steal guest guest_nice, the guests counted again inside user and nice.
	first, _, _ := strings.Cut(read("/proc/stat"), "\n")
	if f := strings.Fields(first); len(f) >= 9 && f[0] == "cpu" {
		var all, idle float64
		for i, v := range f[1:9] {
			n, _ := counted(v)
			all += n
			if i == 3 || i == 4 {
				idle += n
			}
		}
		c["cpu jiffies"], c["cpu busy jiffies"] = all, all-idle
	}
	return c
}

// since is what each counter counted from before to c, the host's CPUs busy as a share of all of
// them, and pgpgin as MiB read.
func (c hostCounters) since(before hostCounters) hostCounters {
	d := hostCounters{}
	for k, v := range c {
		if b, ok := before[k]; ok {
			d[k] = v - b
		}
	}
	if all := d["cpu jiffies"]; all != 0 {
		d["cpu busy %"] = 100 * d["cpu busy jiffies"] / all
	}
	delete(d, "cpu jiffies")
	delete(d, "cpu busy jiffies")
	if kib, ok := d["pgpgin"]; ok {
		d["read MiB"] = kib / 1024
		delete(d, "pgpgin")
	}
	return d
}

// memInfo is the host's MemFree, MemAvailable and Cached, in MiB.
func memInfo() hostCounters { return memInfoFrom(readFileOrEmpty("/proc/meminfo")) }

// memInfoFrom reads MemFree, MemAvailable and Cached from /proc/meminfo's text, in MiB.
func memInfoFrom(meminfo string) hostCounters {
	c := hostCounters{}
	for line := range strings.Lines(meminfo) {
		// "MemFree:         2048 kB"
		k, v, _ := strings.Cut(line, ":")
		switch k {
		case "MemFree", "MemAvailable", "Cached":
			if n, ok := counted(strings.TrimSuffix(strings.TrimSpace(v), " kB")); ok {
				c[k+" MiB"] = n / 1024
			}
		}
	}
	return c
}

// counted is s as a float64, and whether it was one.
func counted(s string) (float64, bool) {
	n, err := strconv.ParseFloat(s, 64) // mutate-exempt: ParseFloat reads every bitSize but 32 as 64
	return n, err == nil
}

func readFileOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
