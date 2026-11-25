// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// KASLR moves three things, and a kernel that boots says nothing when one of them stays put: the
// kernel's virtual base (_text), its physical base ("Kernel code" in /proc/iomem), and with
// RANDOMIZE_MEMORY the bases of the direct map, vmalloc and vmemmap. Each is read in
// SPIN_KASLR_BOOTS guests (default 10) of spin's base shape, 1 vCPU and 128 MiB, the least memory
// the physical base is chosen in, and each must take more than one value.
// One boot cannot tell a fixed address from a random one, and a vmlinux entered through PVH moved
// neither base nor the regions, with CONFIG_RANDOMIZE_BASE and RANDOMIZE_MEMORY both =y (#645).
//
// The regions are the values of page_offset_base, vmalloc_base and vmemmap_base, which
// kernel_randomize_memory writes: the guest finds where they are in kallsyms, and the host reads
// what they hold through the monitor, while the guest runs. The guest cannot read them under
// lockdown, and 7.4 prints no kernel address it could infer them from: /proc/vmallocinfo hashes
// its addresses (%p where 7.3 had %pK) and /proc/net/unix prints 0 for a socket, the two this
// check read until 7.4.
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
	sockets, err := os.MkdirTemp("/tmp", "kaslr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })

	// To the line's end: the console is read as it arrives, and a line read half-way holds an
	// address cut short.
	line := regexp.MustCompile(`KASLR text=([0-9a-f]+) phys=([0-9a-f]+) bases=([0-9a-f]+),([0-9a-f]+),([0-9a-f]+)\r?\n`)
	// Where each region's base is unrandomized, with 4-level paging and with 5-level. A randomized
	// base lands there about once in thousands of boots. The kernel's bases are not checked so: a
	// random kernel is at its linked _text about once in 480 boots, and at its linked physical
	// base in 128 MiB about once in 36, so their check is only that they moved.
	fixed := map[string][]uint64{
		"directmap": {0xffff888000000000, 0xff11000000000000},
		"vmalloc":   {0xffffc90000000000, 0xffa0000000000000},
		"vmemmap":   {0xffffea0000000000, 0xffd4000000000000},
	}
	what := []string{"text", "phys", "directmap", "vmalloc", "vmemmap"}
	seen := map[string]map[uint64]bool{}
	for _, w := range what {
		seen[w] = map[uint64]bool{}
	}
	for i := range boots {
		sock := filepath.Join(sockets, fmt.Sprintf("m%d", i))
		var got map[string]uint64
		checkGuest(t, rel, root, "KASLR_CHECK_OK", "KASLR_CHECK_FAILED", func(s *machine.Spec) {
			s.BootCPUs = 1
			s.Memory.BaseMB = 128
			s.Monitors = []machine.Monitor{{Socket: sock}}
		}, func(_ *machine.QEMU, console string) {
			m := line.FindStringSubmatch(console)
			if m == nil || got != nil {
				return
			}
			got = map[string]uint64{"text": hex(t, m[1]), "phys": hex(t, m[2])}
			for j, region := range what[2:] {
				got[region] = readGuestWord(t, sock, m[3+j])
			}
		})
		if got == nil {
			t.Fatalf("boot %d: the guest said it was done before the host read its bases", i+1)
		}
		t.Logf("boot %d: _text %#x, Kernel code at %#x, direct map %#x, vmalloc %#x, vmemmap %#x",
			i+1, got["text"], got["phys"], got["directmap"], got["vmalloc"], got["vmemmap"])
		for _, w := range what {
			if _, region := fixed[w]; region && got[w] < 0xff00000000000000 {
				t.Fatalf("boot %d: the %s base %#x is not a kernel address", i+1, w, got[w])
			}
			seen[w][got[w]] = true
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

func hex(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return v
}

// humanMonitor is QEMU's human monitor through QMP: `x` reads guest memory as the guest's vCPU
// maps it, which no QMP command does without writing a file.
var humanMonitor = qmpdef.Define[humanCommand, string]("human-monitor-command")

type humanCommand struct {
	CommandLine string `json:"command-line"`
}

var word = regexp.MustCompile(`:\s*0x([0-9a-f]+)`)

// readGuestWord is the 64-bit word at the guest virtual address addr, read through the monitor at
// sock.
func readGuestWord(t *testing.T, sock, addr string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("dialing the monitor at %s: %v", sock, err)
	}
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		t.Fatalf("talking to %s: %v", sock, err)
	}
	defer func() { _ = q.Close() }()
	out, err := qmp.Do(ctx, q, humanMonitor, humanCommand{CommandLine: "x /1xg 0x" + addr})
	if err != nil {
		t.Fatalf("reading the guest's word at %s: %v", addr, err)
	}
	m := word.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the monitor's x at %s answered %q", addr, out)
	}
	return hex(t, m[1])
}
