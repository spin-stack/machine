// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

// pvhELF is an ELF whose loaded segment holds no note and whose PT_NOTE holds a Xen one, as a
// vmlinux does.
func pvhELF(t *testing.T) string {
	t.Helper()
	notes := note("Xen", []byte{0, 0, 0, 1})
	load := []byte("code, not a note: Xen")
	const phoff, n = 64, 2
	data := uint64(phoff + n*56)
	var b bytes.Buffer
	h := elf.Header64{Type: uint16(elf.ET_EXEC), Machine: uint16(elf.EM_X86_64), Version: uint32(elf.EV_CURRENT),
		Phoff: phoff, Ehsize: 64, Phentsize: 56, Phnum: n, Shentsize: 64}
	copy(h.Ident[:], elf.ELFMAG)
	h.Ident[elf.EI_CLASS], h.Ident[elf.EI_DATA], h.Ident[elf.EI_VERSION] = byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)
	_ = binary.Write(&b, binary.LittleEndian, h)
	_ = binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_LOAD), Off: data, Filesz: uint64(len(load)), Memsz: uint64(len(load))})
	_ = binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_NOTE), Off: data + uint64(len(load)), Filesz: uint64(len(notes)), Memsz: uint64(len(notes))})
	b.Write(load)
	b.Write(notes)
	path := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAKernelWithItsPVHNotesVerifies(t *testing.T) {
	if err := PVH(pvhELF(t)); err != nil {
		t.Errorf("a vmlinux with its Xen note: %v", err)
	}
	out := t.TempDir()
	b, _ := os.ReadFile(bzImage(t, "HdrS", 1))
	v, _ := os.ReadFile(pvhELF(t))
	for p, body := range map[string][]byte{"kernel/bzImage": b, "kernel/vmlinux": v} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(out, p)), 0o755)
		_ = os.WriteFile(filepath.Join(out, p), body, 0o644)
	}
	if err := VerifyKernel(Options{Out: out}); err != nil {
		t.Errorf("a whole kernel: %v", err)
	}
}

func TestNotesAreFourByteAligned(t *testing.T) {
	for in, want := range map[uint32]uint32{0: 0, 1: 4, 4: 4, 5: 8, 8: 8} {
		if got := align4(in); got != want {
			t.Errorf("align4(%d) = %d, want %d", in, got, want)
		}
	}
	// The smallest note there is: no name, no description, twelve bytes of header.
	if !hasNote(make([]byte, 12), "") {
		t.Error("a twelve-byte note was not read")
	}
	if hasNote(make([]byte, 11), "") {
		t.Error("eleven bytes were read as a note")
	}
	// A description that is not a whole number of words is padded: the next note is after it.
	two := append(note("Go", []byte{1, 2, 3, 4, 5}), note("Xen", nil)...)
	if !hasNote(two, "Xen") {
		t.Error("a note after a padded description was not found")
	}
}

func TestTheKernelSeriesNeedsAMajorAndAMinor(t *testing.T) {
	for version, want := range map[string]string{"7.3.5": "7.3", "7.3": "7.3", "10.11.12.13": "10.11", "7": "", "7.": "", ".3": ""} {
		v := &versions.Versions{Entries: []versions.Entry{{Name: "kernel", Version: version}}}
		got, err := KernelSeries(v)
		if got != want || (err != nil) != (want == "") {
			t.Errorf("KernelSeries(%q) = %q, %v; want %q", version, got, err, want)
		}
	}
}

func TestARunIsInTheRepositoryAndSaysWhatFailed(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	script(t, filepath.Join(dir, "where"), "pwd; exit 4")
	var out bytes.Buffer
	err := run(context.Background(), Options{Root: root, Stdout: &out}, filepath.Join(dir, "where"), "a", "b", "c")
	if strings.TrimSpace(out.String()) != root {
		t.Errorf("ran in %q, want %q", out.String(), root)
	}
	if err == nil || !strings.HasPrefix(err.Error(), filepath.Join(dir, "where")+" a b: ") {
		t.Errorf("the failure says %v; want the program and its first two arguments", err)
	}
	if err := run(context.Background(), Options{Root: root}, filepath.Join(dir, "where")); err == nil || !strings.HasPrefix(err.Error(), filepath.Join(dir, "where")+" : ") {
		t.Errorf("a failure with no arguments says %v", err)
	}
}

func TestACurrentPartIsSaidWithItsDigest(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "part"), 0o755)
	var said bytes.Buffer
	o := Options{Root: root, Out: out, Pins: &versions.Versions{}, Stderr: &said}
	made := 0
	var checked error
	p := fakePart(&made, &checked, nil)
	r, err := Make(context.Background(), o, []Part{p})
	if err != nil {
		t.Fatal(err)
	}
	said.Reset()
	if _, err := Make(context.Background(), o, []Part{p}); err != nil {
		t.Fatal(err)
	}
	if want := "fake: current (" + r[0].Inputs[:12] + "…)\n"; said.String() != want {
		t.Errorf("said %q, want %q", said.String(), want)
	}
}
