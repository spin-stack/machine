// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// shell boots a machine of spin's shape into /bin/sh with /proc mounted, on an overlay of the
// release's base image, and returns it attached and with its console. With systemd it boots the
// image's own init instead, to its console's login prompt.
func shell(t *testing.T, r *recorder, systemd bool) (*machine.QEMU, *guest) {
	t.Helper()
	overlay := filepath.Join(t.TempDir(), "root.qcow2")
	if b, err := exec.Command(filepath.Join(r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", //nolint:gosec // the release's qemu-img
		"-b", filepath.Join(r.out, "image", "rootfs.qcow2"), "-F", "qcow2", overlay).CombinedOutput(); err != nil {
		t.Fatalf("an overlay of the base image: %v: %s", err, b)
	}
	sock, cid, mac := r.next()
	flags := []string{"--qmp", sock, "--vsock-cid", fmt.Sprint(cid), "--disk", overlay, "--disk-format", "qcow2"}
	if systemd {
		args, start := machineArgs(t, r.cli, r.out, slices.Concat(spinsShape, flags))
		g := startGuest(t, append(args, spinsNIC(mac)...))
		start(t)
		g.await(t, regexp.MustCompile(`login: `), 60*time.Second)
		return attachTo(t, sock, g.said), g
	}
	args, start := machineArgs(t, r.cli, r.out, slices.Concat(spinsShape, flags, []string{"--init", "/bin/sh"}))
	g := startGuest(t, append(args, spinsNIC(mac)...))
	start(t)
	g.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	return attachTo(t, sock, g.said), g
}

func newRecorder(t *testing.T) *recorder {
	out := releaseDir(t)
	r := &recorder{t: t, out: out, cli: filepath.Join(out, "bin", "spin-machine"), cid: 3600000 + os.Getpid()%100000*10}
	r.dir, _ = os.MkdirTemp("/tmp", "vrb")
	t.Cleanup(func() { _ = os.RemoveAll(r.dir) })
	return r
}

// What QEMU says of a machine's end (machine.QEMU.End), which spin's runner explains an exit by:
// a guest that powers itself off, one systemd powers off when Shutdown presses the power button,
// and a panic, which panic=1 reboots and -no-reboot makes an end, a guest's reset (QEMU
// 11.2.0-rc0, 2026-11-21).
//
//	SPIN_RECORD_TEST=1   it starts machines (task boot:record)
func TestTheMachineSaysHowItEnded(t *testing.T) {
	if os.Getenv("SPIN_RECORD_TEST") == "" {
		t.Skip("set SPIN_RECORD_TEST=1: this starts machines")
	}
	r := newRecorder(t)
	for _, tc := range []struct {
		name    string
		systemd bool
		end     func(t *testing.T, m *machine.QEMU, g *guest)
		want    machine.End
	}{
		{"powered off", false, func(t *testing.T, _ *machine.QEMU, g *guest) { g.type_(t, "poweroff -f") },
			machine.End{Guest: true, Reason: machine.GuestShutdown}},
		{"shut down", true, func(t *testing.T, m *machine.QEMU, _ *guest) {
			if err := m.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		}, machine.End{Guest: true, Reason: machine.GuestShutdown}},
		{"panicked", false, func(t *testing.T, _ *machine.QEMU, g *guest) { g.type_(t, "echo c > /proc/sysrq-trigger") },
			machine.End{Guest: true, Reason: machine.GuestReset}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, g := shell(t, r, tc.systemd)
			tc.end(t, m, g)
			select {
			case <-m.Done():
			case <-time.After(30 * time.Second):
				t.Fatalf("QEMU did not end:\n%s", tail([]byte(g.said()), 2000))
			}
			end, said := m.End()
			t.Logf("End: %+v, %v", end, said)
			if !said || end != tc.want {
				t.Errorf("End = %+v, %v, want %+v", end, said, tc.want)
			}
		})
	}
}

// A machine checkpointed in place, as spin's runner holds one for a suspension: the checkpoint says
// what it cost, the machine is left postmigrate, Hold takes its disks back without running it,
// and Continue runs the guest, which answers. Before that the guest reports its memory through
// the balloon once GuestMemory has asked it to.
//
//	SPIN_RECORD_TEST=1   it starts machines (task boot:record)
func TestACheckpointedMachineIsHeldThenContinued(t *testing.T) {
	if os.Getenv("SPIN_RECORD_TEST") == "" {
		t.Skip("set SPIN_RECORD_TEST=1: this starts machines")
	}
	r := newRecorder(t)
	m, g := shell(t, r, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var mem machine.GuestMemory
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		var err error
		if mem, err = m.GuestMemory(ctx); err == nil {
			break
		}
		if !errors.Is(err, machine.ErrNoGuestStats) || time.Now().After(deadline) {
			t.Fatalf("GuestMemory: %v", err)
		}
	}
	t.Logf("GuestMemory: %+v", mem)
	if mem.Total <= 0 || mem.Total > 128 || mem.Available <= 0 || mem.Available > mem.Total || time.Since(mem.At) > time.Minute {
		t.Errorf("GuestMemory = %+v, want a recent report of at most the 128 MiB the guest has", mem)
	}

	state, err := os.Create(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	_, cost, err := m.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, g.said())
	}
	t.Logf("Cost: %+v", cost)
	if cost.Paused <= 0 || cost.Total < cost.Paused || cost.RAM <= 0 {
		t.Errorf("Cost = %+v, want a pause within the save and RAM written", cost)
	}
	for _, step := range []struct {
		do   func(context.Context) error
		want machine.Status
	}{
		{func(context.Context) error { return nil }, machine.Postmigrate},
		{m.Hold, machine.Postmigrate},
		{m.Continue, machine.Running},
	} {
		if err := step.do(ctx); err != nil {
			t.Fatal(err)
		}
		if st, err := m.Status(ctx); err != nil || st != step.want {
			t.Fatalf("Status = %q, %v, want %q", st, err, step.want)
		}
	}
	g.type_(t, "echo UP-$((2+2))")
	g.await(t, regexp.MustCompile(`UP-4\b`), 30*time.Second)
}
