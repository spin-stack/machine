// SPDX-License-Identifier: Apache-2.0

package patched

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The sanitized QEMU is the release's configuration, read from qemu/Dockerfile, less only what the
// sanitizers cannot take, and every flag it leaves out is one the release still sets: a reason
// for a flag gone from the release is a reason nobody reads.
func TestTheSanitizedBuildIsTheReleasesConfigurationLessWhatTheSanitizersCannotTake(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "qemu", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := releaseFlags(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"--with-devices-x86_64=spin", "--enable-kvm", "--without-default-devices"} {
		if !slices.Contains(release, f) {
			t.Errorf("the release's flags read from qemu/Dockerfile lack %s: %q", f, release)
		}
	}
	for f := range notSanitized {
		if !slices.Contains(release, f) {
			t.Errorf("notSanitized names %s, which the release no longer sets", f)
		}
	}
	got := sanitizedFlags(release)
	for _, f := range got {
		if _, out := notSanitized[f]; out {
			t.Errorf("the sanitized build keeps %s", f)
		}
	}
	for _, f := range []string{"--enable-asan", "--enable-ubsan", "--disable-tcg", "--enable-debug-info", "--with-devices-x86_64=spin"} {
		if !slices.Contains(got, f) {
			t.Errorf("the sanitized build lacks %s: %q", f, got)
		}
	}
	if one, err := releaseFlags([]byte("\nENV QEMU_FLAGS=\"--static\"\n")); err != nil || !slices.Equal(one, []string{"--static"}) {
		t.Errorf("one flag read as %q, %v", one, err)
	}
	for _, bad := range []string{"FROM x\n", "ENV QEMU_FLAGS=\"--static \\\n", "ENV QEMU_FLAGS=\" \\\n \"\n"} {
		if _, err := releaseFlags([]byte("\n" + bad)); err == nil {
			t.Errorf("read flags out of %q", bad)
		}
	}
}

// A binary without the sanitizers' runtimes is refused: this test's own, which Go links with
// neither.
func TestABinaryWithoutTheSanitizersIsRefused(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := sanitizedBinary(self); err == nil {
		t.Error("a Go binary passed as sanitized")
	}
	if err := sanitizedBinary(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a missing binary passed as sanitized")
	}
}

// Each report is named by the innermost frame in a file a patch touches, on a line it added or
// not, or upstream when no frame is in one; every log a process started counts, reports or not.
func TestAReportIsNamedByThePatchItStopsIn(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	put(t, filepath.Join(root, "patches/0001-a.patch"), "--- a/hw/a.c\n+++ b/hw/a.c\n")
	put(t, filepath.Join(root, "patches/0002-b.patch"), "--- a/hw/b.c\n+++ b/hw/b.c\n--- a/hw/a.c\n+++ b/hw/a.c\n")
	put(t, filepath.Join(dir, "upstream/hw/a.c"), "1\n2\n3\n5\n")
	put(t, filepath.Join(dir, "src/hw/a.c"), "1\n2\n3\n4\n5\n")
	put(t, filepath.Join(dir, "upstream/hw/b.c"), "1\n2\n")
	put(t, filepath.Join(dir, "src/hw/b.c"), "1\n2\n")
	src := filepath.Join(dir, "src")
	logs := filepath.Join(dir, "sanitizer")
	put(t, filepath.Join(logs, "sanitizer.100"), `==100==AddressSanitizer Init done
==100==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x602000000011 at pc 0x1 bp 0x2 sp 0x3
READ of size 1 at 0x602000000011 thread T0
    #0 0x7f0000000001 in __interceptor_memcpy (/lib/x86_64-linux-gnu/libasan.so.8+0x1)
    #1 0x5600000000a1 in copy ../hw/b.c:2:7
    #2 0x5600000000a2 in caller ../hw/a.c:4:3
    #3 0x5600000000a3 in main `+src+`/system/main.c:50
`)
	put(t, filepath.Join(logs, "sanitizer.200"), `==200==AddressSanitizer Init done
`+src+`/build/../util/c.c:10:5: runtime error: shift exponent 64 is too large for 64-bit type 'long unsigned int'
    #0 0x5600000000b1 in shift ../util/c.c:10:5
    #1 0x5600000000b2 in run /usr/src/glibc/start.c:3
`)
	put(t, filepath.Join(logs, "sanitizer.300"), "==300==AddressSanitizer Init done\n")
	put(t, filepath.Join(logs, "sanitizer.500"), src+"/build/../hw/a.c:3:1: runtime error: load of misaligned address\n")
	put(t, filepath.Join(logs, "sanitizer.400"), "==400==ERROR: AddressSanitizer: SEGV on unknown address\n    #0 0x1 in f ../hw/a.c:2:1\n")

	reports, ran, err := Reports(context.Background(), root, Part{Name: "qemu", Patches: "patches/*.patch"}, dir, logs)
	if err != nil {
		t.Fatal(err)
	}
	if ran != 5 {
		t.Errorf("%d processes ran, want 5", ran)
	}
	var got []string
	for _, r := range reports {
		got = append(got, r.String())
	}
	want := []string{
		"AddressSanitizer: heap-buffer-overflow at hw/b.c:2: 0002-b.patch, hw/b.c:2 in a file it touches (sanitizer.100)",
		"runtime error: shift exponent 64 is too large for 64-bit type 'long unsigned int' at util/c.c:10: upstream (sanitizer.200)",
		"AddressSanitizer: SEGV at hw/a.c:2: 0001-a.patch 0002-b.patch, hw/a.c:2 in a file it touches (sanitizer.400)",
		"runtime error: load of misaligned address at hw/a.c:3: 0001-a.patch 0002-b.patch, hw/a.c:3 in a file it touches (sanitizer.500)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("reports:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if upstream := reports[1].Frames; !slices.Equal(upstream, []Frame{{"util/c.c", 10}}) {
		t.Errorf("the second report's frames are %v, want util/c.c:10 alone: glibc's is outside the tree", upstream)
	}
	if added := reports[0].Frames; len(added) != 3 || added[1] != (Frame{"hw/a.c", 4}) {
		t.Errorf("the first report's frames are %v, want hw/b.c:2, hw/a.c:4 and system/main.c:50", added)
	}
	if at := (attribution{by: map[string][]string{"hw/a.c": {"0001-a.patch"}}, added: map[string]map[int]bool{"hw/a.c": {4: true}}}).patch(reports[0].Frames[1:]); at != "0001-a.patch, hw/a.c:4 on a line it added" {
		t.Errorf("a frame on an added line is named %q", at)
	}
}

// built compiles a program of one shift, with flags, into a file of its own, or skips without a
// compiler that can: what fake ninja hands back as the binary it made.
func built(t *testing.T, flags ...string) string {
	t.Helper()
	dir := t.TempDir()
	put(t, filepath.Join(dir, "a.c"), "int main(int argc, char **argv) { (void)argv; return 1 << argc; }\n")
	out := filepath.Join(dir, "a.out")
	if b, err := exec.Command("cc", append(flags, "-o", out, filepath.Join(dir, "a.c"))...).CombinedOutput(); err != nil {
		t.Skipf("no cc that builds %v here: %v\n%s", flags, err, b)
	}
	return out
}

// fakeBuild is a tree whose configure records its arguments and a ninja that records its own
// and leaves binary as the target it is asked for, in the build directory -C names.
func fakeBuild(t *testing.T, binary string) (dir, calls string) {
	t.Helper()
	bin, dir := t.TempDir(), t.TempDir()
	calls = filepath.Join(bin, "calls")
	put(t, filepath.Join(bin, "ninja"), "#!/bin/sh\necho ninja \"$@\" >> "+calls+"\ncp "+binary+" \"$2/$3\"\n")
	put(t, filepath.Join(dir, "src/configure"), "#!/bin/sh\necho configure \"$@\" >> "+calls+"\n")
	for _, f := range []string{filepath.Join(bin, "ninja"), filepath.Join(dir, "src/configure")} {
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(dir, "src/configs/devices/x86_64-softmmu/default.mak"), "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, calls
}

// The sanitized QEMU is configured with the release's flags and its devices, laid out as a lab
// tree takes a QEMU, beside the release's firmware; and refused when what came out has no
// sanitizer, or when there is no firmware to lay beside it.
func TestTheSanitizedQEMUIsLaidOutBesideTheReleasesFirmware(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "qemu/Dockerfile"), "FROM x\nENV QEMU_FLAGS=\"\\\n    --static \\\n    --with-devices-x86_64=spin\"\n")
	put(t, filepath.Join(root, "qemu/devices.mak"), "CONFIG_VIRTIO_SCSI=y\n")
	firmware := t.TempDir()
	put(t, filepath.Join(firmware, "qboot.bin"), "qboot")

	dir, calls := fakeBuild(t, built(t, "-fsanitize=address,undefined"))
	laid, err := Sanitized(context.Background(), root, dir, firmware, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, calls); !strings.Contains(got, "configure --with-devices-x86_64=spin --disable-tcg --enable-asan --enable-ubsan --enable-debug-info\n") ||
		!strings.Contains(got, "ninja -C "+filepath.Join(dir, "build-sanitized")+" qemu-system-x86_64\n") {
		t.Errorf("calls:\n%s", got)
	}
	if got := read(t, filepath.Join(dir, "src/configs/devices/x86_64-softmmu/spin.mak")); got != "CONFIG_VIRTIO_SCSI=y\n" {
		t.Errorf("the tree's devices are %q, want the release's", got)
	}
	if err := sanitizedBinary(filepath.Join(laid, "bin/qemu-system-x86_64")); err != nil {
		t.Errorf("the laid out binary: %v", err)
	}
	if got := read(t, filepath.Join(laid, "qemu/qboot.bin")); got != "qboot" {
		t.Errorf("the firmware beside it is %q", got)
	}

	dir, _ = fakeBuild(t, built(t))
	if _, err := Sanitized(context.Background(), root, dir, firmware, io.Discard); err == nil || !strings.Contains(err.Error(), "__asan_init") {
		t.Errorf("a QEMU built without the sanitizers: %v", err)
	}
	if _, err := Sanitized(context.Background(), root, dir, t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "no release firmware") {
		t.Errorf("no firmware: %v", err)
	}
}

// The fuzzer is QEMU's libFuzzer target, built with clang under both sanitizers.
func TestTheFuzzerIsQEMUsLibFuzzerTargetUnderBothSanitizers(t *testing.T) {
	dir, calls := fakeBuild(t, built(t, "-fsanitize=address,undefined"))
	bin, err := Fuzzer(context.Background(), dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "build-fuzz", "qemu-fuzz-x86_64"); bin != want {
		t.Errorf("the fuzzer is %s, want %s", bin, want)
	}
	if got := read(t, calls); !strings.Contains(got, "configure --cc=clang --cxx=clang++ --enable-fuzzing --enable-asan --enable-ubsan") {
		t.Errorf("calls:\n%s", got)
	}
}
