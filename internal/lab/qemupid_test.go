// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The machine's pid is QEMU's whether spin-machine boot exec'd QEMU or started it as its child,
// and a child that is not QEMU, as the old boot's helper is to the QEMU it exec'd, is passed over.
func TestThePIDIsQEMUs(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep to stand in for QEMU")
	}
	b, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	start := func(name string) *exec.Cmd {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, b, 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(path, "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	helper := start("spin-machine")
	qemu := start("qemu-system-x86_64")
	if got := qemuPID(os.Getpid()); got != qemu.Process.Pid {
		t.Errorf("the QEMU under this process is %d, want %d", got, qemu.Process.Pid)
	}
	if got := qemuPID(qemu.Process.Pid); got != qemu.Process.Pid {
		t.Errorf("an exec'd QEMU is %d, want itself, %d", got, qemu.Process.Pid)
	}
	if got := qemuPID(helper.Process.Pid); got != helper.Process.Pid {
		t.Errorf("a process with no QEMU is %d, want itself, %d", got, helper.Process.Pid)
	}
}

// QEMU's exec is bracketed between the last look that did not see it and the first that did, after
// what spin-machine did before it.
func TestQEMUsExecIsBracketed(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep to stand in for QEMU")
	}
	b, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	qemu := filepath.Join(t.TempDir(), "qemu-system-x86_64")
	if err := os.WriteFile(qemu, b, 0o700); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	cmd := exec.Command("sh", "-c", `sleep 0.05; exec "$0" 30`, qemu)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	mc := &Machine{cmd: cmd, done: make(chan struct{})}
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-mc.done })
	go func() { _ = cmd.Wait(); close(mc.done) }()
	got := mc.qemuExec(t0, t0.Add(10*time.Second))
	if !got.seen || got.lo < 50 || got.hi < got.lo || got.hi-got.lo > 5 {
		t.Errorf("QEMU's exec, 50 ms after its parent's start, bracketed %+v", got)
	}
	_ = cmd.Process.Kill()
	<-mc.done
	if got := mc.qemuExec(time.Now(), time.Now().Add(time.Second)); got.seen {
		t.Errorf("a spin-machine that exited is seen to exec QEMU: %+v", got)
	}
}
