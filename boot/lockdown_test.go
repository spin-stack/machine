// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"os"
	"testing"

	"github.com/spin-stack/machine/machine"
)

// The guest's root is not the kernel's: the kernel runs lockdown and the BPF LSM, and has no
// /dev/mem and no /proc/kcore. A consumer that enforces policy in the guest with BPF LSM programs
// relies on root being unable to rewrite the kernel they live in; kernel/Dockerfile fails a build
// whose config lost these, and this asks the kernel that boots. A machine runs at confidentiality,
// which its command line raises it to, and one that runs an enforcer reading kernel memory at
// integrity, the kernel's floor (machine.Cmdline.LockdownIntegrity, #734): one guest of each.
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
	for _, integrity := range []bool{false, true} {
		level := map[bool]string{false: "confidentiality", true: "integrity"}[integrity]
		t.Run(level, func(t *testing.T) {
			root := newRawRoot(t, out, base)
			oneshotAtBoot(t, root, "lockdown-check", "lockdown-check.sh", "Environment=LOCKDOWN="+level)
			t.Log(checkGuest(t, rel, root, "LOCKDOWN_CHECK_OK", "LOCKDOWN_CHECK_FAILED",
				func(s *machine.Spec) { s.Cmdline.LockdownIntegrity = integrity }, func(*machine.QEMU, string) {}))
		})
	}
}
