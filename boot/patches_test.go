// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// What the guest of spin's shape sees of each patch the machine carries in QEMU and qboot that
// only a guest can see, one boot for all of them. Each case names its patch: a patch dropped or
// rebased into doing nothing at a bump builds and boots, and fails here instead.
//
//	SPIN_PATCHES_TEST=1   it starts machines (task boot:patches)
func TestTheGuestSeesWhatEachPatchGivesIt(t *testing.T) {
	if os.Getenv("SPIN_PATCHES_TEST") == "" {
		t.Skip("set SPIN_PATCHES_TEST=1: this starts machines")
	}
	g := shell(t, newRecorder(t, 3800000), false)
	g.type_(t, "mount -t sysfs sysfs /sys")
	for _, c := range []struct {
		patch, check string
		// ask is typed on the console. It prints the answer between NAME- and -END, and the
		// shell's echo of the line itself does not match, since it shows the command.
		name, ask string
		want      func(answer string) bool
	}{
		{
			// The controller tells the guest the 16 targets the machine gives disks at (max_target
			// 15), and the guest's SCSI host refuses a scan of a target past them
			// (scsi_scan_host_selected). Without the patch's config it takes 255.
			"qemu/patches/0002 (virtio-scsi max_target)", "a scan of target 15 taken and of 16 refused",
			"SCAN", `h=$(ls -d /sys/class/scsi_host/host*); echo 0 15 0 > $h/scan && printf 15taken; { echo 0 16 0 > $h/scan; } 2>/dev/null || printf 16refused`,
			func(a string) bool { return a == "15taken16refused" },
		},
		{
			// Linux makes the MTRRs' state its own only when the firmware enabled them: stock qboot
			// leaves them off, and /proc/mtrr is then not there to read. With the patch the PCI hole
			// is uncachable in variable ranges.
			"qemu/qboot/mtrr.patch", "/proc/mtrr has an uncachable range",
			"MTRR", `grep -c uncachable /proc/mtrr`,
			func(a string) bool { n, err := strconv.Atoi(a); return err == nil && n > 0 },
		},
	} {
		g.type_(t, "echo "+c.name+"-$({ "+c.ask+"; } 2>&1 | tr -d '\\n')-END")
		re := regexp.MustCompile(c.name + `-([^$\r\n]*)-END`)
		g.await(t, re, 30*time.Second)
		answer := re.FindStringSubmatch(g.said())[1]
		if !c.want(answer) {
			t.Errorf("%s: want %s, the guest said %q", c.patch, c.check, answer)
		} else {
			t.Logf("%s: %s (%s)", c.patch, c.check, answer)
		}
	}
}

// A guest restored from a checkpoint keeps its TSC: qemu/patches/0005, #452 and #472. Without it,
// KVM takes the checkpoint's TSC for an attempt to keep the vCPUs in sync with the TSC the new
// machine's vCPU has counted since its creation, whenever the two are within a second, and the
// guest's TSC goes back. Its clock then stands still until the new count catches up, and so does
// every timer it armed before the save: a guest saved with a timer due was asleep for ~450 ms
// after its restore, and nothing failed.
//
// The guest runs a shell loop that prints its uptime every 10 ms, so a timer is always due. The
// checkpoint's machine is started ahead and let go when its vCPU is younger than the checkpoint's
// TSC by restoredLag, inside KVM's second, wherever the host's own timing put the save. Then no
// two of the restored guest's lines may be further apart than restoredGap.
//
//	SPIN_PATCHES_TEST=1   it starts machines (task boot:patches)
func TestARestoredGuestKeepsItsClock(t *testing.T) {
	if os.Getenv("SPIN_PATCHES_TEST") == "" {
		t.Skip("set SPIN_PATCHES_TEST=1: this starts machines")
	}
	// restoredLag is what KVM would take the restored guest's TSC back by: well inside its
	// second, and long enough that the stall reads far past restoredGap.
	const restoredLag = 650 * time.Millisecond
	// restoredGap is the most two of the restored guest's lines may be apart. The loop prints
	// every ~10 ms, and a nested host's vCPU misses a few of them.
	const restoredGap = 200 * time.Millisecond

	r := newRecorder(t, 3900000)
	dir := t.TempDir()
	overlay := r.overlay(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s, mac := r.next(t, "/bin/sh")
	started := time.Now()
	src := startGuest(t, s, mac, nil)
	src.boot(t, overlay, r.base())
	src.type_(t, `mount -t proc proc /proc; i=0; while :; do i=$((i+1)); read u x < /proc/uptime; echo "TICK $i $u"; sleep 0.01; done`)
	src.await(t, tickLine, 60*time.Second)
	// A TSC well past what the machine resumed into will have counted when it is let go.
	time.Sleep(time.Until(started.Add(1500 * time.Millisecond)))

	state, err := os.Create(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	saving := &timeline{}
	saved, _, err := src.Checkpoint(ctx, state, machine.SaveOptions{Phases: saving.tell}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, src.said())
	}
	src.Kill()
	stopped := saving.at(t, "GuestStopped")
	// The guest's TSC at the save, near enough: it counts from the vCPU's creation, a few ms
	// after QEMU's start, and goes on counting while the machine is held.
	tsc := stopped.Sub(started)

	s = r.spec(t, "/bin/sh")
	s.Restore = true
	here, err := machine.ReadTarget(ctx, s, machine.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mac = s.NICs[0].MAC
	s.NICs = nil
	made := time.Now()
	dst := startGuest(t, s, mac, nil)
	time.Sleep(time.Until(made.Add(tsc - restoredLag)))
	lag := tsc - time.Since(made)
	if lag < 300*time.Millisecond || lag > 900*time.Millisecond {
		t.Fatalf("the checkpoint's TSC is %v ahead of the vCPU it resumes into, outside the 300-900 ms this check reads a stall in (TSC %v)", lag, tsc)
	}
	loading := &timeline{}
	if err := dst.Resume(ctx, state, saved, here, machine.LoadOptions{Phases: loading.tell}, rootDisk(t, overlay, r.base())); err != nil {
		t.Fatalf("resuming: %v\n%s", err, dst.said())
	}
	running := loading.at(t, "GuestRunning")
	for dst.screen.tickCount() == 0 && time.Since(running) < 5*time.Second {
		time.Sleep(time.Millisecond)
	}
	first := time.Since(running)
	time.Sleep(time.Until(running.Add(1500 * time.Millisecond)))
	longest := max(first, dst.screen.largestTickGap(running, running.Add(1500*time.Millisecond)))
	t.Logf("TSC at the save %v, %v ahead of the vCPU it resumed into; the restored guest's first line %v after it ran, and the longest it went without one %v",
		tsc, lag, first.Round(time.Millisecond), longest)
	if longest > restoredGap {
		t.Errorf("the restored guest went %v without a line, want under %v: its TSC went back and its timers waited (qemu/patches/0005, #452)\n%s",
			longest, restoredGap, tail([]byte(dst.said()), 1000))
	}
}
