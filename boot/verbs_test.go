// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// shell boots a machine of spin's shape into /bin/sh with /proc mounted, on an overlay of the
// release's base image. With systemd it boots the image's own init instead, to its console's
// login prompt.
func shell(t *testing.T, r *recorder, systemd bool) *guest {
	t.Helper()
	init := "/bin/sh"
	if systemd {
		init = ""
	}
	s, mac := r.next(t, init)
	g := startGuest(t, s, mac, nil)
	g.boot(t, r.overlay(t, t.TempDir()), r.base())
	if systemd {
		g.await(t, regexp.MustCompile(`login: `), 60*time.Second)
		return g
	}
	g.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	return g
}

// What QEMU says of a machine's end (machine.QEMU.End), which spin's runner explains an exit by:
// a guest that powers itself off, one systemd powers off when Shutdown presses the power button,
// and a panic, which panic=1 reboots and -no-reboot makes an end, a guest's reset (QEMU
// 11.2.0-rc0, 2026-11-21). The process's Exit carries the same End, and QEMU exits 0 on each.
//
//	SPIN_RECORD_TEST=1   it starts machines (task boot:record)
func TestTheMachineSaysHowItEnded(t *testing.T) {
	if os.Getenv("SPIN_RECORD_TEST") == "" {
		t.Skip("set SPIN_RECORD_TEST=1: this starts machines")
	}
	r := newRecorder(t, 3600000)
	for _, tc := range []struct {
		name    string
		systemd bool
		end     func(t *testing.T, g *guest)
		want    machine.End
	}{
		{"powered off", false, func(t *testing.T, g *guest) { g.type_(t, "poweroff -f") },
			machine.End{Guest: true, Reason: machine.GuestShutdown}},
		{"shut down", true, func(t *testing.T, g *guest) {
			if err := g.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		}, machine.End{Guest: true, Reason: machine.GuestShutdown}},
		{"panicked", false, func(t *testing.T, g *guest) { g.type_(t, "echo c > /proc/sysrq-trigger") },
			machine.End{Guest: true, Reason: machine.GuestReset}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := shell(t, r, tc.systemd)
			tc.end(t, g)
			select {
			case <-g.Exited():
			case <-time.After(30 * time.Second):
				t.Fatalf("QEMU did not end:\n%s", tail([]byte(g.said()), 2000))
			}
			end, said := g.End()
			t.Logf("End: %+v, %v", end, said)
			if !said || end != tc.want {
				t.Errorf("End = %+v, %v, want %+v", end, said, tc.want)
			}
			exit := g.Wait()
			t.Logf("Exit: %s", exit)
			if exit.End == nil || *exit.End != tc.want || exit.State.ExitCode() != 0 {
				t.Errorf("Exit = %s, want an exit status of 0 and End %+v", exit, tc.want)
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
	g := shell(t, newRecorder(t, 3700000), false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var mem machine.GuestMemory
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		var err error
		if mem, err = g.GuestMemory(ctx); err == nil {
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
	_, cost, err := g.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
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
		{g.Hold, machine.Postmigrate},
		{g.Continue, machine.Running},
	} {
		if err := step.do(ctx); err != nil {
			t.Fatal(err)
		}
		if st, err := g.Status(ctx); err != nil || st != step.want {
			t.Fatalf("Status = %q, %v, want %q", st, err, step.want)
		}
	}
	g.type_(t, "echo UP-$((2+2))")
	g.await(t, regexp.MustCompile(`UP-4\b`), 30*time.Second)
}
