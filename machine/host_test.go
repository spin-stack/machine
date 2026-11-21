// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// What QEMU opens by name is the binary, the kernel, the initrd and three files of the firmware
// directory, whichever BIOS the Spec runs: a root built of them starts the machine (task
// boot:overhead), so a file missing here is a machine that does not start in spin's root.
func TestFilesAreWhatQEMUOpensByName(t *testing.T) {
	rel, err := OpenRelease(tree(t, whole...))
	if err != nil {
		t.Fatal(err)
	}
	s, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	fw := func(f string) string { return filepath.Join(s.Firmware, f) }
	if got, want := s.Files(), []string{s.QEMU, s.Kernel, fw("qboot.bin"), fw("linuxboot_dma.bin"), fw("kvmvapic.bin")}; !reflect.DeepEqual(got, want) {
		t.Errorf("Files() = %v, want %v", got, want)
	}
	s.Initrd, s.BIOS = "/var/lib/spin/initrd.img", "bios-256k.bin"
	if got, want := s.Files(), []string{s.QEMU, s.Kernel, s.Initrd, fw("bios-256k.bin"), fw("linuxboot_dma.bin"), fw("kvmvapic.bin")}; !reflect.DeepEqual(got, want) {
		t.Errorf("Files() with an initrd and SeaBIOS = %v, want %v", got, want)
	}
}

// Spin's shape at its ceiling, with a kernel of 16 MiB and no initrd, is bounded at 142 MiB and
// 223 tasks: the number spin's cgroup is sized by, held to what task boot:overhead measured. Each
// part of the Spec that QEMU pays for moves it by what it costs.
func TestASpecsOverheadIsWhatItsQEMUUsesAtMost(t *testing.T) {
	dir := t.TempDir()
	file := func(name string, size int64) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	spin := Spec{
		Kernel:   file("bzImage", 16<<20),
		BootCPUs: 1, MaxCPUs: 16,
		Memory:   Memory{BaseMB: 128, CeilingMB: 8192},
		VsockCID: 3,
		NICs:     []NIC{{TapFD: 3}},
	}
	base, err := spin.Overhead()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Overhead{Memory: 128<<20 + 223*64<<10, Tasks: 223}); base != want {
		t.Errorf("spin's shape: Overhead() = %+v, want %+v", base, want)
	}
	for _, tc := range []struct {
		name   string
		edit   func(*Spec)
		memory int64
		tasks  int
	}{
		{"an initrd of 8 MiB", func(s *Spec) { s.Initrd = file("initrd", 8<<20) }, 8 << 20, 0},
		{"8 vCPUs more", func(s *Spec) { s.MaxCPUs = 24 }, 8 * (1<<20 + 64<<10), 8},
		{"no ceiling above 1 vCPU", func(s *Spec) { s.MaxCPUs = 0 }, -15 * (1<<20 + 64<<10), -15},
		{"8 GiB more memory", func(s *Spec) { s.Memory.CeilingMB = 16384 }, 32 << 20, 0},
		{"no ceiling above 128 MiB", func(s *Spec) { s.Memory.CeilingMB = 0 }, -(8192 - 128) << 20 / 256, 0},
		{"a second NIC", func(s *Spec) { s.NICs = append(s.NICs, NIC{TapFD: 4}) }, 64 << 10, 1},
		{"no vsock", func(s *Spec) { s.VsockCID = 0 }, -64 << 10, -1},
	} {
		s := spin
		tc.edit(&s)
		got, err := s.Overhead()
		if err != nil || got.Memory-base.Memory != tc.memory || got.Tasks-base.Tasks != tc.tasks {
			t.Errorf("%s: Overhead() moved by %d bytes and %d tasks (%v), want %d and %d",
				tc.name, got.Memory-base.Memory, got.Tasks-base.Tasks, err, tc.memory, tc.tasks)
		}
	}
	for _, s := range []Spec{{Kernel: filepath.Join(dir, "none")}, {Kernel: spin.Kernel, Initrd: filepath.Join(dir, "none")}} {
		if _, err := s.Overhead(); err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "none")) {
			t.Errorf("Overhead() of a Spec whose file is missing returned %v, want an error naming it", err)
		}
	}
}

// A qcow2 image costs QEMU its L2 table, whole up to 32 MiB, and four clusters of refcounts: the
// caches QEMU sizes for it by default. A raw image costs nothing.
func TestADisksOverheadIsItsQcow2Caches(t *testing.T) {
	dir := t.TempDir()
	image := func(name string, version uint32, edit func(path string)) Image {
		path := filepath.Join(dir, name)
		qcow2(t, path, version, "", "")
		if edit != nil {
			edit(path)
		}
		f, err := os.Open(path) // #nosec G304 -- a file this test made
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return Image{File: f, Format: "qcow2"}
	}
	sized := func(size uint64, bits byte) func(string) {
		return func(path string) {
			patch(t, path, 20, 0, 0, 0, bits)
			patch(t, path, 24, byte(size>>56), byte(size>>48), byte(size>>40), byte(size>>32), byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
		}
	}
	raw, err := os.Open(filepath.Join(dir, "."))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	const refcounts = 4 << 16
	for _, tc := range []struct {
		name string
		disk Disk
		want int64
	}{
		{"64 GiB of 64 KiB clusters", Disk{Images: []Image{image("a", 3, sized(64<<30, 16))}}, 8<<20 + refcounts},
		{"version 2 has no extended L2", Disk{Images: []Image{image("b", 2, func(p string) { sized(64<<30, 16)(p); patch(t, p, 79, extendedL2) })}}, 8<<20 + refcounts},
		{"extended L2", Disk{Images: []Image{image("c", 3, func(p string) { sized(64<<30, 16)(p); patch(t, p, 79, extendedL2) })}}, 16<<20 + refcounts},
		{"an L2 table of a part of a cluster is a cluster", Disk{Images: []Image{image("d", 3, sized(1<<30+1, 16))}}, 192<<10 + refcounts},
		{"at least two clusters of L2", Disk{Images: []Image{image("e", 3, sized(1<<20, 16))}}, 2<<16 + refcounts},
		{"at most 32 MiB of L2", Disk{Images: []Image{image("f", 3, sized(1<<40, 16))}}, 32<<20 + refcounts},
		{"clusters of 512 bytes", Disk{Images: []Image{image("n", 3, sized(1<<20, 9))}}, 16<<10 + 4*512},
		{"clusters of 2 MiB", Disk{Images: []Image{image("g", 3, sized(64<<30, 21))}}, 2*(2<<20) + 4*(2<<20)},
		{"a chain is each image's", Disk{Images: []Image{image("h", 3, sized(64<<30, 16)), image("i", 3, sized(64<<30, 16))}}, 2 * (8<<20 + refcounts)},
		{"a raw image has none", Disk{Images: []Image{{File: raw, Format: "raw"}}}, 0},
	} {
		got, err := tc.disk.Overhead()
		if err != nil || got != tc.want {
			t.Errorf("%s: Overhead() = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		img  Image
	}{
		{"clusters of 256 bytes", image("j", 3, sized(1<<30, 8))},
		{"clusters of 4 MiB", image("k", 3, sized(1<<30, 22))},
		{"a header cut short", image("l", 2, func(p string) {
			if err := os.Truncate(p, v2HeaderLength-1); err != nil {
				t.Fatal(err)
			}
		})},
		{"a version 3 header cut short", image("m", 3, func(p string) {
			if err := os.Truncate(p, v3HeaderLength-1); err != nil {
				t.Fatal(err)
			}
		})},
	} {
		if _, err := (Disk{Images: []Image{tc.img}}).Overhead(); err == nil || !strings.Contains(err.Error(), tc.img.File.Name()) {
			t.Errorf("%s: Overhead() returned %v, want an error naming %s", tc.name, err, tc.img.File.Name())
		}
	}
}
