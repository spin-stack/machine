// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/machine/internal/elftest"
	"github.com/spin-stack/machine/internal/release"
)

// bzImage is a file with a bzImage's setup header magic where a kernel's is, and body after it.
func bzImage(body string) []byte {
	b := make([]byte, 0x210)
	copy(b[0x202:], "HdrS")
	return append(b, body...)
}

// fakeRelease is a directory laid out as a release, every file a few bytes: what a tree is made
// of, without a machine to boot.
func fakeRelease(t *testing.T, kernel string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string][]byte{
		"bin/qemu-system-x86_64": []byte("qemu"),
		"bin/qemu-img":           []byte("qemu-img"),
		"bin/spin-machine":       []byte("spin-machine"),
		"qemu/qboot.bin":         []byte("qboot"),
		"qemu/linuxboot_dma.bin": []byte("rom"),
		"qemu/pvh.bin":           []byte("pvh rom"),
		"image/rootfs.qcow2":     []byte("base"),
		"kernel/vmlinux":         []byte("elf"),
	}
	if kernel != "" {
		files["kernel/bzImage"] = bzImage(kernel)
	}
	for p, b := range files {
		write(t, filepath.Join(dir, p), b)
	}
	return dir
}

// spinMachineArgs is a spin-machine whose args prints a command line with lockdown in it, left out
// for --lockdown=kernel; "always" is one that prints it either way.
func spinMachineArgs(lockdown string) string {
	if lockdown == "always" {
		return "#!/bin/sh\necho '-append console=ttyS0 lockdown=confidentiality'\n"
	}
	return `#!/bin/sh
[ "$1" = args ] || exit 1
case " $* " in *" --lockdown=kernel "*) echo '-append console=ttyS0' ;; *) echo '-append console=ttyS0 ` + lockdown + `' ;; esac
`
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func src(t *testing.T) Sources {
	return Sources{Checkout: checkoutRoot(t), Cache: t.TempDir(), Log: io.Discard}
}

func TestATreeIsItsBaseLinkedAndSaysWhereEachPartCameFrom(t *testing.T) {
	base := fakeRelease(t, "k1")
	tr, err := MakeTree(filepath.Join(t.TempDir(), "t"), TreeSpec{Name: "t", From: base, Append: "quiet x=1", Boot: []string{"--cpu=Skylake-Server-v4"}, Options: []string{"memory=2048", "plugged=0"}}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Name() != "t" || tr.Cmdline("/sbin/init") != "init=/sbin/init quiet x=1" {
		t.Errorf("name %q, cmdline %q", tr.Name(), tr.Cmdline("/sbin/init"))
	}
	// Read back from the manifest on disk, as a measurement opens it.
	if again, err := OpenTree(tr.Dir); err != nil || !slices.Equal(again.Manifest.Boot, []string{"--cpu=Skylake-Server-v4"}) ||
		!maps.Equal(again.Manifest.Options, Options{"memory": "2048", "plugged": "0"}) {
		t.Errorf("the tree's boot flags and options, reopened: %v, %v", again, err)
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "t"), TreeSpec{Name: "t", From: base, Options: []string{"plugged"}}, src(t)); err == nil || !strings.Contains(err.Error(), "NAME=VALUE") {
		t.Errorf("an option with no value made a tree: %v", err)
	}
	for name := range partFiles {
		if got := tr.Manifest.Parts[name].Source; got != base {
			t.Errorf("%s came from %q, want %q", name, got, base)
		}
	}
	sum, _ := fileSHA256(filepath.Join(base, "kernel/bzImage"))
	if tr.Manifest.Parts["kernel"].SHA256 != sum || tr.Manifest.Parts["qemu"].SHA256 == "" || tr.Manifest.Parts["spin-machine"].SHA256 == "" {
		t.Errorf("the one-file parts' sums: %+v", tr.Manifest.Parts)
	}
	if tr.Manifest.Parts["image"].SHA256 != "" {
		t.Error("the image is a gigabyte, and is not summed")
	}
	for _, p := range []string{"bin/qemu-system-x86_64", "image/rootfs.qcow2", "kernel/bzImage"} {
		if target, err := os.Readlink(filepath.Join(tr.Dir, p)); err != nil || target != filepath.Join(base, p) {
			t.Errorf("%s links to %q (%v), want the base's", p, target, err)
		}
	}
	if tr.SpinMachine() != filepath.Join(tr.Dir, "bin/spin-machine") || tr.QEMUImg() != filepath.Join(tr.Dir, "bin/qemu-img") ||
		tr.Rootfs() != filepath.Join(tr.Dir, "image/rootfs.qcow2") {
		t.Error("a part's path is not the tree's")
	}
	var m Manifest
	if err := json.Unmarshal([]byte(read(t, filepath.Join(tr.Dir, "manifest.json"))), &m); err != nil || m.Name != "t" || m.Append != "quiet x=1" {
		t.Errorf("manifest.json: %+v, %v", m, err)
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "x"), TreeSpec{From: base}, src(t)); err == nil {
		t.Error("a tree with no name was made")
	}
}

func TestAPartPutIntoATreeIsACopyAndTheBaseIsUntouched(t *testing.T) {
	base := fakeRelease(t, "release kernel")
	newKernel := filepath.Join(t.TempDir(), "k", "bzImage")
	write(t, newKernel, bzImage("branch kernel"))
	write(t, filepath.Join(filepath.Dir(newKernel), "vmlinux"), []byte("branch elf"))
	newQEMU := t.TempDir()
	write(t, filepath.Join(newQEMU, "bin/qemu-system-x86_64"), []byte("qemu 11.2"))
	write(t, filepath.Join(newQEMU, "qemu/qboot.bin"), []byte("qboot 2"))
	write(t, filepath.Join(newQEMU, "qemu/linuxboot_dma.bin"), []byte("rom 2"))
	write(t, filepath.Join(newQEMU, "bin/swtpm"), []byte("swtpm 0.10"))

	tr, err := MakeTree(filepath.Join(t.TempDir(), "b"), TreeSpec{Name: "b", From: base, Kernel: newKernel, QEMU: newQEMU}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	// swtpm comes with the QEMU it was built beside, and a QEMU from before #719 brings none.
	if read(t, filepath.Join(tr.Dir, "bin/swtpm")) != "swtpm 0.10" {
		t.Error("the tree has not the swtpm of the QEMU put into it")
	}
	old := t.TempDir()
	write(t, filepath.Join(old, "bin/qemu-system-x86_64"), []byte("qemu 11.1"))
	write(t, filepath.Join(old, "qemu/qboot.bin"), []byte("qboot 1"))
	write(t, filepath.Join(old, "qemu/linuxboot_dma.bin"), []byte("rom 1"))
	again, err := MakeTree(filepath.Join(t.TempDir(), "d"), TreeSpec{Name: "d", From: base, QEMU: newQEMU}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src(t).placeQEMU(old, again.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(again.Dir, "bin/swtpm")); err == nil {
		t.Error("a QEMU with no swtpm left the tree another build's")
	}
	if !strings.HasSuffix(read(t, filepath.Join(tr.Dir, "kernel/bzImage")), "branch kernel") ||
		read(t, filepath.Join(tr.Dir, "kernel/vmlinux")) != "branch elf" ||
		read(t, filepath.Join(tr.Dir, "bin/qemu-system-x86_64")) != "qemu 11.2" ||
		read(t, filepath.Join(tr.Dir, "qemu/qboot.bin")) != "qboot 2" {
		t.Error("the tree does not hold the parts put into it")
	}
	if !strings.HasSuffix(read(t, filepath.Join(base, "kernel/bzImage")), "release kernel") || read(t, filepath.Join(base, "kernel/vmlinux")) != "elf" ||
		read(t, filepath.Join(base, "bin/qemu-system-x86_64")) != "qemu" || read(t, filepath.Join(base, "qemu/qboot.bin")) != "qboot" {
		t.Error("putting a part into the tree wrote the base")
	}
	if fi, err := os.Lstat(filepath.Join(tr.Dir, "kernel/bzImage")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Error("a part put into the tree is still a link")
	}
	if tr.Manifest.Parts["kernel"].Source != newKernel+", a bzImage entered through its decompressor" || tr.Manifest.Parts["qemu"].Source != newQEMU || tr.Manifest.Parts["firmware"].Source != newQEMU {
		t.Errorf("the manifest's sources: %+v", tr.Manifest.Parts)
	}
	sum, _ := fileSHA256(newKernel)
	if tr.Manifest.Parts["kernel"].SHA256 != sum {
		t.Error("the kernel's sum is not the one put in")
	}

	// A kernel with no vmlinux beside it leaves none: the release's would describe another kernel.
	lone := filepath.Join(t.TempDir(), "bzImage")
	write(t, lone, bzImage("lone"))
	tr, err = MakeTree(filepath.Join(t.TempDir(), "c"), TreeSpec{Name: "c", From: base, Kernel: lone}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(tr.Dir, "kernel/vmlinux")) {
		t.Error("the release's vmlinux was left beside another kernel")
	}
}

func TestATreeRefusesWhatIsNotWhole(t *testing.T) {
	base := fakeRelease(t, "k")
	elf := filepath.Join(t.TempDir(), "vmlinux")
	write(t, elf, []byte("\x7fELF not a bzImage"))
	longELF := filepath.Join(t.TempDir(), "vmlinux")
	write(t, longELF, append([]byte("\x7fELF"), make([]byte, 4096)...))
	for _, tc := range []struct {
		name string
		spec TreeSpec
		want string
	}{
		{"a vmlinux for a kernel", TreeSpec{Kernel: elf}, "not a bzImage"},
		{"a long file with no setup header", TreeSpec{Kernel: longELF}, "not a bzImage"},
		{"a run id that is not one", TreeSpec{Kernel: "run:12x"}, "is a number"},
		{"a missing kernel", TreeSpec{Kernel: "/nonexistent"}, "opening kernel"},
		{"a QEMU directory without one", TreeSpec{QEMU: t.TempDir()}, "qemu-system-x86_64"},
		{"a spin-machine from nowhere", TreeSpec{SpinMachine: "elsewhere"}, "is here, release, commit:SHA, or left out"},
		{"an edit with no name", TreeSpec{Edits: []string{"nope"}}, "no edit named"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Name, tc.spec.From = "x", base
			_, err := MakeTree(filepath.Join(t.TempDir(), "x"), tc.spec, src(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("MakeTree = %v, want an error saying %q", err, tc.want)
			}
		})
	}

	good, err := MakeTree(filepath.Join(t.TempDir(), "good"), TreeSpec{Name: "good", From: base}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		break_ func(dir string)
		want   string
	}{
		{"no manifest", func(d string) { _ = os.Remove(filepath.Join(d, manifestName)) }, "has no manifest"},
		{"a manifest that is not one", func(d string) { write(t, filepath.Join(d, manifestName), []byte("{")) }, "reading"},
		{"a part replaced after", func(d string) {
			_ = os.Remove(filepath.Join(d, "bin/spin-machine"))
			write(t, filepath.Join(d, "bin/spin-machine"), []byte("another"))
		}, "its manifest says"},
		{"a part with no source", func(d string) {
			var m Manifest
			_ = json.Unmarshal([]byte(read(t, filepath.Join(d, manifestName))), &m)
			delete(m.Parts, "image")
			b, _ := json.Marshal(m)
			write(t, filepath.Join(d, manifestName), b)
		}, "where its image came from"},
		{"a part with an empty source", func(d string) {
			var m Manifest
			_ = json.Unmarshal([]byte(read(t, filepath.Join(d, manifestName))), &m)
			m.Parts["image"] = Part{}
			b, _ := json.Marshal(m)
			write(t, filepath.Join(d, manifestName), b)
		}, "where its image came from"},
		{"a part gone", func(d string) { _ = os.Remove(filepath.Join(d, "image/rootfs.qcow2")) }, "has no image"},
		{"not a machine", func(d string) { _ = os.Remove(filepath.Join(d, "qemu/qboot.bin")) }, "not a whole machine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := filepath.Join(t.TempDir(), "copy")
			if err := linkTree(good.Dir, d); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(filepath.Join(d, manifestName))
			write(t, filepath.Join(d, manifestName), []byte(read(t, filepath.Join(good.Dir, manifestName))))
			if _, err := OpenTree(d); err != nil {
				t.Fatalf("the copy before breaking it: %v", err)
			}
			tc.break_(d)
			if _, err := OpenTree(d); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("OpenTree = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}

// A release from before 2026-11-19 has a vmlinux and no bzImage, and nothing boots it now: it
// is no tree, and the refusal names the file it lacks.
func TestAReleaseFromBeforeTheBzImageIsNoTree(t *testing.T) {
	base := fakeRelease(t, "")
	if _, err := MakeTree(filepath.Join(t.TempDir(), "old"), TreeSpec{Name: "old", From: base}, src(t)); err == nil ||
		!strings.Contains(err.Error(), "kernel/bzImage") {
		t.Errorf("MakeTree of a release without a bzImage = %v", err)
	}
}

func TestTheLaneEditIsThisCheckoutsLane(t *testing.T) {
	base := fakeRelease(t, "k")
	tr, err := MakeTree(filepath.Join(t.TempDir(), "l"), TreeSpec{Name: "l", From: base, Edits: []string{"lane"}}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Manifest.Edits) != 1 || tr.Manifest.Edits[0].Name != "lane" {
		t.Fatalf("edits: %+v", tr.Manifest.Edits)
	}
	e := tr.Manifest.Edits[0].Edit
	want := read(t, filepath.Join(checkoutRoot(t), "image/mkosi.extra/usr/local/lib/spin-base/rootfs/etc/systemd/system/sshd@.service.d/10-lane.conf"))
	if e.Files["/etc/systemd/system/sshd@.service.d/10-lane.conf"] != want || len(e.Files) != 19 || len(e.Links) != 1 ||
		!strings.Contains(e.Setup, "class=user-early/") || !strings.Contains(e.Setup, "linger/root") {
		t.Errorf("the lane edit: %d files, %d links, setup %q", len(e.Files), len(e.Links), e.Setup)
	}
	if _, err := laneEdit(t.TempDir()); err == nil || !strings.Contains(err.Error(), "reading the lane's") {
		t.Errorf("a checkout with no image: %v", err)
	}
	if s := packagesEdit("/x/debs", "check-it").Setup; !strings.Contains(s, "debs=/x/debs") || !strings.Contains(s, "test \"$replaced\" -gt 0\ncheck-it\n") {
		t.Errorf("a packages edit: %q", s)
	}
}

// The edits an unknown name is told about are the ones a tree can ask for, each one an edit.
func TestEveryEditTheErrorNamesIsOne(t *testing.T) {
	_, err := namedEdit("nope", checkoutRoot(t), t.TempDir())
	_, list, ok := strings.Cut(fmt.Sprint(err), "the edits are: ")
	if !ok {
		t.Fatalf("an unknown edit: %v", err)
	}
	for name := range strings.SplitSeq(list, ", ") {
		// A tree whose kernel brought its modules, which the modules edit installs.
		dir := t.TempDir()
		write(t, filepath.Join(dir, "kernel/modules/7.4.0/modules.dep"), nil)
		e, err := namedEdit(name, checkoutRoot(t), dir)
		if err != nil || len(e.Files)+len(e.Links) == 0 && e.Setup == "" {
			t.Errorf("edit %s: %+v, %v", name, e, err)
		}
	}
}

// --spin-machine here builds this checkout's: the tree's is a copy, named for its commit, and a
// checkout that does not build, or a systemd or an OpenSSH that does not, is no tree.
func TestATreesSpinMachineCanBeThisCheckouts(t *testing.T) {
	base := fakeRelease(t, "k")
	tr, err := MakeTree(filepath.Join(t.TempDir(), "h"), TreeSpec{Name: "h", From: base, SpinMachine: "here"}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	// Named by its commit where git can say it, and by its directory in a copy where it cannot.
	root := checkoutRoot(t)
	want := "built from " + root
	if rev, err := exec.Command("git", "-C", root, "describe", "--always", "--dirty").Output(); err == nil {
		want = "built from " + strings.TrimSpace(string(rev))
	}
	if s := tr.Manifest.Parts["spin-machine"].Source; s != want {
		t.Errorf("source %q, want %q", s, want)
	}
	if fi, err := os.Lstat(tr.SpinMachine()); err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Size() < 1<<20 {
		t.Errorf("the built spin-machine: %v, %v", fi, err)
	}
	if read(t, filepath.Join(base, "bin/spin-machine")) != "spin-machine" {
		t.Error("building wrote the base's")
	}
	// Left out, a base that has one keeps it.
	if tr, err := MakeTree(filepath.Join(t.TempDir(), "k"), TreeSpec{Name: "k", From: base}, src(t)); err != nil || tr.Manifest.Parts["spin-machine"].Source != base {
		t.Errorf("a base's own spin-machine: %+v, %v", tr, err)
	}
	// A release ships no spin-machine: left out, it is this checkout's.
	_ = os.Remove(filepath.Join(base, "bin/spin-machine"))
	tr, err = MakeTree(filepath.Join(t.TempDir(), "r"), TreeSpec{Name: "r", From: base}, src(t))
	if err != nil || !strings.HasPrefix(tr.Manifest.Parts["spin-machine"].Source, "built from ") {
		t.Errorf("a base with no spin-machine: %v", err)
	}
	nowhere := Sources{Checkout: t.TempDir(), Cache: t.TempDir(), Log: io.Discard}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "x"), TreeSpec{Name: "x", From: base, SpinMachine: "here"}, nowhere); err == nil ||
		!strings.Contains(err.Error(), "building this checkout's spin-machine") {
		t.Errorf("a checkout that does not build: %v", err)
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "y"), TreeSpec{Name: "y", From: fakeRelease(t, "k"), Systemd: "pid1-marks"}, nowhere); err == nil ||
		!strings.Contains(err.Error(), "building systemd with the experiment pid1-marks") {
		t.Errorf("a systemd that does not build: %v", err)
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "z"), TreeSpec{Name: "z", From: fakeRelease(t, "k"), OpenSSH: true}, nowhere); err == nil ||
		!strings.Contains(err.Error(), "building this checkout's OpenSSH") {
		t.Errorf("an OpenSSH that does not build: %v", err)
	}
}

func TestOneIsTheOneFileWithThatName(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "a/bzImage"), nil)
	write(t, filepath.Join(d, "a/notbzImage"), nil)
	if got, err := one(d, "bzImage"); err != nil || got != filepath.Join(d, "a/bzImage") {
		t.Errorf("one = %q, %v", got, err)
	}
	write(t, filepath.Join(d, "b/bzImage"), nil)
	if _, err := one(d, "bzImage"); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Errorf("two: %v", err)
	}
	if _, err := one(d, "vmlinux"); err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Errorf("none: %v", err)
	}
	if _, err := one(filepath.Join(d, "missing"), "x"); err == nil {
		t.Error("a missing directory has one of something")
	}
}

// A run's kernel or QEMU is its artifact, downloaded once into the cache: a run with no bzImage is
// one from before 2026-11-19, and is refused by name.
func TestAKernelOrQEMUFromARunIsTheRunsBuild(t *testing.T) {
	base := fakeRelease(t, "release kernel")
	cache := t.TempDir()
	s := Sources{Checkout: checkoutRoot(t), Cache: cache, Log: io.Discard}
	write(t, filepath.Join(cache, "runs/11/vmlinux/vmlinux-abc/bzImage"), bzImage("run kernel"))
	qemu := filepath.Join(cache, "runs/22/qemu-*-linux-amd64/qemu-11.2-linux-amd64")
	write(t, filepath.Join(qemu, "bin/qemu-system-x86_64"), []byte("run qemu"))
	write(t, filepath.Join(qemu, "qemu/qboot.bin"), []byte("run qboot"))
	write(t, filepath.Join(qemu, "qemu/linuxboot_dma.bin"), []byte("run rom"))
	tr, err := MakeTree(filepath.Join(t.TempDir(), "r"), TreeSpec{Name: "r", From: base, Kernel: "run:11", QEMU: "run:22"}, s)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Manifest.Parts["kernel"].Source != "run 11, a bzImage entered through its decompressor" || tr.Manifest.Parts["qemu"].Source != "run 22" ||
		!strings.HasSuffix(read(t, filepath.Join(tr.Dir, "kernel/bzImage")), "run kernel") ||
		read(t, filepath.Join(tr.Dir, "qemu/qboot.bin")) != "run qboot" {
		t.Errorf("the run's parts: %+v", tr.Manifest.Parts)
	}
	write(t, filepath.Join(cache, "runs/33/vmlinux/vmlinux-old/vmlinux"), []byte("elf"))
	if err := os.MkdirAll(filepath.Join(cache, "runs/44/qemu-*-linux-amd64/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	noFirmware := t.TempDir()
	write(t, filepath.Join(noFirmware, "bin/qemu-system-x86_64"), []byte("q"))
	for _, tc := range []struct {
		spec TreeSpec
		says string
	}{
		{TreeSpec{Kernel: "run:33"}, "kernel run 33: want one bzImage"},
		{TreeSpec{QEMU: "run:44"}, "QEMU run 44: want one"},
		{TreeSpec{QEMU: noFirmware}, "copying " + noFirmware + "'s firmware"},
		{TreeSpec{QEMU: t.TempDir()}, "opening"},
	} {
		spec, says := tc.spec, tc.says
		spec.Name, spec.From = "x", base
		if _, err := MakeTree(filepath.Join(t.TempDir(), "x"), spec, s); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%+v: %v, want an error saying %q", spec, err, says)
		}
	}
}

// A kernel that randomizes itself is entered through PVH: put at kernel/vmlinux with the tree's
// bzImage gone, which would be another kernel, and the manifest says so. A run since the
// bzImage was dropped built only that.
func TestAVmlinuxThatRandomizesItselfIsBootedThroughPVH(t *testing.T) {
	base := fakeRelease(t, "release kernel")
	cache := t.TempDir()
	s := Sources{Checkout: checkoutRoot(t), Cache: cache, Log: io.Discard}
	write(t, filepath.Join(cache, "runs/11/vmlinux/vmlinux-abc/vmlinux"), pvhKernel())
	given := filepath.Join(t.TempDir(), "vmlinux")
	write(t, given, pvhKernel())
	for what, source := range map[string]string{"run:11": "run 11", given: given} {
		tr, err := MakeTree(filepath.Join(t.TempDir(), "p"), TreeSpec{Name: "p", From: base, Kernel: what}, s)
		if err != nil {
			t.Fatal(err)
		}
		booted, err := tr.Kernel()
		if err != nil || booted != filepath.Join(tr.Dir, "kernel/vmlinux") || read(t, booted) != string(pvhKernel()) {
			t.Errorf("%s: the tree boots %q, %v", what, booted, err)
		}
		if fileExists(filepath.Join(tr.Dir, "kernel/bzImage")) {
			t.Errorf("%s: the release's bzImage was left beside the kernel put in", what)
		}
		sum, _ := fileSHA256(given)
		if p := tr.Manifest.Parts["kernel"]; p.Source != source+", a vmlinux entered through PVH" || p.SHA256 != sum {
			t.Errorf("%s: the manifest's kernel: %+v", what, p)
		}
	}
}

func pvhKernel() []byte { return elftest.Kernel([]uint32{18, 19}, []byte{1, 0, 0, 0}) }

// A kernel run's modules come into the tree with its vmlinux, and the modules edit puts them, and
// the image's drop-in that makes their static nodes, into every overlay. A tree whose kernel brought
// none, a release's, has nothing for the edit to install, and is refused rather than measured as if
// it had.
func TestAKernelRunsModulesGoIntoTheOverlay(t *testing.T) {
	base := fakeRelease(t, "release kernel")
	cache := t.TempDir()
	s := Sources{Checkout: checkoutRoot(t), Cache: cache, Log: io.Discard}
	write(t, filepath.Join(cache, "runs/11/vmlinux/vmlinux-abc/vmlinux"), pvhKernel())
	write(t, filepath.Join(cache, "runs/11/vmlinux/vmlinux-abc/modules/7.4.0/kernel/fs/fuse/fuse.ko"), []byte("fuse"))
	tr, err := MakeTree(filepath.Join(t.TempDir(), "m"), TreeSpec{Name: "m", From: base, Kernel: "run:11", Edits: []string{"modules"}}, s)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(tr.Dir, "kernel/modules/7.4.0/kernel/fs/fuse/fuse.ko")) != "fuse" {
		t.Error("the run's modules are not in the tree")
	}
	e := tr.Manifest.Edits[0].Edit
	if !strings.Contains(e.Setup, filepath.Join(tr.Dir, "kernel/modules/7.4.0")+` "$MNT/usr/lib/modules/"`) ||
		!strings.Contains(e.Files["/etc/systemd/system/kmod-static-nodes.service.d/10-create.conf"], "systemd-tmpfiles") {
		t.Errorf("the modules edit: %+v", e)
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "r"), TreeSpec{Name: "r", From: base, Edits: []string{"modules"}}, s); err == nil ||
		!strings.Contains(err.Error(), "no modules") {
		t.Errorf("a tree whose kernel has no modules, with the modules edit: %v", err)
	}
	two := t.TempDir()
	write(t, filepath.Join(two, "kernel/modules/7.4.0/modules.dep"), nil)
	write(t, filepath.Join(two, "kernel/modules/7.5.0/modules.dep"), nil)
	if _, err := namedEdit("modules", checkoutRoot(t), two); err == nil {
		t.Error("a tree with two kernels' modules was given the edit: which would the kernel load?")
	}
}

// A release's traceable kernel is downloaded beside the release once its signature is release.yml's,
// kept in the cache, and booted from there; one that is not signed is not kept. A release publishes
// its vmlinux from #697 on, entered through PVH as the release's own, and before it a bzImage.
func TestATraceableKernelIsTheSignedOneBesideTheRelease(t *testing.T) {
	base := fakeRelease(t, "release kernel")
	cache, pub, bin := t.TempDir(), t.TempDir(), t.TempDir()
	old, now := release.TraceableBzImage("v1"), release.TraceableVmlinux("v2")
	write(t, filepath.Join(pub, "v1", old), bzImage("traceable kernel"))
	write(t, filepath.Join(pub, "v1", old+".sigstore.json"), []byte("{}"))
	write(t, filepath.Join(pub, "v2", now), pvhKernel())
	write(t, filepath.Join(pub, "v2", now+".sigstore.json"), []byte("{}"))
	write(t, filepath.Join(pub, "v3", "nothing"), nil)
	gh := `tag=$3; [ "$2" = view ] && exec ls "` + pub + `/$tag"
d=""; while [ $# -gt 0 ]; do case "$1" in -D) d=$2;; -p) [ -f "` + pub + `/$tag/$2" ] && cp "` + pub + `/$tag/$2" "$d/";; esac; shift; done`
	write(t, filepath.Join(bin, "gh"), []byte("#!/bin/sh\n"+gh+"\n"))
	write(t, filepath.Join(bin, "go"), []byte("#!/bin/sh\nexit 1\n"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write(t, filepath.Join(base, "bin/spin-machine"), []byte(spinMachineArgs("lockdown=confidentiality")))
	s := Sources{Checkout: checkoutRoot(t), Cache: cache, Log: io.Discard}
	spec := TreeSpec{Name: "t", From: base, Kernel: "traceable:v1"}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "t"), spec, s); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("an unsigned traceable kernel: %v", err)
	}
	if fileExists(filepath.Join(cache, "traceable", "v1", old)) {
		t.Error("an unsigned traceable kernel was kept")
	}
	write(t, filepath.Join(bin, "go"), []byte("#!/bin/sh\nexit 0\n"))
	tr, err := MakeTree(filepath.Join(t.TempDir(), "t"), spec, s)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Manifest.Parts["kernel"].Source != "release v1's traceable kernel, a bzImage entered through its decompressor" ||
		!strings.HasSuffix(read(t, filepath.Join(tr.Dir, "kernel/bzImage")), "traceable kernel") || fileExists(filepath.Join(tr.Dir, "kernel/vmlinux")) {
		t.Errorf("v1's tree's kernel: %+v", tr.Manifest.Parts["kernel"])
	}

	spec.Kernel = "traceable:v2"
	tr, err = MakeTree(filepath.Join(t.TempDir(), "t"), spec, s)
	if err != nil {
		t.Fatal(err)
	}
	if booted, _ := tr.Kernel(); tr.Manifest.Parts["kernel"].Source != "release v2's traceable kernel, a vmlinux entered through PVH" ||
		booted != filepath.Join(tr.Dir, "kernel/vmlinux") || read(t, booted) != string(pvhKernel()) {
		t.Errorf("v2's tree boots %s: %+v", booted, tr.Manifest.Parts["kernel"])
	}

	// The traceable kernel forces lockdown none, and a lockdown= on its command line would raise it
	// and take its trace away: its boots leave lockdown= out, with a spin-machine that would add one
	// and with one from before it did, and a tree whose boots would carry one anyway is no tree.
	for _, c := range []struct {
		name, lockdown string
		boot           []string
	}{
		{"raises lockdown unless told not to", "lockdown=confidentiality", []string{"--lockdown=kernel"}},
		{"from before lockdown= on the command line", "", nil},
		{"raises lockdown whatever it is told", "always", nil},
	} {
		write(t, filepath.Join(base, "bin/spin-machine"), []byte(spinMachineArgs(c.lockdown)))
		tr, err := MakeTree(filepath.Join(t.TempDir(), "t"), spec, s)
		if c.lockdown == "always" {
			if err == nil || !strings.Contains(err.Error(), "would boot locked down") {
				t.Errorf("a spin-machine that %s: %v", c.name, err)
			}
			continue
		}
		if err != nil || !slices.Equal(tr.Manifest.Boot, c.boot) {
			t.Errorf("a spin-machine that %s: boots with %q, %v", c.name, tr.Manifest.Boot, err)
		}
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "t"), TreeSpec{Name: "t", From: base}, s); err != nil {
		t.Errorf("a tree on the release's own kernel asked its spin-machine about lockdown: %v", err)
	}
	write(t, filepath.Join(base, "bin/spin-machine"), []byte(spinMachineArgs("lockdown=confidentiality")))

	spec.Kernel = "traceable:v4"
	if _, err := MakeTree(filepath.Join(t.TempDir(), "t"), spec, s); err == nil || !strings.Contains(err.Error(), "listing release v4's files") {
		t.Errorf("a release GitHub has not: %v", err)
	}
	spec.Kernel = "traceable:v3"
	if _, err := MakeTree(filepath.Join(t.TempDir(), "t"), spec, s); err == nil || !strings.Contains(err.Error(), "publishes no traceable kernel") {
		t.Errorf("a release with none: %v", err)
	}
	if err := os.RemoveAll(pub); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v1", "v2"} {
		spec.Kernel = "traceable:" + tag
		if _, err := MakeTree(filepath.Join(t.TempDir(), "t"), spec, s); err != nil {
			t.Errorf("%s's traceable kernel in the cache was asked of GitHub again: %v", tag, err)
		}
	}
}

// A release asked for by its tag is the one unpacked under the cache, when it is there.
func TestAReleaseByItsTagIsTheOneInTheCache(t *testing.T) {
	cache := t.TempDir()
	dir := filepath.Join(cache, "releases", "v20260101.01", "spin-machine-v20260101.01-linux-x86_64", "usr", "share", "spin-stack")
	write(t, filepath.Join(dir, partFiles["qemu"]), []byte("qemu"))
	got, source, err := Sources{Cache: cache}.from("release:v20260101.01")
	if err != nil || got != dir || source != "release v20260101.01" {
		t.Errorf("from = %s, %q, %v", got, source, err)
	}
	// A directory named like one is a directory: only "release" and "release:TAG" are releases.
	wd, _ := os.Getwd()
	if got, _, err := (Sources{Cache: cache}).from("releases-of-mine"); err != nil || got != filepath.Join(wd, "releases-of-mine") {
		t.Errorf("from(releases-of-mine) = %s, %v", got, err)
	}
}

// A release's own spin-machine is built from the commit its machine.env names, and a base with no
// machine.env has none to build.
func TestATreesSpinMachineCanBeItsReleases(t *testing.T) {
	if _, err := MakeTree(filepath.Join(t.TempDir(), "r"), TreeSpec{Name: "r", From: fakeRelease(t, "k"), SpinMachine: "release"}, src(t)); err == nil ||
		!strings.Contains(err.Error(), "machine.env") {
		t.Errorf("a release with no machine.env: %v", err)
	}
	if _, err := MakeTree(filepath.Join(t.TempDir(), "c"), TreeSpec{Name: "c", From: fakeRelease(t, "k"), SpinMachine: "commit:abc"}, src(t)); err == nil ||
		!strings.Contains(err.Error(), "40 hexadecimal digits") {
		t.Errorf("a commit named short: %v", err)
	}
}

func TestATreeFromADirectoryNamesItWhole(t *testing.T) {
	d := fakeRelease(t, "k")
	wd, _ := os.Getwd()
	rel, _ := filepath.Rel(wd, d)
	got, source, err := Sources{}.from(rel)
	if err != nil || got != d || source != d {
		t.Errorf("from(%s) = %s, %s, %v; want %s", rel, got, source, err, d)
	}
}

func TestReplaceRefusesWhatItCannotRead(t *testing.T) {
	d := t.TempDir()
	if err := replace(filepath.Join(d, "missing"), filepath.Join(d, "to"), 0o644); err == nil {
		t.Error("replaced from a file that is not there")
	}
	write(t, filepath.Join(d, "from"), []byte("x"))
	if err := replace(filepath.Join(d, "from"), filepath.Join(d, "deep/er/to"), 0o600); err != nil || read(t, filepath.Join(d, "deep/er/to")) != "x" {
		t.Errorf("replace into a new directory: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(d, "deep/er/to")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if _, err := fileSHA256(filepath.Join(d, "missing")); err == nil {
		t.Error("summed a file that is not there")
	}
	if err := run("true"); err != nil {
		t.Errorf("run(true) = %v", err)
	}
	if err := run("sh", "-c", "echo nope >&2; exit 3"); err == nil || err.Error() != "sh -c echo nope >&2; exit 3: exit status 3: nope" {
		t.Errorf("run of a failing command = %v", err)
	}
}
