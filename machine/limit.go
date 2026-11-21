// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// Limit is one of the limits spin sets on the cgroup QEMU runs in.
type Limit int

const (
	// CPUMax is cpu.max: the cgroup's CPU throttled at its quota.
	CPUMax Limit = iota + 1
	// MemoryHigh is memory.high: QEMU's memory reclaimed, and its allocations slowed, to keep it
	// under the limit.
	MemoryHigh
	// MemoryMax is memory.max: QEMU's memory reclaimed at the limit.
	MemoryMax
	// MemoryOOMKill is memory.max with nothing left to reclaim: the OOM killer killed in QEMU's
	// cgroup, where QEMU is the one process.
	MemoryOOMKill
	// IOReadBytes, IOWriteBytes, IOReads and IOWrites are io.max's rbps, wbps, riops and wiops.
	IOReadBytes
	IOWriteBytes
	IOReads
	IOWrites
)

var limitNames = map[Limit]string{
	CPUMax: "cpu.max", MemoryHigh: "memory.high", MemoryMax: "memory.max", MemoryOOMKill: "memory.max oom_kill",
	IOReadBytes: "io.max rbps", IOWriteBytes: "io.max wbps", IOReads: "io.max riops", IOWrites: "io.max wiops",
}

func (l Limit) String() string {
	if s, ok := limitNames[l]; ok {
		return s
	}
	return fmt.Sprintf("limit(%d)", int(l))
}

// ioLimits are io.max's keys, each with the io.stat counter it holds.
var ioLimits = []struct {
	limit    Limit
	max, use string
}{
	{IOReadBytes, "rbps", "rbytes"}, {IOWriteBytes, "wbps", "wbytes"}, {IOReads, "riops", "rios"}, {IOWrites, "wiops", "wios"},
}

// Limited is the machine held at a limit spin set on QEMU's own cgroup, in the second before At.
// A Stall says the machine waited; Limited says spin's limit is why. It comes again each second
// the limit holds, with the same Since.
type Limited struct {
	At    time.Time
	Limit Limit
	// Device is io.max's device, major:minor, and empty for the other limits.
	Device string
	// Set is the limit in Used's unit: CPU microseconds a second for cpu.max, bytes for memory,
	// bytes or IOs a second for io.max.
	Set int64
	// Used is what the machine used: over the second for CPU and IO, memory.current for memory.
	Used int64
	// Events is how often the kernel held the machine in that second: cpu.stat's nr_throttled,
	// memory.events' high, max or oom_kill. io.max counts none, so it is 0 for IO.
	Events uint64
	// Since is when the limit began to hold the machine, without a second's break.
	Since time.Time
}

func (l Limited) When() time.Time { return l.At }
func (Limited) pressure()         {}

const (
	// limitTick is how often the cgroup's limits are read: cpu.stat and io.stat cannot be polled.
	limitTick = time.Second
	// ioHeld is the share of an io.max limit, in tenths, that a second's IO must reach to have been
	// held by it. cgroup v2 has no counter of what io.max throttled (Linux 7.0, 2026-11-21), and
	// QEMU's io.pressure is no witness: held 5 s at rbps of 4 MiB/s, which it read at 99.9%, its
	// total did not move, since io_uring leaves no thread of QEMU's waiting (task boot:pressure,
	// laptop, 2026-11-21). The guest's IO stalled each of those seconds.
	ioHeld = 9
)

// limitSample is a cgroup's limits and their counters at one moment. A limit of 0 is "max".
type limitSample struct {
	at time.Time
	// cpuMax is cpu.max's quota in CPU microseconds a second; cpuUsed and throttles, cpu.stat's
	// usage_usec and nr_throttled.
	cpuMax, cpuUsed int64
	throttles       uint64
	memHigh, memMax int64
	memCurrent      int64
	// memEvents is memory.events: high, max, oom, oom_kill.
	memEvents map[string]int64
	// ioMax and ioStat are io.max and io.stat, by device and key.
	ioMax, ioStat map[string]map[string]int64
}

// readLimits reads the limits of the cgroup at dir. A file the cgroup does not have, as when the
// controller is not enabled on it, is a resource without limits.
func readLimits(dir string, at time.Time) (limitSample, error) {
	s := limitSample{at: at}
	read := func(name string) ([]byte, error) {
		// #nosec G304 -- a file of QEMU's cgroup
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the machine's limits: %w", err)
		}
		return b, nil
	}
	files := map[string][]byte{}
	for _, name := range []string{"cpu.max", "cpu.stat", "memory.high", "memory.max", "memory.current", "memory.events", "io.max", "io.stat"} {
		b, err := read(name)
		if err != nil {
			return s, err
		}
		files[name] = b
	}
	// cpu.max is "$QUOTA $PERIOD", or "max $PERIOD".
	if q, p, ok := bytes.Cut(bytes.TrimSpace(files["cpu.max"]), []byte(" ")); ok {
		// "max" parses as no quota: no limit, and cpuMax stays 0.
		if quota, err := strconv.ParseInt(string(q), 10, 64); err == nil {
			if period, err := strconv.ParseInt(string(p), 10, 64); err == nil && period != 0 {
				s.cpuMax = quota * int64(time.Second/time.Microsecond) / period
			}
		}
	}
	cpu := keyed(files["cpu.stat"])
	s.cpuUsed, s.throttles = cpu["usage_usec"], uint64(cpu["nr_throttled"])
	s.memHigh, s.memMax, s.memCurrent = bytesOf(files["memory.high"]), bytesOf(files["memory.max"]), bytesOf(files["memory.current"])
	s.memEvents = keyed(files["memory.events"])
	s.ioMax, s.ioStat = devices(files["io.max"]), devices(files["io.stat"])
	return s, nil
}

// bytesOf is a memory file's one number, and 0 for "max" or nothing.
func bytesOf(b []byte) int64 {
	n, _ := strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
	return n
}

// devices is io.stat's or io.max's lines, "259:0 rbytes=1 wbytes=2" or "259:0 rbps=2097152
// wbps=max", by device and key. A key of max is left out.
func devices(b []byte) map[string]map[string]int64 {
	m := map[string]map[string]int64{}
	for line := range bytes.Lines(b) {
		f := bytes.Fields(line)
		if len(f) < 2 {
			continue
		}
		dev := map[string]int64{}
		for _, kv := range f[1:] {
			k, v, _ := bytes.Cut(kv, []byte("="))
			if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
				dev[string(k)] = n
			}
		}
		m[string(f[0])] = dev
	}
	return m
}

// limitWatch turns samples of one cgroup into what held the machine between them.
type limitWatch struct {
	last  limitSample
	since map[limitKey]time.Time
}

type limitKey struct {
	limit  Limit
	device string
}

// next is what held the machine between the last sample and now.
func (lw *limitWatch) next(now limitSample) []Limited {
	last := lw.last
	lw.last = now
	if last.at.IsZero() {
		return nil
	}
	dt := now.at.Sub(last.at)
	if dt <= 0 { // mutate-exempt: <= 1 differs only on samples a nanosecond apart, a second is what the watch waits
		return nil
	}
	perSecond := func(d int64) int64 { return int64(float64(d) * float64(time.Second) / float64(dt)) }
	var held []Limited
	if now.throttles > last.throttles {
		held = append(held, Limited{Limit: CPUMax, Set: now.cpuMax, Used: perSecond(now.cpuUsed - last.cpuUsed), Events: now.throttles - last.throttles})
	}
	for _, m := range []struct {
		limit Limit
		event string
		set   int64
	}{{MemoryHigh, "high", now.memHigh}, {MemoryMax, "max", now.memMax}, {MemoryOOMKill, "oom_kill", now.memMax}} {
		if d := now.memEvents[m.event] - last.memEvents[m.event]; d > 0 {
			held = append(held, Limited{Limit: m.limit, Set: m.set, Used: now.memCurrent, Events: uint64(d)})
		}
	}
	for _, dev := range slices.Sorted(maps.Keys(now.ioMax)) {
		for _, k := range ioLimits {
			set, ok := now.ioMax[dev][k.max]
			if !ok {
				continue
			}
			used := perSecond(now.ioStat[dev][k.use] - last.ioStat[dev][k.use])
			if used*10 >= set*ioHeld {
				held = append(held, Limited{Limit: k.limit, Device: dev, Set: set, Used: used})
			}
		}
	}
	since := map[limitKey]time.Time{}
	for i := range held {
		key := limitKey{held[i].Limit, held[i].Device}
		s, ok := lw.since[key]
		if !ok {
			s = last.at
		}
		since[key] = s
		held[i].At, held[i].Since = now.at, s
	}
	lw.since = since
	return held
}
