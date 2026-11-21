// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// qcow2 writes a qcow2 header at path naming backing, and its format in the backing format
// extension when format is not empty, for a disk of 16 MiB in clusters of 64 KiB. Version 2 has
// no header_length field: its extensions start at byte 72. The file is the header alone, as
// short as the reader may be given.
func qcow2(t *testing.T, path string, version uint32, backing, format string) {
	t.Helper()
	b := make([]byte, 1024)
	binary.BigEndian.PutUint32(b[0:], qcow2Magic)
	binary.BigEndian.PutUint32(b[4:], version)
	binary.BigEndian.PutUint32(b[20:], 16)
	binary.BigEndian.PutUint64(b[24:], 16<<20)
	headerLength := uint32(v2HeaderLength)
	if version >= 3 {
		headerLength = v3HeaderLength
		binary.BigEndian.PutUint32(b[100:], headerLength)
	}
	if backing != "" {
		binary.BigEndian.PutUint64(b[8:], 512)
		binary.BigEndian.PutUint32(b[16:], uint32(len(backing))) // #nosec G115 -- a test's name
		copy(b[512:], backing)
	}
	ext := b[headerLength:]
	if format != "" {
		// Two extensions this reader skips first: one padded to eight bytes, one eight long.
		binary.BigEndian.PutUint32(ext[0:], 0x6803f857)
		binary.BigEndian.PutUint32(ext[4:], 3)
		ext = ext[16:]
		binary.BigEndian.PutUint32(ext[0:], 0x44415441)
		binary.BigEndian.PutUint32(ext[4:], 8)
		ext = ext[16:]
		binary.BigEndian.PutUint32(ext[0:], backingFormatExtension)
		binary.BigEndian.PutUint32(ext[4:], uint32(len(format))) // #nosec G115 -- a test's format
		copy(ext[8:], format)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// patch writes b at offset off of the file at path.
func patch(t *testing.T, path string, off int64, b ...byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0) // #nosec G304 -- a file this test made
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

func writable(t *testing.T, f *os.File) bool {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFL, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	return flags&syscall.O_ACCMODE == syscall.O_RDWR
}

// A disk's chain is read from its headers: the top opened for writing unless the disk is
// read-only, every image under it read-only, each under the name it was reached by, which QEMU is
// told as its path.
func TestADisksChainIsOpenedFromItsHeaders(t *testing.T) {
	dir, shared := t.TempDir(), t.TempDir()
	at := func(name string) string { return filepath.Join(shared, name) }
	if err := os.WriteFile(at("base.raw"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	qcow2(t, at("base.qcow2"), 3, "", "")
	qcow2(t, at("mid.qcow2"), 2, at("base.qcow2"), "qcow2")
	// A relative name is the backing file's beside the image, as QEMU reads it.
	qcow2(t, at("sealed.qcow2"), 3, "mid.qcow2", "qcow2")
	top := filepath.Join(dir, "top.qcow2")
	qcow2(t, top, 3, at("sealed.qcow2"), "qcow2")
	overRaw := filepath.Join(dir, "over-raw.qcow2")
	qcow2(t, overRaw, 3, at("base.raw"), "raw")

	for _, tc := range []struct {
		name     string
		d        DiskPath
		want     []string
		formats  []string
		readonly bool
	}{
		{"a qcow2 chain, the shared images named by their directory",
			DiskPath{Path: top, Format: "qcow2", Under: []string{shared}},
			[]string{top, at("sealed.qcow2"), at("mid.qcow2"), at("base.qcow2")}, []string{"qcow2", "qcow2", "qcow2", "qcow2"}, false},
		{"over a raw base named as a file",
			DiskPath{Path: overRaw, Format: "qcow2", Under: []string{at("base.raw")}},
			[]string{overRaw, at("base.raw")}, []string{"qcow2", "raw"}, false},
		{"read-only, its top one of the shared images",
			DiskPath{Path: at("sealed.qcow2"), Format: "qcow2", Readonly: true, Under: []string{shared}},
			[]string{at("sealed.qcow2"), at("mid.qcow2"), at("base.qcow2")}, []string{"qcow2", "qcow2", "qcow2"}, true},
		{"raw, which names nothing",
			DiskPath{Path: at("base.raw"), Format: "raw", Readonly: true},
			[]string{at("base.raw")}, []string{"raw"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.d.Serial = "root"
			d, err := OpenDisk(tc.d)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Close() }()
			if d.Serial != "root" || d.Readonly != tc.readonly || d.Locking == tc.readonly || len(d.Images) != len(tc.want) {
				t.Fatalf("disk %+v, want %d images, read-only %t and locked %t", d, len(tc.want), tc.readonly, !tc.readonly)
			}
			if err := d.validate(); err != nil {
				t.Errorf("a disk OpenDisk opened does not validate: %v", err)
			}
			for j, img := range d.Images {
				if img.File.Name() != tc.want[j] || img.Format != tc.formats[j] || writable(t, img.File) != (j == 0 && !tc.readonly) {
					t.Errorf("image %d is %s, %s, writable %t; want %s, %s, writable %t", j,
						img.File.Name(), img.Format, writable(t, img.File), tc.want[j], tc.formats[j], j == 0 && !tc.readonly)
				}
			}
		})
	}

	d, err := OpenDisk(DiskPath{Serial: "root", Path: overRaw, Format: "qcow2", Under: []string{shared}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Images[0].Size != 16<<20 || d.Images[1].Size != 4096 {
		t.Errorf("sizes %d and %d, want the header's 16 MiB and the raw file's 4096 bytes", d.Images[0].Size, d.Images[1].Size)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Images[0].File.Close(); err == nil {
		t.Error("Close left the top open")
	}
}

// What a header names is whoever wrote the image's choice, the guest's for an image it wrote: a
// chain that leaves the places its images may be, comes back on itself or reads a file that is
// not an image is refused, naming the file, and so is a writable top that is the shared base.
func TestADisksChainIsRefusedWhereItLeavesWhatItMayRead(t *testing.T) {
	dir, shared, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	base := filepath.Join(shared, "base.qcow2")
	qcow2(t, base, 3, "", "")
	outside := filepath.Join(elsewhere, "outside.qcow2")
	qcow2(t, outside, 3, "", "")
	if err := os.Mkdir(shared+"-more", 0o700); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(shared+"-more", "base.qcow2")
	qcow2(t, sibling, 3, "", "")
	// A link where the images may be, to a file where they may not.
	if err := os.Symlink(outside, filepath.Join(shared, "escape.qcow2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "loop"), filepath.Join(shared, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, at("base-link.qcow2")); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(shared, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Two shared images, each the other's backing file.
	qcow2(t, filepath.Join(shared, "a.qcow2"), 3, "b.qcow2", "qcow2")
	qcow2(t, filepath.Join(shared, "b.qcow2"), 3, "a.qcow2", "qcow2")

	overlay := func(name, backing, format string) string {
		qcow2(t, at(name), 3, backing, format)
		return at(name)
	}
	header := func(name string, edit func(path string)) string {
		p := overlay(name, base, "qcow2")
		edit(p)
		return p
	}
	be := binary.BigEndian
	put32 := func(v uint32) []byte { return be.AppendUint32(nil, v) }
	put64 := func(v uint64) []byte { return be.AppendUint64(nil, v) }
	if err := os.WriteFile(at("not.qcow2"), make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at("empty.qcow2"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	under := []string{shared}

	for _, tc := range []struct {
		name string
		d    DiskPath
		want string
	}{
		{"a backing file outside the places given",
			DiskPath{Path: overlay("out.qcow2", outside, "qcow2"), Format: "qcow2", Under: under},
			"names " + outside + " (" + outside + "), which is under none of"},
		{"a backing file beside the places given, its directory's name theirs and more",
			DiskPath{Path: overlay("sibling.qcow2", sibling, "qcow2"), Format: "qcow2", Under: under},
			"names " + sibling + " (" + sibling + "), which is under none of"},
		{"/etc/passwd, as a raw backing file",
			DiskPath{Path: overlay("passwd.qcow2", "/etc/passwd", "raw"), Format: "qcow2", Under: under},
			"names /etc/passwd (/etc/passwd), which is under none of"},
		{"a link among the shared images to a file outside them",
			DiskPath{Path: overlay("escape.qcow2", filepath.Join(shared, "escape.qcow2"), "qcow2"), Format: "qcow2", Under: under},
			"names " + filepath.Join(shared, "escape.qcow2") + " (" + outside + "), which is under none of"},
		{"a link that is its own target",
			DiskPath{Path: overlay("loop.qcow2", filepath.Join(shared, "loop"), "qcow2"), Format: "qcow2", Under: under},
			"opening " + filepath.Join(shared, "loop") + ": too many levels of symbolic links"},
		{"the base as a writable top",
			DiskPath{Path: base, Format: "qcow2", Under: under},
			base + " (" + base + ") is one of the images a disk may only read"},
		{"the base as a writable top, named by a link to it",
			DiskPath{Path: at("base-link.qcow2"), Format: "qcow2", Under: []string{base}},
			at("base-link.qcow2") + " (" + base + ") is one of the images a disk may only read"},
		{"a backing file that is the top itself",
			DiskPath{Path: overlay("self.qcow2", "self.qcow2", "qcow2"), Format: "qcow2", Under: under},
			"names " + at("self.qcow2") + " (" + at("self.qcow2") + "), which is under none of"},
		{"a backing file that is the read-only top itself",
			DiskPath{Path: at("self.qcow2"), Format: "qcow2", Readonly: true, Under: []string{dir}},
			"the chain under " + at("self.qcow2") + " comes back to " + at("self.qcow2")},
		{"a chain that comes back on itself",
			DiskPath{Path: overlay("cycle.qcow2", filepath.Join(shared, "a.qcow2"), "qcow2"), Format: "qcow2", Under: under},
			"the chain under " + at("cycle.qcow2") + " comes back to " + filepath.Join(shared, "a.qcow2")},
		{"a FIFO, which is not opened to find out",
			DiskPath{Path: overlay("fifo.qcow2", fifo, "raw"), Format: "qcow2", Under: under},
			fifo + " (" + fifo + ") is not a regular file"},
		{"a directory",
			DiskPath{Path: overlay("dir.qcow2", shared, "raw"), Format: "qcow2", Under: under},
			shared + " (" + shared + ") is not a regular file"},
		{"a backing file that does not exist",
			DiskPath{Path: overlay("gone.qcow2", filepath.Join(shared, "gone"), "qcow2"), Format: "qcow2", Under: under},
			"opening " + filepath.Join(shared, "gone") + ": no such file or directory"},
		{"a backing format not named",
			DiskPath{Path: overlay("unnamed.qcow2", base, ""), Format: "qcow2", Under: under},
			at("unnamed.qcow2") + " names its backing file " + base + " and not its format, which is not guessed"},
		{"a backing format that is neither",
			DiskPath{Path: overlay("vmdk.qcow2", base, "vmdk"), Format: "qcow2", Under: under},
			base + `: format "vmdk" is not one of [qcow2 raw]`},
		{"a top whose format is neither",
			DiskPath{Path: base, Format: "vmdk", Readonly: true},
			base + `: format "vmdk" is not one of [qcow2 raw]`},
		{"an encrypted image",
			DiskPath{Path: header("crypt.qcow2", func(p string) { patch(t, p, 32, put32(1)...) }), Format: "qcow2", Under: under},
			at("crypt.qcow2") + " is encrypted"},
		{"an external data file",
			DiskPath{Path: header("data.qcow2", func(p string) { patch(t, p, 72, put64(externalDataFile)...) }), Format: "qcow2", Under: under},
			at("data.qcow2") + " keeps its data in an external file"},
		{"a version this reader does not know",
			DiskPath{Path: header("v4.qcow2", func(p string) { patch(t, p, 4, put32(4)...) }), Format: "qcow2", Under: under},
			at("v4.qcow2") + " is qcow2 version 4"},
		{"clusters smaller than QEMU allows",
			DiskPath{Path: header("small.qcow2", func(p string) { patch(t, p, 20, put32(8)...) }), Format: "qcow2", Under: under},
			at("small.qcow2") + " has clusters of 2^8 bytes"},
		{"clusters larger than QEMU allows",
			DiskPath{Path: header("large.qcow2", func(p string) { patch(t, p, 20, put32(22)...) }), Format: "qcow2", Under: under},
			at("large.qcow2") + " has clusters of 2^22 bytes"},
		{"a backing name longer than QEMU allows",
			DiskPath{Path: header("long.qcow2", func(p string) { patch(t, p, 16, put32(maxBackingName+1)...) }), Format: "qcow2", Under: under},
			at("long.qcow2") + " names a backing file of 1024 bytes"},
		{"a backing name past the header",
			DiskPath{Path: header("past.qcow2", func(p string) { patch(t, p, 8, put64(1020)...) }), Format: "qcow2", Under: under},
			at("past.qcow2") + " names a backing file past its header"},
		{"a header that claims to be longer than it is",
			DiskPath{Path: header("length.qcow2", func(p string) { patch(t, p, 100, put32(4096)...) }), Format: "qcow2", Under: under},
			at("length.qcow2") + " has a header 4096 bytes long"},
		{"a header shorter than a version 2 header",
			DiskPath{Path: header("short.qcow2", func(p string) { patch(t, p, 100, put32(71)...) }), Format: "qcow2", Under: under},
			at("short.qcow2") + " has a header 71 bytes long"},
		{"an extension past the header",
			DiskPath{Path: header("ext.qcow2", func(p string) { patch(t, p, v3HeaderLength+4, put32(4096)...) }), Format: "qcow2", Under: under},
			at("ext.qcow2") + ": its header extensions run past its header"},
		{"extensions with no end",
			DiskPath{Path: header("noend.qcow2", func(p string) {
				for off := int64(v3HeaderLength); off < 1024; off += 8 {
					patch(t, p, off, put32(1)...)
				}
			}), Format: "qcow2", Under: under},
			at("noend.qcow2") + ": its header extensions run past its header"},
		{"not a qcow2 image",
			DiskPath{Path: at("not.qcow2"), Format: "qcow2", Readonly: true},
			at("not.qcow2") + " is not a qcow2 image"},
		{"an empty file",
			DiskPath{Path: at("empty.qcow2"), Format: "qcow2", Readonly: true},
			at("empty.qcow2") + " is not a qcow2 image: 0 bytes of header"},
		{"a version 3 header cut short",
			DiskPath{Path: header("cut.qcow2", func(p string) {
				if err := os.Truncate(p, 80); err != nil {
					t.Fatal(err)
				}
			}), Format: "qcow2", Under: under},
			at("cut.qcow2") + " is not a qcow2 image: 80 bytes of a version 3 header"},
		{"no path", DiskPath{Format: "raw"}, `disk "root": no path`},
		{"a place to read that does not exist",
			DiskPath{Path: base, Format: "qcow2", Readonly: true, Under: []string{filepath.Join(shared, "absent")}},
			"resolving " + filepath.Join(shared, "absent") + ", where a disk's images may be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.d.Serial = "root"
			d, err := OpenDisk(tc.d)
			if err == nil {
				_ = d.Close()
				t.Fatalf("OpenDisk opened %+v, want an error saying %q", d, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("OpenDisk = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}
