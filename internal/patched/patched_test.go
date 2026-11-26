// SPDX-License-Identifier: Apache-2.0

package patched

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/go-tools/versions"
	"go.yaml.in/yaml/v3"
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
	if !slices.Equal(Names(), []string{"kernel", "qemu", "qboot", "systemd", "openssh", "swtpm"}) {
		t.Errorf("Names = %v", Names())
	}
}

// Every part's patches are where Parts says, in this checkout, and each names files. A part may
// carry none, as systemd since 262 (#791), and then nothing analyses it: codeql.yml's matrix is
// the parts that carry patches, so a part's first patch is analysed from the day it lands.
func TestCodeQLAnalysesEveryPartThatCarriesPatches(t *testing.T) {
	var carried []string
	for _, p := range Parts {
		if _, err := os.Stat(filepath.Join("..", "..", filepath.Dir(p.Patches))); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
		patches, _ := filepath.Glob(filepath.Join("..", "..", p.Patches))
		if len(patches) == 0 {
			continue
		}
		if _, err := Touched(patches); err != nil {
			t.Errorf("%s: %d patches at %s: %v", p.Name, len(patches), p.Patches, err)
		}
		carried = append(carried, p.Name)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "codeql.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs struct {
			Analyze struct {
				Strategy struct {
					Matrix struct {
						Part    []string
						Include []struct{ Part string }
					}
				}
			}
		}
	}
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatal(err)
	}
	m := wf.Jobs.Analyze.Strategy.Matrix
	analysed := slices.Clone(m.Part)
	for _, i := range m.Include {
		analysed = append(analysed, i.Part)
	}
	slices.Sort(analysed)
	slices.Sort(carried)
	if len(carried) < 4 || !slices.Equal(analysed, carried) {
		t.Errorf("codeql.yml analyses %v; the parts carrying patches are %v", analysed, carried)
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

func TestTheKernelIsCompiledWithoutNamedAddressSpacesOrNotAtAll(t *testing.T) {
	src := t.TempDir()
	put(t, filepath.Join(src, "arch/x86/Kconfig"), "config CC_HAS_NAMED_AS\n\tbool\n")
	root := t.TempDir()
	put(t, filepath.Join(root, "kernel/config-7.3-x86_64"), "CONFIG_X86_64=y\n")
	if err := prepareKernel(context.Background(), root, src, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "no longer says") {
		t.Errorf("a Kconfig that turns __seg_gs on some other way: %v", err)
	}
}

// fakeTools puts scripts named for the build tools first on PATH, each appending its arguments
// to dir/calls, and answers ninja's target listing with what targets holds.
func fakeTools(t *testing.T, targets string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	for _, tool := range []string{"make", "ninja"} {
		put(t, filepath.Join(bin, tool), "#!/bin/sh\necho "+tool+" \"$@\" >> "+calls+"\n"+
			"case \"$*\" in *'-t targets all'*) printf '"+targets+"' ;; esac\n")
		if err := os.Chmod(filepath.Join(bin, tool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestTheKernelIsCompiledFromItsConfigurationWithoutNamedAddressSpaces(t *testing.T) {
	calls := fakeTools(t, "")
	src, root := t.TempDir(), t.TempDir()
	put(t, filepath.Join(src, "arch/x86/Kconfig"), "config A\n\tbool\n\nconfig CC_HAS_NAMED_AS\n\tdef_bool $(success,echo x | $(CC))\n\tdepends on CC_IS_GCC\n")
	put(t, filepath.Join(root, "kernel/config-7.3-x86_64"), "CONFIG_X86_64=y\n")
	if err := prepareKernel(context.Background(), root, src, []string{"lib/a.c"}, io.Discard); err == nil || !strings.Contains(err.Error(), "module-signing.crt") {
		t.Errorf("a tree without the module certificate: %v", err)
	}
	put(t, filepath.Join(root, "kernel/module-signing.crt"), "the certificate\n")
	if err := os.Mkdir(filepath.Join(src, "certs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareKernel(context.Background(), root, src, []string{"lib/a.c"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(src, "certs/module-signing.crt")); got != "the certificate\n" {
		t.Errorf("certs/module-signing.crt = %q, want the tree's module certificate", got)
	}
	if got := read(t, filepath.Join(src, "arch/x86/Kconfig")); got != "config A\n\tbool\n\nconfig CC_HAS_NAMED_AS\n\tdef_bool n\n\tdepends on CC_IS_GCC\n" {
		t.Errorf("Kconfig = %q", got)
	}
	if got := read(t, filepath.Join(src, ".config")); got != "CONFIG_X86_64=y\n" {
		t.Errorf(".config = %q, want the series' configuration", got)
	}
	got := read(t, calls)
	for _, want := range []string{"make -C " + src + " ARCH=x86_64 olddefconfig\n", " prepare\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("calls:\n%s\nwant %q in them", got, want)
		}
	}
	if strings.Contains(got, "lib/a.o") || strings.Contains(got, "vmlinux") {
		t.Errorf("calls:\n%s\nprepare compiled a touched file, which the tracer would then not see", got)
	}

	put(t, filepath.Join(src, "arch/x86/Kconfig"), "config CC_HAS_NAMED_AS\n\tdef_bool $(x)\n")
	put(t, filepath.Join(root, "kernel/config-7.3-x86_64"), "CONFIG_CC_HAS_NAMED_AS=y\n")
	if err := prepareKernel(context.Background(), root, src, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "CONFIG_CC_HAS_NAMED_AS=y") {
		t.Errorf("a configuration that kept named address spaces: %v", err)
	}
}

// The host tools are built only through vmlinux, with flags of their own: prepare builds it
// untraced, and compile again over each touched file.
func TestTheKernelsHostToolsAreBuiltThroughVmlinux(t *testing.T) {
	calls := fakeTools(t, "")
	src, root := t.TempDir(), t.TempDir()
	put(t, filepath.Join(src, "arch/x86/Kconfig"), "config CC_HAS_NAMED_AS\n\tdef_bool y\n")
	put(t, filepath.Join(src, "certs/Makefile"), "")
	put(t, filepath.Join(root, "kernel/config-7.3-x86_64"), "CONFIG_X86_64=y\n")
	put(t, filepath.Join(root, "kernel/module-signing.crt"), "the certificate\n")
	touched := []string{"lib/a.c", "kernel/b.c", "arch/x86/tools/relocs.c"}
	if err := prepareKernel(context.Background(), root, src, touched, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := read(t, calls); !strings.HasSuffix(got, " vmlinux\n") {
		t.Errorf("prepare's calls:\n%s\nwant vmlinux last", got)
	}
}

// compile makes each touched file's object, after touching it: a file prepare already built is
// compiled again, under the tracer, or the analysis would not have read it.
func TestTheKernelCompilesEachTouchedFileAgain(t *testing.T) {
	calls := fakeTools(t, "")
	src := t.TempDir()
	touched := []string{"lib/a.c", "kernel/b.c", "arch/x86/tools/relocs.c"}
	if err := compileKernel(context.Background(), "", src, touched, io.Discard); err == nil || !strings.Contains(err.Error(), "prepare it first") {
		t.Errorf("an unprepared tree: %v", err)
	}
	put(t, filepath.Join(src, ".config"), "CONFIG_X86_64=y\n")
	old := time.Now().Add(-time.Hour)
	for _, f := range touched {
		put(t, filepath.Join(src, f), "int x;\n")
		if err := os.Chtimes(filepath.Join(src, f), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := compileKernel(context.Background(), "", src, touched, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := read(t, calls); !strings.HasSuffix(got, " lib/a.o kernel/b.o\nmake -C "+src+" ARCH=x86_64 -j"+jobs()+" vmlinux\n") {
		t.Errorf("calls:\n%s\nwant each object, and vmlinux for the host tools only it builds", got)
	}
	for _, f := range touched {
		if fi, err := os.Stat(filepath.Join(src, f)); err != nil || !fi.ModTime().After(old) {
			t.Errorf("%s was not touched: make would not compile it again", f)
		}
	}
	if err := compileKernel(context.Background(), "", src, []string{"lib/gone.c"}, io.Discard); err == nil || !strings.Contains(err.Error(), "touching lib/gone.c") {
		t.Errorf("a touched file that is not there: %v", err)
	}
}

func TestQEMUIsConfiguredForItsTargetAndCompilesOnlyTheTouchedObjects(t *testing.T) {
	calls := fakeTools(t, "libqemu-x86_64-softmmu.a.p/target_i386_cpu.c.o: C_COMPILER\\nlibcommon.a.p/system_vl.c.o: C_COMPILER\\nqemu-system-x86_64: link\\n")
	src := t.TempDir()
	put(t, filepath.Join(src, "configure"), "#!/bin/sh\necho configure \"$@\" >> "+calls+"\n")
	if err := os.Chmod(filepath.Join(src, "configure"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := compileQEMU(context.Background(), t.TempDir(), src, []string{"target/i386/cpu.c", "system/vl.c"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := read(t, calls)
	for _, want := range []string{"configure --target-list=x86_64-softmmu", "ninja -C " + filepath.Join(src, "build") + " libqemu-x86_64-softmmu.a.p/target_i386_cpu.c.o libcommon.a.p/system_vl.c.o\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("calls:\n%s\nwant %q in them", got, want)
		}
	}
	if err := compileQEMU(context.Background(), t.TempDir(), src, []string{"hw/arm/virt.c"}, io.Discard); err == nil || !strings.Contains(err.Error(), "compiles no hw/arm/virt.c") {
		t.Errorf("a file the target does not build: %v", err)
	}
}

func TestCompileHandsThePartOnlyTheTouchedCFiles(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "touched"), "lib/a.c\nlib/a.h\nlib/Kconfig\nlib/b.cc\n")
	var got []string
	p := Part{Name: "x", compile: func(_ context.Context, _, src string, touched []string, _ io.Writer) error {
		got = touched
		if src != filepath.Join(dir, "src") {
			t.Errorf("src = %s", src)
		}
		return nil
	}}
	if err := Compile(context.Background(), t.TempDir(), p, dir, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"lib/a.c", "lib/b.cc"}) {
		t.Errorf("touched = %v", got)
	}
	p.compile = func(context.Context, string, string, []string, io.Writer) error { return errors.New("make failed") }
	if err := Compile(context.Background(), t.TempDir(), p, dir, io.Discard); err == nil || err.Error() != "x: make failed" {
		t.Errorf("a failed compile: %v", err)
	}
	if err := Prepare(context.Background(), t.TempDir(), p, dir, io.Discard); err != nil {
		t.Errorf("a part with nothing to build before its compile: %v", err)
	}
	p.prepare = p.compile
	if err := Prepare(context.Background(), t.TempDir(), p, dir, io.Discard); err == nil || err.Error() != "x: make failed" {
		t.Errorf("a failed prepare: %v", err)
	}
	if err := Prepare(context.Background(), t.TempDir(), p, t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "make the tree first") {
		t.Errorf("no tree: %v", err)
	}
	qboot, _ := Find("qboot")
	if err := Prepare(context.Background(), t.TempDir(), qboot, dir, io.Discard); err == nil || !strings.Contains(err.Error(), "nothing to prepare") {
		t.Errorf("qboot: %v", err)
	}
}
