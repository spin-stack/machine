// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// DiskPath is a disk named by the path of its top image, for OpenDisk to open with the chain
// under it.
type DiskPath struct {
	// Serial, Readonly: as the Disk's.
	Serial   string
	Readonly bool
	// Path is the top image's, and Format its format: qcow2 or raw, never guessed.
	Path, Format string
	// Under is where the images below the top may be: each entry a file, or a directory any
	// file beneath which may be one, symbolic links resolved. These are the images many disks
	// share, the base among them, so a writable top is refused when it is one of them.
	Under []string
}

// OpenDisk opens a disk's top image and every image under it, reading the chain from the
// images' own qcow2 headers: the top read-write unless the disk is read-only, every other image
// read-only, and the shared base never for writing whatever it is passed (rule 1 in CLAUDE.md).
//
// A backing file a header names is what the guest or whoever wrote the image chose, so each is
// opened without reading it (O_PATH), resolved to the file it is through the descriptor, and
// refused unless it is a regular file under d.Under; only then is it reopened through the same
// descriptor to read. What was checked is what QEMU is handed, with no path between the two for
// a rename to swap.
//
// The headers are read here and not by `qemu-img info --backing-chain`, because boot and restore
// open their disks on the goal's clock: on one three-image chain over the base, qemu-img took
// 3.4 ms p50 and OpenDisk 0.05 ms (laptop, qemu-img 10.2.1, 2026-11-21). Reading in
// process is also why no sandbox is needed: spin ran qemu-img under Landlock so that its parser
// could open nothing a header named, and this reader opens nothing it has not checked, executes
// nothing, and bounds every read by the header's first cluster.
func OpenDisk(d DiskPath) (Disk, error) {
	if d.Path == "" {
		return Disk{}, fmt.Errorf("disk %q: no path", d.Serial)
	}
	under, err := resolveAll(d.Under)
	if err != nil {
		return Disk{}, err
	}
	disk := Disk{Serial: d.Serial, Readonly: d.Readonly,
		// The lock lets a caller outside the VMM find out whether a VM has the top open; the
		// images under it are shared, read by every VM over them, and take none.
		Locking: !d.Readonly}
	var seen []fileID
	path, format := d.Path, d.Format
	for path != "" {
		writable := len(disk.Images) == 0 && !d.Readonly
		f, id, size, backing, err := openImage(path, format, writable, len(disk.Images) > 0, under)
		if err == nil && slices.Contains(seen, id) {
			_ = f.Close()
			err = fmt.Errorf("the chain under %s comes back to %s", d.Path, path)
		}
		if err != nil {
			_ = disk.Close()
			return Disk{}, err
		}
		seen = append(seen, id)
		disk.Images = append(disk.Images, Image{File: f, Format: format, Size: size})
		path, format = backing.path, backing.format
		if path != "" && !filepath.IsAbs(path) {
			// A relative name is beside the image that names it, as QEMU reads it.
			path = filepath.Join(filepath.Dir(disk.Images[len(disk.Images)-1].File.Name()), path)
		}
	}
	return disk, nil
}

// Close closes every image's file: a VMM bound to the disk holds copies of its own.
func (d Disk) Close() error {
	var errs []error
	for _, img := range d.Images {
		if img.File != nil {
			errs = append(errs, img.File.Close())
		}
	}
	return errors.Join(errs...)
}

// resolveAll resolves each place an image may be to the file or directory it is.
func resolveAll(places []string) ([]string, error) {
	out := make([]string, 0, len(places))
	for _, p := range places {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, fmt.Errorf("resolving %s, where a disk's images may be: %w", p, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// within says whether the file at real is one of places or beneath one.
func within(real string, places []string) bool {
	return slices.ContainsFunc(places, func(p string) bool {
		return real == p || strings.HasPrefix(real, p+"/")
	})
}

// fileID is which file an image is, whatever path reached it.
type fileID struct{ dev, ino uint64 }

// link is the backing file a qcow2 header names, and its format.
type link struct{ path, format string }

// openImage opens the image at path for the disk, refusing it unless it is a regular file and,
// below the top, one under the places given, and reads the backing file its header names. The
// file keeps the name it was reached by, which QEMU is told as the image's path (addImages).
func openImage(path, format string, writable, below bool, under []string) (_ *os.File, id fileID, size int64, backing link, err error) {
	if !slices.Contains(diskFormats, format) {
		return nil, id, 0, link{}, fmt.Errorf("%s: format %q is not one of %v", path, format, diskFormats)
	}
	// O_PATH reads nothing and opens a FIFO or a device without blocking or touching it.
	pfd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, id, 0, link{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = unix.Close(pfd) }()
	proc := fmt.Sprintf("/proc/self/fd/%d", pfd)
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return nil, id, 0, link{}, fmt.Errorf("reading what %s is: %w", path, err)
	}
	real, err := os.Readlink(proc)
	if err != nil {
		return nil, id, 0, link{}, fmt.Errorf("reading which file %s is: %w", path, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return nil, id, 0, link{}, fmt.Errorf("%s (%s) is not a regular file", path, real)
	case below && !within(real, under):
		return nil, id, 0, link{}, fmt.Errorf("a disk's chain names %s (%s), which is under none of %v, where its images may be", path, real, under)
	case writable && within(real, under):
		return nil, id, 0, link{}, fmt.Errorf("%s (%s) is one of the images a disk may only read, %v, and was asked for as a writable top", path, real, under)
	}
	access := unix.O_RDONLY
	if writable {
		access = unix.O_RDWR
	}
	fd, err := unix.Open(proc, access|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, id, 0, link{}, fmt.Errorf("opening %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	id = fileID{st.Dev, st.Ino}
	size = st.Size
	if format == "qcow2" {
		size, backing, err = readQcow2(f)
	}
	if err != nil {
		_ = f.Close()
		return nil, id, 0, link{}, err
	}
	return f, id, size, backing, nil
}

const (
	// qcow2Magic is the first four bytes of every qcow2 image.
	qcow2Magic = 0x514649fb
	// backingFormatExtension is the header extension that names the backing file's format.
	backingFormatExtension = 0xe2792aca
	// v2HeaderLength is where a version 2 header ends and its extensions start; version 3 says
	// where in header_length, and is at least v3HeaderLength.
	v2HeaderLength, v3HeaderLength = 72, 104
	// maxBackingName is QEMU's own bound on a backing file's name (qcow2_do_open).
	maxBackingName = 1023
	// externalDataFile is the incompatible feature bit of an image whose data is in another file.
	externalDataFile = 1 << 2
)

// readQcow2 reads the size the guest sees and the backing file a qcow2 header names (QEMU's
// docs/interop/qcow2.rst): the name at backing_file_offset, and its format in the header
// extension of type 0xe2792aca. Everything is read from the first cluster, where QEMU requires
// the header, its extensions and the name to be, so a header that claims more is refused and
// never read past.
//
// An image that names a backing file and not its format is refused, since the format is never
// guessed; so are an encrypted image, which needs a key from outside it, and one with an
// external data file, which is another file the image reads outside its chain.
func readQcow2(f *os.File) (size int64, backing link, err error) {
	path := f.Name()
	head := make([]byte, v3HeaderLength)
	n, err := f.ReadAt(head, 0)
	if n < v2HeaderLength {
		return 0, link{}, fmt.Errorf("%s is not a qcow2 image: %d bytes of header: %w", path, n, err)
	}
	be := binary.BigEndian
	version, clusterBits := be.Uint32(head[4:]), be.Uint32(head[20:])
	backingOffset, backingSize := be.Uint64(head[8:]), be.Uint32(head[16:])
	switch {
	case be.Uint32(head) != qcow2Magic:
		return 0, link{}, fmt.Errorf("%s is not a qcow2 image", path)
	case version != 2 && version != 3:
		return 0, link{}, fmt.Errorf("%s is qcow2 version %d, not 2 or 3", path, version)
	case clusterBits < 9 || clusterBits > 21:
		return 0, link{}, fmt.Errorf("%s has clusters of 2^%d bytes, not 2^9 to 2^21", path, clusterBits)
	case be.Uint32(head[32:]) != 0:
		return 0, link{}, fmt.Errorf("%s is encrypted, which needs a key from outside it", path)
	}
	extensions := uint64(v2HeaderLength)
	if version == 3 {
		if n < v3HeaderLength {
			return 0, link{}, fmt.Errorf("%s is not a qcow2 image: %d bytes of a version 3 header", path, n)
		}
		if be.Uint64(head[72:])&externalDataFile != 0 {
			return 0, link{}, fmt.Errorf("%s keeps its data in an external file, outside its chain", path)
		}
		extensions = uint64(be.Uint32(head[100:]))
	}
	size = int64(be.Uint64(head[24:])) // #nosec G115 -- a size the guest sees; QEMU bounds it far below 2^63
	if backingOffset == 0 {
		return size, link{}, nil
	}
	cluster := make([]byte, 1<<clusterBits)
	n, err = f.ReadAt(cluster, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, link{}, fmt.Errorf("reading the header of %s: %w", path, err)
	}
	cluster = cluster[:n]
	end := uint64(len(cluster))
	switch {
	case backingSize > maxBackingName:
		return 0, link{}, fmt.Errorf("%s names a backing file of %d bytes, more than %d", path, backingSize, maxBackingName)
	case backingOffset+uint64(backingSize) > end:
		return 0, link{}, fmt.Errorf("%s names a backing file past its header", path)
	case extensions < v2HeaderLength || extensions > end:
		return 0, link{}, fmt.Errorf("%s has a header %d bytes long, which is not within its first cluster", path, extensions)
	}
	name := string(cluster[backingOffset : backingOffset+uint64(backingSize)])
	for off := extensions; ; {
		if off+8 > end {
			return 0, link{}, fmt.Errorf("%s: its header extensions run past its header", path)
		}
		typ, length := be.Uint32(cluster[off:]), uint64(be.Uint32(cluster[off+4:]))
		data := off + 8
		if typ == 0 {
			return 0, link{}, fmt.Errorf("%s names its backing file %s and not its format, which is not guessed", path, name)
		}
		if data+length > end {
			return 0, link{}, fmt.Errorf("%s: its header extensions run past its header", path)
		}
		if typ == backingFormatExtension {
			return size, link{path: name, format: string(cluster[data : data+length])}, nil
		}
		off = data + (length+7)&^7
	}
}
