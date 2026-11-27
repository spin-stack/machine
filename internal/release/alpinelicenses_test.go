// SPDX-License-Identifier: Apache-2.0

package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeCommit is the aports commit every package of apkDB was built from.
const fakeCommit = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"

// origin is the aport apkDB says package p was built from.
func origin(p string) string { return strings.TrimSuffix(strings.TrimSuffix(p, "-static"), "-dev") }

// fakeAlpine serves aports and Alpine's distfiles, and each aport's upstream: an APKBUILD for any
// aport at fakeCommit, in main, naming <origin>-1.0.tar.gz upstream (glib's by a URL it cannot
// expand, so only from distfiles), with that tarball's sha512.
type fakeAlpine struct {
	*httptest.Server
	mu sync.Mutex
	// tarballs replaces the tarball an aport's APKBUILD names, and upstream and distfiles what
	// each serves in its place.
	tarballs, upstream, distfiles map[string][]byte
	// recipes replaces an aport's APKBUILD.
	recipes map[string]string
	// gone are aports with no APKBUILD; down serves no tarball.
	gone map[string]bool
	down bool
	// downloads counts the tarballs served.
	downloads int
}

type entry struct{ name, body, link string }

func tgz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	w := tar.NewWriter(z)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e.name, "/") {
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if e.link != "" {
			h = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: e.link}
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newFakeAlpine(t *testing.T) *fakeAlpine {
	t.Helper()
	f := &fakeAlpine{tarballs: map[string][]byte{}, upstream: map[string][]byte{}, distfiles: map[string][]byte{}, recipes: map[string]string{}, gone: map[string]bool{}}
	// The licence closest to the top is the package's; the one under vendored/ is not.
	tarball := func(o string) []byte {
		if b, ok := f.tarballs[o]; ok {
			return b
		}
		top := o + "-1.0/"
		if o == "glib" {
			return tgz(t, entry{top + "LICENSES/LGPL-2.1-or-later.txt", "glib's LGPL\n", ""}, entry{name: top + "COPYING", link: "LICENSES/LGPL-2.1-or-later.txt"})
		}
		return tgz(t, entry{name: top}, entry{top + "LICENSE", "the licence of " + o + "\n", ""}, entry{top + "COPYRIGHT", "(c) " + o, ""},
			entry{top + "vendored/COPYING", "a vendored licence\n", ""}, entry{top + "src/a.c", "int a;\n", ""})
	}
	alpine, err := pins(t).Get("alpine")
	if err != nil {
		t.Fatal(err)
	}
	series := "v" + alpine.Version
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case len(parts) == 4 && parts[0] == "aports" && parts[3] == "APKBUILD":
			o := parts[2]
			if parts[1] != "main" || r.FormValue("id") != fakeCommit || f.gone[o] {
				http.NotFound(w, r)
				return
			}
			if r, ok := f.recipes[o]; ok {
				_, _ = w.Write([]byte(r))
				return
			}
			sum := sha512.Sum512(tarball(o))
			version := "$pkgver"
			if o == "glib" {
				version = "$_unset/$pkgname-$pkgver"
			}
			_, _ = fmt.Fprintf(w, "pkgname=%s\npkgver=1.0\npkgrel=0\n_up=%s/upstream\nlicense=\"MIT\"\n"+
				"source=\"$_up/$pkgname-%s.tar.gz\n\tfix.patch\n\t\"\n\nbuild() {\n\tmake\n}\n\n"+
				"sha512sums=\"\n%s  %s-1.0.tar.gz\n%s  fix.patch\n\"\n",
				o, f.URL, version, hex.EncodeToString(sum[:]), o, strings.Repeat("0", 128))
		case len(parts) == 2 && parts[0] == "upstream", len(parts) == 3 && parts[0] == "distfiles" && parts[1] == series:
			o := strings.TrimSuffix(parts[len(parts)-1], "-1.0.tar.gz")
			if f.down {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			instead := f.upstream
			if parts[0] == "distfiles" {
				instead = f.distfiles
			}
			b, ok := instead[o]
			if !ok {
				b = tarball(o)
			}
			f.downloads++
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAlpine) sources(t *testing.T) AlpineSources {
	t.Helper()
	return AlpineSources{Aports: f.URL + "/aports", Distfiles: f.URL + "/distfiles", Cache: t.TempDir()}
}

// linkedOrigins are the aports apkDB names for the packages apkLinked lists.
func linkedOrigins() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range apkLinked {
		for p := range strings.SplitSeq(a.only, ",") {
			if o := origin(p); !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	return out
}

func TestTheAlpineLicencesAreEachLinkedSourcesOwn(t *testing.T) {
	root, out := tree(t)
	o := options(t, root, out)
	f := newFakeAlpine(t)
	o.Alpine = f.sources(t)
	text, err := AlpineLicenses(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range linkedOrigins() {
		if n := strings.Count(text, "==> "+a+" 1.0-r0 ("); n != 1 {
			t.Errorf("%s has %d sections", a, n)
		}
		if a != "gcc" && a != "glib" && !strings.Contains(text, "the licence of "+a+"\n") {
			t.Errorf("no licence text for %s", a)
		}
	}
	for _, want := range []string{
		"==> zlib 1.0-r0 (zlib-static)\naports commit: " + fakeCommit + "\nlicence:       MIT\nAPKBUILD:      " + f.URL + "/aports/main/zlib/APKBUILD?id=" + fakeCommit + "\n" +
			"source:        " + f.URL + "/upstream/zlib-1.0.tar.gz\nmirror:        " + f.URL + "/distfiles/v",
		"--> jemalloc-1.0/COPYRIGHT\n\n(c) jemalloc\n\n--> jemalloc-1.0/LICENSE\n\nthe licence of jemalloc\n",
		"--> glib-1.0/COPYING -> glib-1.0/LICENSES/LGPL-2.1-or-later.txt\n\nglib's LGPL\n",
		"source:        " + f.URL + "/distfiles/v",
		"==> gcc 1.0-r0 (gcc)\naports commit: " + fakeCommit + "\nlicence:       MIT\nno text:       ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the licences do not say %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "a vendored licence") || strings.Contains(text, "the licence of gcc") {
		t.Errorf("the licences carry a vendored text or gcc's:\n%s", text)
	}

	// Kept, they are read again with nothing to download, and the file is the same.
	f.down = true
	if again, err := AlpineLicenses(context.Background(), o); err != nil || again != text {
		t.Errorf("from the cache: %v, the same text %v", err, again == text)
	}
	// One kept that no longer checks is downloaded again.
	kept, _ := filepath.Glob(filepath.Join(o.Alpine.Cache, "*-jemalloc-1.0.tar.gz"))
	if len(kept) != 1 {
		t.Fatalf("jemalloc's source kept as %v", kept)
	}
	put(t, kept[0], "changed", 0o644)
	if _, err := AlpineLicenses(context.Background(), o); err == nil || !strings.Contains(err.Error(), "jemalloc") {
		t.Errorf("a kept source that does not check, with nowhere to download it again: %v", err)
	}
	f.down = false
	before := f.downloads
	if again, err := AlpineLicenses(context.Background(), o); err != nil || again != text || f.downloads != before+1 {
		t.Errorf("a kept source that does not check: %v, the same text %v, %d downloads", err, again == text, f.downloads-before)
	}
}

// Two packages of one aport are one source, and its section names both.
func TestAnAportsPackagesShareItsSection(t *testing.T) {
	root, out := tree(t)
	o := options(t, root, out)
	db := filepath.Join(out, "sbom/e2fsprogs.apk")
	put(t, db, strings.Replace(read(t, db), "o:xz\n", "o:zstd\n", 1), 0o644)
	text, err := AlpineLicenses(context.Background(), o)
	if err != nil || !strings.Contains(text, "==> zstd 1.0-r0 (xz-static, zstd-static)\n") || strings.Contains(text, "==> xz ") {
		t.Errorf("AlpineLicenses = %v:\n%s", err, text)
	}
}

func TestAnUpstreamThatServesOtherBytesIsReadFromAlpinesCopy(t *testing.T) {
	root, out := tree(t)
	o := options(t, root, out)
	f := newFakeAlpine(t)
	f.upstream["jemalloc"] = []byte("not jemalloc")
	o.Alpine = f.sources(t)
	text, err := AlpineLicenses(context.Background(), o)
	if err != nil || !strings.Contains(text, "the licence of jemalloc\n") {
		t.Errorf("AlpineLicenses = %v, with jemalloc's text %v", err, strings.Contains(text, "the licence of jemalloc"))
	}
}

func TestTheAlpineLicencesRefuseWhatTheyCannotVouchFor(t *testing.T) {
	edit := func(t *testing.T, path, pattern, with string) {
		t.Helper()
		put(t, path, regexp.MustCompile(pattern).ReplaceAllString(read(t, path), with), 0o644)
	}
	for _, tc := range []struct {
		name, want string
		spoil      func(t *testing.T, f *fakeAlpine, o *Options)
	}{
		{"a tarball whose sha512 is not the APKBUILD's", "is sha512", func(_ *testing.T, f *fakeAlpine, _ *Options) {
			f.upstream["jemalloc"], f.distfiles["jemalloc"] = []byte("not it"), []byte("nor this")
		}},
		{"a source with no licence file", "pixman 1.0-r0: pixman-1.0.tar.gz: it has no licence file", func(t *testing.T, f *fakeAlpine, _ *Options) {
			f.tarballs["pixman"] = tgz(t, entry{"pixman-1.0/src/a.c", "int a;\n", ""})
		}},
		{"a licence file that links out of the source", "links to ../../etc/passwd", func(t *testing.T, f *fakeAlpine, _ *Options) {
			f.tarballs["pcre2"] = tgz(t, entry{name: "pcre2-1.0/COPYING", link: "../../etc/passwd"})
		}},
		{"a licence file that links to a link", "none of its licence files is a text", func(t *testing.T, f *fakeAlpine, _ *Options) {
			f.tarballs["pcre2"] = tgz(t, entry{name: "pcre2-1.0/COPYING", link: "doc/text"}, entry{name: "pcre2-1.0/doc/text", link: "/etc/hostname"})
		}},
		{"a source that is not a tarball", "libaio-1.0.tar.gz: tar -tf: exit status 2: tar: ", func(_ *testing.T, f *fakeAlpine, _ *Options) {
			f.tarballs["libaio"] = []byte("not a tarball")
		}},
		{"an APKBUILD that names no tarball", "its sha512sums name no tarball", func(_ *testing.T, f *fakeAlpine, _ *Options) {
			f.recipes["libffi"] = "pkgver=1.0\npkgrel=0\nsource=\"a.patch\"\nsha512sums=\"\n00  a.patch\n\"\n"
		}},
		{"an APKBUILD with no version", "is -r0, and the apk database has 1.0-r0", func(_ *testing.T, f *fakeAlpine, _ *Options) {
			f.recipes["libffi"] = "pkgrel=0\n"
		}},
		{"a cache that is not a directory", "making ", func(t *testing.T, _ *fakeAlpine, o *Options) {
			o.Alpine.Cache = filepath.Join(t.TempDir(), "file")
			put(t, o.Alpine.Cache, "", 0o644)
		}},
		{"an aport aports does not have", "no APKBUILD for libffi", func(_ *testing.T, f *fakeAlpine, _ *Options) {
			f.gone["libffi"] = true
		}},
		{"a linked package the database lacks", "qemu.apk: jemalloc-static is not in the apk database", func(t *testing.T, _ *fakeAlpine, o *Options) {
			edit(t, filepath.Join(o.Out, "sbom/qemu.apk"), `P:jemalloc-static\n`, "P:jemalloc\n")
		}},
		{"a package whose APKBUILD is of another version", "and the apk database has 1.1-r0", func(t *testing.T, _ *fakeAlpine, o *Options) {
			edit(t, filepath.Join(o.Out, "sbom/e2fsprogs.apk"), `(P:lz4-static\nV:)1.0-r0`, "${1}1.1-r0")
		}},
		{"one aport at two commits", "zlib is 1.0-r0 from aports", func(t *testing.T, _ *fakeAlpine, o *Options) {
			edit(t, filepath.Join(o.Out, "sbom/e2fsprogs.apk"), `(P:zlib-static\n(?:.*\n)*?c:)`+fakeCommit, "${1}beef")
		}},
		{"a record with no commit", "zlib-static's record in the apk database names no origin, commit or version", func(t *testing.T, _ *fakeAlpine, o *Options) {
			edit(t, filepath.Join(o.Out, "sbom/qemu.apk"), `(P:zlib-static\n(?:.*\n)*?)c:`+fakeCommit+`\n`, "${1}")
		}},
		{"nowhere to keep the sources", "no directory to keep the sources in", func(_ *testing.T, _ *fakeAlpine, o *Options) {
			o.Alpine.Cache = ""
		}},
		{"no apk database", "missing the apk database e2fsprogs.apk", func(t *testing.T, _ *fakeAlpine, o *Options) {
			if err := os.Remove(filepath.Join(o.Out, "sbom/e2fsprogs.apk")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, out := tree(t)
			o := options(t, root, out)
			f := newFakeAlpine(t)
			o.Alpine = f.sources(t)
			tc.spoil(t, f, &o)
			if _, err := AlpineLicenses(context.Background(), o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("AlpineLicenses = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
