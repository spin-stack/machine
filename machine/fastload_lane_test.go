// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAFastLoadNeedsNoUserfaultfdGrant is the lane of the userfaultfd device a fast load hands
// QEMU (spin-stack/spin#1711): on a host whose /dev/userfaultfd only root opens and whose
// vm.unprivileged_userfaultfd is 0, a QEMU run as nobody in a root of Spec.Files and /dev/null
// loads a state of more than fastAbove fast, with the device Resume opened as root and handed it.
// The guest runs before its state is all read, and keeps running. QEMU holds the device only
// while Resume runs: not while it is held for the load, and not once Resume has returned, with
// the state still loading (#789). Root, for the device, the root and the credentials, and KVM:
// task boot:fastload.
func TestAFastLoadNeedsNoUserfaultfdGrant(t *testing.T) {
	if os.Getenv("SPIN_FASTLOAD_TEST") != "1" {
		t.Skip("set SPIN_FASTLOAD_TEST=1, as root, to load a state fast into a QEMU run as nobody: task boot:fastload")
	}
	if os.Geteuid() != 0 {
		t.Fatal("opening /dev/userfaultfd and dropping QEMU to nobody need root: task boot:fastload runs this under sudo")
	}
	// The lane proves a fast load without the host's grant, so a host that grants it proves nothing.
	if fi, err := os.Stat(userfaultfdDevice); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s must be open to its owner alone: %v %v", userfaultfdDevice, fi.Mode(), err)
	}
	if b, err := os.ReadFile("/proc/sys/vm/unprivileged_userfaultfd"); err != nil || strings.TrimSpace(string(b)) != "0" {
		t.Fatalf("vm.unprivileged_userfaultfd must be 0: %q %v", b, err)
	}
	out := outputDir(t)
	rel, err := OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}
	root := newGuestRoot(t, out, base)
	// PID 1 fills 1700 MiB of a tmpfs, so the state carries more than fastAbove of data, and then
	// counts, so a line after the load is the guest running.
	root.write("/fastload.sh", "#!/bin/sh\nmount -t proc proc /proc\n"+
		"mount -t tmpfs -o size=1800m fill /tmp && head -c 1700M /dev/zero | tr '\\0' '\\377' > /tmp/f && echo FILLED\n"+
		"n=0; while :; do n=$((n+1)); echo TICK-$n; sleep 0.2; done\n")
	root.debugfs("sif /fastload.sh mode 0100755")

	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	spec.BootCPUs, spec.Memory = 1, Memory{BaseMB: 2048}
	spec.Places = []Place{{Serial: "root"}}
	c := DefaultCmdline()
	c.Root, c.Init = "/dev/vda", "/fastload.sh"
	spec.Cmdline = c
	jail := newJail(t, spec.Files())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	start := func(t *testing.T, s Spec) (*Process, *consoleText) {
		t.Helper()
		console := &consoleText{news: make(chan struct{}, 1)}
		p, err := Start(ctx, s, StartOptions{Console: console, Cmd: func(c *exec.Cmd) {
			c.Dir = "/"
			c.SysProcAttr = &syscall.SysProcAttr{Chroot: jail, Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Kill() })
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.Pid()))
		if err != nil || !regexp.MustCompile(`(?m)^Uid:\s+65534\s`).Match(b) {
			t.Fatalf("QEMU is not nobody (%v):\n%s", err, b)
		}
		return p, console
	}
	await := func(t *testing.T, p *Process, console *consoleText, want *regexp.Regexp) {
		t.Helper()
		for !want.MatchString(console.String()) {
			select {
			case <-console.news:
			case <-p.Exited():
				t.Fatalf("QEMU %s\n%s", p.Wait(), console.String())
			case <-ctx.Done():
				t.Fatalf("%s never came: %v\n%s", want, ctx.Err(), console.String())
			}
		}
	}
	disk := func(t *testing.T) Disk {
		t.Helper()
		f, err := os.OpenFile(root.path, os.O_RDWR, 0) // #nosec G304 -- the root this test made
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return Disk{Serial: "root", Images: []Image{{File: f, Format: "raw"}}}
	}

	p, console := start(t, spec)
	if err := p.Boot(ctx, disk(t)); err != nil {
		t.Fatal(err)
	}
	await(t, p, console, regexp.MustCompile(`TICK-1\b`))
	state, err := os.Create(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	saved, _, err := p.Checkpoint(ctx, state, SaveOptions{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v", err)
	}
	p.Kill()
	if data := written(state); data <= fastAbove {
		t.Fatalf("the state holds %d MiB of data, not over fastAbove's %d: it would load in full", data>>20, fastAbove>>20)
	}
	spec.Restore = true
	r, console := start(t, spec)
	here, err := r.Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := userfaultfdsHeld(t, r.Pid()); n != (heldUserfaultfds{}) {
		t.Errorf("QEMU held for the load holds %+v", n)
	}
	var phases []string
	load := LoadOptions{Phases: func(ph Phase) { phases = append(phases, fmt.Sprintf("%T", ph)) }}
	if err := r.Resume(ctx, state, saved, here, load, disk(t)); err != nil {
		t.Fatalf("resuming as nobody: %v", err)
	}
	if n := userfaultfdsHeld(t, r.Pid()); n.devices != 0 {
		t.Errorf("QEMU loading fast holds %+v once Resume has returned, want no device", n)
	}
	if err := r.Loaded(ctx); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if n := userfaultfdsLetGo(t, r.Pid()); n != (heldUserfaultfds{}) {
		t.Errorf("QEMU that has loaded holds %+v", n)
	}
	t.Logf("phases: %v", phases)
	if got := strings.Join(phases, " "); got != "machine.LoadStarted machine.GuestRunning machine.StateLoaded" {
		t.Errorf("phases %s: want the guest running before its state is loaded, a fast load", got)
	}
	// This console is the restored QEMU's alone: every line on it is the loaded guest's.
	tick := regexp.MustCompile(`TICK-(\d+)`)
	await(t, r, console, tick)
	var first int
	_, _ = fmt.Sscan(tick.FindStringSubmatch(console.String())[1], &first)
	await(t, r, console, regexp.MustCompile(fmt.Sprintf(`TICK-%d\b`, first+5)))
}

// heldUserfaultfds is what a process holds of userfaultfd: descriptors of the device, which make
// userfaultfds, and userfaultfds.
type heldUserfaultfds struct{ devices, userfaultfds int }

// userfaultfdsHeld is what process pid holds of userfaultfd, read from its descriptors.
func userfaultfdsHeld(t *testing.T, pid int) heldUserfaultfds {
	t.Helper()
	dev, err := os.Stat(userfaultfdDevice)
	if err != nil {
		t.Fatal(err)
	}
	dir := fmt.Sprintf("/proc/%d/fd", pid)
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
		if st, err := os.Stat(path); err == nil && os.SameFile(st, dev) {
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
