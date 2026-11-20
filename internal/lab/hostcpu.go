// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Where on the host a burst of restores ran: whether one CPU, CPU 0 most likely, did a burst's work
// while the others waited. Nothing pins QEMU: spin-machine runs it with -sandbox
// resourcecontrol=deny, so it cannot set its own affinity, and spin gives each workspace a cgroup
// cpu.weight and no cpuset (spin-stack/spin, 2026-11-21). What could still put a burst on one CPU
// is the job's own affinity, which QEMU inherits, or the interrupts: an NVMe queue's IRQ lands on
// the CPU it is bound to, and without irqbalance that is often CPU 0. So a burst records each
// CPU's busy time, the interrupts each took and on which lines, the CPUs its QEMU threads were on
// while it ran, and the affinities.
//
// The detail keys are cpuDetail, then:
//
//	busy ms\x00N       CPU N's busy time during the burst (/proc/stat, in USER_HZ ticks of 10 ms)
//	irqs\x00N          the interrupts CPU N took
//	irq\x00N\x00LINE   the interrupts CPU N took on LINE, for its three busiest lines
//	qemu on\x00N       how many times a running QEMU thread was found on CPU N
//	qemu allowed\x00L  how many of the burst's QEMUs may run on the CPU list L
//	lab allowed\x00L   1: the lab job's own CPU list
//	irqbalance         1 if irqbalance was running, 0 if not
const cpuDetail = "cpu\x00"

// cpuTimes is each CPU's busy and total ticks by CPU number, from /proc/stat's cpuN lines: user nice
// system idle iowait irq softirq steal, idle and iowait not busy, the guest columns after them
// already counted in user and nice.
func cpuTimes(stat string) map[int][2]float64 {
	times := map[int][2]float64{}
	for line := range strings.Lines(stat) {
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		cpu, err := strconv.Atoi(strings.TrimPrefix(f[0], "cpu")) // the all-CPU line, intr, ctxt: not a number
		if err != nil {
			continue
		}
		var all, idle float64
		for i, v := range f[1:9] {
			t, _ := counted(v)
			all += t
			if i == 3 || i == 4 {
				idle += t
			}
		}
		times[cpu] = [2]float64{all - idle, all}
	}
	return times
}

// interrupts is /proc/interrupts' counts by line and CPU column. A line is named by its label and
// the last word after the counts - the device for a numbered IRQ ("125 nvme0q3"), the end of the
// description for the others ("LOC interrupts", "TLB shootdowns"). A line with fewer counts than
// CPUs (ERR, MIS) is left out.
func interrupts(text string) map[string][]float64 {
	header, rest, _ := strings.Cut(text, "\n")
	cpus := len(strings.Fields(header))
	lines := map[string][]float64{}
	for line := range strings.Lines(rest) {
		f := strings.Fields(line)
		if len(f) < cpus+1 {
			continue
		}
		counts := make([]float64, cpus)
		ok := true
		for i := range cpus {
			counts[i], ok = counted(f[1+i])
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		name := strings.TrimSuffix(f[0], ":")
		if len(f) > cpus+1 {
			name += " " + f[len(f)-1]
		}
		lines[name] = counts
	}
	return lines
}

// runningOn is the CPU a running or runnable thread is on, and whether it is one, from its
// /proc/PID/task/TID/stat: field 39, counted after the command in parentheses, which may hold
// spaces and parentheses of its own.
func runningOn(stat string) (cpu int, ok bool) {
	_, after, found := strings.Cut(stat[strings.LastIndexByte(stat, ')')+1:], " ")
	f := strings.Fields(after) // f[0] is field 3, the state
	if !found || len(f) < 37 || f[0] != "R" {
		return -1, false
	}
	cpu, err := strconv.Atoi(f[36])
	return cpu, err == nil
}

// cpusAllowed is the Cpus_allowed_list of a /proc/PID/status.
func cpusAllowed(status string) string {
	for line := range strings.Lines(status) {
		if v, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "?"
}

// cpuLoad is the host's per-CPU counters at a moment.
type cpuLoad struct {
	times map[int][2]float64
	irqs  map[string][]float64
}

func readCPULoad() cpuLoad {
	return cpuLoad{cpuTimes(readFileOrEmpty("/proc/stat")), interrupts(readFileOrEmpty("/proc/interrupts"))}
}

// details is what each CPU counted from before to c, as cpuDetail's keys.
func (c cpuLoad) details(before cpuLoad) map[string]float64 {
	d := map[string]float64{}
	for cpu, t := range c.times {
		if b, ok := before.times[cpu]; ok {
			d[cpuDetail+"busy ms\x00"+strconv.Itoa(cpu)] = (t[0] - b[0]) * 10
		}
	}
	perCPU := map[int]map[string]float64{}
	for name, counts := range c.irqs {
		b, ok := before.irqs[name]
		if !ok || len(b) != len(counts) {
			continue
		}
		for cpu, n := range counts {
			if n -= b[cpu]; n > 0 {
				if perCPU[cpu] == nil {
					perCPU[cpu] = map[string]float64{}
				}
				perCPU[cpu][name] = n
				d[cpuDetail+"irqs\x00"+strconv.Itoa(cpu)] += n
			}
		}
	}
	for cpu, lines := range perCPU {
		for _, name := range mostFirst(lines) {
			d[cpuDetail+"irq\x00"+strconv.Itoa(cpu)+"\x00"+name] = lines[name]
		}
	}
	return d
}

// watchQEMUs counts, every few milliseconds until stop is closed, the CPU each running thread of
// pids is on, and returns the counts as cpuDetail's keys with each QEMU's allowed CPUs.
// mutate-lane: TestEveryProbeMeasuresATree
func watchQEMUs(pids []int, stop <-chan struct{}) map[string]float64 {
	d := map[string]float64{}
	for _, pid := range pids {
		d[cpuDetail+"qemu allowed\x00"+cpusAllowed(readFileOrEmpty(fmt.Sprintf("/proc/%d/status", pid)))]++
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, pid := range pids {
			stats, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/stat", pid))
			for _, stat := range stats {
				if cpu, ok := runningOn(readFileOrEmpty(stat)); ok {
					d[cpuDetail+"qemu on\x00"+strconv.Itoa(cpu)]++
				}
			}
		}
		select {
		case <-stop:
			return d
		case <-tick.C:
		}
	}
}

// labCPUs is the lab job's own CPU list and whether irqbalance runs, as cpuDetail's keys.
// mutate-lane: TestEveryProbeMeasuresATree
var labCPUs = sync.OnceValue(func() map[string]float64 {
	d := map[string]float64{cpuDetail + "lab allowed\x00" + cpusAllowed(readFileOrEmpty("/proc/self/status")): 1, cpuDetail + "irqbalance": 0}
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, comm := range comms {
		if strings.TrimSpace(readFileOrEmpty(comm)) == "irqbalance" {
			d[cpuDetail+"irqbalance"] = 1
		}
	}
	return d
})

// cpuSummary is a row's cpuDetail over its samples: the busiest CPU and its share of all the busy
// time, CPU 0's busy time per burst against the others' mean, the CPU taking most interrupts with
// its share and its busiest lines, the CPUs QEMU's running threads were found on, and the
// affinities. Empty if no sample has any.
func cpuSummary(samples []Sample) string {
	busy, irqs, on := map[int]float64{}, map[int]float64{}, map[int]float64{}
	lines := map[int]map[string]float64{}
	var allowed []string
	irqbalance, bursts := "", 0
	for _, s := range samples {
		counted := false
		for k, v := range s.Detail {
			rest, ok := strings.CutPrefix(k, cpuDetail)
			if !ok {
				continue
			}
			counted = true
			kind, arg, _ := strings.Cut(rest, "\x00")
			cpu, _ := strconv.Atoi(arg)
			switch kind {
			case "busy ms":
				busy[cpu] += v
			case "irqs":
				irqs[cpu] += v
			case "qemu on":
				on[cpu] += v
			case "irq":
				n, name, _ := strings.Cut(arg, "\x00")
				c, _ := strconv.Atoi(n)
				if lines[c] == nil {
					lines[c] = map[string]float64{}
				}
				lines[c][name] += v
			case "qemu allowed", "lab allowed":
				if a := strings.TrimSuffix(kind, " allowed") + " " + arg; !slices.Contains(allowed, a) {
					allowed = append(allowed, a)
				}
			case "irqbalance":
				irqbalance = map[bool]string{true: "irqbalance runs", false: "no irqbalance"}[v > 0]
			}
		}
		if counted {
			bursts++
		}
	}
	if bursts == 0 {
		return ""
	}
	var parts []string
	if top, share, ok := busiest(busy); ok {
		var others float64
		for cpu, v := range busy {
			if cpu != 0 {
				others += v
			}
		}
		part := fmt.Sprintf("busiest cpu%d %.0f%% of busy", top, share)
		if len(busy) > 1 {
			part += fmt.Sprintf(", cpu0 %.0f ms a burst against the others' %.0f", busy[0]/float64(bursts), others/float64(len(busy)-1)/float64(bursts))
		}
		parts = append(parts, part)
	}
	if top, share, ok := busiest(irqs); ok {
		parts = append(parts, fmt.Sprintf("irqs cpu%d %.0f%% (%s)", top, share, strings.Join(mostFirst(lines[top]), ", ")))
	}
	if len(on) > 0 {
		var hist []string
		for _, cpu := range slices.Sorted(maps.Keys(on)) {
			hist = append(hist, fmt.Sprintf("%d:%.0f", cpu, on[cpu]))
		}
		parts = append(parts, "qemu running on "+strings.Join(hist, " "))
	}
	if len(allowed) > 0 {
		slices.Sort(allowed)
		parts = append(parts, "allowed "+strings.Join(allowed, ", "))
	}
	if irqbalance != "" {
		parts = append(parts, irqbalance)
	}
	return strings.Join(parts, "; ")
}

// busiest is the CPU with the most of counts, its percentage of them all, and whether any counted.
func busiest(counts map[int]float64) (cpu int, share float64, ok bool) {
	var all, most float64
	for _, c := range slices.Sorted(maps.Keys(counts)) {
		all += counts[c]
		if counts[c] > most {
			cpu, most = c, counts[c]
		}
	}
	return cpu, 100 * most / all, most > 0
}

// mostFirst are the three names of counts that counted most, most first.
func mostFirst(counts map[string]float64) []string {
	names := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
	})
	return names[:min(3, len(names))]
}
