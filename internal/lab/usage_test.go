// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// fakeMonitor serves a QMP monitor on a socket that answers query-status and query-blockstats as
// a running machine of one disk does, and returns the machine attached to it.
func fakeMonitor(t *testing.T) *machine.QEMU {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "usage")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "q")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = fmt.Fprintln(c, `{"QMP": {"version": {"qemu": {"major": 11, "minor": 1, "micro": 90}, "package": ""}, "capabilities": []}}`)
		in := bufio.NewScanner(c)
		for in.Scan() {
			var req struct {
				Execute string          `json:"execute"`
				ID      json.RawMessage `json:"id"`
			}
			if json.Unmarshal(in.Bytes(), &req) != nil {
				return
			}
			ret := `{}`
			switch req.Execute {
			case "query-status":
				ret = `{"status": "running", "running": true}`
			case "query-blockstats":
				ret = `[{"qdev": "/machine/peripheral/disk0/virtio-backend", "stats": {"rd_bytes": 1}}]`
			}
			_, _ = fmt.Fprintf(c, `{"return": %s, "id": %s}`+"\n", ret, req.ID)
		}
	}()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		t.Fatal(err)
	}
	vm := machine.Attach(q)
	t.Cleanup(func() { _ = vm.Close() })
	return vm
}

// Each call is timed beside a query-status, with the main thread's CPU time across each.
func TestTimeCallsTimesEachDiskUsageBesideAStatus(t *testing.T) {
	vm := fakeMonitor(t)
	c, err := timeCalls(context.Background(), vm, os.Getpid(), 3)
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string][]time.Duration{"usage": c.usage, "usageQEMU": c.usageQEMU, "status": c.status, "statusQEMU": c.statusQEMU} {
		if len(d) != 3 {
			t.Errorf("%s has %d calls, want 3", name, len(d))
		}
	}
	for _, d := range c.usage {
		if d <= 0 {
			t.Errorf("a DiskUsage took %v", d)
		}
	}
}

// A machine that has gone is an error naming the call, not a sample of nothing.
func TestTimeCallsSaysWhichCallFailed(t *testing.T) {
	vm := fakeMonitor(t)
	_ = vm.Close()
	if _, err := timeCalls(context.Background(), vm, os.Getpid(), 1); err == nil {
		t.Fatal("timeCalls on a closed monitor succeeded")
	}
}

// The main thread's CPU time grows as it runs, and a process that is not there has none.
func TestMainCPUIsTheMainThreadsCPUTime(t *testing.T) {
	loop := exec.Command("sh", "-c", "while :; do :; done")
	if err := loop.Start(); err != nil {
		t.Skip("no sh to run a loop in")
	}
	t.Cleanup(func() { _ = loop.Process.Kill(); _ = loop.Wait() })
	before := mainCPU(loop.Process.Pid)
	time.Sleep(50 * time.Millisecond)
	if after := mainCPU(loop.Process.Pid); after <= before {
		t.Errorf("a busy loop's CPU time went from %v to %v while it ran", before, after)
	}
	if got := mainCPU(1 << 30); got != 0 {
		t.Errorf("a pid that is not there has %v", got)
	}
}

// Every thread of the process is pinned to the last CPU while the busy loops run, and let go
// after.
func TestContendPinsEveryThreadBesideTwoLoops(t *testing.T) {
	if _, err := exec.LookPath("taskset"); err != nil {
		t.Skip("no taskset")
	}
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Skip("no sleep to stand in for QEMU")
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	var before unix.CPUSet
	if err := unix.SchedGetaffinity(sleep.Process.Pid, &before); err != nil {
		t.Fatal(err)
	}
	stop, err := contend(sleep.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	var pinned unix.CPUSet
	if err := unix.SchedGetaffinity(sleep.Process.Pid, &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned.Count() != 1 || !pinned.IsSet(runtime.NumCPU()-1) {
		t.Errorf("pinned to %d CPUs, want the last alone", pinned.Count())
	}
	stop()
	var after unix.CPUSet
	if err := unix.SchedGetaffinity(sleep.Process.Pid, &after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("after the loops the CPUs are %d, want the %d before", after.Count(), before.Count())
	}
	if _, err := contend(1 << 30); err == nil {
		t.Error("contend of a pid that is not there succeeded")
	}
}

// A root deeper than the overlay needs spin-machine to take the images under it.
func TestADeeperRootNeedsDiskUnder(t *testing.T) {
	has := func(depth string) bool {
		return slices.Contains((usage{}).Needs(Options{"depth": depth, "plugged": "0"}), Need{"boot", "disk-under"})
	}
	if has("1") || !has("2") {
		t.Errorf("disk-under needed at depth 1: %v, at depth 2: %v", has("1"), has("2"))
	}
}

// A root of one image is the overlay the probe made, and nothing is put under it.
func TestDeepenLeavesADepthOfOneAlone(t *testing.T) {
	if flags, err := deepen(nil, "overlay.qcow2", 1); err != nil || flags != nil {
		t.Errorf("deepen of 1 = %v, %v", flags, err)
	}
}
