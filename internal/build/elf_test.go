// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/elftest"
	"github.com/spin-stack/machine/internal/modsign"
)

// A whole kernel is a vmlinux that randomizes itself with its modules beside it, and carries the
// module certificate when the tree has one. Without its modules, or without the certificate, it is
// refused: the image would have nothing to install, or the kernel would load none of them.
func TestAWholeKernelVerifies(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	v := elftest.Kernel([]uint32{18, 19}, []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0x10, 0, 0, 0})
	_ = os.MkdirAll(filepath.Join(out, "kernel"), 0o755)
	_ = os.WriteFile(filepath.Join(out, "kernel/vmlinux"), v, 0o644)
	o := Options{Root: root, Out: out}
	if err := VerifyKernel(o); err == nil || !strings.Contains(err.Error(), "modules") {
		t.Errorf("a kernel without its modules: %v", err)
	}
	fuse := filepath.Join(out, "kernel/modules/7.4.0/kernel/fs/fuse/fuse.ko")
	_ = os.MkdirAll(filepath.Dir(fuse), 0o755)
	_ = os.WriteFile(fuse, []byte("\x7fELF"), 0o644)
	if err := VerifyKernel(o); err != nil {
		t.Errorf("a whole kernel, before the tree has a certificate: %v", err)
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := modsign.Certificate(k)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, "kernel"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ModuleCertificate), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0o644)
	if err := VerifyKernel(o); err == nil || !strings.Contains(err.Error(), "does not carry") {
		t.Errorf("a kernel without the tree's certificate: %v", err)
	}
	_ = os.WriteFile(filepath.Join(out, "kernel/vmlinux"), append(v, cert...), 0o644)
	if err := VerifyKernel(o); err != nil {
		t.Errorf("a whole kernel carrying the certificate: %v", err)
	}
}

// A table is filled when its last word is a relocation, however short it is: one word is enough.
func TestAOneWordRelocationTableIsFilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vmlinux")
	_ = os.WriteFile(path, elftest.Kernel([]uint32{18, 19}, []byte{0x10, 0, 0, 0}), 0o644)
	if err := SelfRandomizing(path); err != nil {
		t.Errorf("a one-word table: %v", err)
	}
}

// A vmlinux that would run where it was linked is refused, each way it can: QEMU cannot place it
// (no PHYS32_RELOC note), it cannot move itself (no .data.reloc), or the relocs tool never filled
// the table it reserved.
func TestAVmlinuxThatCannotMoveIsRefused(t *testing.T) {
	filled := []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0x10, 0, 0, 0}
	for _, tc := range []struct {
		what  string
		notes []uint32
		reloc []byte
		says  string
	}{
		{"no PVH entry", []uint32{19}, filled, "note 18"},
		{"no PHYS32_RELOC", []uint32{18}, filled, "note 19"},
		{"no .data.reloc", []uint32{18, 19}, nil, "no .data.reloc"},
		{"an unfilled table", []uint32{18, 19}, []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}, "empty"},
	} {
		path := filepath.Join(t.TempDir(), "vmlinux")
		_ = os.WriteFile(path, elftest.Kernel(tc.notes, tc.reloc), 0o644)
		if err := SelfRandomizing(path); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v, want an error saying %q", tc.what, err, tc.says)
		}
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
