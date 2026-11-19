// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// qcow2 writes a qcow2 header at path naming backing, and its format in the backing format
// extension when format is not empty. Version 2 has no header_length field: its extensions
// start at byte 72.
func qcow2(t *testing.T, path string, version uint32, backing, format string) {
	t.Helper()
	b := make([]byte, 1024)
	binary.BigEndian.PutUint32(b[0:], qcow2Magic)
	binary.BigEndian.PutUint32(b[4:], version)
	headerLength := uint32(72)
	if version >= 3 {
		headerLength = 104
		binary.BigEndian.PutUint32(b[100:], headerLength)
	}
	if backing != "" {
		binary.BigEndian.PutUint64(b[8:], 512)
		binary.BigEndian.PutUint32(b[16:], uint32(len(backing)))
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
		binary.BigEndian.PutUint32(ext[4:], uint32(len(format)))
		copy(ext[8:], format)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestChainIsReadFromTheHeaders(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	if err := os.WriteFile(at("base.raw"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	qcow2(t, at("base.qcow2"), 3, "", "")
	qcow2(t, at("mid.qcow2"), 2, at("base.qcow2"), "qcow2")
	// A relative name is the backing file's beside the image, as QEMU reads it.
	qcow2(t, at("top.qcow2"), 3, "mid.qcow2", "qcow2")
	qcow2(t, at("over-raw.qcow2"), 3, "base.raw", "raw")
	qcow2(t, at("unnamed.qcow2"), 3, "base.qcow2", "")
	qcow2(t, at("loop.qcow2"), 3, "loop.qcow2", "qcow2")
	qcow2(t, at("vmdk.qcow2"), 3, "base.qcow2", "vmdk")
	if err := os.WriteFile(at("not.qcow2"), make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at("empty.qcow2"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path, format string
		want         []link
		wantErr      string
	}{
		{path: "top.qcow2", format: "qcow2", want: []link{{at("top.qcow2"), "qcow2"}, {at("mid.qcow2"), "qcow2"}, {at("base.qcow2"), "qcow2"}}},
		{path: "over-raw.qcow2", format: "qcow2", want: []link{{at("over-raw.qcow2"), "qcow2"}, {at("base.raw"), "raw"}}},
		{path: "base.raw", format: "raw", want: []link{{at("base.raw"), "raw"}}},
		{path: "unnamed.qcow2", format: "qcow2", wantErr: "not its format, which is not guessed"},
		{path: "loop.qcow2", format: "qcow2", wantErr: "a backing chain that comes back to"},
		{path: "vmdk.qcow2", format: "qcow2", wantErr: `format "vmdk" is not qcow2 or raw`},
		{path: "not.qcow2", format: "qcow2", wantErr: "is not a qcow2 image"},
		{path: "empty.qcow2", format: "qcow2", wantErr: "is not a qcow2 image: EOF"},
		{path: "absent.qcow2", format: "qcow2", wantErr: "no such file"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := chain(at(tc.path), tc.format)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("chain = %v, %v, want an error saying %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("chain = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}
