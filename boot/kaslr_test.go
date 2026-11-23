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
// RANDOMIZE_MEMORY the direct map, vmalloc and vmemmap regions. Each is read in SPIN_KASLR_BOOTS
// guests (default 10) of spin's base shape, 1 vCPU and 128 MiB, the least memory the physical
// base is chosen in, and each must take more than one value.
// One boot cannot tell a fixed address from a random one, and a vmlinux entered through PVH moved
// neither base nor the regions, with CONFIG_RANDOMIZE_BASE and RANDOMIZE_MEMORY both =y (#645).
//
// The regions are seen through the lowest vmalloc address and the lowest unix socket, a direct
// map object, each rounded down to the 1 GiB that kernel_randomize_memory aligns a region to.
// Unrandomized they sit at the region's fixed base whatever the boot allocated. A %p hash, which
// an earlier version of this check compared, differs every boot whatever the layout, and is
// refused as not a kernel address. vmemmap moves with the other two or not at all: one test of
// KASLR_FLAG gates all three (kernel/patches/0019).
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

	line := regexp.MustCompile(`KASLR text=([0-9a-f]+) phys=([0-9a-f]+) vmalloc=([0-9a-f]+) directmap=([0-9a-f]+)`)
	// Where a region is unrandomized, with 4-level paging and with 5-level. A randomized region
	// lands there about once in thousands of boots. The bases are not checked so: a random
	// kernel is at its linked _text about once in 480 boots, and at its linked physical base in
	// 128 MiB about once in 36, so their check is only that they moved.
	fixed := map[string][]uint64{
		"vmalloc":   {0xffffc90000000000, 0xffa0000000000000},
		"directmap": {0xffff888000000000, 0xff11000000000000},
	}
	what := []string{"text", "phys", "vmalloc", "directmap"}
	seen := map[string]map[uint64]bool{}
	for _, w := range what {
		seen[w] = map[uint64]bool{}
	}
	for i := range boots {
		console := checkGuest(t, rel, root, "KASLR_CHECK_OK", "KASLR_CHECK_FAILED", func(s *machine.Spec) {
			s.BootCPUs = 1
			s.Memory.BaseMB = 128
		}, func(*machine.QEMU, string) {})
		m := line.FindStringSubmatch(console)
		if m == nil {
			t.Fatalf("boot %d: no KASLR line on the console:\n%s", i+1, console)
		}
		t.Logf("boot %d: _text %s, Kernel code at %s, lowest vmalloc %s, lowest unix socket %s", i+1, m[1], m[2], m[3], m[4])
		for j, what := range what {
			v, err := strconv.ParseUint(m[j+1], 16, 64)
			if err != nil {
				t.Fatalf("boot %d: %s %q: %v", i+1, what, m[j+1], err)
			}
			if what == "vmalloc" || what == "directmap" {
				if v < 0xff00000000000000 {
					t.Fatalf("boot %d: %s %#x is not a kernel address: a hash, or a pointer kptr_restrict hid", i+1, what, v)
				}
				v &^= 1<<30 - 1
			}
			seen[what][v] = true
		}
	}
	for _, w := range what {
		for _, a := range fixed[w] {
			if seen[w][a] {
				t.Errorf("%s was at %#x, where it is unrandomized, in a boot", w, a)
			}
		}
		if len(seen[w]) < 2 {
			t.Errorf("%s took one value in %d boots (the boots above)", w, boots)
		}
	}
}
