// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// start runs script as a machine would run: its own process, its stderr kept.
func start(t *testing.T, script string) *Machine {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Stop ends the process group
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	mc := &Machine{cmd: cmd, done: make(chan struct{}), Stderr: stderr}
	go func() { _ = cmd.Wait(); close(mc.done) }()
	t.Cleanup(mc.Stop)
	return mc
}

// A machine that has exited says why at once: QEMU refusing it on this host refuses the tree, and
// anything else loses the one measurement. A running one says nothing.
func TestAMachineThatExitedSaysWhy(t *testing.T) {
	refused := start(t, `echo "qemu-system-x86_64: warning: host doesn't support requested feature: CPUID.la57" >&2
echo "qemu-system-x86_64: Host doesn't support requested features" >&2; exit 1`)
	<-refused.Done()
	var r *RefusedError
	if err := refused.Exited(); !errors.As(err, &r) || !strings.HasSuffix(r.Why, "qemu-system-x86_64: Host doesn't support requested features") {
		t.Errorf("a machine QEMU refused: %v, want the tree refused with QEMU's last line", err)
	}

	crashed := start(t, `echo "qemu-system-x86_64: -device vhost-vsock-pci: guest-cid in use" >&2; exit 1`)
	<-crashed.Done()
	var miss *MissError
	if err := crashed.Exited(); !errors.As(err, &miss) || !strings.Contains(miss.Why, "guest-cid in use") {
		t.Errorf("a machine that exited otherwise: %v, want the measurement lost, saying why", err)
	}

	running := start(t, `sleep 30`)
	if err := running.Exited(); err != nil {
		t.Errorf("a running machine: %v, want nothing", err)
	}
}
