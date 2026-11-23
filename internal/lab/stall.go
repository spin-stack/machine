// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
	xssh "golang.org/x/crypto/ssh"
)

// stall is whether a guest's small synced writes, on a host disk another process saturates, stall
// QEMU's monitor (#391). On a laptop with a 97% full disk, query-status took 175-950 ms at worst
// beside such writes, against 74 µs p50 idle. #402 found every slow call there was QEMU's main
// thread blocked in the host kernel, never the command's own work, but did not reproduce this case.
//
// After the login, each row sets its load going, waits 2 s for it to build, and times `calls`
// query-status round trips `interval` ms apart. Across each call it samples QEMU's main thread and
// its monitor's I/O thread every 0.5 ms (`/proc/PID/task/TID/stat` and `wchan`, as #402 did), and a
// call past 20 ms also takes both threads' kernel stacks. Each call past 20 ms is written to
// TREE-ROW-REP.stalls.txt beside result.json: what each thread was doing and waiting in.
//
// The guest's writes are dd's 4 KiB writes with oflag=dsync, each one written through before the
// next, which is fio's --bs=4k --fsync=1 without fio: the image carries no fio. The host's are
// dd's 1 MiB writes, four streams, on the disk the overlay is on: O_DIRECT, or through the host's
// page cache in the last row.
type stall struct{}

func (stall) Name() string { return "stall" }
func (stall) About() string {
	return "QMP round trips beside a guest's 4 KiB synced writes, with the host disk idle and saturated, and what QEMU's main and monitor threads waited in"
}

func (stall) Options() []Option {
	return append(shape, Option{"calls", "200", "query-status round trips timed in each row"},
		Option{"interval", "100", "ms between two round trips"})
}

// The rows' loads: the guest's synced writes alone, the host's disk saturated alone, and both.
const (
	stallWrites     = "guest writes"
	stallBusy       = "host disk busy"
	stallWritesBusy = "guest writes, host disk busy"
	// stallWritesDirty saturates the disk through the host's page cache instead: buffered writes,
	// so the host's dirty-page throttling and writeback are in play as on a laptop's full disk.
	stallWritesDirty = "guest writes, host dirty pages"
)

func (stall) Rows(Options) []string { return []string{stallWrites, stallBusy, stallWritesBusy, stallWritesDirty} }
func (stall) Phases() int           { return 1 }

func (stall) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}), plugNeeds(o)...)
}

func (stall) Metrics() []Metric {
	return []Metric{
		{"status", "µs", "the median query-status round trip"},
		{"status_max", "ms", "the slowest"},
		{"stalls", "count", "round trips over 20 ms"},
		{"main_d", "%", "the share of the main thread's samples in D (uninterruptible) across the round trips over 20 ms"},
		{"disk_busy", "%", "the host's scratch disk's busy time over the row, from its io_ticks"},
		{"guest_writes", "count", "the guest's synced 4 KiB writes over the row"},
	}
}

// stallSlow is a round trip worth a look: #402 took a stack past it.
const stallSlow = 20 * time.Millisecond

// mutate-lane: TestEveryProbeMeasuresATree
func (stall) Measure(_ context.Context, m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	cid := NewCID()
	sock := filepath.Join(m.Socks, "stall.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(overlay, "--vsock-cid", fmt.Sprint(cid), "--qmp", sock, "--append", m.Tree.Cmdline(m.Key.Init()))...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	plugged := m.Plug(sock)
	if _, err := m.Key.LoggedIn(mc, cid, t0); err != nil {
		return err
	}
	if err := plugged(); err != nil {
		return err
	}
	pid := mc.PID()
	mon, err := threadNamed(pid, "IO mon_iothread")
	if err != nil {
		return err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", sock, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		return fmt.Errorf("the monitor %s: %w", sock, err)
	}
	vm := machine.Attach(q)
	defer func() { _ = vm.Close() }()

	if m.Row != stallWrites {
		stop, err := saturate(m.Dir, m.Row != stallWritesDirty)
		if err != nil {
			return err
		}
		defer stop()
	}
	if m.Row != stallBusy {
		const writes = `nohup sh -c 'while :; do dd if=/dev/zero of=/var/tmp/stall bs=4k count=25000 oflag=dsync status=none; done' >/dev/null 2>&1 &`
		if _, _, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, writes); err != nil {
			return Miss("the guest did not start its writes: %v", err)
		}
	}
	dev := scratchDisk(m.Dir)
	ticks0, at0 := ioTicks(dev), time.Now()
	writes0 := vdaWrites(m, cid)
	time.Sleep(2 * time.Second)

	var rtts []time.Duration
	var slow []stallCall
	for range m.Options.Int("calls") {
		s := sampleThreads(pid, map[string]int{"main": pid, "monitor": mon})
		t := time.Now()
		_, err := vm.Status(ctx)
		rtt := time.Since(t)
		call := s.stop()
		if err != nil {
			return fmt.Errorf("timing query-status: %w", err)
		}
		rtts = append(rtts, rtt)
		if rtt > stallSlow {
			call.rtt = rtt
			slow = append(slow, call)
		}
		time.Sleep(time.Duration(m.Options.Int("interval")) * time.Millisecond)
	}
	if ticks1 := ioTicks(dev); ticks0 >= 0 && ticks1 >= 0 {
		m.Record("disk_busy", 100*float64(ticks1-ticks0)/float64(time.Since(at0).Milliseconds()))
	}
	if w0, w1 := writes0, vdaWrites(m, cid); w0 >= 0 && w1 >= 0 {
		m.Record("guest_writes", float64(w1-w0))
	}
	p50, _ := Percentile(rtts, .5)
	top, _ := Percentile(rtts, 1)
	m.Record("status", micros(p50))
	m.RecordMs("status_max", top)
	m.Record("stalls", float64(len(slow)))
	var d, all int
	for _, c := range slow {
		d += c.states["main"]['D']
		for _, n := range c.states["main"] {
			all += n
		}
	}
	if all > 0 {
		m.Record("main_d", 100*float64(d)/float64(all))
	}
	if len(slow) > 0 && m.Keep != "" {
		name := strings.NewReplacer(", ", "-", " ", "-").Replace(fmt.Sprintf("%s-%s-%d.stalls.txt", m.Tree.Name(), m.Row, m.Rep+1))
		if err := os.WriteFile(filepath.Join(m.Keep, name), []byte(stallReport(slow)), 0o644); err != nil {
			return fmt.Errorf("writing the stalls: %w", err)
		}
	}
	m.Logf("query-status p50 %v, max %v, %d over %v", p50, top.Round(time.Millisecond), len(slow), stallSlow)
	return nil
}

// threadNamed is the id of pid's thread whose comm is name.
func threadNamed(pid int, name string) (int, error) {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return 0, fmt.Errorf("listing QEMU's threads: %w", err)
	}
	for _, t := range tasks {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/comm", pid, t.Name()))
		if err == nil && strings.TrimSpace(string(b)) == name {
			return strconv.Atoi(t.Name())
		}
	}
	return 0, fmt.Errorf("QEMU %d has no thread named %q", pid, name)
}

// saturate starts four streams of 1 MiB writes in dir, O_DIRECT or through the page cache with a
// flush at the end of each GiB, and returns what stops them.
// mutate-lane: TestEveryProbeMeasuresATree
func saturate(dir string, direct bool) (func(), error) {
	flags := "conv=fdatasync"
	if direct {
		flags = "oflag=direct"
	}
	var hogs []*exec.Cmd
	stop := func() {
		for _, h := range hogs {
			_ = h.Process.Kill()
			_ = h.Wait()
		}
	}
	for i := range 4 {
		f := filepath.Join(dir, fmt.Sprintf("hog%d", i))
		h := exec.Command("sh", "-c", `while :; do dd if=/dev/zero of="$1" bs=1M count=1024 `+flags+` status=none; done`, "sh", f) // #nosec G204 -- the measurement's own file
		if err := h.Start(); err != nil {
			stop()
			return nil, fmt.Errorf("starting a writer on %s: %w", f, err)
		}
		hogs = append(hogs, h)
	}
	return stop, nil
}

// scratchDisk is the block device dir is on, as /sys/block names it, or "" when it cannot be told.
func scratchDisk(dir string) string {
	out, err := exec.Command("df", "--output=source", dir).Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return ""
	}
	return filepath.Base(f[len(f)-1])
}

// ioTicks is the milliseconds dev has been busy (/sys/block/DEV/stat's tenth field), or -1.
func ioTicks(dev string) int64 {
	b, err := os.ReadFile("/sys/block/" + dev + "/stat")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b))
	if len(f) < 10 {
		return -1
	}
	n, err := strconv.ParseInt(f[9], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// vdaWrites is the writes the guest on cid has completed to its root disk, or -1.
func vdaWrites(m *Measurement, cid uint32) int64 {
	_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, "cat /sys/block/vda/stat")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(out))
	if len(f) < 5 {
		return -1
	}
	n, err := strconv.ParseInt(f[4], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// stallCall is what QEMU's threads did across one round trip.
type stallCall struct {
	rtt time.Duration
	// states and wchans count each thread's samples by state and by the kernel function it slept in.
	states map[string]map[byte]int
	wchans map[string]map[string]int
	stacks map[string]string
}

// threadSampler samples threads every 0.5 ms until stopped, and takes their kernel stacks once the call
// has run past stallSlow.
type threadSampler struct {
	done chan struct{}
	out  chan stallCall
}

func sampleThreads(pid int, threads map[string]int) *threadSampler {
	s := &threadSampler{done: make(chan struct{}), out: make(chan stallCall, 1)}
	go func() {
		c := stallCall{states: map[string]map[byte]int{}, wchans: map[string]map[string]int{}, stacks: map[string]string{}}
		for name := range threads {
			c.states[name], c.wchans[name] = map[byte]int{}, map[string]int{}
		}
		var stacks sync.WaitGroup
		var mu sync.Mutex
		start, took := time.Now(), false
		for {
			for name, tid := range threads {
				st, wchan := threadState(pid, tid)
				c.states[name][st]++
				if st == 'D' || st == 'S' {
					c.wchans[name][wchan]++
				}
			}
			if !took && time.Since(start) > stallSlow {
				took = true
				for name, tid := range threads {
					stacks.Go(func() {
						b, _ := exec.Command("sudo", "-n", "cat", fmt.Sprintf("/proc/%d/task/%d/stack", pid, tid)).Output() // #nosec G204 -- QEMU's own ids
						mu.Lock()
						c.stacks[name] = string(b)
						mu.Unlock()
					})
				}
			}
			select {
			case <-s.done:
				stacks.Wait()
				s.out <- c
				return
			case <-time.After(500 * time.Microsecond):
			}
		}
	}()
	return s
}

func (s *threadSampler) stop() stallCall {
	close(s.done)
	return <-s.out
}

// threadState is a thread's state letter from its stat and the function it sleeps in from wchan.
func threadState(pid, tid int) (byte, string) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/stat", pid, tid))
	if err != nil {
		return '?', ""
	}
	// The state follows the comm, which is in parentheses and may hold spaces.
	_, rest, ok := strings.Cut(string(b), ") ")
	if !ok || rest == "" {
		return '?', ""
	}
	w, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/wchan", pid, tid))
	return rest[0], string(w)
}

// stallReport is each slow round trip, slowest first: each thread's samples by state, by wchan,
// and its kernel stack.
func stallReport(calls []stallCall) string {
	calls = slices.Clone(calls)
	slices.SortFunc(calls, func(a, b stallCall) int { return int(b.rtt - a.rtt) })
	var b strings.Builder
	for _, c := range calls {
		fmt.Fprintf(&b, "query-status %v\n", c.rtt.Round(10*time.Microsecond))
		for _, name := range slices.Sorted(maps.Keys(c.states)) {
			fmt.Fprintf(&b, "  %s: states", name)
			for _, st := range slices.Sorted(maps.Keys(c.states[name])) {
				fmt.Fprintf(&b, " %c=%d", st, c.states[name][st])
			}
			b.WriteString("; waiting in")
			for _, w := range slices.Sorted(maps.Keys(c.wchans[name])) {
				fmt.Fprintf(&b, " %s=%d", cmp.Or(w, "-"), c.wchans[name][w])
			}
			b.WriteString("\n")
			if st := strings.TrimSpace(c.stacks[name]); st != "" {
				for line := range strings.Lines(st) {
					fmt.Fprintf(&b, "    %s", line)
				}
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}
