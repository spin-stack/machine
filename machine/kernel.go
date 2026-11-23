// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"debug/elf"
	"fmt"
	"os"
)

// The Xen ELF notes QEMU reads from a kernel it enters through PVH (xen/include/public/elfnote.h).
const (
	// xenPhys32Entry is the 32-bit entry, pvh_start_xen: QEMU enters an ELF kernel through PVH,
	// with pvh.bin, only when it has one.
	xenPhys32Entry = 18
	// xenPhys32Reloc says the kernel runs from any aligned address in a range. QEMU puts it at a
	// random one (qemu/patches/0008), the physical base a bzImage's decompressor chose before.
	xenPhys32Reloc = 19
)

// PVH is whether the file at path is a kernel the machine enters through PVH at a random base: a
// 64-bit ELF with both Xen notes. A vmlinux without the relocation note boots, at the address it
// was linked for, and that is what a release from before #645 would have done: it is refused.
func PVH(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("reading %s as an ELF kernel: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if f.Class != elf.ELFCLASS64 {
		return fmt.Errorf("%s is a %s ELF, and the machine enters a 64-bit kernel", path, f.Class)
	}
	for _, note := range []uint32{xenPhys32Entry, xenPhys32Reloc} {
		if !hasXenNote(f, note) {
			return fmt.Errorf("%s has no Xen ELF note %d: QEMU cannot enter it through PVH at a random physical base", path, note)
		}
	}
	return nil
}

// hasXenNote is whether f has a note named "Xen" of type t in a PT_NOTE segment, where QEMU looks.
func hasXenNote(f *elf.File, t uint32) bool {
	for _, p := range f.Progs {
		if p.Type != elf.PT_NOTE {
			continue
		}
		b := make([]byte, p.Filesz)
		if _, err := p.ReadAt(b, 0); err != nil {
			continue
		}
		for len(b) >= 12 { // mutate-exempt: a header alone, 12 bytes, has no room for the name "Xen" and matches nothing
			namesz, descsz, typ := f.ByteOrder.Uint32(b), f.ByteOrder.Uint32(b[4:]), f.ByteOrder.Uint32(b[8:])
			next := 12 + (int(namesz)+3)&^3 + (int(descsz)+3)&^3
			if next > len(b) {
				break
			}
			if typ == t && namesz == 4 && string(b[12:16]) == "Xen\x00" {
				return true
			}
			b = b[next:]
		}
	}
	return false
}

// bootable refuses a kernel the machine does not enter: one that is neither a bzImage, "HdrS" at
// 0x202 (Documentation/arch/x86/boot.rst), nor an ELF kernel entered through PVH. QEMU given
// anything else tries it as a multiboot kernel, and the failure would be a guest that never
// starts, not a sentence.
func bootable(path string) error {
	f, err := os.Open(path) //nolint:gosec // the kernel the Spec names
	if err != nil {
		return fmt.Errorf("opening kernel %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := f.ReadAt(magic, 0); err == nil && string(magic) == elf.ELFMAG {
		return PVH(path)
	}
	if _, err := f.ReadAt(magic, 0x202); err != nil || string(magic) != "HdrS" {
		return fmt.Errorf("%s is neither a bzImage (no HdrS at 0x202) nor an ELF kernel, the two the machine enters", path)
	}
	return nil
}

// kernelROM is the option ROM QEMU enters the kernel at path with: pvh.bin for an ELF kernel,
// linuxboot_dma.bin for a bzImage. Spec.Args refuses a kernel that is neither (bootable).
func kernelROM(path string) string {
	f, err := os.Open(path) //nolint:gosec // the kernel the Spec names
	if err != nil {
		return bzImageROM
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := f.ReadAt(magic, 0); err == nil && string(magic) == elf.ELFMAG {
		return pvhROM
	}
	return bzImageROM
}
