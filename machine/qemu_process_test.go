// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// heldTCG is a machine of the TCG build held for a boot, its stub kernel never run.
func heldTCG(t *testing.T) Spec {
	t.Helper()
	qemu, firmware := qemuTCG(t)
	return Spec{QEMU: qemu, Kernel: bzImageStub(t), Firmware: firmware, Accel: "tcg",
		BootCPUs: 1, Memory: Memory{BaseMB: 256}, Cmdline: DefaultCmdline()}
}

// What Start and Stop say of a real QEMU: a held machine is quit, one killed says so, and one QEMU
// refuses fails the start with QEMU's own words, at once.
func TestARealQEMUStartsAndEnds(t *testing.T) {
	spec := heldTCG(t)
	socket := qmpSocket(t)
	spec.Monitors = []Monitor{{Socket: socket}}
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := Start(c, spec, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if st, err := p.Status(c); err != nil || st != Prelaunch {
		t.Errorf("a started machine is %q, %v", st, err)
	}
	// The caller's monitor was bound before QEMU ran, and QEMU serves it.
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := connect(conn)
	if err != nil {
		t.Fatalf("the caller's monitor: %v", err)
	}
	defer func() { _ = other.Close() }()
	// Held, the guest cannot answer its button: Stop quits QEMU.
	e := p.Stop(c)
	if e.State.ExitCode() != 0 || e.End == nil || e.End.Reason != "host-qmp-quit" {
		t.Errorf("Stop of a held machine: %s", e)
	}
	t.Logf("stopped: %s", e)

	p, err = Start(c, spec, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e = p.Kill()
	if ws := e.State.Sys().(syscall.WaitStatus); !ws.Signaled() || ws.Signal() != syscall.SIGKILL || e.End != nil {
		t.Errorf("Kill: %s", e)
	}
	t.Logf("killed: %s", e)

	start := time.Now()
	_, err = Start(c, spec, StartOptions{Cmd: func(c *exec.Cmd) {
		c.Args = append(c.Args, "-device", "virtio-balloon-pci,no-such-property=on")
	}})
	if err == nil || !strings.Contains(err.Error(), "Property 'virtio-balloon-pci.no-such-property' not found") {
		t.Errorf("Start of a machine QEMU refuses: %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("Start took %v to see QEMU refuse", took)
	}
	t.Logf("refused: %v", err)
}
