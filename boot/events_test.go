// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// The events a checkpoint's waits are made of, on the release's QEMU: machine.QEMU waits on
// MIGRATION and never asks query-migrate again, so a QEMU that stopped sending one would leave a
// save or a resume waiting until its context ended. Each side's stream is held to the order the
// machine relies on: the save stops at pre-switchover and completes after it, a full load runs
// the guest (RESUME) before it reports completed, and a fast load passes postcopy-device to
// postcopy-active and runs the guest after it, where Resume returns (bench, QEMU 11.2.0-rc0,
// lab 44898418357, 2026-11-21). A resume tells GuestRunning with QEMU's run state running (#465).
// A load of a truncated state ends the destination's QEMU.
//
//	SPIN_RECORD_TEST=1   it starts machines (task boot:record)
func TestACheckpointIsWaitedOnByItsEvents(t *testing.T) {
	if os.Getenv("SPIN_RECORD_TEST") == "" {
		t.Skip("set SPIN_RECORD_TEST=1: this starts machines")
	}
	r := newRecorder(t, 3500000)
	dir := t.TempDir()
	overlay := r.overlay(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	s, mac := r.next(t, "/bin/sh")
	saving := &stream{}
	a := startGuest(t, s, mac, saving.event)
	a.boot(t, overlay, r.base())
	// The shell may not be reading the console yet when the first line arrives, blanks and all.
	for i := 0; !regexp.MustCompile(`UP-2\b`).MatchString(a.said()); i++ {
		if i == 30 {
			t.Fatalf("the guest's shell never answered:\n%s", tail([]byte(a.said()), 2000))
		}
		a.type_(t, "echo UP-$((1+1))")
		time.Sleep(time.Second)
	}
	state := filepath.Join(dir, "state")
	f, err := os.Create(state)
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := a.Checkpoint(ctx, f, machine.SaveOptions{}, func(context.Context) error { return nil })
	_ = f.Close()
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, a.said())
	}
	a.Kill()
	t.Logf("saving: %s", saving)
	saving.inOrder(t, "MIGRATION setup", "MIGRATION active", "MIGRATION pre-switchover", "MIGRATION device", "MIGRATION completed")

	// resume loads state into a fresh machine held for one, and returns its stream, its error and
	// the guest.
	resume := func(t *testing.T, state string, load machine.LoadOptions, told *timeline) (*stream, error, *guest) {
		t.Helper()
		spec, mac := r.next(t, "/bin/sh")
		spec.Restore = true
		s := &stream{}
		g := startGuest(t, spec, mac, s.event)
		here, err := g.Target(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root := rootDisk(t, overlay, r.base())
		f, err := os.Open(state)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		load.Phases = func(p machine.Phase) {
			if _, ok := p.(machine.GuestRunning); ok {
				if st, err := g.Status(ctx); err != nil || st != machine.Running {
					t.Errorf("told GuestRunning with the machine %s (%v)", st, err)
				}
			}
			told.tell(p)
		}
		err = g.Resume(ctx, f, saved, here, load, root)
		if err == nil && load.Fast {
			err = g.Loaded(ctx)
		}
		return s, err, g
	}

	t.Run("in full", func(t *testing.T) {
		told := &timeline{}
		s, err, g := resume(t, state, machine.LoadOptions{}, told)
		if err != nil {
			t.Fatalf("resuming: %v\n%s", err, g.said())
		}
		t.Logf("loading: %s", s)
		t.Logf("phases:\n%s", told)
		told.inOrder(t, "LoadStarted", "StateLoaded", "GuestRunning")
		s.inOrder(t, "MIGRATION setup", "MIGRATION active", "RESUME", "MIGRATION completed")
		g.type_(t, "echo RESUMED-$((2+3))")
		g.await(t, regexp.MustCompile(`RESUMED-5\b`), 30*time.Second)
	})

	t.Run("fast", func(t *testing.T) {
		if !userfaults() {
			t.Skip("QEMU cannot open a userfaultfd here: /dev/userfaultfd is not readable and writable, and vm.unprivileged_userfaultfd is 0")
		}
		told := &timeline{}
		s, err, g := resume(t, state, machine.LoadOptions{Fast: true}, told)
		if err != nil {
			t.Fatalf("resuming: %v\n%s", err, g.said())
		}
		t.Logf("loading: %s", s)
		t.Logf("phases:\n%s", told)
		told.inOrder(t, "LoadStarted", "GuestRunning", "StateLoaded")
		s.inOrder(t, "MIGRATION setup", "MIGRATION active", "MIGRATION postcopy-device", "MIGRATION postcopy-active", "RESUME", "MIGRATION completed")
		g.type_(t, "echo RESUMED-$((2+3))")
		g.await(t, regexp.MustCompile(`RESUMED-5\b`), 30*time.Second)
	})

	// QEMU takes a loaded machine's disks where it runs the guest, and when another process holds
	// an image's lock it leaves the guest stopped and says so only on its stderr: the load
	// completed, no RESUME came, and the guest never ran (#459).
	t.Run("a disk another machine holds", func(t *testing.T) {
		_, err, g := resume(t, state, machine.LoadOptions{}, &timeline{})
		if err != nil {
			t.Fatalf("resuming: %v\n%s", err, g.said())
		}
		s, err, h := resume(t, state, machine.LoadOptions{}, &timeline{})
		if err == nil {
			st, _ := h.Status(ctx)
			t.Fatalf("a resume onto a disk a running machine holds returned, the machine %s\nloading: %s\n%s", st, s, h.said())
		}
		if !strings.Contains(err.Error(), `"write" lock`) {
			t.Errorf("Resume = %v, want QEMU's refusal of the lock", err)
		}
		if strings.Contains(s.String(), "MIGRATION setup") {
			t.Errorf("the state was read: %s", s)
		}
	})

	t.Run("a truncated state", func(t *testing.T) {
		// The state is sparse, as long as the RAM's ceilings, its device state at the end.
		short := filepath.Join(dir, "short")
		if b, err := exec.Command("cp", "--sparse=always", state, short).CombinedOutput(); err != nil {
			t.Fatalf("copying the state: %v: %s", err, b)
		}
		st, err := os.Stat(short)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(short, st.Size()/2); err != nil {
			t.Fatal(err)
		}
		s, err, g := resume(t, short, machine.LoadOptions{}, &timeline{})
		if err == nil {
			t.Fatalf("a truncated state resumed\n%s", g.said())
		}
		select {
		case <-g.Exited():
		case <-time.After(10 * time.Second):
			t.Errorf("QEMU still runs 10 s after a failed load")
		}
		t.Logf("loading: %s\nResume: %v\nQEMU said: %s", s, err, tail([]byte(g.said()), 600))
	})
}

// stream is the events one monitor saw, each as its name and, for MIGRATION, its state.
type stream struct {
	mu     sync.Mutex
	events []string
}

func (s *stream) event(e qmp.Event) {
	name := e.Name
	if name == "MIGRATION" {
		var d struct{ Status string }
		_ = json.Unmarshal(e.Data, &d)
		name += " " + d.Status
	}
	s.mu.Lock()
	s.events = append(s.events, name)
	s.mu.Unlock()
}

func (s *stream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.events, ", ")
}

// inOrder is want being in the stream in that order, other events between them allowed.
func (s *stream) inOrder(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	i := 0
	for _, e := range s.events {
		if i < len(want) && e == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Errorf("events %v, want %v in that order: %q never came where it should", s.events, want, want[i])
	}
}

// userfaults is whether a QEMU run as this user can open the userfaultfd a fast load needs. On a
// bench runner, a throwaway host, it is turned on, as internal/lab's restore-first does.
func userfaults() bool {
	if unix.Access("/dev/userfaultfd", unix.R_OK|unix.W_OK) == nil {
		return true
	}
	const knob = "/proc/sys/vm/unprivileged_userfaultfd"
	if b, err := os.ReadFile(knob); err == nil && strings.TrimSpace(string(b)) == "1" {
		return true
	}
	return os.Getenv("GITHUB_ACTIONS") == "true" && exec.Command("sudo", "sysctl", "-q", "-w", "vm.unprivileged_userfaultfd=1").Run() == nil
}
