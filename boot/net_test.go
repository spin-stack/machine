// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spin-stack/machine/machine"
)

// A machine with a NIC on passt (machine.NIC.Passt), as spin-machine boot --net gives one: the
// guest's systemd brings eth0 up with the address, route and nameserver the kernel command line
// carries, resolves a name through passt to the host's resolver and fetches over HTTPS from the
// internet, with nothing on the host but the user running it. A machine without a NIC has none.
// Under TCG where there is no /dev/kvm. The lane reaches the internet (GitHub's runners do), so
// the check asks for github.com and not only for passt's gateway.
//
//	SPIN_NET_TEST=1   it starts machines (task boot:net)
func TestAGuestOnPasstReachesTheInternet(t *testing.T) {
	if os.Getenv("SPIN_NET_TEST") == "" {
		t.Skip("set SPIN_NET_TEST=1: this starts machines")
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
	passt, err := rel.Passt()
	if err != nil {
		t.Skipf("the tree has no passt (%v): a QEMU built without it, as sanitize.yml's is", err)
	}
	// passt sandboxes itself in a user namespace of its own, and dies when it cannot make one: a
	// host that refuses is said here, by what refused it, and not as a guest with no network.
	if out, err := exec.Command("unshare", "--user", "true").CombinedOutput(); err != nil {
		label, _ := os.ReadFile("/proc/self/attr/current")
		var sysctls []string
		for _, k := range []string{"kernel/apparmor_restrict_unprivileged_userns", "kernel/apparmor_restrict_unprivileged_unconfined", "user/max_user_namespaces", "kernel/unprivileged_userns_clone"} {
			v, _ := os.ReadFile("/proc/sys/" + k)
			sysctls = append(sysctls, k+"="+strings.TrimSpace(string(v)))
		}
		t.Fatalf("this host makes no user namespace for an unprivileged process, and passt needs one: unshare --user: %v: %s; AppArmor label %q; %s",
			err, out, strings.TrimSpace(string(label)), strings.Join(sysctls, " "))
	}
	t.Run("on passt", func(t *testing.T) {
		root := newRawRoot(t, out, base)
		oneshotAtBoot(t, root, "net-check", "net-check.sh", "Environment=NET=passt")
		t.Log(checkGuest(t, rel, root, "NET_CHECK_OK", "NET_CHECK_FAILED", func(s *machine.Spec) {
			s.NICs = []machine.NIC{{Passt: passt, MAC: "52:54:00:12:34:56"}}
		}, func(*machine.QEMU, string) {}))
	})
	t.Run("without a NIC", func(t *testing.T) {
		root := newRawRoot(t, out, base)
		oneshotAtBoot(t, root, "net-check", "net-check.sh", "Environment=NET=none")
		t.Log(checkOnConsole(t, rel, root, "NET_CHECK_OK", "NET_CHECK_FAILED"))
	})
}
