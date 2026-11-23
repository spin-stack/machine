// SPDX-License-Identifier: Apache-2.0

// Package elftest writes the smallest ELF file that says what a kernel entered through PVH says
// about itself, for tests of what reads one: its Xen notes and its .data.reloc.
package elftest

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
)

// Kernel is a 64-bit ELF with a PT_NOTE segment holding a "Xen" note of each type in notes, and,
// unless reloc is nil, a .data.reloc section holding reloc.
func Kernel(notes []uint32, reloc []byte) []byte {
	var note bytes.Buffer
	for _, n := range notes {
		desc := make([]byte, 12) // PHYS32_RELOC's alignment, minimum and maximum; the others read 4
		_ = binary.Write(&note, binary.LittleEndian, [3]uint32{4, uint32(len(desc)), n})
		note.WriteString("Xen\x00")
		note.Write(desc)
	}
	const ehsize, phsize, shsize = 64, 56, 64
	noteOff := uint64(ehsize + phsize)
	relocOff := noteOff + uint64(note.Len())
	strtab := []byte("\x00.data.reloc\x00.shstrtab\x00")
	strOff := relocOff + uint64(len(reloc))
	shOff := strOff + uint64(len(strtab))

	sections := []elf.Section64{{}}
	if reloc != nil {
		sections = append(sections, elf.Section64{Name: 1, Type: uint32(elf.SHT_PROGBITS), Flags: uint64(elf.SHF_ALLOC | elf.SHF_WRITE), Off: relocOff, Size: uint64(len(reloc)), Addralign: 4})
	}
	sections = append(sections, elf.Section64{Name: 13, Type: uint32(elf.SHT_STRTAB), Off: strOff, Size: uint64(len(strtab)), Addralign: 1})

	var b bytes.Buffer
	h := elf.Header64{
		Type: uint16(elf.ET_EXEC), Machine: uint16(elf.EM_X86_64), Version: uint32(elf.EV_CURRENT),
		Phoff: ehsize, Shoff: shOff, Ehsize: ehsize, Phentsize: phsize, Phnum: 1,
		Shentsize: shsize, Shnum: uint16(len(sections)), Shstrndx: uint16(len(sections) - 1),
	}
	copy(h.Ident[:], elf.ELFMAG)
	h.Ident[elf.EI_CLASS], h.Ident[elf.EI_DATA], h.Ident[elf.EI_VERSION] = byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)
	_ = binary.Write(&b, binary.LittleEndian, h)
	_ = binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_NOTE), Off: noteOff, Filesz: uint64(note.Len()), Align: 4})
	b.Write(note.Bytes())
	b.Write(reloc)
	b.Write(strtab)
	for _, s := range sections {
		_ = binary.Write(&b, binary.LittleEndian, s)
	}
	return b.Bytes()
}
