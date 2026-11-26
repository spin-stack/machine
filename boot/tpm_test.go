// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"crypto/rand"
	"fmt"
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

	// The digests of what the machine boots, which its firmware measures, made as a caller makes
	// them: once a file.
	digest := func(path string) [32]byte {
		d, err := machine.FileSHA256(path)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tpm := func(dir string) func(*machine.Spec) {
		return func(s *machine.Spec) {
			s.TPM = machine.TPM{SWTPM: swtpm, State: dir, Kernel: digest(s.Kernel)}
			if s.Initrd != "" {
				s.TPM.Initrd = digest(s.Initrd)
			}
		}
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
		// The console echoes what is typed: the failure's marker is what the shell computes.
		g.type_(t, "/usr/local/bin/spin-tpmcheck "+cmd+" || echo TPMCHECK-EXIT-$((5+5))")
		start := time.Now()
		deadline := start.Add(2 * time.Minute)
		for {
			text := g.said()[before:]
			if m := said.FindStringSubmatch(text); m != nil {
				t.Logf("tpmcheck %s answered in %s", cmd, time.Since(start).Round(time.Millisecond))
				return m[1:]
			}
			if strings.Contains(text, "TPMCHECK-EXIT-10") || time.Now().After(deadline) {
				t.Fatalf("tpmcheck %s did not say what the TPM holds:\n%s", cmd, tail([]byte(text), 2000))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	mount := "mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev 2>/dev/null; "
	// The firmware's measurements, as the guest reads them: its log has the events the machine
	// named (machine.Spec.TPMEvents), in order, and every bank of every PCR in it holds what the
	// log replays to (qemu/qboot/tpm.patch).
	event := regexp.MustCompile(`SPIN-TPM-EVENT pcr=(\d+) type=(0x[0-9a-f]+) sha256=([0-9a-f]{64}) data=([0-9a-f]*)`)
	measured := func(t *testing.T, g *guest, s machine.Spec) {
		t.Helper()
		before := len(g.said())
		g.type_(t, "mount -t securityfs securityfs /sys/kernel/security 2>/dev/null; /usr/local/bin/spin-tpmcheck log || echo TPMCHECK-EXIT-$((5+5))")
		replayed := regexp.MustCompile(`SPIN-TPM-LOG events=(\d+) replayed=(\S+)`)
		g.await(t, regexp.MustCompile(`SPIN-TPM-LOG events=\d+|TPMCHECK-EXIT-10`), 60*time.Second)
		text := g.said()[before:]
		m := replayed.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("the guest's event log does not replay to its PCRs:\n%s", tail([]byte(text), 3000))
		}
		var got []string
		for _, e := range event.FindAllStringSubmatch(text, -1) {
			got = append(got, strings.Join(e[1:], " "))
		}
		var want []string
		for _, e := range s.TPMEvents() {
			want = append(want, fmt.Sprintf("%d %#x %x %x", e.PCR, e.Type, e.Digest, e.Data))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("the guest's log has the events\n%s\nand the machine measures\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if !strings.Contains(m[2], "0xb") {
			t.Errorf("the log replays the banks %s, not SHA-256's", m[2])
		}
		t.Logf("the guest's log: %d events, replayed in the banks %s", len(got), m[2])
	}

	first := t.TempDir()
	s, mac := r.next(t, "/bin/sh")
	tpm(first)(&s)
	src := startGuest(t, s, mac, nil)
	srcDisk := disk(t)
	if err := src.Boot(ctx, srcDisk); err != nil {
		t.Fatalf("booting: %v\n%s", err, src.said())
	}
	// A kernel with no tpm_crb driver has no node to make: a release's from before #733, which a
	// tree of it boots until a release carries one. The driver is built in, so it is asked for by
	// what it makes, its directory on the ACPI bus, never as a module.
	src.type_(t, mount+"if ls /dev/tpm0 /dev/tpmrm0; then echo TPM-NODES-$((1+1)); elif [ -d /sys/bus/acpi/drivers/tpm_crb ]; then echo TPM-NO-NODES-$((1+2)); else echo TPM-NO-DRIVER-$((2+2)); fi")
	answer := regexp.MustCompile(`TPM-(NODES-2|NO-NODES-3|NO-DRIVER-4)\b`)
	src.await(t, answer, 60*time.Second)
	switch answer.FindStringSubmatch(src.said())[1] {
	case "NO-DRIVER-4":
		t.Skipf("the tree's kernel has no tpm_crb driver: a kernel from before #733")
	case "NO-NODES-3":
		t.Fatalf("the kernel has tpm_crb and the guest no /dev/tpm0 or /dev/tpmrm0:\n%s", tail([]byte(src.said()), 3000))
	}
	measured(t, src, s)
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
	_ = srcDisk.Close()

	t.Run("a checkpoint resumed onto an empty TPM state reads back what it saved", func(t *testing.T) {
		resumed := t.TempDir()
		// The record is read of the machine as spin starts it, its NIC and console in the Spec.
		s := r.spec(t, "/bin/sh")
		tpm(resumed)(&s)
		s.Restore = true
		here, err := machine.ReadTarget(ctx, s, machine.StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		mac := s.NICs[0].MAC
		s.NICs = nil
		dst := startGuest(t, s, mac, nil)
		d := disk(t)
		if err := dst.Resume(ctx, state, checkpoint, here, machine.LoadOptions{}, d); err != nil {
			t.Fatalf("resuming: %v\n%s", err, dst.said())
		}
		if got := run(t, dst, "read"); strings.Join(got, " ") != strings.Join(saved, " ") {
			t.Errorf("the restored guest reads %v from its TPM, and saved %v", got, saved)
		}
		dst.Kill()
		// The disk's lock is on the description QEMU was handed, which this holds until closed.
		_ = d.Close()

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

	// shirudo's initrd is ~62 MB, and QEMU puts an initrd at the top of the memory below 4 GiB.
	// Under SeaBIOS one that size covered the ACPI tables, the kernel ran without ACPI and no TPM
	// was enumerated (#719). qboot puts the tables below 1 MiB, and the kernel's own log says where
	// each went. 512 MiB and not spin's 128: at 128 the kernel runs out of memory unpacking 62 MB
	// into its rootfs and panics before userspace, whatever the tables (spin-stack/spin#1708).
	t.Run("a shirudo-sized initrd leaves the TPM where the guest finds it", func(t *testing.T) {
		pad := make([]byte, 62<<20)
		if _, err := rand.Read(pad); err != nil {
			t.Fatal(err)
		}
		initrd := filepath.Join(t.TempDir(), "initrd.cpio")
		if err := os.WriteFile(initrd, newcArchive([]cpioEntry{{name: "spin-pad", mode: 0o100644, data: pad}}), 0o644); err != nil {
			t.Fatal(err)
		}
		s, mac := r.next(t, "/bin/sh")
		s.Initrd = initrd
		tpm(t.TempDir())(&s)
		s.Memory.BaseMB = 512
		g := startGuest(t, s, mac, nil)
		if err := g.Boot(ctx, disk(t)); err != nil {
			t.Fatalf("booting: %v\n%s", err, g.said())
		}
		g.type_(t, mount+"dmesg | grep -E 'RAMDISK|ACPI: (RSDP|TPM2|Failed)|AE_NO_MEMORY|tpm'; ls /dev/tpm0 && echo TPM-WITH-INITRD-$((4+4))")
		g.await(t, regexp.MustCompile(`TPM-WITH-INITRD-8\b`), 60*time.Second)
		measured(t, g, s)
		// What the kernel printed, not the grep the console echoed.
		if regexp.MustCompile(`\]\s+ACPI.*(Failed|AE_NO_MEMORY)`).MatchString(g.said()) {
			t.Errorf("the kernel lost its ACPI tables under a 62 MB initrd:\n%s", tail([]byte(g.said()), 3000))
		}
		t.Logf("where the initrd and the ACPI tables went:\n%s", tail([]byte(g.said()), 1500))
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
