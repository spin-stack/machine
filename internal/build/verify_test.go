// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/machine"
)

// script writes an executable shell script at path.
func script(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// qemuTree lays out a QEMU tree that verifies: three binaries that answer as QEMU does, the TCG one
// alone emulating, and the firmware with a 64 KiB qboot.
func qemuTree(t *testing.T, out string) {
	t.Helper()
	machines := "Supported machines are:\\n" + machine.Type + "  Standard PC (Q35 + ICH9, 2009)"
	answer := func(tcg bool) string {
		accels := "Accelerators supported in QEMU binary:\\nkvm"
		if tcg {
			accels += "\\ntcg"
		}
		return `case "$1" in --version) echo QEMU 11.2;; -accel) printf '` + accels + `\n';; -machine) printf '` + machines + `\n';; esac`
	}
	script(t, filepath.Join(out, "bin/qemu-system-x86_64"), answer(false))
	script(t, filepath.Join(out, "bin/qemu-system-x86_64-tcg"), answer(true))
	script(t, filepath.Join(out, "bin/qemu-img"), `echo qemu-img 11.2`)
	for _, f := range Firmware {
		size := 1
		if f == "qboot.bin" {
			size = qbootSize
		}
		if err := os.MkdirAll(filepath.Join(out, "qemu"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "qemu", f), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAQEMUTreeVerifiesOnlyWhole(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(out string)
		want   string
	}{
		{"whole", func(string) {}, ""},
		{"a binary missing", func(out string) { _ = os.Remove(filepath.Join(out, "bin/qemu-img")) }, "missing qemu-img"},
		{"a binary not executable", func(out string) { _ = os.Chmod(filepath.Join(out, "bin/qemu-img"), 0o644) }, "not executable"},
		{"a directory where a binary goes", func(out string) {
			_ = os.Remove(filepath.Join(out, "bin/qemu-img"))
			_ = os.Mkdir(filepath.Join(out, "bin/qemu-img"), 0o755)
		}, "not executable"},
		{"firmware missing", func(out string) { _ = os.Remove(filepath.Join(out, "qemu/efi-virtio.rom")) }, "missing firmware efi-virtio.rom"},
		{"qboot the wrong size", func(out string) {
			_ = os.WriteFile(filepath.Join(out, "qemu/qboot.bin"), make([]byte, qbootSize+1), 0o644)
		}, "qboot.bin is 65537 bytes"},
		{"a binary that does not run", func(out string) { script(t, filepath.Join(out, "bin/qemu-img"), "exit 1") }, "qemu-img does not run"},
		{"the KVM binary emulating", func(out string) {
			script(t, filepath.Join(out, "bin/qemu-system-x86_64"), `case "$1" in -accel) printf 'kvm\ntcg\n';; esac`)
		}, "has TCG compiled in"},
		{"the CI binary unable to emulate", func(out string) {
			script(t, filepath.Join(out, "bin/qemu-system-x86_64-tcg"), `case "$1" in -accel) echo kvm;; esac`)
		}, "cannot emulate"},
		{"no accelerators said", func(out string) {
			script(t, filepath.Join(out, "bin/qemu-system-x86_64"), `case "$1" in -accel) exit 1;; esac`)
		}, "asking qemu-system-x86_64 for its accelerators"},
		{"the machine type gone", func(out string) {
			script(t, filepath.Join(out, "bin/qemu-system-x86_64-tcg"), `case "$1" in -accel) echo tcg;; -machine) echo pc-q35-1.0;; esac`)
		}, "qemu-system-x86_64-tcg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			qemuTree(t, out)
			tc.break_(out)
			err := VerifyQEMU(Options{Out: out})
			if tc.want == "" {
				if err != nil {
					t.Errorf("a whole tree: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("VerifyQEMU = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestANewerMachineTypeIsSaidAndNotFailed(t *testing.T) {
	out := t.TempDir()
	qemuTree(t, out)
	script(t, filepath.Join(out, "bin/qemu-system-x86_64"), `case "$1" in -accel) echo kvm;; -machine) printf 'pc-q35-99.0  newer\n`+machine.Type+`  pinned\n';; esac`)
	var said bytes.Buffer
	if err := VerifyQEMU(Options{Out: out, Stderr: &said}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said.String(), "pc-q35-99.0") {
		t.Errorf("a newer type was not said: %q", said.String())
	}
	if _, err := MachineType(filepath.Join(out, "absent")); err == nil {
		t.Error("a QEMU that is not there answered")
	}
}

// bzImage is a setup header as a bzImage carries one.
func bzImage(t *testing.T, magic string, relocatable byte) string {
	t.Helper()
	b := make([]byte, 0x300)
	copy(b[0x202:], magic)
	b[0x234] = relocatable
	path := filepath.Join(t.TempDir(), "bzImage")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestABzImageIsBootableOnlyRelocatable(t *testing.T) {
	if err := Relocatable(bzImage(t, "HdrS", 1)); err != nil {
		t.Errorf("a relocatable bzImage: %v", err)
	}
	if err := Relocatable(bzImage(t, "HdrX", 1)); err == nil || !strings.Contains(err.Error(), "HdrS") {
		t.Errorf("no HdrS: %v", err)
	}
	if err := Relocatable(bzImage(t, "HdrS", 0)); err == nil || !strings.Contains(err.Error(), "not relocatable") {
		t.Errorf("not relocatable: %v", err)
	}
	short := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(short, make([]byte, 0x234), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Relocatable(short); err == nil {
		t.Error("a file shorter than a setup header passed")
	}
	if err := Relocatable(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("no file passed")
	}
}

func TestAKernelWithoutItsPartsIsRefused(t *testing.T) {
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "kernel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyKernel(Options{Out: out}); err == nil || !strings.Contains(err.Error(), "no kernel") {
		t.Errorf("no bzImage: %v", err)
	}
	b, _ := os.ReadFile(bzImage(t, "HdrS", 1))
	_ = os.WriteFile(filepath.Join(out, "kernel/bzImage"), b, 0o644)
	if err := VerifyKernel(Options{Out: out}); err == nil || !strings.Contains(err.Error(), "ELF") {
		t.Errorf("no vmlinux: %v", err)
	}
	_ = os.WriteFile(filepath.Join(out, "kernel/vmlinux"), b, 0o644)
	if err := VerifyKernel(Options{Out: out}); err == nil || !strings.Contains(err.Error(), "ELF") {
		t.Errorf("a vmlinux that is not an ELF: %v", err)
	}
}

func TestE2fsprogsVerifiesAsThePinnedVersion(t *testing.T) {
	v := pins(t)
	e, _ := v.Get("e2fsprogs")
	tree := func(answer string) string {
		out := t.TempDir()
		for _, b := range E2fsprogsBinaries {
			script(t, filepath.Join(out, "bin", b), "true")
		}
		script(t, filepath.Join(out, "bin/mkfs.ext4"), answer)
		if err := os.MkdirAll(filepath.Join(out, "e2fsprogs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "e2fsprogs/mke2fs.conf"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// mke2fs -V says its version on stderr and exits 1 in some versions: what it says decides.
	if err := VerifyE2fsprogs(Options{Out: tree("echo mke2fs " + e.Version + " >&2; exit 1"), Pins: v}); err != nil {
		t.Errorf("the pinned version: %v", err)
	}
	if err := VerifyE2fsprogs(Options{Out: tree("echo mke2fs 1.0.0"), Pins: v}); err == nil || !strings.Contains(err.Error(), "mke2fs 1.0.0") {
		t.Errorf("another version: %v", err)
	}
	out := tree("echo mke2fs " + e.Version)
	_ = os.Remove(filepath.Join(out, "e2fsprogs/mke2fs.conf"))
	if err := VerifyE2fsprogs(Options{Out: out, Pins: v}); err == nil || !strings.Contains(err.Error(), "mke2fs.conf") {
		t.Errorf("no mke2fs.conf: %v", err)
	}
	out = tree("echo mke2fs " + e.Version)
	_ = os.Remove(filepath.Join(out, "bin/debugfs"))
	if err := VerifyE2fsprogs(Options{Out: out, Pins: v}); err == nil || !strings.Contains(err.Error(), "debugfs") {
		t.Errorf("no debugfs: %v", err)
	}
	if err := VerifyE2fsprogs(Options{Out: tree("true"), Pins: pinsWithout(t, "e2fsprogs")}); err == nil {
		t.Error("no e2fsprogs pin passed")
	}
}

// pinsWithout is versions.yaml less one entry.
func pinsWithout(t *testing.T, name string) *versions.Versions {
	t.Helper()
	v := pins(t)
	w := *v
	w.Entries = nil
	for _, e := range v.Entries {
		if e.Name != name {
			w.Entries = append(w.Entries, e)
		}
	}
	return &w
}

func TestTheImageAndItsPackagesVerifyByWhatTheyLeave(t *testing.T) {
	out := t.TempDir()
	if err := verifyDebs("systemd")(Options{Out: out}); err == nil {
		t.Error("no packages passed")
	}
	_ = os.MkdirAll(filepath.Join(out, "systemd"), 0o755)
	_ = os.WriteFile(filepath.Join(out, "systemd/systemd_1_amd64.deb"), nil, 0o644)
	if err := verifyDebs("systemd")(Options{Out: out}); err != nil {
		t.Errorf("a package: %v", err)
	}
	if err := verifyDebs("openssh")(Options{Out: out}); err == nil || !strings.Contains(err.Error(), "openssh") {
		t.Errorf("systemd's packages passed for openssh's: %v", err)
	}
	for _, f := range []string{"image/rootfs.qcow2", "packages.txt", "licenses.txt"} {
		if err := verifyImage(Options{Out: out}); err == nil || !strings.Contains(err.Error(), f) {
			t.Errorf("no %s: %v", f, err)
		}
		_ = os.MkdirAll(filepath.Dir(filepath.Join(out, f)), 0o755)
		_ = os.WriteFile(filepath.Join(out, f), nil, 0o644)
	}
	if err := verifyImage(Options{Out: out}); err != nil {
		t.Errorf("an image and its lists: %v", err)
	}
}

func TestAFetchedQEMUIsThePublishedImagesAndVerified(t *testing.T) {
	v := pins(t)
	version, _ := QEMUVersion(Options{Pins: v})
	src := t.TempDir()
	qemuTree(t, src)
	// docker cp ID:/usr/local/bin/X dest, and ID:/usr/share/spin-stack/qemu dest, from src.
	log := fakeDocker(t, `case "$1" in
create) echo cid ;;
cp) from=${2#cid:}; case "$from" in /usr/local/bin/*) cp "`+src+`/bin/${from##*/}" "$3";; /usr/share/spin-stack/qemu) cp -r "`+src+`/qemu" "$3";; esac ;;
esac`)
	out := t.TempDir()
	_ = os.MkdirAll(filepath.Join(out, "qemu"), 0o755)
	_ = os.WriteFile(filepath.Join(out, "qemu/stale.rom"), nil, 0o644)
	if err := FetchQEMU(context.Background(), Options{Root: t.TempDir(), Out: out, Pins: v}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "qemu/stale.rom")); err == nil {
		t.Error("a firmware file the image does not carry survived the fetch")
	}
	m, ok, _ := ReadManifest(out, "qemu")
	if !ok || m.From != PublishedQEMU+":"+version || m.Inputs != "" {
		t.Errorf("the fetch's manifest is %+v", m)
	}
	got := calls(t, log)
	image := PublishedQEMU + ":" + version
	for i, want := range []string{"pull --quiet --platform linux/amd64 " + image, "create --platform linux/amd64 " + image} {
		if at(got, i) != want {
			t.Errorf("call %d = %q, want %q", i, at(got, i), want)
		}
	}
	if last := got[len(got)-1]; last != "rm --force cid" {
		t.Errorf("the container was not removed: %q", last)
	}
	fakeDocker(t, `case "$1" in create) echo cid;; cp) exit 1;; esac`)
	if err := FetchQEMU(context.Background(), Options{Root: t.TempDir(), Out: t.TempDir(), Pins: v}); err == nil {
		t.Error("a copy that failed gave no error")
	}
	fakeDocker(t, `case "$1" in create) echo cid;; esac`)
	if err := FetchQEMU(context.Background(), Options{Root: t.TempDir(), Out: t.TempDir(), Pins: v}); err == nil || !strings.Contains(err.Error(), "verifying") {
		t.Errorf("an image that held nothing: %v", err)
	}
	for _, step := range []string{"pull", "create"} {
		fakeDocker(t, `[ "$1" = `+step+` ] && exit 1; echo cid`)
		if err := FetchQEMU(context.Background(), Options{Root: t.TempDir(), Out: t.TempDir(), Pins: v}); err == nil {
			t.Errorf("a failed %s gave no error", step)
		}
	}
	if err := FetchQEMU(context.Background(), Options{Out: t.TempDir(), Pins: pinsWithout(t, "qemu")}); err == nil {
		t.Error("no QEMU pin fetched")
	}
}

func TestThePublishedQEMUIsTaggedWithThePinnedVersion(t *testing.T) {
	v := pins(t)
	version, _ := QEMUVersion(Options{Pins: v})
	log := fakeDocker(t, "")
	if err := PublishQEMU(context.Background(), Options{Root: t.TempDir(), Pins: v}); err != nil {
		t.Fatal(err)
	}
	args, _ := Args(v, qemuPins...)
	want := "buildx build --file qemu/Dockerfile --target runtime --platform linux/amd64 --cache-from type=registry,ref=" + CacheRegistry + ":qemu " +
		strings.Join(args, " ") + " --build-arg JOBS=4 --label org.opencontainers.image.source=https://github.com/spin-stack/spin-machine --tag ghcr.io/spin-stack/spin-machine/qemu:" + version + " --push ."
	if got := calls(t, log); len(got) != 1 || got[0] != want {
		t.Errorf("publish ran %q, want %q", got, want)
	}
	if err := PublishQEMU(context.Background(), Options{Pins: pinsWithout(t, "qemu")}); err == nil {
		t.Error("no QEMU pin published")
	}
	if err := PublishQEMU(context.Background(), Options{Pins: pinsWithout(t, "alpine")}); err == nil {
		t.Error("a QEMU with a pin missing published")
	}
}

func TestToolsIsAStaticSpinMachine(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "go-calls")
	script(t, filepath.Join(dir, "go"), `echo "CGO_ENABLED=$CGO_ENABLED $*" >> `+log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := t.TempDir()
	if err := Tools(context.Background(), Options{Root: t.TempDir(), Out: out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	want := "CGO_ENABLED=0 build -ldflags -s -w -o " + filepath.Join(out, "bin", "spin-machine") + " ./cmd/spin-machine\n"
	if string(got) != want {
		t.Errorf("go ran %q, want %q", got, want)
	}
	if err := Tools(context.Background(), Options{Out: filepath.Join(log, "x")}); err == nil {
		t.Error("an output under a file gave no error")
	}
}

func TestASummaryIsEachPartsHashes(t *testing.T) {
	out := t.TempDir()
	qemuTree(t, out)
	for _, f := range []string{"kernel/vmlinux", "kernel/bzImage", "kernel/kernel-config", "image/rootfs.qcow2"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(out, f)), 0o755)
		_ = os.WriteFile(filepath.Join(out, f), []byte(f), 0o644)
	}
	sum := func(f string) string { s, _ := FileSHA256(filepath.Join(out, f)); return s }
	for part, want := range map[string][]string{
		"qemu":      {"qemu sha256: " + sum("bin/qemu-system-x86_64")},
		"kernel":    {"kernel sha256: " + sum("kernel/vmlinux"), "bzImage sha256: " + sum("kernel/bzImage"), "config sha256: " + sum("kernel/kernel-config")},
		"image":     {"rootfs.qcow2 sha256: " + sum("image/rootfs.qcow2")},
		"e2fsprogs": nil,
	} {
		got, err := Summary(Options{Out: out}, part)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Summary(%s) = %q, %v; want %q", part, got, err, want)
		}
	}
	script(t, filepath.Join(out, "bin/qemu-system-x86_64"), `printf 'pc-q35-99.0 x\n`+machine.Type+` y\n'`)
	if got, _ := Summary(Options{Out: out}, "qemu"); len(got) != 2 || !strings.Contains(got[1], "pc-q35-99.0") {
		t.Errorf("a newer machine type is not in QEMU's summary: %q", got)
	}
	_ = os.Remove(filepath.Join(out, "kernel/bzImage"))
	if _, err := Summary(Options{Out: out}, "kernel"); err == nil {
		t.Error("a summary of a part not there gave no error")
	}
	if _, err := FileSHA256(t.TempDir()); err == nil {
		t.Error("hashing a directory gave no error")
	}
}

func TestOutputIsWhatABinarySaid(t *testing.T) {
	dir := t.TempDir()
	script(t, filepath.Join(dir, "x"), `echo out; echo err >&2; exit 2`)
	got, err := output(filepath.Join(dir, "x"), "a")
	if err == nil || got != "out\nerr\n" || !strings.Contains(err.Error(), "x a") {
		t.Errorf("output = %q, %v", got, err)
	}
	if !hasLine("a\ntcg\nb", "tcg") || hasLine("tcg2", "tcg") || hasLine("a.c", "a+c") {
		t.Error("hasLine matched other than a whole line")
	}
}

func TestTouchesErrsTowardsBuilding(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	pinned := func(version, note string) {
		y := "- name: e2fsprogs\n  kind: download\n  source: https://e/{version}.tar.xz\n  version: " + version +
			"\n  pin: " + strings.Repeat("b", 64) + "\n  track: releases https://e\n  note: " + note + "\n"
		if err := os.WriteFile(filepath.Join(root, "versions.yaml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() string {
		git("add", "-A")
		git("commit", "-q", "-m", "c")
		return git("rev-parse", "HEAD")
	}
	git("init", "-q")
	_ = os.MkdirAll(filepath.Join(root, "part"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "part/f"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "other"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "untouched"), []byte("1"), 0o644)
	first := commit()
	pinned("1.0", "a")
	base := commit()
	now := func() *versions.Versions {
		v, err := versions.Load(filepath.Join(root, "versions.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	touches := func(base string) bool {
		return Touches(context.Background(), root, base, now(), []string{"e2fsprogs"}, []string{"part"})
	}
	if touches(base) {
		t.Error("no change built")
	}
	_ = os.WriteFile(filepath.Join(root, "other"), []byte("2"), 0o644)
	pinned("1.0", "a note edited")
	commit()
	if touches(base) {
		t.Error("a note and a file outside the part built")
	}
	pinned("1.1", "a")
	commit()
	if !touches(base) {
		t.Error("a moved pin did not build")
	}
	pinned("1.0", "a")
	commit()
	_ = os.WriteFile(filepath.Join(root, "part/f"), []byte("2"), 0o644)
	commit()
	if !touches(base) {
		t.Error("a changed file of the part did not build")
	}
	if !touches("") || !touches(strings.Repeat("0", 40)) {
		t.Error("no base, or one this clone lacks, did not build")
	}
	if !Touches(context.Background(), root, first, now(), []string{"e2fsprogs"}, []string{"untouched"}) {
		t.Error("a base with no versions.yaml did not build")
	}
	if !Touches(context.Background(), root, base, now(), []string{"nope"}, []string{"untouched"}) {
		t.Error("an entry the base cannot be read for did not build")
	}
	fork := git("rev-parse", "HEAD")
	_ = os.WriteFile(filepath.Join(root, "part/f"), []byte("3"), 0o644)
	ahead := commit()
	git("checkout", "-q", fork)
	_ = os.WriteFile(filepath.Join(root, "other"), []byte("3"), 0o644)
	commit()
	if touches(ahead) {
		t.Error("a base that moved on in the part after the change forked built the change")
	}
}
