// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
