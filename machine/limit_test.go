// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeCgroup writes files into a directory that stands for a cgroup.
func writeCgroup(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A cgroup's limits read as what each file says, a "max" as no limit, and a file the cgroup does
// not have as a resource without limits.
func TestACgroupsLimitsAreWhatItsFilesSay(t *testing.T) {
	dir := t.TempDir()
	at := time.Unix(100, 0)
	s, err := readLimits(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if want := (limitSample{at: at, memEvents: map[string]int64{}, ioMax: map[string]map[string]int64{}, ioStat: map[string]map[string]int64{}}); !reflect.DeepEqual(s, want) {
		t.Errorf("a cgroup with no files read %+v", s)
	}
	writeCgroup(t, dir, map[string]string{
		"cpu.max":        "20000 100000\n",
		"cpu.stat":       "usage_usec 5000\nnr_periods 9\nnr_throttled 7\nthrottled_usec 300\n",
		"memory.high":    "268435456\n",
		"memory.max":     "max\n",
		"memory.current": "1048576\n",
		"memory.events":  "low 0\nhigh 3\nmax 2\noom 1\noom_kill 1\n",
		"io.max":         "259:0 rbps=4194304 wbps=max riops=max wiops=100\n",
		"io.stat":        "259:0 rbytes=10 wbytes=20 rios=1 wios=2 dbytes=0 dios=0\n7:1\n",
	})
	if s, err = readLimits(dir, at); err != nil {
		t.Fatal(err)
	}
	want := limitSample{
		at: at, cpuMax: 200000, cpuUsed: 5000, throttles: 7, memHigh: 1 << 28, memCurrent: 1 << 20,
		memEvents: map[string]int64{"low": 0, "high": 3, "max": 2, "oom": 1, "oom_kill": 1},
		ioMax:     map[string]map[string]int64{"259:0": {"rbps": 4194304, "wiops": 100}},
		ioStat:    map[string]map[string]int64{"259:0": {"rbytes": 10, "wbytes": 20, "rios": 1, "wios": 2, "dbytes": 0, "dios": 0}},
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("read %+v, want %+v", s, want)
	}
	writeCgroup(t, dir, map[string]string{"cpu.max": "max 100000\n"})
	if s, _ = readLimits(dir, at); s.cpuMax != 0 {
		t.Errorf("cpu.max of max read as %d", s.cpuMax)
	}
	// A period of 0 is no rate to divide by: no limit read, and no panic.
	writeCgroup(t, dir, map[string]string{"cpu.max": "20000 0\n"})
	if s, _ = readLimits(dir, at); s.cpuMax != 0 {
		t.Errorf("cpu.max with a period of 0 read as %d", s.cpuMax)
	}
	if err := os.Remove(filepath.Join(dir, "io.stat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "io.stat"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readLimits(dir, at); err == nil || !strings.Contains(err.Error(), "io.stat") {
		t.Errorf("an io.stat that cannot be read: %v", err)
	}
}

// What held a machine is what its counters say moved between two samples, at the rate of the
// time between them, and a limit that holds again without a break keeps its Since.
func TestALimitHoldsWhenItsCountersMove(t *testing.T) {
	t0 := time.Unix(1000, 0)
	sample := func(at time.Duration, cpuUsed int64, throttles uint64, high, max, kills, rbytes, wios int64) limitSample {
		return limitSample{
			at: t0.Add(at), cpuMax: 200000, cpuUsed: cpuUsed, throttles: throttles,
			memHigh: 1 << 28, memMax: 1 << 30, memCurrent: 1 << 27,
			memEvents: map[string]int64{"high": high, "max": max, "oom_kill": kills},
			ioMax:     map[string]map[string]int64{"259:0": {"rbps": 4 << 20, "wiops": 100}, "8:0": {"wbps": 1 << 20}},
			ioStat:    map[string]map[string]int64{"259:0": {"rbytes": rbytes, "wios": wios}},
		}
	}
	var lw limitWatch
	if got := lw.next(sample(0, 0, 0, 0, 0, 0, 0, 0)); got != nil {
		t.Errorf("a first sample held %+v", got)
	}
	// Two seconds: 0.4 s of CPU, 2 throttles, 3 reclaims at high, 8 MiB read (4 MiB/s, the limit),
	// 160 writes (80/s, under 90 of 100).
	got := lw.next(sample(2*time.Second, 400000, 2, 3, 0, 0, 8<<20, 160))
	since := t0
	at := t0.Add(2 * time.Second)
	want := []Limited{
		{At: at, Limit: CPUMax, Set: 200000, Used: 200000, Events: 2, Since: since},
		{At: at, Limit: MemoryHigh, Set: 1 << 28, Used: 1 << 27, Events: 3, Since: since},
		{At: at, Limit: IOReadBytes, Device: "259:0", Set: 4 << 20, Used: 4 << 20, Since: since},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("held\n%+v\nwant\n%+v", got, want)
	}
	// A second on: CPU and memory held again, IO under 90% of its limit, writes at 90 of 100, and
	// memory.max reclaimed and killed.
	got = lw.next(sample(3*time.Second, 600000, 3, 4, 1, 1, 8<<20+3<<20, 250))
	at = t0.Add(3 * time.Second)
	want = []Limited{
		{At: at, Limit: CPUMax, Set: 200000, Used: 200000, Events: 1, Since: since},
		{At: at, Limit: MemoryHigh, Set: 1 << 28, Used: 1 << 27, Events: 1, Since: since},
		{At: at, Limit: MemoryMax, Set: 1 << 30, Used: 1 << 27, Events: 1, Since: t0.Add(2 * time.Second)},
		{At: at, Limit: MemoryOOMKill, Set: 1 << 30, Used: 1 << 27, Events: 1, Since: t0.Add(2 * time.Second)},
		{At: at, Limit: IOWrites, Device: "259:0", Set: 100, Used: 90, Since: t0.Add(2 * time.Second)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("held\n%+v\nwant\n%+v", got, want)
	}
	// A second with nothing held breaks every run: what holds after it holds since then.
	lw.next(sample(4*time.Second, 600000, 3, 4, 1, 1, 8<<20+3<<20, 250))
	got = lw.next(sample(5*time.Second, 800000, 4, 4, 1, 1, 8<<20+3<<20, 250))
	want = []Limited{{At: t0.Add(5 * time.Second), Limit: CPUMax, Set: 200000, Used: 200000, Events: 1, Since: t0.Add(4 * time.Second)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("held\n%+v\nwant\n%+v", got, want)
	}
	// Writes at 85 of 100 are under the 90% that counts as held.
	if got := lw.next(sample(6*time.Second, 800000, 4, 4, 1, 1, 8<<20+3<<20, 335)); got != nil {
		t.Errorf("IO at 85%% of its limit held %+v", got)
	}
	// A sample no later than the last says nothing.
	if got := lw.next(sample(6*time.Second, 900000, 9, 9, 9, 9, 1<<40, 1<<40)); got != nil {
		t.Errorf("a sample at the same moment held %+v", got)
	}
}

func TestALimitIsNamedForItsFile(t *testing.T) {
	for l, want := range map[Limit]string{CPUMax: "cpu.max", MemoryOOMKill: "memory.max oom_kill", IOWrites: "io.max wiops", Limit(0): "limit(0)"} {
		if got := l.String(); got != want {
			t.Errorf("%d is %q, not %q", int(l), got, want)
		}
	}
	if got := (Limited{At: time.Unix(5, 0)}).When(); !got.Equal(time.Unix(5, 0)) {
		t.Errorf("a Limited at 5 s is when %v", got)
	}
}
