// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// What a held machine says of itself, asked of the pinned QEMU: the verbs spin's runner drives a
// machine by, each sent to a machine of spin's kind (a vCPU and a memory ceiling, the balloon) and
// answered by QEMU, not by a fake that agrees with the definitions it was written from.
//
// The machine is held at -S with a stub kernel, so no guest answers: what a guest makes of the power
// button or a report is boot:record's (boot/verbs_test.go).
func TestAHeldMachineAnswersTheRunnersVerbs(t *testing.T) {
	qemu, firmware := qemuTCG(t)
	socket := qmpSocket(t)
	spec := Spec{
		QEMU:     qemu,
		Kernel:   bzImageStub(t),
		Firmware: firmware,
		BootCPUs: 1,
		MaxCPUs:  4,
		Memory:   Memory{SizeMB: 512, MaxMB: 1536},
		Cmdline:  DefaultCmdline(),
		Monitors: []Monitor{{Socket: socket}, {Socket: socket + ".2"}},
	}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	args, _, err = tcgOnly(args)
	if err != nil {
		t.Fatal(err)
	}
	vm := startQEMU(t, qemu, args)
	askStatus(t, socket, vm)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket + ".2", Net: "unix"})
	if err != nil {
		vm.fail(t, "dialling the second monitor", err)
	}
	m, err := connect(conn)
	if err != nil {
		vm.fail(t, "attaching", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if st, err := m.Status(ctx); err != nil || st != Prelaunch {
		t.Errorf("Status = %q, %v, want %q", st, err, Prelaunch)
	}
	if n, err := m.CPUs(ctx); err != nil || n != 1 {
		t.Errorf("CPUs = %d, %v, want the 1 it was started with", n, err)
	}
	// The runner plugs a workspace's cores before the boot.
	if err := m.SetCPUs(ctx, 3); err != nil {
		t.Fatalf("SetCPUs before the boot: %v", err)
	}
	if n, err := m.CPUs(ctx); err != nil || n != 3 {
		t.Errorf("CPUs = %d, %v, want 3 after SetCPUs", n, err)
	}
	size, err := m.Memory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if size.Now != 512 || size.Base != 512 || size.Ceiling != 1536 || size.Block < 1 {
		t.Errorf("Memory = %+v, want 512 MiB that may grow to 1536 in blocks of a MiB or more", size)
	}
	if err := m.SetMemory(ctx, 512+size.Block); err != nil {
		t.Errorf("SetMemory of one block more: %v", err)
	}
	// No guest has reported: the polling is on and the report empty.
	if _, err := m.GuestMemory(ctx); !errors.Is(err, ErrNoGuestStats) {
		t.Errorf("GuestMemory = %v, want ErrNoGuestStats from a machine whose guest never ran", err)
	}
	if err := m.Hold(ctx); err != nil {
		t.Errorf("Hold: %v", err)
	}
	// The button is pressed, and a held guest has nothing to answer it with.
	if err := m.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if _, ended := m.End(); ended {
		t.Error("End says the machine ended, and it is held")
	}
}
