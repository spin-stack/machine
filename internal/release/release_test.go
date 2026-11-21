// SPDX-License-Identifier: Apache-2.0

package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/go-tools/sbom"
	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/spin-machine/internal/build"
)

func pins(t *testing.T) *versions.Versions {
	t.Helper()
	v, err := versions.Load(filepath.Join("..", "..", versions.File))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func put(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// apkDB is an apk database holding every package of only.
func apkDB(only string) string {
	var b strings.Builder
	for _, p := range strings.Split(only, ",") {
		b.WriteString("P:" + p + "\nV:1.0-r0\nA:x86_64\n\n")
	}
	return b.String() + "P:make\nV:4.4-r0\nA:x86_64\n"
}

// tree is a repository and a built _output a release can be packed from.
func tree(t *testing.T) (root, out string) {
	t.Helper()
	root, out = t.TempDir(), t.TempDir()
	for _, f := range []string{"LICENSE", "NOTICE", "qemu/qboot/COPYING", "qemu/qboot/mtrr.patch", "qemu/qboot/pam.patch", "qemu/patches/0001-a.patch"} {
		put(t, filepath.Join(root, f), f, 0o644)
	}
	for _, b := range append(slices.Clone(build.QEMUBinaries), build.E2fsprogsBinaries...) {
		put(t, filepath.Join(out, "bin", b), b, 0o755)
	}
	for _, f := range build.Firmware {
		put(t, filepath.Join(out, "qemu", f), f, 0o644)
	}
	for _, f := range []string{"e2fsprogs/mke2fs.conf", "kernel/bzImage", "kernel/vmlinux", "kernel/kernel-config", "kernel/sources",
		"kernel/patches/0001-k.patch", "image/rootfs.qcow2", "licenses.txt"} {
		put(t, filepath.Join(out, f), f, 0o644)
	}
	put(t, filepath.Join(out, "packages.txt"), "bash 5.2-1 amd64\nlibc6 2.39-1 amd64\n", 0o644)
	put(t, filepath.Join(out, "sbom/qemu.apk"), apkDB("glib-static,pcre2-static,libffi-dev,pixman-static,zlib-static,libseccomp-static,libcap-ng-static,liburing-dev,libaio-dev,gettext-static,musl-dev,gcc"), 0o644)
	put(t, filepath.Join(out, "sbom/e2fsprogs.apk"), apkDB("libarchive-static,acl-static,zlib-static,xz-static,zstd-static,bzip2-static,lz4-static,expat-static,openssl-libs-static,musl-dev,gcc"), 0o644)
	return root, out
}

var fp = strings.Repeat("f", 64)

func options(t *testing.T, root, out string) Options {
	t.Helper()
	return Options{Root: root, Out: out, Version: "v20261119.07", Pins: pins(t), Commit: "abc", Epoch: time.Unix(1, 0),
		Now:         time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC),
		Fingerprint: func(context.Context, string) (string, error) { return fp, nil }}
}

// listing is the tarball's entries, and their modes, past its top directory.
func listing(t *testing.T, path string) map[string]int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(z)
	got := map[string]int64{}
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, rest, _ := strings.Cut(h.Name, "/")
			got[rest] = h.Mode
		}
	}
}

func TestAReleaseShipsEveryPartAndSaysWhatItIs(t *testing.T) {
	root, out := tree(t)
	o := options(t, root, out)
	packed, err := Pack(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	name := Name(o.Version)
	if packed.Tarball != filepath.Join(out, "release", name+".tar.gz") {
		t.Errorf("tarball at %s", packed.Tarball)
	}
	share := "usr/share/spin-stack/"
	got := listing(t, packed.Tarball)
	want := map[string]int64{"LICENSE": 0o644, "NOTICE": 0o644, share + "image/rootfs.qcow2": 0o444, share + "bin/qemu-img": 0o755,
		share + "bin/debugfs": 0o755, share + "qemu/qboot.bin": 0o644, share + "qemu/qboot-mtrr.patch": 0o644, share + "qemu/qboot-pam.patch": 0o644,
		share + "qemu/qboot-COPYING": 0o644, share + "qemu/patches/0001-a.patch": 0o644, share + "kernel/patches/0001-k.patch": 0o644,
		share + "kernel/bzImage": 0o644, share + "kernel/sources": 0o644, share + "machine.env": 0o644, share + "SOURCES": 0o644,
		share + "e2fsprogs/mke2fs.conf": 0o644, share + "packages.txt": 0o644}
	for f, mode := range want {
		if m, ok := got[f]; !ok || m != mode {
			t.Errorf("%s: in the tarball %v, mode %o; want %o", f, ok, m, mode)
		}
	}
	if len(got) != 31 {
		t.Errorf("the tarball holds %d files, want 31: %v", len(got), slices.Sorted(func(yield func(string) bool) {
			for k := range got {
				if !yield(k) {
					return
				}
			}
		}))
	}
	sum, _ := build.FileSHA256(packed.Tarball)
	if b, _ := os.ReadFile(packed.Tarball + ".sha256"); string(b) != sum+"  "+name+".tar.gz\n" {
		t.Errorf("the tarball's sha256 file says %q", b)
	}
	env, err := os.ReadFile(filepath.Join(out, "release", "machine.env"))
	if err != nil {
		t.Fatal(err)
	}
	m := ParseEnv(string(env))
	vm, _ := build.FileSHA256(filepath.Join(out, "kernel/vmlinux"))
	q, _ := o.Pins.Get("qemu")
	for k, v := range map[string]string{"version": o.Version, "source_commit": "abc", "built": "2026-11-19T01:02:03Z",
		"fingerprint_sha256": fp, "kernel_sha256": vm, "qemu_version": q.Version} {
		if m[k] != v {
			t.Errorf("machine.env %s=%q, want %q", k, m[k], v)
		}
	}
	if !slices.Equal(slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}), slices.Sorted(slices.Values(ManifestKeys))) {
		t.Errorf("machine.env's keys are %v", m)
	}
	if !maps(packed.Manifest, m) {
		t.Errorf("Pack said the manifest is %v, and wrote %v", packed.Manifest, m)
	}
	b, err := sbom.Read(filepath.Join(out, "release", name+".cdx.json"))
	if err != nil || sbom.Check(b) != nil {
		t.Errorf("the tarball's SBOM: %v", err)
	}
	if b.Metadata.Component.Name != name+".tar.gz" || len(*b.Metadata.Component.Hashes) == 0 {
		t.Errorf("the tarball's SBOM describes %+v", b.Metadata.Component)
	}
	inner, err := sbom.Read(filepath.Join(out, "release", name, share, "sbom", "machine.cdx.json"))
	if err != nil || inner.Metadata.Component.Name != "spin-machine" {
		t.Fatalf("the machine's SBOM: %v", err)
	}
	text, _ := io.ReadAll(mustOpen(t, filepath.Join(out, "release", name, share, "sbom", "machine.cdx.json")))
	for _, c := range []string{"pkg:apk/alpine/glib-static@1.0-r0", "pkg:deb/ubuntu/bash@5.2-1", `"name": "qboot.bin"`, `"name": "rootfs.qcow2"`, "0001-k.patch", "mtrr.patch"} {
		if !strings.Contains(string(text), c) {
			t.Errorf("the machine's SBOM does not name %s", c)
		}
	}
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func mustOpen(t *testing.T, path string) io.Reader {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestAReleaseMissingAnythingIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, remove, want string
	}{
		{"the licence", "root:LICENSE", "the licence"},
		{"qemu-img", "out:bin/qemu-img", "the qemu-img binary"},
		{"an option ROM", "out:qemu/linuxboot_dma.bin", "firmware linuxboot_dma.bin"},
		{"every qboot patch", "root:qemu/qboot/*.patch", "qboot's patch"},
		{"every kernel patch", "out:kernel/patches", "kernel patch"},
		{"the base image", "out:image/rootfs.qcow2", "the base image"},
		{"an apk database", "out:sbom/qemu.apk", "qemu.apk"},
		{"the package list", "out:packages.txt", "package list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, out := tree(t)
			where, glob, _ := strings.Cut(tc.remove, ":")
			dir := root
			if where == "out" {
				dir = out
			}
			found, _ := filepath.Glob(filepath.Join(dir, glob))
			for _, f := range found {
				_ = os.RemoveAll(f)
			}
			_, err := Pack(context.Background(), options(t, root, out))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Pack = %v, want it to name %s", err, tc.want)
			}
		})
	}
	root, out := tree(t)
	o := options(t, root, out)
	o.Fingerprint = func(context.Context, string) (string, error) { return "", errors.New("no machine") }
	if _, err := Pack(context.Background(), o); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("a fingerprint that failed: %v", err)
	}
	o = options(t, root, out)
	o.Pins = &versions.Versions{}
	if _, err := Pack(context.Background(), o); err == nil {
		t.Error("a release with no pins packed")
	}
}

func TestFingerprintOfIsSpinMachinesAnswer(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "sm"), "#!/bin/sh\n[ \"$*\" = \"fingerprint --release /share --cpu "+FingerprintCPU+"\" ] || exit 9\necho '{\"fingerprint\": \""+fp+"\", \"shape\": {}}'\n", 0o755)
	if got, err := FingerprintOf(context.Background(), filepath.Join(dir, "sm"), "/share"); err != nil || got != fp {
		t.Errorf("FingerprintOf = %q, %v", got, err)
	}
	for _, body := range []string{"exit 1", "echo nope", `echo '{"fingerprint": "short"}'`} {
		put(t, filepath.Join(dir, "bad"), "#!/bin/sh\n"+body+"\n", 0o755)
		if _, err := FingerprintOf(context.Background(), filepath.Join(dir, "bad"), "/share"); err == nil {
			t.Errorf("%q gave a fingerprint", body)
		}
	}
}

func TestAManifestReadsBackAsWritten(t *testing.T) {
	m := map[string]string{}
	for i, k := range ManifestKeys {
		m[k] = strings.Repeat("x", i)
	}
	text := FormatEnv(m)
	if !strings.HasPrefix(text, "version=\nsource_commit=x\n") || strings.Count(text, "\n") != len(ManifestKeys) {
		t.Errorf("FormatEnv wrote %q", text)
	}
	if got := ParseEnv(text + "version=second\nno equals sign\n"); !maps(got, m) {
		t.Errorf("ParseEnv read back %v, want %v: the first of a key wins and a line without = is no key", got, m)
	}
}

func TestSourcesNameEachPartsSourceByItsPin(t *testing.T) {
	v := pins(t)
	text, err := Sources("v20261119.07", v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "spin-machine v20261119.07\n") {
		t.Errorf("SOURCES begins %q", text[:40])
	}
	for _, name := range []string{"qemu", "kernel", "e2fsprogs", "systemd-source"} {
		e, _ := v.Get(name)
		if !strings.Contains(text, e.URL()) || !strings.Contains(text, e.Pin) || !strings.Contains(text, e.Version) {
			t.Errorf("SOURCES does not name %s's %s, %s and %s", name, e.Version, e.URL(), e.Pin)
		}
	}
	q, _ := v.Get("qboot")
	if !strings.Contains(text, "qboot "+q.Pin) || !strings.Contains(text, "writes for it is "+q.Archive) {
		t.Error("SOURCES does not name qboot's commit and archive")
	}
	if strings.Contains(text, "%!") {
		t.Error("SOURCES has a verb with no value")
	}
	for _, missing := range []string{"qemu", "kernel", "e2fsprogs", "qboot", "systemd-source"} {
		w := *v
		w.Entries = slices.DeleteFunc(slices.Clone(v.Entries), func(e versions.Entry) bool { return e.Name == missing })
		if _, err := Sources("v", &w); err == nil {
			t.Errorf("SOURCES with no %s pin", missing)
		}
	}
}

func TestAVersionIsGivenOrTheNextOfToday(t *testing.T) {
	day := time.Date(2026, 11, 19, 23, 0, 0, 0, time.FixedZone("x", -5*3600))
	for _, tc := range []struct {
		given     string
		tags      []string
		want, why string
	}{
		{"", nil, "v20261120.01", "generated for today (0 already taken)"},
		{"", []string{"v20261120.01", "v20261119.01", "v20261120.02"}, "v20261120.03", "generated for today (2 already taken)"},
		{"v20261001.04", []string{"v20261120.01"}, "v20261001.04", "given by the caller"},
	} {
		got, why, err := Version(tc.given, tc.tags, day)
		if err != nil || got != tc.want || why != tc.why {
			t.Errorf("Version(%q, %v) = %q, %q, %v; want %q, %q", tc.given, tc.tags, got, why, err, tc.want, tc.why)
		}
	}
	for _, bad := range []string{"20261119.01", "v20261119.1", "v2026111.01", "v20261119.01; rm -rf /"} {
		if _, _, err := Version(bad, nil, day); err == nil {
			t.Errorf("%q was taken as a version", bad)
		}
	}
}

func TestTheVerdictSaysWhatCheckpointsLose(t *testing.T) {
	now := map[string]string{"fingerprint_sha256": "f1", "qemu_sha256": "q2", "kernel_sha256": "k1"}
	for _, tc := range []struct {
		name      string
		reference string
		then      map[string]string
		now       map[string]string
		want      []string
		not       []string
	}{
		{"the first release", "", nil, now, []string{"this is the first published release"}, []string{"###"}},
		{"a release with no manifest", "v1", nil, now, []string{"`v1` published no `machine.env`"}, []string{"###"}},
		{"the fingerprint moved", "v1", map[string]string{"fingerprint_sha256": "f0", "qemu_sha256": "q2"}, now,
			[]string{"### ⚠️ Every checkpoint taken against `v1` stops resuming", "- `fingerprint_sha256`\n  - was `f0` (v1)\n  - now `f1`"},
			[]string{"still resume", "qemu_sha256"}},
		{"only what a boot reads moved", "v1", map[string]string{"fingerprint_sha256": "f1", "qemu_sha256": "q1", "kernel_sha256": "k1"}, now,
			[]string{"### Checkpoints taken against `v1` still resume", "Changed, and not part of the fingerprint", "- `qemu_sha256`\n  - was `q1` (v1)\n  - now `q2`"},
			[]string{"kernel_sha256", "stops resuming"}},
		{"nothing moved", "v1", map[string]string{"fingerprint_sha256": "f1", "qemu_sha256": "q2"}, now,
			[]string{"still resume"}, []string{"Changed, and not"}},
		{"the reference never recorded the fingerprint", "v1", map[string]string{"qemu_sha256": "q2"}, now,
			[]string{"### Nothing compared here moves the fingerprint", "Unchanged since `v1`: nothing.", "Not compared, and therefore not vouched for: fingerprint_sha256."},
			[]string{"still resume"}},
		{"this build never said its fingerprint", "v1", map[string]string{"fingerprint_sha256": "f1", "qemu_sha256": "q1"}, map[string]string{"qemu_sha256": "q1"},
			[]string{"Not compared, and therefore not vouched for: fingerprint_sha256"}, []string{"still resume"}},
		{"an empty value is not compared", "v1", map[string]string{"fingerprint_sha256": ""}, now,
			[]string{"Not compared"}, []string{"stops resuming"}},
	} {
		got := Verdict(tc.reference, tc.then, tc.now)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: the verdict lacks %q:\n%s", tc.name, w, got)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: the verdict says %q:\n%s", tc.name, n, got)
			}
		}
	}
	if !isFingerprint("fingerprint_sha256") || isFingerprint("qemu_sha256") {
		t.Error("isFingerprint")
	}
	if orNothing([]string{"a", "b"}) != "a b" || orNothing(nil) != "nothing" {
		t.Error("orNothing")
	}
}
