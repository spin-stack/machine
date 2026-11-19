// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/spin-machine/boot"
)

// Whether a QEMU started ahead of the workspace moves either of the goal's numbers. QEMU's own
// start - exec, the machine's creation, the reset that copies the kernel into memory - is ~30 ms
// nested (boot:firmware-stages), and a cold boot and a restore both pay it. Every machine starts
// held - its root disk's place a raw node over an empty placeholder, no vCPU run - and a cold
// boot is the one whose boot follows at once. One started with --held or --restore pays it before
// anyone asks, and `spin-machine start` or `restore` is the request: it binds the workspace's
// overlay to its place (machine.VM.Boot, machine.VM.Resume) and runs the machine, or loads a
// state into it.
//
// Two rows booted and two restored, each pair interleaved, all to the first SSH login over vsock
// as boot:ssh-first logs in, the image as boot:ssh-first's "as this branch" row builds it:
//
//   - cold boot: today's, from the moment before QEMU is exec'd;
//   - prestarted boot: QEMU launched and settled first - its monitor answering a command, which
//     only the main loop does once the machine is created - and timed from the moment before
//     `spin-machine start` is exec'd;
//   - restored and restored into prestarted: the same, a state saved by a cold boot and evicted
//     from the page cache, loaded with `restore` into a QEMU exec'd for it, or into one started
//     ahead with --restore.
//
// Every boot runs before any cold boot that makes a state, and every state is made before any
// restore, for boot:restore-first's reason: a cold boot after a restored guest had run measured
// that and not the machine.
//
//	SPIN_PRESTART=1  run at all
//	REPS=<n>         of each row (default 10)
//	CPUS=<n>         vCPUs (default 2)
func TestPrestart(t *testing.T) {
	if os.Getenv("SPIN_PRESTART") == "" {
		t.Skip("set SPIN_PRESTART=1: boots, saves and restores VMs with a vsock device, some started ahead")
	}
	out := releaseDir(t)
	if !canSudo() {
		t.Skip("this writes a key into the boot's own overlay through qemu-nbd, which needs sudo")
	}
	if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
		t.Skip("no /dev/vhost-vsock: run task vsock")
	}
	reps := envInt(t, "REPS", 10)
	rows, signer := firstLoginRows(t)
	v := rows[len(rows)-1]
	// This tree's spin-machine, built here: --held and start are this tree's, and the lab
	// boots a published release whose own spin-machine has neither.
	spinMachine := filepath.Join(t.TempDir(), "spin-machine")
	if b, err := exec.Command("go", "build", "-o", spinMachine, "../cmd/spin-machine").CombinedOutput(); err != nil {
		t.Fatalf("building this tree's spin-machine: %v\n%s", err, b)
	}

	newRun := func(label string) *checkpointRun {
		socks, err := os.MkdirTemp("/tmp", "ps")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(socks) })
		return &checkpointRun{cli: spinMachine, v: v, label: label, out: out, signer: signer, dir: t.TempDir(), socks: socks}
	}
	type sample struct {
		label          string
		running, login time.Duration // running: start or restore returned; zero for a cold boot
	}
	var samples []sample

	const cold, prestarted = "cold boot", "prestarted boot"
	for i := range reps {
		for _, label := range []string{cold, prestarted} {
			r := newRun(label)
			mustRun(t, filepath.Join(out, "bin", "qemu-img"), "create", "-f", "qcow2",
				"-F", "qcow2", "-b", filepath.Join(out, "image", "rootfs.qcow2"), r.overlay())
			editOverlay(t, r.overlay(), v)
			r.cid = uint32(100000 + rand.IntN(1<<20))
			qmp := filepath.Join(r.socks, "m.qmp")
			s := sample{label: label}
			if label == cold {
				t0 := time.Now()
				_, stop := r.launch(t, qmp)
				s.login = r.loggedIn(t, t0, label)
				stop()
			} else {
				_, stop := r.launch(t, qmp, "--held")
				settled(t, qmp, "prelaunch")
				t0 := time.Now()
				mustRun(t, spinMachine, "start", "--release", out, "--qmp", qmp, "--disk", r.overlay())
				s.running = time.Since(t0)
				s.login = r.loggedIn(t, t0, label)
				stop()
			}
			t.Logf("%s, repetition %d: running %v, login %v", label, i+1, s.running.Round(time.Millisecond), s.login.Round(time.Millisecond))
			samples = append(samples, s)
		}
	}

	const restored, restoredPrestarted = "restored", "restored into prestarted"
	var states []*checkpointRun
	for range reps {
		for _, label := range []string{restored, restoredPrestarted} {
			r := newRun(label)
			r.cold(t)
			states = append(states, r)
		}
	}
	for i, r := range states {
		s := sample{label: r.label}
		if r.label == restored {
			r.restore(t)
			s.running, s.login = r.running, r.login
		} else {
			evict(t, r.state())
			qmp := filepath.Join(r.socks, "prestarted.qmp")
			_, stop := r.launch(t, qmp, "--restore")
			settled(t, qmp, "inmigrate")
			t0 := time.Now()
			mustRun(t, spinMachine, "restore", "--release", out, "--qmp", qmp, "--disk", r.overlay(), "--from", r.state())
			s.running = time.Since(t0)
			s.login = r.loggedIn(t, t0, r.label)
			stop()
		}
		t.Logf("%s, repetition %d: running %v, login %v", r.label, i/2+1, s.running.Round(time.Millisecond), s.login.Round(time.Millisecond))
		samples = append(samples, s)
	}

	ms := func(label string, f func(sample) time.Duration) string {
		var ds []time.Duration
		for _, s := range samples {
			if s.label == label && f(s) != 0 {
				ds = append(ds, f(s))
			}
		}
		p50, ok := boot.Percentile(ds, .5)
		if !ok {
			return fmt.Sprintf("%13s", "-")
		}
		p95, _ := boot.Percentile(ds, .95)
		return fmt.Sprintf("%6d/%-6d", p50.Milliseconds(), p95.Milliseconds())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nms p50/p95 over %d of each, %s vCPUs, from the moment before QEMU is exec'd or, prestarted, before `start` is\n\n", reps, v.cpus)
	fmt.Fprintf(&b, "  %-26s %13s %13s\n", "", "RUNNING", "FIRST LOGIN")
	for _, label := range []string{cold, prestarted, restored, restoredPrestarted} {
		fmt.Fprintf(&b, "  %-26s %s %s\n", label,
			ms(label, func(s sample) time.Duration { return s.running }),
			ms(label, func(s sample) time.Duration { return s.login }))
	}
	t.Log(b.String())
}

// settled returns once the machine on qmp has been created and is waiting in state: its monitor
// has answered query-status, which the main loop runs, and the main loop runs only once QEMU's
// start is over. The greeting alone says less - QMP's I/O thread can send it earlier.
func settled(t *testing.T, qmp, state string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var c net.Conn
	for {
		var err error
		if c, err = net.Dial("unix", qmp); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the prestarted QEMU never opened its QMP socket %s", qmp)
		}
		time.Sleep(time.Millisecond)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(deadline)
	in := bufio.NewScanner(c)
	var status struct {
		Return struct {
			Status string `json:"status"`
		} `json:"return"`
	}
	in.Scan() // the greeting
	for _, cmd := range []string{"qmp_capabilities", "query-status"} {
		if _, err := fmt.Fprintf(c, `{"execute": %q}`+"\n", cmd); err != nil {
			t.Fatalf("asking the prestarted QEMU %s: %v", cmd, err)
		}
		for in.Scan() && strings.Contains(in.Text(), `"event"`) {
		}
	}
	if err := json.Unmarshal(in.Bytes(), &status); err != nil || status.Return.Status != state {
		t.Fatalf("the prestarted QEMU answered %q (%v), want status %s", in.Text(), err, state)
	}
	// What a pool's machine would have had: a moment idle after its start, not the instant.
	time.Sleep(100 * time.Millisecond)
}
