// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// A checkpoint returns once QEMU has left finish-migrate (machine.QEMU.finished, #421), so a
// Continue right after it is never refused. QEMU sends MIGRATION{completed} before its migration
// thread takes the BQL back and moves the run state to postmigrate (migration_completion_end and
// migration_iteration_finish, QEMU 11.2.0-rc0), and cont is refused in between. An idle host
// almost never shows that window: 0 of 2000 checkpoints were refused on a laptop before the fix.
// QEMU pinned to two host CPUs that a spin loop each keeps busy shows it: 102 of 300 were refused
// before the fix and none after (laptop, 2026-11-21, #425). So this does the same: spin's shape
// plugged to 2 GiB with a writer in the guest, SPIN_FINISH_SAVES checkpoints (default 300), each
// continued at once, and any refusal fails it. A QEMU bump that reorders the two, or adds a run
// state after completed, shows here (versions.yaml's qemu note).
//
//	SPIN_FINISH_TEST=1   it starts machines (task boot:finish-migrate)
func TestACheckpointIsContinuedAtOnceOnABusyHost(t *testing.T) {
	if os.Getenv("SPIN_FINISH_TEST") == "" {
		t.Skip("set SPIN_FINISH_TEST=1: this starts machines")
	}
	saves := envInt(t, "SPIN_FINISH_SAVES", 300)
	if runtime.NumCPU() < 3 {
		t.Skip("needs two host CPUs for QEMU and one for the test")
	}
	cpus := fmt.Sprintf("%d,%d", runtime.NumCPU()-2, runtime.NumCPU()-1)
	for _, cpu := range []int{runtime.NumCPU() - 2, runtime.NumCPU() - 1} {
		hog := exec.Command("taskset", "-c", fmt.Sprint(cpu), "sh", "-c", "while :; do :; done")
		if err := hog.Start(); err != nil {
			t.Fatalf("keeping CPU %d busy: %v", cpu, err)
		}
		t.Cleanup(func() { _ = hog.Process.Kill(); _ = hog.Wait() })
	}

	r := newRecorder(t, 4100000)
	s, mac := r.next(t, "/bin/sh")
	g := startGuest(t, s, mac, nil)
	// Every thread QEMU has now, and so every thread they start, the migration's included.
	if b, err := exec.Command("taskset", "-a", "-p", "-c", cpus, fmt.Sprint(g.Pid())).CombinedOutput(); err != nil {
		t.Fatalf("pinning QEMU to CPUs %s: %v: %s", cpus, err, b)
	}
	g.boot(t, r.overlay(t, t.TempDir()), r.base())
	g.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := g.SetMemory(ctx, 2048); err != nil {
		t.Fatal(err)
	}
	g.type_(t, "mkdir -p /t; mount -t tmpfs -o size=1g t /t; (while :; do dd if=/dev/zero of=/t/f bs=1M count=600 2>/dev/null; done) & echo W-$((3+3))")
	g.await(t, regexp.MustCompile(`W-6\b`), 30*time.Second)

	dir := t.TempDir()
	refused := 0
	for i := range saves {
		state, err := os.Create(filepath.Join(dir, "state"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := g.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("checkpoint %d: %v", i, err)
		}
		if err := g.Continue(ctx); err != nil {
			refused++
			t.Errorf("checkpoint %d: continuing at once: %v", i, err)
			for g.Continue(ctx) != nil {
				time.Sleep(time.Millisecond)
			}
		}
		_ = state.Close()
	}
	t.Logf("%d checkpoints with QEMU on busy CPUs %s, %d continues refused", saves, cpus, refused)
}
