// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/machine"
)

// The shirudo a release ships (guest/shirudo) runs on a machine of that release as an operator
// deploys it: a key of this run's, the signed config image, the dm-verity runtime image and a
// systemd initrd made around that binary (shirudo/Dockerfile's operator and initrd images, from the
// same commit), the machine booted with --lockdown shirudo and a TPM. Its init runs to the end in
// the initrd and again in the root file system, status says ok with the keyrings sealed, and one
// upatch signed with the key, shirudo's own CVE-2026-31431 mitigation, loads and blocks the path it
// is for (an AF_ALG socket) and leaves another (AF_UNIX) alone (testdata/shirudo-check.sh).
//
//	SPIN_SHIRUDO_TEST=1   it builds the operator's images and starts a machine (task boot:shirudo)
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
	vmlinux, err := os.ReadFile(filepath.Join(out, "kernel", "vmlinux"))
	if err != nil {
		t.Fatal(err)
	}
	kver := regexp.MustCompile(`Linux version (\S+) `).FindSubmatch(vmlinux)
	if kver == nil {
		t.Fatal("the release's vmlinux names no Linux version")
	}
	pins, err := versions.Load(filepath.Join("..", "versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	o := build.Options{Root: root, Out: out, Pins: pins, Stdout: os.Stderr, Stderr: os.Stderr}
	const operator, initrdImage = "spin-machine/shirudo-operator:check", "spin-machine/shirudo-initrd:check"
	for target, tag := range map[string]string{"operator": operator, "initrd": initrdImage} {
		if err := build.ShirudoImage(ctx, o, target, tag); err != nil {
			t.Fatalf("building shirudo's %s image: %v", target, err)
		}
	}

	work := t.TempDir()
	script, err := filepath.Abs(filepath.Join("testdata", "shirudo-operator.sh"))
	if err != nil {
		t.Fatal(err)
	}
	docker := func(image string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"--ulimit", "memlock=-1:-1", "-e", "HOME=/tmp", "-e", "TMPDIR=/tmp",
			"-v", work + ":" + work, "-v", out + ":" + out + ":ro", "-v", script + ":/shirudo-operator.sh:ro", image}, args...)...)
		b, err := cmd.CombinedOutput()
		t.Logf("%s:\n%s", strings.Join(args, " "), b)
		if err != nil {
			t.Fatalf("%s in %s: %v", strings.Join(args, " "), image, err)
		}
	}
	docker(operator, "sh", "/shirudo-operator.sh", work, out)
	initrd := filepath.Join(work, "initrd.img")
	genInitrd := []string{"/opt/gen-initrd/shirudo-gen-initrd", "--kver", string(kver[1]), "--no-kernel",
		"--binary", shirudo, "--binary-path", "/usr/bin/shirudo",
		"--cert", filepath.Join(work, "keys", "cert.der"), "--bootstrap-conf", filepath.Join(work, "uki-bootstrap.conf"),
		"--unit", filepath.Join(work, "shirudo-initrd.service"), "--module-dir", "/opt/gen-initrd", "--output", initrd,
		"--", "--no-hostonly", "--no-hostonly-cmdline", "--zstd"}
	docker(initrdImage, genInitrd...)
	docker(initrdImage, "/opt/gen-initrd/shirudo-gen-initrd", "--check", initrd)

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
