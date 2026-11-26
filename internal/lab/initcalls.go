// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// initcalls is where the kernel's own boot goes, initcall by initcall: the largest single block
// of a cold boot, and a tool for finding why the goal's numbers moved rather than one of them.
//
// The console stays quiet for the whole boot and the ring buffer is read afterwards:
// initcall_debug with its lines on the console puts a serial write, and a VM exit, inside every
// interval it reports. `spin-machine boot --profile` gives the command line; a unit ordered after
// multi-user.target prints the buffer once the part measured is over - to /dev/ttyS0 from inside
// its shell with StandardOutput=null, because through the journal every line reaches the console
// with a second, fresh stamp, and the first stamp on a line is then the dump's clock and not the
// boot's (it once put "Freeing unused kernel image" at 931 ms in a boot of about 200).
type initcalls struct{}

func (initcalls) Name() string { return "initcalls" }
func (initcalls) About() string {
	return "ms of the kernel's boot from its ring buffer, initcall_debug with the console quiet"
}
func (initcalls) Options() []Option {
	return append(shape, Option{"top", "30", "how many initcalls, and half as many gaps, to list"})
}
func (initcalls) Rows(Options) []string { return []string{"boot"} }

func (initcalls) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "console"}, Need{"boot", "append"}, Need{"boot", "profile"}), plugNeeds(o)...)
}
func (initcalls) Phases() int { return 1 }

func (initcalls) Metrics() []Metric {
	return []Metric{
		{"freeing", "ms", `kernel start to "Freeing unused kernel image"`},
		{"initcalls", "ms", "the sum of every initcall"},
		{"outside", "ms", "freeing less initcalls: memory, SMP bringup, RCU, the scheduler, which are not initcalls"},
	}
}

const dmesgUnit = `[Unit]
Description=Print the kernel ring buffer for the initcall probe
After=multi-user.target
[Service]
Type=oneshot
StandardOutput=null
ExecStart=/bin/sh -c "{ echo SPIN-DMESG-BEGIN; dmesg; echo SPIN-DMESG-END; } > /dev/ttyS0"
[Install]
WantedBy=multi-user.target
`

// mutate-lane: TestEveryProbeMeasuresATree
func (initcalls) Measure(_ context.Context, m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", Edit{
		Files: map[string]string{"/etc/systemd/system/spin-dmesg.service": dmesgUnit},
		Links: map[string]string{"/etc/systemd/system/multi-user.target.wants/spin-dmesg.service": "/etc/systemd/system/spin-dmesg.service"},
	})
	if err != nil {
		return err
	}
	console, w := io.Pipe()
	qmp := filepath.Join(m.Socks, "m.qmp")
	args := append([]string{"--console", "file:/dev/stdout", "--append", m.Tree.Cmdline("/sbin/init"), "--profile"}, m.qmpToPlug(qmp)...)
	mc, err := m.Launch(w, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	// Sized as the shape it records says, as spin sizes a workspace. Left at 128 MiB, this probe lost
	// 7 of 80 boots across two release reports, each a dump that never came in 90 s (report runs
	// 44800662338 and 44805222647, 2026-11-20), where ssh-first and the restores, which size their
	// guests, lost none; such a guest had 6 MiB of its 72 free and had already stalled on memory
	// when the dump ran on a laptop that lost none (2026-11-20).
	plugged := m.Plug(qmp)
	// The read end closed first: once the marker is read nothing drains the pipe, and the copy of
	// the console into it - which the machine's Wait waits for - would block on it for good.
	defer func() { _ = console.Close(); mc.Stop(); _ = w.Close() }()
	out, err := readUntil(console, "SPIN-DMESG-END", 90*time.Second)
	if err != nil {
		return Miss("%v; console tail: %s", err, tail(out, 2000))
	}
	if err := plugged(); err != nil {
		return err
	}
	p, err := parseRingBuffer(out)
	if err != nil {
		return err
	}
	m.Record("initcalls", float64(p.total)/1000)
	if p.freeing > 0 {
		m.Record("freeing", p.freeing*1000)
		m.Record("outside", p.freeing*1000-float64(p.total)/1000)
	}
	for name, us := range p.calls {
		m.Detail("initcall\x00"+name, float64(us)/1000)
	}
	for _, g := range p.gaps {
		m.Detail("gap\x00"+g.after+"\x00"+g.before, g.ms)
	}
	return nil
}

// readUntil reads r until marker has appeared, and returns all of it - or, when it never does,
// all that came before the timeout, which is what says where the guest stopped: a timeout that
// returned nothing once hid that the console had been read all along (2026-11-20).
func readUntil(r io.Reader, marker string, timeout time.Duration) (string, error) {
	var (
		mu sync.Mutex
		b  bytes.Buffer
	)
	read := func() string { mu.Lock(); defer mu.Unlock(); return b.String() }
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1<<16) // mutate-exempt: the size of each read; the console is read whole either way
		for {
			n, err := r.Read(buf)
			mu.Lock()
			b.Write(buf[:n])
			seen := bytes.Contains(b.Bytes(), []byte(marker))
			mu.Unlock()
			if seen {
				done <- nil
				return
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			return read(), fmt.Errorf("the console ended before %q: %w", marker, err)
		}
		return read(), nil
	case <-time.After(timeout):
		return read(), fmt.Errorf("never saw %q in %s", marker, timeout)
	}
}

var (
	// [    0.123456] initcall acpi_init+0x0/0x1a0 returned 0 after 1234 usecs
	reDone  = regexp.MustCompile(`initcall ([^ ]+) returned (-?\d+) after (\d+) usecs`)
	reStamp = regexp.MustCompile(`^\[\s*(\d+)\.(\d{6})\]`)
	// A second stamp further along the line: the dump was re-stamped on its way out.
	reDouble = regexp.MustCompile(`^\[\s*\d+\.\d{6}\].*\[\s*\d+\.\d{6}\]`)
	// A process id in a userspace line's prefix, which would split one gap across as many keys as
	// the boot handed out pids.
	rePID = regexp.MustCompile(`\[\d+\]:`)
)

// gap is the time between two adjacent ring-buffer stamps and what was printed on either side.
type gap struct {
	ms            float64
	after, before string
}

// ringBuffer is one boot's ring buffer, reduced.
type ringBuffer struct {
	calls   map[string]int // initcall -> usecs
	gaps    []gap
	total   int     // usecs in every initcall
	freeing float64 // seconds to "Freeing unused kernel image", on the kernel's clock (PrintkLag)
}

func parseRingBuffer(console string) (ringBuffer, error) {
	out := ringBuffer{calls: map[string]int{}}
	byBefore := map[string]gap{}
	prev, prevMsg := -1.0, ""
	for line := range strings.SplitSeq(console, "\n") {
		if reDouble.MatchString(line) {
			return out, fmt.Errorf("a console line carries two kernel stamps, so the dump is re-stamped on its way out and every timing would be the console's: %s", strings.TrimSpace(line))
		}
		if m := reStamp.FindStringSubmatch(line); m != nil {
			sec, _ := strconv.Atoi(m[1])
			us, _ := strconv.Atoi(m[2])
			at := float64(sec) + float64(us)/1e6
			msg := clip(rePID.ReplaceAllString(strings.TrimSpace(line[len(m[0]):]), "[]:"))
			// Monotonic only: a reprint that went backwards would make a negative gap.
			if prev >= 0 && at >= prev {
				byBefore[msg] = gap{ms: (at - prev) * 1000, after: prevMsg, before: msg}
			}
			prev, prevMsg = at, msg
			if strings.Contains(line, "Freeing unused kernel image") {
				out.freeing = at
			}
		}
		if m := reDone.FindStringSubmatch(line); m != nil {
			us, _ := strconv.Atoi(m[3])
			out.calls[m[1]] = us
			out.total += us
		}
	}
	if out.freeing > 0 {
		lag, err := PrintkLag(console)
		if err != nil {
			return out, err
		}
		out.freeing += lag / 1000
	}
	if len(out.calls) == 0 {
		return out, fmt.Errorf("no initcall lines in the ring buffer: was initcall_debug on the command line, and log_buf_len large enough? console tail: %s", tail(console, 2000)) // mutate-exempt: how much of the console the error quotes
	}
	for _, g := range byBefore {
		out.gaps = append(out.gaps, g)
	}
	return out, nil
}

func clip(s string) string {
	if len(s) > 88 {
		return s[:88]
	}
	return s
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// initcallsOfInterest are listed side by side for every tree: a tree that moved the total is
// only interesting once it is clear which initcall moved.
var initcallsOfInterest = []string{
	"acpi_init", "inet_init", "virtio_pci_driver_init", "virtio_blk_init", "virtio_scsi_init",
}

// Detail lists, for every tree, the initcalls of interest side by side, then each tree's slowest
// initcalls and largest gaps.
func (initcalls) Detail(r *Result) string {
	var b strings.Builder
	top := 30
	if n, err := strconv.Atoi(r.Options["top"]); err == nil {
		top = n
	}
	fmt.Fprintf(&b, "\n  %-26s", "INITCALL (p50 ms)")
	for _, t := range r.Trees {
		fmt.Fprintf(&b, " %14s", t.Name)
	}
	fmt.Fprintln(&b)
	for _, name := range initcallsOfInterest {
		fmt.Fprintf(&b, "  %-26s", name)
		for _, t := range r.Trees {
			var xs []float64
			for _, s := range r.Samples {
				for k, v := range s.Detail {
					if s.Tree == t.Name && strings.HasPrefix(k, "initcall\x00"+name+"+") {
						xs = append(xs, v)
					}
				}
			}
			if p, ok := Percentile(xs, .5); ok {
				fmt.Fprintf(&b, " %14.2f", p)
			} else {
				fmt.Fprintf(&b, " %14s", "-")
			}
		}
		fmt.Fprintln(&b)
	}
	for _, t := range r.Trees {
		fmt.Fprintf(&b, "\n  %s\n", t.Name)
		ranked := func(prefix string) []string {
			byKey := map[string][]float64{}
			for _, s := range r.Samples {
				if s.Tree != t.Name {
					continue
				}
				for k, v := range s.Detail {
					if strings.HasPrefix(k, prefix) {
						byKey[k] = append(byKey[k], v)
					}
				}
			}
			keys := slices.Collect(maps.Keys(byKey))
			p50 := func(k string) float64 { p, _ := Percentile(byKey[k], .5); return p }
			slices.SortFunc(keys, func(a, c string) int { return cmp.Compare(p50(c), p50(a)) })
			for i, k := range keys {
				p95, _ := Percentile(byKey[k], .95)
				keys[i] = fmt.Sprintf("%8.2f %8.2f\x00%s", p50(k), p95, strings.TrimPrefix(k, prefix))
			}
			return keys
		}
		fmt.Fprintf(&b, "  %8s %8s  %s\n", "GAP P50", "P95", "BETWEEN (ms)")
		for i, l := range ranked("gap\x00") {
			if i >= top/2 {
				break
			}
			nums, rest, _ := strings.Cut(l, "\x00")
			after, before, _ := strings.Cut(rest, "\x00")
			fmt.Fprintf(&b, "  %s  after:  %s\n  %17s  before: %s\n", nums, after, "", before)
		}
		fmt.Fprintf(&b, "  %8s %8s  %s\n", "P50", "P95", "INITCALL")
		for i, l := range ranked("initcall\x00") {
			if i >= top {
				break
			}
			nums, name, _ := strings.Cut(l, "\x00")
			fmt.Fprintf(&b, "  %s  %s\n", nums, name)
		}
	}
	return b.String()
}
