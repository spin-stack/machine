// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spin-stack/machine/machine"
)

// The guest's root is not the kernel's: the kernel runs lockdown and the BPF LSM, and has no
// /dev/mem and no /proc/kcore. A consumer that enforces policy in the guest with BPF LSM programs
// relies on root being unable to rewrite the kernel they live in; kernel/Dockerfile fails a build
// whose config lost these, and this asks the kernel that boots. One guest of each machine.Lockdown:
// confidentiality, which the command line raises a machine to, with the .bpf and .dm-verity
// keyrings sealed; shirudo's, at integrity, the kernel's floor (#734), with both keyrings open for
// its init to enroll in (#741); and the kernel's own level, sealed.
// It also asks where that kernel is: at a random address, which a config cannot promise, since
// the way the machine enters the kernel decides whether KASLR runs (lockdown-check.sh).
func TestTheGuestKernelIsLockedDown(t *testing.T) {
	if os.Getenv("SPIN_LOCKDOWN_TEST") != "1" {
		t.Skip("set SPIN_LOCKDOWN_TEST=1 to check the guest kernel's lockdown in a built image")
	}
	out := releaseTree(t)
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(t.TempDir(), "keyring-probe")
	build := exec.Command("go", "build", "-o", probe, "./testdata/keyring-probe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building keyring-probe: %v: %s", err, b)
	}
	probed, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []machine.Lockdown{machine.LockdownConfidentiality, machine.LockdownShirudo, machine.LockdownKernel} {
		t.Run(l.String(), func(t *testing.T) {
			root := newRawRoot(t, out, base)
			root.write("/keyring-probe", string(probed))
			root.edit("sif /keyring-probe mode 0100755")
			oneshotAtBoot(t, root, "lockdown-check", "lockdown-check.sh", "Environment=LOCKDOWN="+l.String())
			t.Log(checkGuest(t, rel, root, "LOCKDOWN_CHECK_OK", "LOCKDOWN_CHECK_FAILED",
				func(s *machine.Spec) { s.Cmdline.Lockdown = l }, func(*machine.QEMU, string) {}))
		})
	}
}
