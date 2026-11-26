// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// A machine with a TPM (machine.Spec.TPM) at spin's shape, used as shirudo uses it: the guest sees
// /dev/tpm0 and /dev/tpmrm0, and through the second makes an RSA-3072 primary, extends PCR 16 and
// writes an NV index (boot/tpmcheck). A checkpoint of it resumed into a machine whose TPM state is
// a new, empty directory reads back the same primary, PCR and index: the migration stream carries
// the TPM, and the directory takes it. A machine cold-booted on that directory then has the same
// seed and the index, and PCR 16 back at zero, as a TPM has after a reset. A machine without one
// has neither node.
//
//	SPIN_TPM_TEST=1   it starts machines (task boot:tpm)
func TestTheGuestUsesItsTPMAcrossACheckpoint(t *testing.T) {
	if os.Getenv("SPIN_TPM_TEST") == "" {
		t.Skip("set SPIN_TPM_TEST=1: this starts machines")
	}
	r := newRecorder(t, 4000000)
	rel, err := machine.OpenRelease(r.out)
	if err != nil {
		t.Fatal(err)
	}
	swtpm, err := rel.SWTPM()
	if err != nil {
		t.Fatal(err)
	}
	root := newRawRoot(t, r.out, r.base())
	check := filepath.Join(t.TempDir(), "tpmcheck")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", check, "./tpmcheck")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building tpmcheck: %v\n%s", err, out)
	}
	b, err := os.ReadFile(check)
	if err != nil {
		t.Fatal(err)
	}
	root.write("/usr/local/bin/spin-tpmcheck", string(b))
	root.edit("sif /usr/local/bin/spin-tpmcheck mode 0100755")

	tpm := func(dir string) func(*machine.Spec) {
		return func(s *machine.Spec) { s.TPM = machine.TPM{SWTPM: swtpm, State: dir} }
	}
	disk := func(t *testing.T) machine.Disk {
		d, err := machine.OpenDisk(machine.DiskPath{Serial: "root", Path: root.path, Format: "raw"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// A line tpmcheck printed, and the console's text after it, for the next one.
	said := regexp.MustCompile(`SPIN-TPM rsa3072=([0-9a-f]+) pcr16=([0-9a-f]+) nv=([0-9a-f]+|none)`)
	run := func(t *testing.T, g *guest, cmd string) []string {
		t.Helper()
		before := len(g.said())
		g.type_(t, "/usr/local/bin/spin-tpmcheck "+cmd+" || echo SPIN-TPM-FAILED")
		deadline := time.Now().Add(30 * time.Second)
		for {
			text := g.said()[before:]
			if m := said.FindStringSubmatch(text); m != nil {
				return m[1:]
			}
			if strings.Contains(text, "SPIN-TPM-FAILED") || time.Now().After(deadline) {
				t.Fatalf("tpmcheck %s did not say what the TPM holds:\n%s", cmd, tail([]byte(text), 2000))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	mount := "mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev 2>/dev/null; "

	first := t.TempDir()
	s, mac := r.next(t, "/bin/sh")
	tpm(first)(&s)
	src := startGuest(t, s, mac, nil)
	if err := src.Boot(ctx, disk(t)); err != nil {
		t.Fatalf("booting: %v\n%s", err, src.said())
	}
	src.type_(t, mount+"ls /dev/tpm0 /dev/tpmrm0 && echo TPM-NODES-$((1+1)); cat /sys/class/tpm/tpm0/tpm_version_major")
	src.await(t, regexp.MustCompile(`TPM-NODES-2\b`), 60*time.Second)
	saved := run(t, src, "write")
	if saved[1] == strings.Repeat("0", 64) || saved[2] == "none" {
		t.Fatalf("PCR 16 or the NV index is not written: %v", saved)
	}
	t.Logf("the guest's TPM: RSA-3072 primary %s…, PCR 16 %s, NV %s", saved[0], saved[1], saved[2])

	state, err := os.Create(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	checkpoint, _, err := src.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, src.said())
	}
	src.Kill()

	t.Run("a checkpoint resumed onto an empty TPM state reads back what it saved", func(t *testing.T) {
		resumed := t.TempDir()
		s, mac := r.next(t, "/bin/sh")
		tpm(resumed)(&s)
		s.Restore = true
		here, err := machine.ReadTarget(ctx, s, machine.StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		dst := startGuest(t, s, mac, nil)
		if err := dst.Resume(ctx, state, checkpoint, here, machine.LoadOptions{}, disk(t)); err != nil {
			t.Fatalf("resuming: %v\n%s", err, dst.said())
		}
		if got := run(t, dst, "read"); strings.Join(got, " ") != strings.Join(saved, " ") {
			t.Errorf("the restored guest reads %v from its TPM, and saved %v", got, saved)
		}
		dst.Kill()

		t.Run("a cold boot on that state keeps the seed and the index, not the PCR", func(t *testing.T) {
			s, mac := r.next(t, "/bin/sh")
			tpm(resumed)(&s)
			g := startGuest(t, s, mac, nil)
			if err := g.Boot(ctx, disk(t)); err != nil {
				t.Fatalf("booting: %v\n%s", err, g.said())
			}
			g.type_(t, mount+"echo MOUNTED-$((2+2))")
			g.await(t, regexp.MustCompile(`MOUNTED-4\b`), 60*time.Second)
			got := run(t, g, "read")
			if got[0] != saved[0] || got[2] != saved[2] || got[1] != strings.Repeat("0", 64) {
				t.Errorf("a cold boot on the resumed state reads %v, want the primary and NV of %v and PCR 16 at zero", got, saved)
			}
		})
	})

	t.Run("a machine without a TPM has none", func(t *testing.T) {
		s, mac := r.next(t, "/bin/sh")
		g := startGuest(t, s, mac, nil)
		if err := g.Boot(ctx, disk(t)); err != nil {
			t.Fatalf("booting: %v\n%s", err, g.said())
		}
		g.type_(t, mount+"ls /dev/tpm0 /dev/tpmrm0 2>/dev/null || echo NO-TPM-$((3+3))")
		g.await(t, regexp.MustCompile(`NO-TPM-6\b`), 60*time.Second)
	})
}
