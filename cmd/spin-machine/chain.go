// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"
)

// link is one image of a disk's chain as its header names it.
type link struct {
	path, format string
}

// chain reads the backing chain of the image at path, top first, from the images' own headers.
//
// Read here and not by running `qemu-img info --backing-chain`, because boot and restore read it
// on the path a probe times: an exec of qemu-img is milliseconds a cold boot or a resume would
// carry and spin, which hands its images over open, never pays.
//
// A qcow2 image's backing format is read from its header too, and an image that names a backing
// file and not its format is refused: the format is never guessed.
func chain(path, format string) ([]link, error) {
	var out []link
	seen := map[string]bool{}
	for path != "" {
		if seen[path] {
			return nil, fmt.Errorf("%s: a backing chain that comes back to %s", out[0].path, path)
		}
		seen[path] = true
		out = append(out, link{path, format})
		if format == "raw" {
			break
		}
		if format != "qcow2" {
			return nil, fmt.Errorf("%s: format %q is not qcow2 or raw", path, format)
		}
		backing, backingFormat, err := qcow2Backing(path)
		if err != nil {
			return nil, err
		}
		if backing != "" && !filepath.IsAbs(backing) {
			backing = filepath.Join(filepath.Dir(path), backing)
		}
		path, format = backing, backingFormat
	}
	return out, nil
}

// qcow2Magic is the first four bytes of every qcow2 image.
const qcow2Magic = 0x514649fb

// backingFormatExtension is the header extension that names the backing file's format.
const backingFormatExtension = 0xe2792aca

// qcow2Backing reads the backing file and its format out of a qcow2 header (QEMU's
// docs/interop/qcow2.rst): the name at backing_file_offset, and the format in the header
// extension of type 0xe2792aca, which follows the header at header_length (72 bytes in a version
// 2 image, which has no such field).
func qcow2Backing(path string) (backing, format string, err error) {
	f, err := os.Open(path) // #nosec G304 -- an image the caller named
	if err != nil {
		return "", "", fmt.Errorf("reading the header of %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	// The header up to header_length, every field in order; a version 2 header ends at
	// incompatible_features, where its extensions start.
	var h struct {
		Magic, Version                      uint32
		BackingOffset                       uint64
		BackingSize, ClusterBits            uint32
		Size                                uint64
		CryptMethod, L1Size                 uint32
		L1Offset, RefcountOffset            uint64
		RefcountClusters, Snapshots         uint32
		SnapshotsOffset                     uint64
		Incompatible, Compatible, Autoclear uint64
		RefcountOrder, HeaderLength         uint32
	}
	if err := binary.Read(f, binary.BigEndian, &h); err != nil {
		return "", "", fmt.Errorf("%s is not a qcow2 image: %w", path, err)
	}
	if h.Magic != qcow2Magic {
		return "", "", fmt.Errorf("%s is not a qcow2 image", path)
	}
	if h.BackingOffset == 0 {
		return "", "", nil
	}
	name := make([]byte, h.BackingSize)
	if _, err := f.ReadAt(name, int64(h.BackingOffset)); err != nil {
		return "", "", fmt.Errorf("reading the backing file %s names: %w", path, err)
	}
	extensions := int64(unsafe.Offsetof(h.Incompatible))
	if h.Version >= 3 {
		extensions = int64(h.HeaderLength)
	}
	if _, err := f.Seek(extensions, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("reading the header extensions of %s: %w", path, err)
	}
	r := f
	for {
		var ext struct{ Type, Length uint32 }
		if err := binary.Read(r, binary.BigEndian, &ext); err != nil {
			return "", "", fmt.Errorf("reading the header extensions of %s: %w", path, err)
		}
		if ext.Type == 0 {
			return "", "", fmt.Errorf("%s names its backing file %s and not its format, which is not guessed", path, name)
		}
		data := make([]byte, (ext.Length+7)&^7)
		if _, err := io.ReadFull(r, data); err != nil {
			return "", "", fmt.Errorf("reading the header extensions of %s: %w", path, err)
		}
		if ext.Type == backingFormatExtension {
			return string(name), string(data[:ext.Length]), nil
		}
	}
}
