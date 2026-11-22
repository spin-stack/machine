// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// experiment for #452: what QEMU's threads did between `running` and the login.

type thrStat struct {
	name                       string
	utime, stime, guest, minfl int64
	ran, waited                time.Duration
}

type diagSample struct {
	at  time.Time
	thr map[int]thrStat
}

func readThr(pid int) map[int]thrStat {
	out := map[int]thrStat{}
	dirs, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*", pid))
	for _, d := range dirs {
		tid, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(d + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		name := s[strings.IndexByte(s, '(')+1 : i]
		f := strings.Fields(s[i+2:])
		get := func(field int) int64 { v, _ := strconv.ParseInt(f[field-3], 10, 64); return v }
		t := thrStat{name: name, minfl: get(10), utime: get(14), stime: get(15), guest: get(43)}
		if b, err := os.ReadFile(d + "/schedstat"); err == nil {
			t.ran, t.waited, _ = parseSchedstat(string(b))
		}
		out[tid] = t
	}
	return out
}

type diagWatch struct {
	boot int
	stop chan struct{}
	done chan []diagSample
}

func startDiag(boot int) *diagWatch {
	w := &diagWatch{boot: boot, stop: make(chan struct{}), done: make(chan []diagSample, 1)}
	go func() {
		var out []diagSample
		qemu := 0
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if qemu == 0 {
				if p := qemuPID(w.boot); p != w.boot || isQEMU(p) {
					qemu = p
				}
			}
			if qemu != 0 {
				out = append(out, diagSample{at: time.Now(), thr: readThr(qemu)})
			}
			select {
			case <-w.stop:
				if qemu != 0 {
					out = append(out, diagSample{at: time.Now(), thr: readThr(qemu)})
				}
				w.done <- out
				return
			case <-tick.C:
			}
		}
	}()
	return w
}

func (w *diagWatch) end() []diagSample {
	close(w.stop)
	return <-w.done
}

// diagReport is each thread's deltas between the sample nearest from and the last sample, and the
// vCPUs' time on a CPU, waiting and in the guest, in ~20 ms windows.
func diagReport(samples []diagSample, from time.Time) string {
	if len(samples) == 0 {
		return "no samples"
	}
	a := samples[0]
	for _, s := range samples {
		if s.at.After(from) {
			break
		}
		a = s
	}
	z := samples[len(samples)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "window %v\n", z.at.Sub(a.at).Round(time.Millisecond))
	tids := make([]int, 0, len(z.thr))
	for tid := range z.thr {
		tids = append(tids, tid)
	}
	sort.Ints(tids)
	for _, tid := range tids {
		e := z.thr[tid]
		s := a.thr[tid]
		dr, dw := e.ran-s.ran, e.waited-s.waited
		if dr < time.Millisecond && dw < time.Millisecond && e.minfl == s.minfl {
			continue
		}
		fmt.Fprintf(&b, "  %-16s ran %6.1fms waited %6.1fms utime %3d stime %3d guest %3d (ticks) minflt %d\n",
			e.name, float64(dr)/1e6, float64(dw)/1e6, e.utime-s.utime, e.stime-s.stime, e.guest-s.guest, e.minfl-s.minfl)
	}
	b.WriteString("  vCPU [+ms ran waited guestticks minflt]: ")
	var prev *diagSample
	for i := range samples {
		s := &samples[i]
		if s.at.Before(a.at) {
			continue
		}
		if prev != nil && s.at.Sub(prev.at) < 20*time.Millisecond {
			continue
		}
		if prev != nil {
			var r, w time.Duration
			var g, mf int64
			for tid, t := range s.thr {
				if isVCPU(t.name) {
					r += t.ran - prev.thr[tid].ran
					w += t.waited - prev.thr[tid].waited
					g += t.guest - prev.thr[tid].guest
					mf += t.minfl - prev.thr[tid].minfl
				}
			}
			fmt.Fprintf(&b, "[+%d %.0f %.0f %d %d] ", s.at.Sub(a.at).Milliseconds(), float64(r)/1e6, float64(w)/1e6, g, mf)
		}
		prev = s
	}
	b.WriteString("\n")
	return b.String()
}
