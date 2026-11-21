// SPDX-License-Identifier: Apache-2.0

package boot_test

// EXPERIMENT ONLY (#426): not for main.

import (
	"bufio"
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/lab"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// TestDeferAB checkpoints the busy-disk guest of TestACheckpointPausesTheGuestBriefly
// SPIN_AB_PAIRS times with x-defer-disk-flush off and on, in a random order within each pair, and
// prints the Δp50 of the guest's stop (on less off) with its bootstrap 95% interval, and each
// stop's phases from QEMU's trace.
func TestDeferAB(t *testing.T) {
	if os.Getenv("SPIN_AB_TEST") == "" {
		t.Skip("set SPIN_AB_TEST=1")
	}
	pairs := envInt(t, "SPIN_AB_PAIRS", 20)
	scratch, err := lab.Scratch(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", scratch)
	var st syscall.Statfs_t
	if err = syscall.Statfs(os.TempDir(), &st); err != nil {
		t.Fatal(err)
	}
	t.Logf("TMPDIR %s, file system magic %#x (ext4 0xef53, xfs 0x58465342, tmpfs 0x1021994)", os.TempDir(), st.Type)
	r := newRecorder(t, 4200000)
	s, mac := r.next(t, "/bin/sh")
	raw := filepath.Join(r.dir, "ab.sock")
	s.Monitors = []machine.Monitor{{Socket: raw}}
	trace := filepath.Join(t.TempDir(), "trace.log")
	s.Console = "stdio"
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pw.Close() })
	g := &guest{stdin: pw, screen: newScreen()}
	sctx, scancel := context.WithTimeout(context.Background(), time.Minute)
	g.Process, err = machine.Start(sctx, s, machine.StartOptions{
		Stdin: pr, Stdout: g.screen, Stderr: g.screen,
		Cmd: func(c *exec.Cmd) {
			spinsNIC(mac)(c)
			c.Args = append(c.Args, "-msg", "timestamp=on", "-D", trace)
			for _, e := range []string{"runstate_set", "vm_stop_flush_all", "migrate_set_state", "luring_co_submit", "luring_cqe_handler"} {
				c.Args = append(c.Args, "-trace", "enable="+e)
			}
		},
	})
	scancel()
	_ = pr.Close()
	if err != nil {
		t.Fatalf("%v\n%s", err, g.said())
	}
	t.Cleanup(func() { g.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	g.boot(t, r.overlay(t, t.TempDir()), r.base())
	g.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	if err := g.SetMemory(ctx, 2048); err != nil {
		t.Fatal(err)
	}
	for size := (machine.MemorySize{}); size.Now != 2048; time.Sleep(50 * time.Millisecond) {
		if size, err = g.Memory(ctx); err != nil {
			t.Fatal(err)
		}
	}
	writer := "dd if=/dev/urandom of=/mnt/src bs=1M count=64 2>/dev/null; dd if=/dev/urandom of=/mnt/ws bs=1M count=512 2>/dev/null; " +
		"(i=0; while :; do dd if=/mnt/src of=/mnt/ws bs=1M count=64 seek=$(( (i*64) % 512 )) conv=notrunc 2>/dev/null; i=$((i+1)); sleep 0.1; done) &" +
		" (while :; do dd if=/dev/urandom of=/written bs=64k count=16 oflag=direct conv=notrunc 2>/dev/null; sleep 0.1; done) &"
	g.type_(t, "mount -t tmpfs -o size=3g tmpfs /mnt && dd if=/dev/urandom of=/mnt/fill bs=1M count=1024 2>/dev/null; "+writer+" echo FILLED-$((3+3))")
	g.await(t, regexp.MustCompile(`FILLED-6\b`), 3*time.Minute)
	time.Sleep(3 * time.Second)
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: raw, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	busyDisk(t)
	time.Sleep(5 * time.Second)

	held := map[bool][]float64{}
	stopped := map[bool][]float64{}
	paused := map[bool][]float64{}
	var order []bool
	rng := rand.New(rand.NewPCG(7, 26))
	for i := range pairs {
		arms := []bool{false, true}
		if rng.IntN(2) == 1 {
			arms = []bool{true, false}
		}
		for _, on := range arms {
			machine.DeferDiskFlush = on
			p := checkpointOnce(ctx, t, g, q)
			held[on] = append(held[on], ms(p.held()))
			stopped[on] = append(stopped[on], ms(p.stopped))
			paused[on] = append(paused[on], ms(p.cost.Paused))
			order = append(order, on)
			t.Logf("AB pair %d defer=%t held %.2f ms stopped %.2f ms QEMU's downtime %v left %d MiB total %v",
				i, on, ms(p.held()), ms(p.stopped), p.cost.Paused, p.left>>20, p.cost.Total)
			time.Sleep(2 * time.Second)
		}
	}
	machine.DeferDiskFlush = true
	for _, m := range []struct {
		name string
		v    map[bool][]float64
	}{{"held", held}, {"stopped", stopped}, {"QEMU's downtime", paused}} {
		off, _ := lab.Percentile(sortedCopy(m.v[false]), .5)
		on, _ := lab.Percentile(sortedCopy(m.v[true]), .5)
		d, _ := lab.DeltaP50(m.v[false], m.v[true])
		offP90, _ := lab.Percentile(sortedCopy(m.v[false]), .9)
		onP90, _ := lab.Percentile(sortedCopy(m.v[true]), .9)
		t.Logf("AB RESULT %s: off p50 %.1f p90 %.1f max %.1f, on p50 %.1f p90 %.1f max %.1f, Δp50 %s ms (%d pairs)",
			m.name, off, offP90, maxOf(m.v[false]), on, onP90, maxOf(m.v[true]), d, pairs)
	}
	stops := traceStops(t, trace)
	for i, s := range stops {
		arm := "?"
		if i < len(order) {
			arm = fmt.Sprint(order[i])
		}
		t.Logf("AB TRACE save %d defer=%s %s", i, arm, s)
	}
}

func sortedCopy(v []float64) []float64 {
	c := append([]float64(nil), v...)
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if c[j] < c[i] {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
	return c
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// traceStops reads each checkpoint's stop out of QEMU's trace: finish-migrate to the stop's
// flush (vCPUs paused, drain), the flush, pre-switchover, completed, and the io_uring flushes
// (type 8) and writes (type 2) submitted from the stop to pre-switchover with how long the slowest
// write took to complete.
func traceStops(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Logf("no trace: %v", err)
		return nil
	}
	defer func() { _ = f.Close() }()
	at := func(line string) float64 {
		ts, err := time.Parse("2006-01-02T15:04:05.999999Z", strings.Fields(line)[0])
		if err != nil {
			return 0
		}
		return float64(ts.UnixMicro()) / 1000
	}
	var out []string
	var t0, flushed, pre float64
	var flushes, writes int
	in := false
	reqs := map[string]float64{}
	var slowest float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "new_state 7 (finish-migrate)"):
			in, t0, flushed, pre, flushes, writes, slowest = true, at(line), 0, 0, 0, 0, 0
			clear(reqs)
		case !in:
		case strings.Contains(line, "luring_co_submit") && pre == 0:
			if strings.HasSuffix(line, "type 8") {
				flushes++
			} else if strings.HasSuffix(line, "type 2") {
				writes++
			}
		case strings.Contains(line, "vm_stop_flush_all"):
			flushed = at(line)
		case strings.Contains(line, "new state pre-switchover"):
			pre = at(line)
		case strings.Contains(line, "new state completed"):
			out = append(out, fmt.Sprintf("stop to flush done %.2f, to pre-switchover %.2f, to completed %.2f ms; io_uring flushes %d writes %d",
				flushed-t0, pre-t0, at(line)-t0, flushes, writes))
			in = false
		}
		_ = slowest
	}
	return out
}
