// SPDX-License-Identifier: Apache-2.0

package patched

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func sum(t *testing.T, path string) string {
	t.Helper()
	s := sha256.Sum256([]byte(read(t, path)))
	return hex.EncodeToString(s[:])
}

// serve answers files under dir over TLS, trusted by the HTTP client the downloads use.
func serve(t *testing.T, dir string) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	was := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = was })
	return srv.URL
}

func pins(t *testing.T, yaml string) *versions.Versions {
	t.Helper()
	v, err := versions.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// upstreamTree is a source with a C file a patch changes, one it does not, a header and a
// Makefile.
func upstreamTree(t *testing.T, dir string) {
	put(t, filepath.Join(dir, "lib/a.c"), "one\ntwo\nthree\n")
	put(t, filepath.Join(dir, "lib/b.c"), "untouched\n")
	put(t, filepath.Join(dir, "lib/b.cc"), "untouched\n")
	put(t, filepath.Join(dir, "include/a.h"), "header\n")
	put(t, filepath.Join(dir, "Makefile"), "all:\n")
}

const patchA = `--- a/lib/a.c
+++ b/lib/a.c
@@ -1,3 +1,4 @@
 one
-two
+TWO
 three
+four
`

const patchNew = `--- /dev/null
+++ b/lib/new.c	2026-11-23
@@ -0,0 +1,2 @@
+new one
+new two
`

func checkout(t *testing.T, patches ...string) string {
	root := t.TempDir()
	for i, p := range patches {
		put(t, filepath.Join(root, "kernel/patches", string(rune('1'+i))+".patch"), p)
	}
	return root
}

func TestTreeFromATarballAppliesThePatchesAndKeepsOnlyTheCFilesTheyTouch(t *testing.T) {
	files := t.TempDir()
	upstreamTree(t, filepath.Join(files, "linux-7.3.5"))
	sh(t, files, "tar", "-cJf", "linux-7.3.5.tar.xz", "linux-7.3.5")
	url := serve(t, files)
	v := pins(t, "- name: kernel\n  kind: download\n  source: "+url+"/linux-{version}.tar.xz\n  version: 7.3.5\n  pin: "+
		sum(t, filepath.Join(files, "linux-7.3.5.tar.xz"))+"\n  track: releases https://k\n")
	dir := t.TempDir()
	put(t, filepath.Join(dir, "src/stale.c"), "from an earlier tree\n")
	put(t, filepath.Join(dir, "upstream/stale.c"), "from an earlier tree\n")
	buildless := Part{Name: "kernel", Pin: "kernel", Patches: "kernel/patches/*.patch"}
	if err := Tree(context.Background(), checkout(t, patchA, patchNew), v, buildless, dir); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "src/lib/a.c")); got != "one\nTWO\nthree\nfour\n" {
		t.Errorf("the patched a.c = %q", got)
	}
	if got := read(t, filepath.Join(dir, "upstream/lib/a.c")); got != "one\ntwo\nthree\n" {
		t.Errorf("upstream's a.c = %q", got)
	}
	if got := read(t, filepath.Join(dir, "src/lib/new.c")); got != "new one\nnew two\n" {
		t.Errorf("the new file = %q", got)
	}
	if got := read(t, filepath.Join(dir, "touched")); got != "lib/a.c\nlib/new.c\n" {
		t.Errorf("touched = %q", got)
	}
	for _, gone := range []string{"src/lib/b.c", "src/lib/b.cc", "src/stale.c", "upstream/stale.c", "upstream/lib/new.c"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	for _, kept := range []string{"src/include/a.h", "src/Makefile"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s: %v", kept, err)
		}
	}

	if err := Tree(context.Background(), checkout(t, patchA, patchNew), v, Parts[0], dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/lib/b.c")); err != nil {
		t.Errorf("the kernel's tree, which its analysis compiles, lost a file its build needs: %v", err)
	}
}

func TestTreeRefusesWhatDoesNotMatchItsPinOrDoesNotApply(t *testing.T) {
	files := t.TempDir()
	upstreamTree(t, filepath.Join(files, "linux-7.3.5"))
	sh(t, files, "tar", "-cJf", "linux-7.3.5.tar.xz", "linux-7.3.5")
	url := serve(t, files)
	entry := func(pin string) *versions.Versions {
		return pins(t, "- name: kernel\n  kind: download\n  source: "+url+"/linux-{version}.tar.xz\n  version: 7.3.5\n  pin: "+pin+"\n  track: releases https://k\n")
	}
	good := entry(sum(t, filepath.Join(files, "linux-7.3.5.tar.xz")))
	moved := strings.Replace(patchA, "-two", "-zwei", 1)
	for name, c := range map[string]struct {
		root string
		v    *versions.Versions
		want string
	}{
		"a tarball that is not the pinned one": {checkout(t, patchA), entry(strings.Repeat("0", 64)), "unpacking kernel 7.3.5"},
		"a patch that does not apply":          {checkout(t, moved), good, "1.patch does not apply to kernel 7.3.5"},
		"no patches":                           {t.TempDir(), good, "no patches at kernel/patches/*.patch"},
		"no pin":                               {checkout(t, patchA), pins(t, "[]"), "kernel"},
		"patches that name no file":            {checkout(t, "a changelog\n"), good, "name no file"},
	} {
		t.Run(name, func(t *testing.T) {
			err := Tree(context.Background(), c.root, c.v, Parts[0], t.TempDir())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q in it", err, c.want)
			}
		})
	}
}

func TestTreeFromGitIsTheCommitItPins(t *testing.T) {
	repo := t.TempDir()
	upstreamTree(t, repo)
	sh(t, repo, "git", "init", "--quiet", "--initial-branch", "master")
	sh(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "before")
	sh(t, repo, "git", "add", ".")
	sh(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "pinned")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(out))
	put(t, filepath.Join(repo, "lib/a.c"), "after the pin\n")
	sh(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-am", "after")
	root := t.TempDir()
	put(t, filepath.Join(root, "qemu/qboot/a.patch"), patchA)
	v := pins(t, "- name: qboot\n  kind: git\n  source: https://example.invalid/qboot.git\n  version: master\n  pin: "+commit+"\n  track: branch\n")
	e, _ := v.Get("qboot")
	e.Source = repo
	dir := t.TempDir()
	if err := unpack(context.Background(), e, t.TempDir(), filepath.Join(dir, "src")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "src/lib/a.c")); got != "one\ntwo\nthree\n" {
		t.Errorf("a.c at the pin = %q", got)
	}
	e.Pin = strings.Repeat("0", 40)
	if err := unpack(context.Background(), e, t.TempDir(), t.TempDir()+"/src"); err == nil || !strings.Contains(err.Error(), "checking out") {
		t.Errorf("a commit the repository lacks: %v", err)
	}
	e.Version = "no-such-branch"
	if err := unpack(context.Background(), e, t.TempDir(), t.TempDir()+"/src"); err == nil || !strings.Contains(err.Error(), "cloning") {
		t.Errorf("a branch the repository lacks: %v", err)
	}
}

func TestTreeFromADebianSourceHasTheDistributionsPatches(t *testing.T) {
	if _, err := exec.LookPath("dpkg-source"); err != nil {
		t.Skip("no dpkg-source")
	}
	files := t.TempDir()
	pkg := filepath.Join(files, "build", "openssh-10.2p1")
	upstreamTree(t, pkg)
	put(t, filepath.Join(pkg, "debian/source/format"), "3.0 (native)\n")
	put(t, filepath.Join(pkg, "debian/control"), "Source: openssh\nMaintainer: t <t@t>\n\nPackage: openssh\nArchitecture: any\nDescription: t\n t\n")
	put(t, filepath.Join(pkg, "debian/changelog"), "openssh (1:10.2p1) unstable; urgency=low\n\n  * t\n\n -- t <t@t>  Mon, 23 Nov 2026 00:00:00 +0000\n")
	sh(t, filepath.Join(files, "build"), "dpkg-source", "-b", "openssh-10.2p1")
	srcs, _ := filepath.Glob(filepath.Join(files, "build", "openssh_*"))
	for _, f := range srcs {
		if err := os.Rename(f, filepath.Join(files, filepath.Base(f))); err != nil {
			t.Fatal(err)
		}
	}
	url := serve(t, files)
	dsc := filepath.Join(files, "openssh_10.2p1.dsc")
	v := pins(t, "- name: openssh-source\n  kind: download\n  source: "+url+"/openssh_{version}.dsc\n  version: 10.2p1\n  pin: "+sum(t, dsc)+"\n  track: page https://l\n")
	root := t.TempDir()
	put(t, filepath.Join(root, "image/openssh/a.patch"), patchA)
	dir := t.TempDir()
	if err := Tree(context.Background(), root, v, Parts[4], dir); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "src/lib/a.c")); got != "one\nTWO\nthree\nfour\n" {
		t.Errorf("the patched a.c = %q", got)
	}

	tarball, _ := filepath.Glob(filepath.Join(files, "openssh_*.tar.*"))
	put(t, tarball[0], "not the tarball the .dsc names\n")
	err := Tree(context.Background(), root, v, Parts[4], t.TempDir())
	if err == nil || !strings.Contains(err.Error(), filepath.Base(tarball[0])) {
		t.Errorf("a file that is not the one the .dsc sums: %v", err)
	}
}

func TestDscFilesAreItsSHA256Field(t *testing.T) {
	got, err := dscFiles([]byte("Format: 3.0 (quilt)\nChecksums-Sha1:\n aaa 1 x.tar.gz\nChecksums-Sha256:\n bbb 10 x.orig.tar.gz\n ccc 20 x.debian.tar.xz\nFiles:\n ddd 10 x.orig.tar.gz\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["x.orig.tar.gz"] != "bbb" || got["x.debian.tar.xz"] != "ccc" {
		t.Errorf("files = %v", got)
	}
	if _, err := dscFiles([]byte("Format: 3.0 (quilt)\nFiles:\n ddd 10 x.orig.tar.gz\n")); err == nil {
		t.Error("a .dsc without Checksums-Sha256 names no file and is refused")
	}
}

func TestFindNamesEveryPart(t *testing.T) {
	if p, err := Find("openssh"); err != nil || p.Pin != "openssh-source" {
		t.Errorf("Find(openssh) = %v, %v", p, err)
	}
	_, err := Find("seabios")
	if err == nil || !strings.Contains(err.Error(), strings.Join(Names(), ", ")) {
		t.Errorf("an unknown part: %v", err)
	}
	if !slices.Equal(Names(), []string{"kernel", "qemu", "qboot", "systemd", "openssh"}) {
		t.Errorf("Names = %v", Names())
	}
}

// Every part's patches are where Parts says, in this checkout, and each names files.
func TestEveryPartHasThePatchesItNames(t *testing.T) {
	for _, p := range Parts {
		patches, _ := filepath.Glob(filepath.Join("..", "..", p.Patches))
		if _, err := Touched(patches); len(patches) == 0 || err != nil {
			t.Errorf("%s: %d patches at %s: %v", p.Name, len(patches), p.Patches, err)
		}
	}
}

func TestPruneTakesOutOnlyTheUntouchedCSources(t *testing.T) {
	src := t.TempDir()
	for _, f := range []string{"a.c", "b.c", "c.cc", "d.cpp", "e.cxx", "f.h", "g.S", "dir.c/h.h"} {
		put(t, filepath.Join(src, f), "x\n")
	}
	if err := prune(src, []string{"a.c"}); err != nil {
		t.Fatal(err)
	}
	var left []string
	_ = filepath.WalkDir(src, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			rel, _ := filepath.Rel(src, p)
			left = append(left, rel)
		}
		return nil
	})
	if want := []string{"a.c", "dir.c/h.h", "f.h", "g.S"}; !slices.Equal(left, want) {
		t.Errorf("left %v, want %v", left, want)
	}
	if os.Getuid() != 0 {
		locked := filepath.Join(src, "locked")
		put(t, filepath.Join(locked, "i.c"), "x\n")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if err := prune(src, nil); err == nil {
			t.Error("a directory it cannot read is an error, not a tree pruned in part")
		}
	}
}

func TestCompileRefusesAPartWithoutABuildOrATree(t *testing.T) {
	qboot, _ := Find("qboot")
	if err := Compile(context.Background(), t.TempDir(), qboot, t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "without a build") {
		t.Errorf("qboot: %v", err)
	}
	if err := Compile(context.Background(), t.TempDir(), Parts[0], t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "make the tree first") {
		t.Errorf("no tree: %v", err)
	}
}
