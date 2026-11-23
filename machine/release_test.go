// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/machine/internal/elftest"
)

// tree writes a release-shaped directory containing exactly the given files.
func tree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var whole = []string{qemuName, qemuImgName, bzImageName, filepath.Join(firmwareDir, bzImageROM), filepath.Join(firmwareDir, DefaultBIOS)}

func TestOpenReleaseNamesWhatIsMissing(t *testing.T) {
	// One file at a time, because a release with a hole in it is the failure
	// this exists to turn into a sentence: the message has to say which file.
	for _, absent := range whole {
		t.Run(absent, func(t *testing.T) {
			var kept []string
			for _, f := range whole {
				if f != absent {
					kept = append(kept, f)
				}
			}
			if _, err := OpenRelease(tree(t, kept...)); err == nil {
				t.Errorf("OpenRelease succeeded on a tree with no %s", absent)
			} else if !strings.Contains(err.Error(), filepath.Base(absent)) {
				t.Errorf("error does not name %s: %v", absent, err)
			}
		})
	}
}

// A release from before v20261119.02 carries a vmlinux that cannot move, pvh.bin and no
// bzImage. Entered, it ran where it was linked, so it is refused, and the refusal names what it
// lacks.
func TestOpenReleaseRefusesAReleaseWithoutABzImage(t *testing.T) {
	old := tree(t, qemuName, qemuImgName, vmlinuxName, filepath.Join(firmwareDir, pvhROM), filepath.Join(firmwareDir, DefaultBIOS))
	if _, err := OpenRelease(old); err == nil || !strings.Contains(err.Error(), bzImageName) {
		t.Errorf("OpenRelease of a release without a bzImage returned %v", err)
	}
	// Nor is an ELF with the entry note alone: QEMU would enter it where it was linked.
	writeKernel(t, filepath.Join(old, vmlinuxName), elftest.Kernel([]uint32{xenPhys32Entry}, nil))
	if _, err := OpenRelease(old); err == nil || !strings.Contains(err.Error(), bzImageName) {
		t.Errorf("OpenRelease of a release whose vmlinux cannot move returned %v", err)
	}
}

// A vmlinux that QEMU places at a random physical base and that moves its own virtual base is
// what a release boots, through PVH, whether a bzImage is beside it or not; and pvh.bin, the
// option ROM that enters it, is then part of a whole release.
func TestOpenReleaseBootsAVmlinuxThatRandomizesItself(t *testing.T) {
	dir := tree(t, qemuName, qemuImgName, bzImageName, filepath.Join(firmwareDir, bzImageROM), filepath.Join(firmwareDir, DefaultBIOS))
	writeKernel(t, filepath.Join(dir, vmlinuxName), elftest.Kernel([]uint32{xenPhys32Entry, xenPhys32Reloc}, nil))
	if _, err := OpenRelease(dir); err == nil || !strings.Contains(err.Error(), pvhROM) {
		t.Errorf("OpenRelease without pvh.bin returned %v", err)
	}
	writeKernel(t, filepath.Join(dir, firmwareDir, pvhROM), nil)
	r, err := OpenRelease(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	if s.Kernel != filepath.Join(dir, vmlinuxName) {
		t.Errorf("the release boots %s, not its vmlinux", s.Kernel)
	}
	if !slices.Contains(s.Files(), filepath.Join(dir, firmwareDir, pvhROM)) {
		t.Errorf("a machine of it opens %v, without pvh.bin", s.Files())
	}
}

func writeKernel(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReleaseAcceptsAMachineWithoutTheOptionalParts(t *testing.T) {
	// The TCG binary and the base image are not required, and asking for one
	// that is absent must fail where it is asked for rather than at OpenRelease.
	r, err := OpenRelease(tree(t, whole...))
	if err != nil {
		t.Fatalf("OpenRelease on a whole machine: %v", err)
	}
	if _, err := r.Rootfs(); err == nil {
		t.Error("Rootfs returned a path for an image that is not there")
	}
	if _, err := r.Spec("tcg"); err == nil || !strings.Contains(err.Error(), qemuTCGName) {
		t.Errorf("Spec(tcg) on a release without the TCG build returned %v, want an error naming %s", err, qemuTCGName)
	}
	if _, err := r.Spec("kvm"); err != nil {
		t.Errorf("Spec(kvm) needs no TCG build: %v", err)
	}
}

// machine.env, as spin-machine-dev release writes it and as it can arrive damaged.
func TestOpenReleaseReadsTheManifest(t *testing.T) {
	for _, tc := range []struct {
		name string
		// manifest is machine.env's content; nil is a tree with no manifest, which a
		// developer's build directory is.
		manifest *string
		version  string
		kernel   string
		// wantErr is a part of the error, which must say where the damage is.
		wantErr string
	}{
		{name: "no manifest"},
		{name: "comments, blank lines and spaces",
			manifest: new("# a comment\n\nversion=v20260908.01\nkernel_version = 6.19.2\n"),
			version:  "v20260908.01", kernel: "6.19.2"},
		// A truncated or hand-edited manifest is refused rather than read past: the
		// version it would report is of a file nobody wrote.
		{name: "a line that is not key=value",
			manifest: new("version=v20260908.01\nkernel_version\n"),
			wantErr:  "machine.env:2"},
		{name: "a value with no key",
			manifest: new("=v20260908.01\n"),
			wantErr:  "machine.env:1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tree(t, whole...)
			if tc.manifest != nil {
				if err := os.WriteFile(filepath.Join(dir, manifest), []byte(*tc.manifest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r, err := OpenRelease(dir)
			switch {
			case tc.wantErr != "" && err == nil:
				t.Fatalf("OpenRelease accepted a damaged manifest, version %q", r.Version())
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error does not say where the damage is (%s): %v", tc.wantErr, err)
			case tc.wantErr != "":
				return
			case err != nil:
				t.Fatal(err)
			}
			if got := r.Version(); got != tc.version {
				t.Errorf("Version = %q, want %q", got, tc.version)
			}
			if got := r.env["kernel_version"]; got != tc.kernel {
				t.Errorf("kernel_version = %q, want %q", got, tc.kernel)
			}
		})
	}
}

// Release.Spec pairs each accelerator with the QEMU built for it: the ordinary build refuses
// -accel tcg, and the TCG build is what a machine with no /dev/kvm runs.
func TestSpecTakesItsPathsFromTheRelease(t *testing.T) {
	dir := tree(t, append(whole, rootfsName, qemuTCGName)...)
	r, err := OpenRelease(dir)
	if err != nil {
		t.Fatal(err)
	}

	for accel, qemu := range map[string]string{"": qemuName, "kvm": qemuName, "tcg": qemuTCGName} {
		s, err := r.Spec(accel)
		if err != nil {
			t.Fatalf("Spec(%q): %v", accel, err)
		}
		// The rest of a machine is about one VM and is the caller's to fill in.
		want := Spec{
			QEMU:     filepath.Join(dir, qemu),
			Kernel:   filepath.Join(dir, bzImageName),
			Firmware: filepath.Join(dir, firmwareDir),
			Accel:    accel,
		}
		if !reflect.DeepEqual(s, want) {
			t.Errorf("Spec(%q)\n got %+v\nwant %+v", accel, s, want)
		}
	}
}
