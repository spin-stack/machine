// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"cmp"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/spin-stack/machine/machine"
)

// KASLR moves three things, and a kernel that boots says nothing when one of them stays put: the
// kernel's virtual base (_text), its physical base ("Kernel code" in /proc/iomem), and with
// RANDOMIZE_MEMORY the direct map, vmalloc and vmemmap regions, seen here through the first
// vmalloc address. Each is read in SPIN_KASLR_BOOTS guests (default 10) of spin's base shape,
// 1 vCPU and 128 MiB, the least memory the physical base is chosen in, and each must take more
// than one value. One boot cannot tell a fixed address from a random one (lockdown-check.sh
// asks only that _text is not the linked address), and a vmlinux entered through PVH moved
// neither base nor the regions, with CONFIG_RANDOMIZE_BASE and RANDOMIZE_MEMORY both =y (#645).
func TestTheKernelMovesEveryBoot(t *testing.T) {
	if os.Getenv("SPIN_KASLR_TEST") != "1" {
		t.Skip("set SPIN_KASLR_TEST=1 to boot a built release repeatedly and compare where its kernel is")
	}
	boots, err := strconv.Atoi(cmp.Or(os.Getenv("SPIN_KASLR_BOOTS"), "10"))
	if err != nil || boots < 2 {
		t.Fatalf("SPIN_KASLR_BOOTS=%q: two boots at least", os.Getenv("SPIN_KASLR_BOOTS"))
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
	root := newRawRoot(t, out, base)
	oneshotAtBoot(t, root, "kaslr-check", "kaslr-check.sh", "")

	line := regexp.MustCompile(`KASLR text=([0-9a-f]+) phys=([0-9a-f]+) vmalloc=([0-9a-fx]+)`)
	seen := map[string]map[string]bool{"text": {}, "phys": {}, "vmalloc": {}}
	for i := range boots {
		console := checkGuest(t, rel, root, "KASLR_CHECK_OK", "KASLR_CHECK_FAILED", func(s *machine.Spec) {
			s.BootCPUs = 1
			s.Memory.BaseMB = 128
		}, func(*machine.QEMU, string) {})
		m := line.FindStringSubmatch(console)
		if m == nil {
			t.Fatalf("boot %d: no KASLR line on the console:\n%s", i+1, console)
		}
		t.Logf("boot %d: _text %s, Kernel code at %s, first vmalloc %s", i+1, m[1], m[2], m[3])
		seen["text"][m[1]] = true
		seen["phys"][m[2]] = true
		seen["vmalloc"][m[3]] = true
	}
	if seen["text"]["ffffffff81000000"] {
		t.Error("_text was the linked address, ffffffff81000000, in a boot")
	}
	for _, what := range []string{"text", "phys", "vmalloc"} {
		if len(seen[what]) < 2 {
			t.Errorf("%s took one value in %d boots: %v", what, boots, seen[what])
		}
	}
}
