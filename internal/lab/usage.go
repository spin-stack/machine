// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// usage is what reading a running machine's counters costs it: machine.QEMU.DiskUsage, the QMP
// half of Process.Usage and what the probe's watch asks each second while a disk is held (#402).
// QEMU runs the command on its main loop, which also runs the disks' I/O, so its time there is
// time the guest's disks wait.
//
// After the cold login it times `calls` DiskUsage round trips, each beside a query-status that
// is the floor of a round trip, and reads QEMU's main thread's CPU time across each. Then it pins
// every thread of QEMU's to one host CPU with two busy loops, as the pressure probe's steal phase
// does, and times them again: what a host whose CPU is taken does to the same calls. The root
// disk is `depth` images above the base, since every seal of a workspace's disk adds one, and the
// reply QEMU builds without x-flat holds each.
type usage struct{}

func (usage) Name() string { return "usage" }
func (usage) About() string {
	return "what reading a running machine's disk counters costs it (DiskUsage), quiet and with its QEMU on one host CPU beside two busy loops"
}

func (usage) Options() []Option {
	return append(shape, Option{"calls", "200", "DiskUsage and query-status round trips timed in each phase"},
		Option{"depth", "1", "images in the root disk's chain above the base: 1 is the overlay every workspace boots, and each seal adds one"})
}
func (usage) Rows(Options) []string { return []string{"running"} }
func (usage) Phases() int           { return 1 }

func (usage) Needs(o Options) []Need {
	needs := append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}), plugNeeds(o)...)
	if o.Int("depth") > 1 {
		needs = append(needs, Need{"boot", "disk-under"})
	}
	return needs
}

func (usage) Metrics() []Metric {
	return []Metric{
		{"disk_usage", "µs", "the median DiskUsage round trip on the running machine's monitor"},
		{"disk_usage_qemu", "µs", "the median of QEMU's main thread's CPU time across a DiskUsage: the command's cost to the main loop"},
		{"status", "µs", "the median query-status round trip: a round trip's floor"},
		{"status_qemu", "µs", "the median of QEMU's main thread's CPU time across a query-status"},
		{"contended", "µs", "the median DiskUsage with every thread of QEMU's on one host CPU beside two busy loops"},
		{"contended_max", "ms", "the slowest of those"},
		{"contended_status_max", "ms", "the slowest query-status beside them: a stall every command meets is not the command's"},
	}
}

// mutate-lane: TestEveryProbeMeasuresATree
func (usage) Measure(_ context.Context, m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	under, err := deepen(m, overlay, m.Options.Int("depth"))
	if err != nil {
		return err
	}
	cid := NewCID()
	sock := filepath.Join(m.Socks, "usage.qmp")
	args := append([]string{"--vsock-cid", fmt.Sprint(cid), "--qmp", sock, "--append", m.Tree.Cmdline()}, under...)
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(overlay, args...)...)
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
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", sock, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		return fmt.Errorf("the monitor %s: %w", sock, err)
	}
	vm := machine.Attach(q)
	defer func() { _ = vm.Close() }()
	n := m.Options.Int("calls")
	quiet, err := timeCalls(ctx, vm, pid, n)
	if err != nil {
		return err
	}
	unpin, err := contend(pid)
	if err != nil {
		return err
	}
	contended, err := timeCalls(ctx, vm, pid, n)
	unpin()
	if err != nil {
		return err
	}
	p50 := func(d []time.Duration) float64 { v, _ := Percentile(d, .5); return micros(v) }
	m.Record("disk_usage", p50(quiet.usage))
	m.Record("disk_usage_qemu", p50(quiet.usageQEMU))
	m.Record("status", p50(quiet.status))
	m.Record("status_qemu", p50(quiet.statusQEMU))
	m.Record("contended", p50(contended.usage))
	top, _ := Percentile(contended.usage, 1)
	m.RecordMs("contended_max", top)
	top, _ = Percentile(contended.status, 1)
	m.RecordMs("contended_status_max", top)
	return nil
}

// deepen puts depth-1 empty images between the overlay and the base it was made over, and returns
// the boot flags that let spin-machine open them: the base stays one of them, and is only read.
// mutate-lane: TestEveryProbeMeasuresATree
func deepen(m *Measurement, overlay string, depth int) ([]string, error) {
	if depth <= 1 {
		return nil, nil
	}
	base, err := filepath.EvalSymlinks(filepath.Join(m.Tree.Dir, "image", "rootfs.qcow2"))
	if err != nil {
		return nil, fmt.Errorf("finding the tree's base image: %w", err)
	}
	dir := filepath.Join(m.Dir, "chain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the chain's directory: %w", err)
	}
	qemuImg := filepath.Join(m.Tree.Dir, "bin", "qemu-img")
	below := base
	for i := range depth - 1 {
		layer := filepath.Join(dir, fmt.Sprintf("layer%02d.qcow2", i))
		if err := run(qemuImg, "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", below, layer); err != nil {
			return nil, err
		}
		below = layer
	}
	// -u writes only the overlay's header: the images between are empty, so it reads as it did.
	if err := run(qemuImg, "rebase", "-u", "-F", "qcow2", "-b", below, overlay); err != nil {
		return nil, err
	}
	return []string{"--disk-under", base, "--disk-under", dir}, nil
}

// calls are one phase's round trips, and QEMU's main thread's CPU time across each.
type calls struct {
	usage, usageQEMU, status, statusQEMU []time.Duration
}

// timeCalls times n DiskUsage round trips on vm, each after a query-status, 5 ms apart: a host
// polls its machines' counters now and then, not back to back.
// mutate-lane: TestEveryProbeMeasuresATree
func timeCalls(ctx context.Context, vm *machine.QEMU, pid, n int) (calls, error) {
	var c calls
	for range n {
		t0, cpu0 := time.Now(), mainCPU(pid)
		if _, err := vm.Status(ctx); err != nil {
			return c, fmt.Errorf("timing query-status: %w", err)
		}
		c.status = append(c.status, time.Since(t0))
		t1, cpu1 := time.Now(), mainCPU(pid)
		c.statusQEMU = append(c.statusQEMU, cpu1-cpu0)
		if _, err := vm.DiskUsage(ctx); err != nil {
			return c, fmt.Errorf("timing DiskUsage: %w", err)
		}
		c.usage = append(c.usage, time.Since(t1))
		c.usageQEMU = append(c.usageQEMU, mainCPU(pid)-cpu1)
		time.Sleep(5 * time.Millisecond)
	}
	return c, nil
}

// mainCPU is the CPU time QEMU's main thread has had: the first field of its schedstat.
func mainCPU(pid int) time.Duration {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/schedstat", pid, pid))
	if err != nil {
		return 0
	}
	ns, _ := strconv.ParseInt(strings.Fields(string(b))[0], 10, 64)
	return time.Duration(ns)
}

// contend pins every thread of QEMU's pid to the host's last CPU and starts two busy loops there,
// and returns what stops them and lets QEMU's threads run anywhere again.
// mutate-lane: TestEveryProbeMeasuresATree
func contend(pid int) (func(), error) {
	cpu := runtime.NumCPU() - 1
	var one, all unix.CPUSet
	one.Set(cpu)
	if err := unix.SchedGetaffinity(pid, &all); err != nil {
		return nil, fmt.Errorf("reading QEMU's CPUs: %w", err)
	}
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return nil, fmt.Errorf("listing QEMU's threads: %w", err)
	}
	var tids []int
	for _, t := range tasks {
		tid, err := strconv.Atoi(t.Name())
		if err != nil {
			continue
		}
		if err := unix.SchedSetaffinity(tid, &one); err != nil {
			return nil, fmt.Errorf("pinning QEMU's thread %d to CPU %d: %w", tid, cpu, err)
		}
		tids = append(tids, tid)
	}
	var hogs []*exec.Cmd
	stop := func() {
		for _, h := range hogs {
			_ = h.Process.Kill()
			_ = h.Wait()
		}
		for _, tid := range tids {
			_ = unix.SchedSetaffinity(tid, &all)
		}
	}
	for range 2 {
		h := exec.Command("taskset", "-c", strconv.Itoa(cpu), "sh", "-c", "while :; do :; done") // #nosec G204 -- a CPU number
		if err := h.Start(); err != nil {
			stop()
			return nil, fmt.Errorf("starting a busy loop on CPU %d: %w", cpu, err)
		}
		hogs = append(hogs, h)
	}
	return stop, nil
}
