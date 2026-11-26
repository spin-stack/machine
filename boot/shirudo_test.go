// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/machine"
)

// The shirudo a release ships (guest/shirudo) runs on a machine of that release as an operator
// deploys it with nothing but the release: in a guest of it, as root, the release's operator half
// (shirudo-operator, its second tarball) makes a key of this run's, the signed config image, the
// dm-verity runtime image and a systemd initrd around that binary, and two upatches signed with
// the key (testdata/shirudo-operator.sh). Then the machine boots with --lockdown shirudo and a TPM
// from what it made. Its init runs to the end in the initrd and again in the root file system,
// status says ok with the keyrings sealed, and one of the upatches, shirudo's own CVE-2026-31431
// mitigation, loads and blocks the path it is for (an AF_ALG socket) and leaves another (AF_UNIX)
// alone (testdata/shirudo-check.sh).
//
//	SPIN_SHIRUDO_TEST=1   it starts two machines (task boot:shirudo)
func TestTheShippedShirudoRunsOnTheRelease(t *testing.T) {
	if os.Getenv("SPIN_SHIRUDO_TEST") != "1" {
		t.Skip("set SPIN_SHIRUDO_TEST=1 to run the release's shirudo on a machine of the release")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Fatal("a shirudo machine boots a 4 GiB guest through a systemd initrd: it needs /dev/kvm")
	}
	out := releaseTree(t)
	shirudo := filepath.Join(out, build.ShirudoBinary)
	if _, err := os.Stat(shirudo); err != nil {
		t.Fatalf("the release ships no shirudo: %v", err)
	}
	operator := filepath.Join(out, build.ShirudoOperator)
	if _, err := os.Stat(filepath.Join(operator, "install")); err != nil {
		t.Fatalf("the release ships no operator half for its shirudo: %v", err)
	}
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	swtpm, err := rel.SWTPM()
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}

	// The operator's disk: the release's shirudo and operator half as they unpack, and the
	// script, made by the release's own mkfs.ext4. The guest leaves what it made in /work on it.
	stage := t.TempDir()
	mustRun(t, "mkdir", "-p", filepath.Join(stage, "spin-stack", "guest"))
	mustRun(t, "cp", "-a", operator, filepath.Join(stage, "spin-stack", build.ShirudoOperator))
	mustRun(t, "cp", "-L", shirudo, filepath.Join(stage, "spin-stack", build.ShirudoBinary))
	opDisk := filepath.Join(t.TempDir(), "op.img")
	mkfs := exec.Command(filepath.Join(out, "bin/mkfs.ext4"), "-q", "-t", "ext4", "-d", stage, opDisk, "4G")
	mkfs.Env = append(os.Environ(), "MKE2FS_CONFIG="+filepath.Join(out, "e2fsprogs/mke2fs.conf"))
	if b, err := mkfs.CombinedOutput(); err != nil {
		t.Fatalf("making the operator's disk: %v\n%s", err, b)
	}
	or := newRawRoot(t, out, base)
	oneshotAtBoot(t, or, "shirudo-operator", "shirudo-operator.sh", "")
	t.Log(checkGuestFor(t, 15*time.Minute, rel, or, "SHIRUDO_OPERATOR_OK", "SHIRUDO_OPERATOR_FAILED", func(s *machine.Spec) {
		s.BootCPUs = 4
		s.Memory.BaseMB = 4096
	}, func(*machine.QEMU, string) {}, machine.DiskPath{Serial: "op", Path: opDisk, Format: "raw"}))
	made := t.TempDir()
	if b, err := exec.Command(filepath.Join(out, "bin/debugfs"), "-R", "rdump /work "+made, opDisk).CombinedOutput(); err != nil {
		t.Fatalf("reading what the operator made: %v\n%s", err, b)
	}
	work := filepath.Join(made, "work")
	initrd := filepath.Join(work, "initrd.img")
	if _, err := os.Stat(initrd); err != nil {
		t.Fatalf("the operator's guest left no initrd: %v", err)
	}

	// The root file system as the boot integration guide leaves a target: the images, the
	// certificate and the root file system's unit with the runtime image's
	// drop-in, enabled; and the upatch and the check.
	r := newRawRoot(t, out, base)
	put := func(from, to, mode string) {
		t.Helper()
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		r.write(to, string(b))
		r.edit("sif " + to + " mode " + mode)
	}
	for _, d := range []string{"/var/lib/shirudo/x", "/var/lib/shirudo/images/x", "/var/lib/shirudo/keys/x"} {
		r.mkdirParent(d)
	}
	r.edit("sif /var/lib/shirudo/images mode 040700")
	for _, f := range []string{"runtime.img", "runtime.verity", "runtime.roothash", "initial.img", "initial.verity", "initial.roothash", "initial.p7s"} {
		put(filepath.Join(work, "images", f), "/var/lib/shirudo/images/"+f, "0100644")
	}
	put(filepath.Join(work, "keys", "cert.der"), "/var/lib/shirudo/keys/cert.der", "0100644")
	put(filepath.Join(work, "shirudo.service"), "/etc/systemd/system/shirudo.service", "0100644")
	put(filepath.Join(work, "runtime-service.conf"), "/etc/systemd/system/shirudo.service.d/runtime.conf", "0100644")
	r.link("/etc/systemd/system/sysinit.target.wants/shirudo.service", "/etc/systemd/system/shirudo.service")
	put(filepath.Join(work, "af-alg-block.upatch"), "/af-alg-block.upatch", "0100644")
	sum, err := build.FileSHA256(shirudo)
	if err != nil {
		t.Fatal(err)
	}
	oneshotAtBoot(t, r, "shirudo-check", "shirudo-check.sh", "Environment=SHIRUDO_SHA256="+sum)

	t.Log(checkGuest(t, rel, r, "SHIRUDO_CHECK_OK", "SHIRUDO_CHECK_FAILED", func(s *machine.Spec) {
		s.Cmdline.Lockdown = machine.LockdownShirudo
		s.Initrd = initrd
		s.TPM = machine.TPM{SWTPM: swtpm, State: t.TempDir()}
		// The digests of what the firmware measures, as a caller makes them, once a file.
		for path, digest := range map[string]*[32]byte{s.Kernel: &s.TPM.Kernel, s.Initrd: &s.TPM.Initrd} {
			if *digest, err = machine.FileSHA256(path); err != nil {
				t.Fatal(err)
			}
		}
		// An initrd of ~62 MB unpacked into the guest's memory: at 1 GiB the boot is short of it.
		s.Memory.BaseMB = 4096
	}, func(*machine.QEMU, string) {}))
}
