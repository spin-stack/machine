// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/spin-stack/spin-machine/internal/build"
	"github.com/spin-stack/spin-machine/machine"
)

// kernelFixture is a relocatable bzImage and a vmlinux carrying a Xen note, as kernel/Dockerfile
// leaves them.
func kernelFixture(t *testing.T, out string) {
	t.Helper()
	bz := make([]byte, 0x300)
	copy(bz[0x202:], "HdrS")
	bz[0x234] = 1
	put(t, filepath.Join(out, "kernel/bzImage"), string(bz), 0o644)
	var note bytes.Buffer
	_ = binary.Write(&note, binary.LittleEndian, []uint32{4, 4, 18})
	note.WriteString("Xen\x00\x00\x00\x00\x01")
	var b bytes.Buffer
	h := elf.Header64{Type: uint16(elf.ET_EXEC), Machine: uint16(elf.EM_X86_64), Version: uint32(elf.EV_CURRENT),
		Phoff: 64, Ehsize: 64, Phentsize: 56, Phnum: 1, Shentsize: 64}
	copy(h.Ident[:], elf.ELFMAG)
	h.Ident[elf.EI_CLASS], h.Ident[elf.EI_DATA], h.Ident[elf.EI_VERSION] = byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)
	_ = binary.Write(&b, binary.LittleEndian, h)
	_ = binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_NOTE), Off: 120, Filesz: uint64(note.Len()), Memsz: uint64(note.Len())})
	b.Write(note.Bytes())
	put(t, filepath.Join(out, "kernel/vmlinux"), b.String(), 0o644)
	for _, f := range []string{"kernel/kernel-config", "kernel/sources", "kernel/patches/0001-k.patch"} {
		put(t, filepath.Join(out, f), f, 0o644)
	}
}

// everything is a whole built _output: what every part's build leaves, and spin-machine.
func everything(t *testing.T, dir string) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "_output")
	packed(t, filepath.Join(fixture, "..")) // the repository's files land beside it, unused
	src := qemuSource(t)
	for _, d := range []string{"bin", "qemu"} {
		entries, _ := os.ReadDir(filepath.Join(src, d))
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(src, d, e.Name()))
			put(t, filepath.Join(fixture, d, e.Name()), string(b), 0o755)
		}
	}
	kernelFixture(t, fixture)
	w, _ := open()
	e, _ := w.pins.Get("e2fsprogs")
	for _, b := range build.E2fsprogsBinaries {
		put(t, filepath.Join(fixture, "bin", b), "#!/bin/sh\necho mke2fs "+e.Version+"\n", 0o755)
	}
	put(t, filepath.Join(fixture, "systemd/systemd_1_amd64.deb"), "", 0o644)
	_ = os.Remove(filepath.Join(fixture, "bin/spin-machine"))
	for _, f := range []string{"LICENSE", "NOTICE", "qemu/qboot/COPYING", "qemu/qboot/a.patch", "qemu/patches/0001-a.patch",
		"qemu/Dockerfile", "kernel/config", "e2fsprogs/Dockerfile", "image/systemd/Dockerfile", "image/Dockerfile"} {
		put(t, filepath.Join(dir, f), f, 0o644)
	}
	return fixture
}

// A release builds every part that is missing, then packs them.
func TestAReleaseIsBuiltAndPacked(t *testing.T) {
	dir := checkout(t)
	gitRepo(t, dir)
	fixture := everything(t, dir)
	// Each build leaves its part: here, the whole fixture, which the next build's emptying trims.
	docker := `dest=""; prev=""; for a; do case "$a" in type=local,dest=*) dest=${a#type=local,dest=};; esac
  [ "$prev" = -v ] && case "$a" in *:/out) dest=${a%:/out};; esac; prev=$a; done
case "$dest" in */systemd) cp -r ` + fixture + `/systemd/. "$dest"/;; ?*) cp -r ` + fixture + `/. "$dest"/;; esac; exit 0`
	goTool := `out=""; prev=""; for a; do [ "$prev" = -o ] && out=$a; prev=$a; done
printf '#!/bin/sh\necho "{\\"fingerprint\\": \\"` + strings.Repeat("d", 64) + `\\"}"\n' > "$out"; chmod +x "$out"`
	fakes(t, map[string]string{"docker": docker, "go": goTool})
	stdout, stderr, status := verb(t, "release", "--version", "v20261119.11", "--json")
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if !strings.Contains(stdout, "spin-machine-v20261119.11-linux-x86_64.tar.gz") || !strings.Contains(stdout, strings.Repeat("d", 64)) {
		t.Errorf("--json said %s", stdout)
	}
	fakes(t, map[string]string{"docker": docker, "go": "exit 1"})
	if _, stderr, status := verb(t, "release", "--version", "v1"); status != 2 || !strings.Contains(stderr, "building spin-machine") {
		t.Errorf("a spin-machine that does not build: exit %d, %q", status, stderr)
	}
	fakes(t, map[string]string{"docker": docker, "go": goTool})
	_ = os.Remove(filepath.Join(dir, "LICENSE"))
	if _, stderr, status := verb(t, "release", "--version", "v1"); status != 2 || !strings.Contains(stderr, "the licence") {
		t.Errorf("a release missing its licence: exit %d, %q", status, stderr)
	}
}

func TestTheSBOMsClockIsTheCommitsUnlessSaid(t *testing.T) {
	dir := checkout(t)
	t.Setenv("SOURCE_DATE_EPOCH", "5")
	if got, err := sourceDateEpoch(context.Background(), dir); err != nil || !got.Equal(time.Unix(5, 0)) {
		t.Errorf("SOURCE_DATE_EPOCH=5 gave %v, %v", got, err)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "")
	if got, err := sourceDateEpoch(context.Background(), dir); err != nil || !got.Equal(time.Unix(0, 0)) {
		t.Errorf("no commit gave %v, %v; want the epoch", got, err)
	}
	gitRepo(t, dir)
	if got, err := sourceDateEpoch(context.Background(), dir); err != nil || !got.Equal(time.Unix(1763510400, 0)) {
		t.Errorf("the commit's time is %v, %v", got, err)
	}
	// git answers in the repository it is asked about, wherever the verb runs.
	t.Chdir(t.TempDir())
	if got := git(context.Background(), dir, "log", "-1", "--format=%ct"); got != "1763510400" {
		t.Errorf("git in %s said %q", dir, got)
	}
}

func TestAFailedGHSaysWhatItWasAskedAndWhy(t *testing.T) {
	fakes(t, map[string]string{"gh": "echo 'HTTP 404' >&2; exit 1"})
	_, err := gh(context.Background(), "release", "list", "--limit", "30")
	if err == nil || !strings.HasPrefix(err.Error(), "gh release list: ") || !strings.HasSuffix(err.Error(), ": HTTP 404") {
		t.Errorf("gh's failure says %v", err)
	}
	if _, err := gh(context.Background(), "x"); err == nil || !strings.HasPrefix(err.Error(), "gh x: ") {
		t.Errorf("a one-word call's failure says %v", err)
	}
}

func TestACommandRunsInTheDirectoryItIsGiven(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	if err := command(context.Background(), c, dir, "pwd"); err != nil || strings.TrimSpace(out.String()) != dir {
		t.Errorf("ran in %q, %v; want %q", out.String(), err, dir)
	}
}

func TestTheNotesSayANewerMachineTypeOnALineOfItsOwn(t *testing.T) {
	dir := checkout(t)
	share := filepath.Join(dir, "_output/release/spin-machine-v2-linux-x86_64/usr/share/spin-stack")
	put(t, filepath.Join(share, "machine.env"), "version=v2\n", 0o644)
	put(t, filepath.Join(share, "bin/qemu-system-x86_64"), "#!/bin/sh\nprintf 'pc-q35-99.0 newer\\n"+machine.MachineType+" q35\\n'\n", 0o755)
	fakes(t, map[string]string{"gh": `[ "$1 $2" = "release list" ] && echo v2; exit 0`})
	stdout, _, status := verb(t, "release", "notes", "--version", "v2")
	if status != 0 {
		t.Fatal(status)
	}
	if !regexp.MustCompile(`(?m)^Nothing to compare against: this is the first published release\.\n\nQEMU offers pc-q35-99\.0[^\n]*\n\nThe machine as it stood on this date\.\n\n` + "```\nversion=v2\n```\n$").MatchString(stdout) {
		t.Errorf("the notes are\n%s", stdout)
	}
	if !strings.HasPrefix(stdout, "The first release: nothing before it to say what changed against.\n\n") {
		t.Errorf("the notes do not begin with what changed:\n%s", stdout)
	}
	put(t, filepath.Join(share, "bin/qemu-system-x86_64"), "#!/bin/sh\nexit 1\n", 0o755)
	if _, _, status := verb(t, "release", "notes", "--version", "v2"); status != 2 {
		t.Errorf("a shipped QEMU that cannot be asked exited %d", status)
	}
}

func TestProvenanceIsWrittenForARun(t *testing.T) {
	dir := checkout(t)
	for k, v := range map[string]string{"GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "o/r",
		"GITHUB_WORKFLOW_REF": "o/r/.github/workflows/release.yml@refs/heads/main", "GITHUB_REF": "refs/heads/main",
		"GITHUB_SHA": strings.Repeat("a", 40), "GITHUB_EVENT_NAME": "push", "RUNNER_ENVIRONMENT": "github-hosted",
		"GITHUB_RUN_ID": "1", "GITHUB_RUN_ATTEMPT": "1"} {
		t.Setenv(k, v)
	}
	if _, stderr, status := verb(t, "release", "provenance", "--version", "v1"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, filepath.Join(dir, "_output/provenance.json")); !strings.Contains(got, "o/r") {
		t.Errorf("provenance.json holds %q", got)
	}
	put(t, filepath.Join(dir, "file"), "", 0o644)
	if _, _, status := verb(t, "--out", filepath.Join(dir, "file"), "release", "provenance", "--version", "v1"); status != 2 {
		t.Errorf("an output that is a file exited %d", status)
	}
	if _, _, status := verb(t, "--out", filepath.Join(dir, "_output/provenance.json/"), "release", "provenance", "--version", "v1"); status != 2 {
		t.Errorf("an output the predicate cannot be written in exited %d", status)
	}
}

func TestOneFileUnattestedIsRefused(t *testing.T) {
	dir := checkout(t)
	rel := filepath.Join(dir, "_output/release")
	for _, f := range []string{"r.tar.gz", "r.tar.gz.sha256", "machine.env", "r.cdx.json"} {
		put(t, filepath.Join(rel, f), f, 0o644)
	}
	// Every bundle but machine.env's provenance.
	fakes(t, map[string]string{"cosign": `while [ $# -gt 0 ]; do [ "$1" = --bundle ] && case "$2" in *machine.env.provenance*) ;; *) : > "$2";; esac; shift; done`})
	if _, stderr, status := verb(t, "release", "sign", "--identity", "me"); status != 1 || !strings.Contains(stderr, "not attested: machine.env") {
		t.Errorf("one bundle missing: exit %d, %q", status, stderr)
	}
	fakes(t, map[string]string{"cosign": "exit 1"})
	if _, stderr, _ := verb(t, "release", "sign", "--identity", "me"); !strings.Contains(stderr, "cosign sign-blob: ") {
		t.Errorf("a failed signature does not say which step: %q", stderr)
	}
}

func TestVsockMakesTheDeviceUsableOrSaysItIsNotThere(t *testing.T) {
	checkout(t)
	was := vsockDevice
	t.Cleanup(func() { vsockDevice = was })
	vsockDevice = filepath.Join(t.TempDir(), "absent")
	calls := fakes(t, map[string]string{"sudo": ""})
	if _, stderr, status := verb(t, "vsock"); status != 0 || !strings.Contains(stderr, "no vhost_vsock here") {
		t.Errorf("no device: exit %d, %q", status, stderr)
	}
	if got := read(t, calls); got != "sudo modprobe vhost_vsock\nsudo udevadm settle\n" {
		t.Errorf("sudo ran %q", got)
	}
	vsockDevice = filepath.Join(t.TempDir(), "file")
	put(t, vsockDevice, "", 0o644)
	if _, stderr, _ := verb(t, "vsock"); !strings.Contains(stderr, "no vhost_vsock here") {
		t.Errorf("a file that is not a device was taken for one: %q", stderr)
	}
	vsockDevice = "/dev/null"
	calls = fakes(t, map[string]string{"sudo": ""})
	if _, stderr, status := verb(t, "vsock"); status != 0 || !strings.Contains(read(t, calls), "sudo chmod 0666 /dev/null") {
		t.Errorf("a device: exit %d, %q, %q", status, stderr, read(t, calls))
	}
	fakes(t, map[string]string{"sudo": `[ "$1" = chmod ] && exit 1; exit 0`})
	if _, stderr, status := verb(t, "vsock"); status != 2 || !strings.Contains(stderr, "making /dev/null usable") {
		t.Errorf("a chmod that failed: exit %d, %q", status, stderr)
	}
}

func TestABumpIsOneEntryAndAtMostAVersion(t *testing.T) {
	checkout(t)
	fakes(t, map[string]string{"go": ""})
	if _, _, status := verb(t, "bump", "kernel", "7.3.9", "extra"); status == 0 {
		t.Error("a bump of three arguments ran")
	}
}

func TestTheLocksAreProposedFromAnywhereInTheCheckout(t *testing.T) {
	dir := checkout(t)
	put(t, filepath.Join(dir, "qemu/apk.list"), "musl\n", 0o644)
	put(t, filepath.Join(dir, "e2fsprogs/apk.list"), "musl\n", 0o644)
	put(t, filepath.Join(dir, "qemu/apk.lock"), "musl=1\n", 0o644)
	put(t, filepath.Join(dir, "e2fsprogs/apk.lock"), "musl=1\n", 0o644)
	gitRepo(t, dir)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	fakes(t, map[string]string{"docker": `printf 'P:musl\nV:2\n'`, "gh": ""})
	t.Chdir(filepath.Join(dir, "qemu"))
	// No origin: the branch is made and committed, and the push fails.
	if _, _, status := verb(t, "refresh", "apk"); status != 2 {
		t.Errorf("moved locks from a subdirectory exited %d", status)
	}
	branch, _ := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if !regexp.MustCompile(`^ref: refs/heads/refresh/apk-[0-9a-f]{12}\n$`).Match(branch) {
		t.Errorf("the branch is %q", branch)
	}
}

func TestAnOSVScanThatFailsIsNotAFinding(t *testing.T) {
	checkout(t)
	fakes(t, map[string]string{"gh": ghRelease, "cosign": "", "go": `echo '{"results":[]}'; exit 2`})
	if _, _, status := verb(t, "osv"); status != 2 {
		t.Errorf("osv-scanner failing with a report exited %d", status)
	}
}

func TestAnEmptyChecksumIsNoChecksum(t *testing.T) {
	checkout(t)
	gh := `case "$1 $2" in
"release view") echo v1;;
"release download") d=""; while [ $# -gt 0 ]; do [ "$1" = -D ] && d=$2; shift; done; mkdir -p "$d"; : > "$d/spin-machine-v1-linux-x86_64.tar.gz"; : > "$d/spin-machine-v1-linux-x86_64.tar.gz.sha256";;
esac`
	fakes(t, map[string]string{"gh": gh, "cosign": "", "git": `[ "$1" = clone ] && mkdir -p "$7/.git"; exit 0`})
	if _, stderr, status := verb(t, "cves"); status != 2 || !strings.Contains(stderr, "does not have the sha256") {
		t.Errorf("an empty checksum file: exit %d, %q", status, stderr)
	}
}

func TestAShellIsSystemdIn2GiBAndTwoCPUsByDefault(t *testing.T) {
	dir := checkout(t)
	out := filepath.Join(dir, "_output")
	for _, f := range []string{"bin/qemu-system-x86_64", "kernel/bzImage", "image/rootfs.qcow2"} {
		put(t, filepath.Join(out, f), "x", 0o644)
	}
	put(t, filepath.Join(out, "bin/spin-machine"), "#!/bin/sh\necho \"$*\" > "+filepath.Join(dir, "boot")+"\n", 0o755)
	if _, stderr, status := verb(t, "shell"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, filepath.Join(dir, "boot")); got != "boot --release "+out+" --memory 2048 --cpus 2 --init /sbin/init\n" {
		t.Errorf("spin-machine ran %q", got)
	}
}

func TestABuildIsEightJobsByDefaultAndSaysWhenItCannotTellTheWorkflow(t *testing.T) {
	dir := checkout(t)
	src := qemuSource(t)
	calls := fakes(t, map[string]string{"docker": `dest=""; for a; do case "$a" in type=local,dest=*) dest=${a#type=local,dest=};; esac; done
[ -n "$dest" ] && cp -r ` + src + `/bin ` + src + `/qemu "$dest"/; exit 0`})
	put(t, filepath.Join(dir, "qemu/Dockerfile"), "FROM x", 0o644)
	t.Setenv("GITHUB_OUTPUT", t.TempDir())
	if _, _, status := verb(t, "build", "qemu"); status != 2 {
		t.Errorf("a step's outputs that cannot be written: exit %d", status)
	}
	if !strings.Contains(read(t, calls), "--build-arg JOBS=8 ") {
		t.Errorf("the build is not eight jobs: %q", read(t, calls))
	}
}
