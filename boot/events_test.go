// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
// A truncated state is refused by its length before QEMU reads it (#546). The machine loads fast a state of more
// than 1.5 GiB of data, so the guest has 2 GiB of base memory and fills 1.6 GiB of it for a
// second checkpoint, which the fast load reads. QEMU holds the userfaultfd device only while a
// fast load's Resume runs: not while it is held for a load, never in a load in full, and not once
// Resume has returned (#789).
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

	// next is a machine held for the checkpoints, all of one record: 2 GiB of base memory, which
	// the guest fills without plugging any.
	next := func(t *testing.T) (machine.Spec, string) {
		s, mac := r.next(t, "/bin/sh")
		s.Memory.BaseMB = 2048
		return s, mac
	}
	s, mac := next(t)
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
	t.Logf("saving: %s", saving)
	saving.inOrder(t, "MIGRATION setup", "MIGRATION active", "MIGRATION pre-switchover", "MIGRATION device", "MIGRATION completed")
	if err := a.Continue(ctx); err != nil {
		t.Fatalf("continuing: %v\n%s", err, a.said())
	}
	a.type_(t, `mount -t tmpfs -o size=1700m fill /mnt && head -c 1600M /dev/zero | tr '\0' '\377' > /mnt/f && echo FILLED-$((1+1))`)
	a.await(t, regexp.MustCompile(`FILLED-2\b`), 2*time.Minute)
	large := filepath.Join(dir, "large")
	if f, err = os.Create(large); err != nil {
		t.Fatal(err)
	}
	savedLarge, _, err := a.Checkpoint(ctx, f, machine.SaveOptions{}, func(context.Context) error { return nil })
	_ = f.Close()
	if err != nil {
		t.Fatalf("checkpointing the filled guest: %v\n%s", err, a.said())
	}
	a.Kill()

	// resume loads state into a fresh machine held for one, and returns its stream, its error and
	// the guest.
	resume := func(t *testing.T, state string, saved machine.Saved, told *timeline) (*stream, error, *guest) {
		t.Helper()
		spec, mac := next(t)
		spec.Restore = true
		s := &stream{}
		g := startGuest(t, spec, mac, s.event)
		here, err := g.Target(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if h := userfaultfdsHeld(t, g.Pid()); h != (heldUserfaultfds{}) {
			t.Errorf("QEMU held for the load holds %+v", h)
		}
		root := rootDisk(t, overlay, r.base())
		f, err := os.Open(state)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		load := machine.LoadOptions{Phases: func(p machine.Phase) {
			if _, ok := p.(machine.GuestRunning); ok {
				if st, err := g.Status(ctx); err != nil || st != machine.Running {
					t.Errorf("told GuestRunning with the machine %s (%v)", st, err)
				}
			}
			told.tell(p)
		}}
		if err := g.Resume(ctx, f, saved, here, load, root); err != nil {
			return s, err, g
		}
		if h := userfaultfdsHeld(t, g.Pid()); h.devices != 0 {
			t.Errorf("QEMU holds %+v once Resume has returned, want no device", h)
		}
		if err := g.Loaded(ctx); err != nil {
			return s, err, g
		}
		if h := userfaultfdsLetGo(t, g.Pid()); h != (heldUserfaultfds{}) {
			t.Errorf("QEMU that has loaded holds %+v", h)
		}
		return s, nil, g
	}

	t.Run("in full", func(t *testing.T) {
		told := &timeline{}
		s, err, g := resume(t, state, saved, told)
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
			t.Skip("this process cannot open /dev/userfaultfd to hand QEMU")
		}
		told := &timeline{}
		s, err, g := resume(t, large, savedLarge, told)
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
		_, err, g := resume(t, state, saved, &timeline{})
		if err != nil {
			t.Fatalf("resuming: %v\n%s", err, g.said())
		}
		s, err, h := resume(t, state, saved, &timeline{})
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
		s, err, g := resume(t, short, saved, &timeline{})
		if err == nil {
			t.Fatalf("a truncated state resumed\n%s", g.said())
		}
		// Its length is not the one the checkpoint recorded, which Resume refuses before QEMU
		// reads a byte: QEMU's own failed load would say only that it failed (#546).
		var r *machine.Refusal
		if !errors.As(err, &r) || !slices.ContainsFunc(r.Differences, func(d machine.Difference) bool { return d.Part == "state length" }) {
			t.Errorf("Resume = %v, want a *machine.Refusal of its state length", err)
		}
		if strings.Contains(s.String(), "MIGRATION setup") {
			t.Errorf("the truncated state was read: %s", s)
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

// userfaults is whether this process can open the userfaultfd device, which machine.Resume then
// hands QEMU for a fast load. On a bench runner, a throwaway host, the device is given to this
// user, as internal/lab's restore-first does; vm.unprivileged_userfaultfd stays 0.
func userfaults() bool {
	const dev = "/dev/userfaultfd"
	if unix.Access(dev, unix.R_OK|unix.W_OK) == nil {
		return true
	}
	return os.Getenv("GITHUB_ACTIONS") == "true" && exec.Command("sudo", "chown", strconv.Itoa(os.Getuid()), dev).Run() == nil
}

// heldUserfaultfds is what a process holds of userfaultfd: descriptors of the device, which make
// userfaultfds, and userfaultfds.
type heldUserfaultfds struct{ devices, userfaultfds int }

// userfaultfdsHeld is what process pid holds of userfaultfd, read from its descriptors. A host with
// no device has no descriptor of it to hold.
func userfaultfdsHeld(t *testing.T, pid int) heldUserfaultfds {
	t.Helper()
	dev, devErr := os.Stat("/dev/userfaultfd")
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	fds, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var held heldUserfaultfds
	for _, fd := range fds {
		path := filepath.Join(dir, fd.Name())
		if to, err := os.Readlink(path); err == nil && to == "anon_inode:[userfaultfd]" {
			held.userfaultfds++
		}
		if st, err := os.Stat(path); err == nil && devErr == nil && os.SameFile(st, dev) {
			held.devices++
		}
	}
	return held
}

// userfaultfdsLetGo is what process pid, a QEMU whose load has completed, still holds of
// userfaultfd once it has let go of it or 5 s on. QEMU says completed from the thread that read
// the state, and closes postcopy's userfaultfd after it, from a BH on its main loop (QEMU
// 11.2.0-rc1, postcopy_incoming_complete_bh).
func userfaultfdsLetGo(t *testing.T, pid int) heldUserfaultfds {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h := userfaultfdsHeld(t, pid)
		if h == (heldUserfaultfds{}) || time.Now().After(deadline) {
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
}
