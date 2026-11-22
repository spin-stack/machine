// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const traceInst = "/sys/kernel/tracing/instances/d452"

func traceStart() {
	script := `I=` + traceInst + `
mkdir -p $I && echo 0 > $I/tracing_on && echo mono > $I/trace_clock && echo 32768 > $I/buffer_size_kb
for e in vsock kvm/kvm_vcpu_wakeup kvm/kvm_msi_set_irq kvm/kvm_apic_accept_irq kvm/kvm_exit kvm/kvm_msr kvm/kvm_fast_mmio kvm/kvm_mmio kvm/kvm_pio kvm/kvm_userspace_exit kvm/kvm_set_irq kvm/kvm_ioapic_set_irq kvm/kvm_inj_virq kvm/kvm_apic_ipi kvm/kvm_pv_eoi; do [ -d $I/events/$e ] && echo 1 > $I/events/$e/enable; done
echo > $I/trace; echo 1 > $I/tracing_on`
	_ = exec.Command("sudo", "-n", "sh", "-c", script).Run()
}

func monoNow() int64 {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return ts.Nano()
}

func monoAt(t time.Time) int64 { return monoNow() - time.Since(t).Nanoseconds() }

// traceStop ends the trace, keeps it in path and returns its lines between from and to (ns,
// CLOCK_MONOTONIC), kvm_exit lines counted by reason instead of listed.
func traceStop(path string, from, to int64) string {
	out, err := exec.Command("sudo", "-n", "sh", "-c", `I=`+traceInst+`; echo 0 > $I/tracing_on; cat $I/trace; echo > $I/trace`).Output()
	if err != nil {
		return fmt.Sprintf("trace: %v", err)
	}
	_ = os.WriteFile(path, out, 0o600)
	var b strings.Builder
	exits := map[string]int{}
	n := 0
	for line := range strings.Lines(string(out)) {
		if strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, ": ")
		if i < 0 {
			continue
		}
		f := strings.Fields(line[:i])
		if len(f) == 0 {
			continue
		}
		sec, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			continue
		}
		ts := int64(sec * 1e9)
		if ts < from || ts > to {
			continue
		}
		rel := float64(ts-from)/1e6 - 5
		ev := strings.TrimSpace(line[i+2:])
		if strings.HasPrefix(ev, "kvm_exit:") {
			r := ev
			if k := strings.Index(ev, "reason "); k >= 0 {
				r = strings.Fields(ev[k+7:])[0]
			}
			exits[fmt.Sprintf("%4d %s", int(rel)/10*10, r)]++
			continue
		}
		if n < 400 {
			fmt.Fprintf(&b, "%+8.3f %s %s\n", rel, f[0], ev)
		}
		n++
	}
	fmt.Fprintf(&b, "(%d events listed of %d)\nkvm_exit by 10ms bucket and reason:\n", min(n, 400), n)
	keys := make([]string, 0, len(exits))
	for k := range exits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s x%d\n", k, exits[k])
	}
	return b.String()
}

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
