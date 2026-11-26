// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"os"
	"path"
	"testing"

	"github.com/spin-stack/machine/internal/modsign"
	"github.com/spin-stack/machine/machine"
)

// The guest kernel loads the modules the release signed and nothing else (#736). Its features load
// when they are asked for, with the image's own modules: fuse when /dev/fuse is opened, wireguard
// when a link of its type is added, squashfs and erofs by modprobe. A module whose signature is
// taken off is refused, and so is one signed by a key that is not spin-stack's, as a tenant's would
// be: made here and signed by modsign, the code a release signs with. One guest at confidentiality,
// which the command line raises every machine to, and one at shirudo's integrity, the kernel's
// floor (#734): MODULE_SIG_FORCE refuses below lockdown, so both refuse alike.
func TestOnlyModulesSpinStackSignedLoad(t *testing.T) {
	if os.Getenv("SPIN_MODULES_TEST") != "1" {
		t.Skip("set SPIN_MODULES_TEST=1 to check the guest kernel's modules in a built image")
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
	probe := newRawRoot(t, out, base)
	releases := probe.names("/usr/lib/modules")
	if len(releases) != 1 {
		t.Fatalf("the image's /usr/lib/modules holds %v, not one kernel's modules", releases)
	}
	signed := probe.read(path.Join("/usr/lib/modules", releases[0], "kernel/fs/squashfs/squashfs.ko"))
	unsigned, _, ok := modsign.Split(signed)
	if !ok {
		t.Fatal("the image's squashfs.ko is unsigned: this image was not built by a release")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := modsign.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := modsign.Sign(unsigned, key, theirs)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []machine.Lockdown{machine.LockdownConfidentiality, machine.LockdownShirudo} {
		t.Run(l.String(), func(t *testing.T) {
			root := newRawRoot(t, out, base)
			root.write("/unsigned.ko", string(unsigned))
			root.write("/foreign.ko", string(foreign))
			oneshotAtBoot(t, root, "modules-check", "modules-check.sh", "")
			t.Log(checkGuest(t, rel, root, "MODULES_CHECK_OK", "MODULES_CHECK_FAILED",
				func(s *machine.Spec) { s.Cmdline.Lockdown = l }, func(*machine.QEMU, string) {}))
		})
	}
}
