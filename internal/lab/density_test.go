// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The rows are the crowds asked for, and a crowd that is not a number stops the experiment
// before anything boots.
func TestADensityRowIsTheNumberOfWorkspacesAlreadyRunning(t *testing.T) {
	o, err := settle(density{}, Options{"crowds": "0 4 12"})
	if err != nil {
		t.Fatal(err)
	}
	if got := (density{}).Rows(o); !slices.Equal(got, []string{"0", "4", "12"}) {
		t.Errorf("rows = %v", got)
	}
	if o["load"] != "idle" || o["settle"] != "10" {
		t.Errorf("defaults: load %q, settle %q", o["load"], o["settle"])
	}
	// One pass: a measurement boots its crowd and ends it, so a second phase would boot every crowd
	// again for nothing it measures.
	if p := (density{}).Phases(); p != 1 {
		t.Errorf("density makes %d passes, want 1", p)
	}
	// A crowd that cannot be made stops at its first workspace and says so, with nothing left
	// running: this tree has no qemu-img to make the overlay with.
	m := &Measurement{Tree: fakeSpinMachine(t, "sleep 30"), Row: "2", Options: o, Dir: t.TempDir(), Key: &Key{}}
	if err := (density{}).Measure(context.Background(), m); err == nil || !strings.Contains(err.Error(), "qemu-img") {
		t.Errorf("Measure of a crowd whose overlays cannot be made = %v, want the overlay's error", err)
	}
	// An empty crowd goes straight to the measured boot, which stops the same way.
	o0 := maps.Clone(o)
	o0["settle"] = "0"
	m = &Measurement{Tree: fakeSpinMachine(t, "sleep 30"), Row: "0", Options: o0, Dir: t.TempDir(), Key: &Key{}, values: map[string]float64{}}
	if err := (density{}).Measure(context.Background(), m); err == nil || !strings.Contains(err.Error(), "qemu-img") {
		t.Errorf("Measure of an empty crowd whose measured overlay cannot be made = %v, want the overlay's error", err)
	}
	for _, row := range []string{"many", "-1"} {
		err := (density{}).Measure(context.Background(), &Measurement{Row: row, Options: o})
		if err == nil || !strings.Contains(err.Error(), "not a number") {
			t.Errorf("Measure of a crowd of %q = %v, want refused", row, err)
		}
	}
}

// TestHelperThreads stands in for a crowd's QEMU, a process of many threads, when run by
// TestReniceSetsEveryThreadOfTheProcess: a Go program runs several before main. It lives until
// its stdin closes.
func TestHelperThreads(t *testing.T) {
	if os.Getenv("LAB_HELPER_THREADS") != "1" {
		t.Skip("run as a process with threads to renice")
	}
	fmt.Println("running")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// A crowd's QEMU runs at the row's nice value on every thread, not only the one whose id is its
// pid, which is all setpriority on the pid would change; and a process that is gone is an error.
func TestReniceSetsEveryThreadOfTheProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperThreads$")
	cmd.Env = append(os.Environ(), "LAB_HELPER_THREADS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatalf("the helper never said it was running: %v", err)
	}
	pid := cmd.Process.Pid
	// The threads running before renice: the Go runtime may start another while it runs, which
	// takes the nice value of whichever thread made it.
	before, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*", pid))
	if len(before) < 2 {
		t.Fatalf("the helper runs %d threads, want several", len(before))
	}
	if err := renice(pid, 7); err != nil {
		t.Fatal(err)
	}
	for _, d := range before {
		b, err := os.ReadFile(d + "/stat")
		if err != nil {
			t.Fatal(err)
		}
		// The fields after comm, which ends at the last ')': nice is the 19th of the line, the 17th after it.
		f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
		if nice, _ := strconv.Atoi(f[16]); nice != 7 {
			t.Errorf("thread %s of %d runs at nice %d, want 7", filepath.Base(d), pid, nice)
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := renice(pid, 7); err == nil {
		t.Errorf("renice of %d, which has exited, was taken", pid)
	}
}
