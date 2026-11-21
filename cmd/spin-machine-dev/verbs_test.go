// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/internal/release"
	"github.com/spin-stack/machine/machine"
)

// checkout makes a repository with this one's versions.yaml, works in it, and gives the workflow
// files a verb writes to; it returns the repository.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "..", "versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "versions.yaml"), string(raw), 0o644)
	t.Chdir(dir)
	t.Setenv("GITHUB_OUTPUT", filepath.Join(t.TempDir(), "output"))
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "summary"))
	return dir
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

// fakes puts programs on the PATH, each a shell script that records its arguments in calls and
// runs its body; it returns the file the calls are in.
func fakes(t *testing.T, programs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	for name, body := range programs {
		put(t, filepath.Join(dir, name), "#!/bin/sh\necho \""+name+" $*\" >> "+calls+"\n"+body+"\n", 0o755)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// verb runs the command with args, and says what it wrote and the status it exits with.
func verb(t *testing.T, args ...string) (stdout, stderr string, status int) {
	t.Helper()
	return execute(root(), args...)
}

// execute runs c with args as the command line, as verb runs root.
func execute(c *cobra.Command, args ...string) (stdout, stderr string, status int) {
	// Never nil: cobra reads os.Args when it is, which under the test runner are its own flags.
	c.SetArgs(append([]string{}, args...))
	var o, e bytes.Buffer
	c.SetOut(&o)
	c.SetErr(&e)
	c.SetIn(strings.NewReader(""))
	err := c.ExecuteContext(context.Background())
	return o.String(), e.String() + func() string {
		if err != nil {
			return err.Error()
		}
		return ""
	}(), exitCode(err)
}

func TestAWorkflowGetsACopyOfWhatAVerbSays(t *testing.T) {
	out, sum := filepath.Join(t.TempDir(), "o"), filepath.Join(t.TempDir(), "s")
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	if output("k", "v") != nil || summary("x") != nil {
		t.Error("with no workflow, a copy failed")
	}
	t.Setenv("GITHUB_OUTPUT", out)
	t.Setenv("GITHUB_STEP_SUMMARY", sum)
	if err := output("one", "v"); err != nil {
		t.Fatal(err)
	}
	if err := output("notes", "a\nb\n"); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(read(t, out), "\n")
	if got[0] != "one=v" || !strings.HasPrefix(got[1], "notes<<EOF_") || got[2] != "a" || got[3] != "b" || got[4] != strings.TrimPrefix(got[1], "notes<<") {
		t.Errorf("GITHUB_OUTPUT holds %q", got)
	}
	_ = summary("a\n")
	_ = summary("b\n")
	if read(t, sum) != "a\nb\n" {
		t.Errorf("the summary holds %q", read(t, sum))
	}
	t.Setenv("GITHUB_OUTPUT", t.TempDir())
	if output("k", "v") == nil {
		t.Error("an output that could not be written gave no error")
	}
}

func TestAProgramsExitStatusIsTheVerbs(t *testing.T) {
	if status(nil, "x") != nil {
		t.Error("success failed")
	}
	for code, want := range map[string]int{"1": 1, "3": 2, "128": 2} {
		err := status(exec.Command("sh", "-c", "exit "+code).Run(), "sh")
		if exitCode(err) != want || !strings.Contains(err.Error(), "sh:") {
			t.Errorf("exit %s became %d, %v; want %d", code, exitCode(err), err, want)
		}
	}
	if err := status(exec.Command("/no/such/program").Run(), "p"); exitCode(err) != 2 {
		t.Errorf("a program that could not start became %d", exitCode(err))
	}
}

func TestTheRepositoryIsFoundAboveTheWorkingDirectory(t *testing.T) {
	dir := checkout(t)
	put(t, filepath.Join(dir, "a/b/.keep"), "", 0o644)
	t.Chdir(filepath.Join(dir, "a", "b"))
	if got, err := repo(); err != nil || got != dir {
		t.Errorf("repo() = %q, %v; want %q", got, err, dir)
	}
	t.Chdir(t.TempDir())
	if _, err := repo(); exitCode(err) != 2 {
		t.Errorf("no repository gave %v", err)
	}
	if _, _, status := verb(t, "verify", "qemu"); status != 2 {
		t.Errorf("a verb outside a checkout exited %d", status)
	}
}

func TestOpenIsTheRepositoryItsOutputAndItsPins(t *testing.T) {
	dir := checkout(t)
	globals.out = "_output"
	t.Cleanup(func() { globals.out = "_output" })
	w, err := open()
	if err != nil || w.root != dir || w.out != filepath.Join(dir, "_output") || w.pins == nil {
		t.Errorf("open() = %+v, %v", w, err)
	}
	globals.out = "/elsewhere"
	if w, _ := open(); w.out != "/elsewhere" {
		t.Errorf("an absolute --out became %s", w.out)
	}
	put(t, filepath.Join(dir, "versions.yaml"), "not: [a list", 0o644)
	if _, err := open(); exitCode(err) != 2 {
		t.Errorf("an unreadable versions.yaml gave %v", err)
	}
}

func TestWhatAVerbIsAskedFor(t *testing.T) {
	if names, tools := parts(nil); names != nil || !tools {
		t.Errorf("nothing named: %v, %v", names, tools)
	}
	if names, tools := parts([]string{"qemu", "spin-machine", "image"}); strings.Join(names, " ") != "qemu image" || !tools {
		t.Errorf("parts and spin-machine: %v, %v", names, tools)
	}
	if names, tools := parts([]string{"kernel"}); strings.Join(names, " ") != "kernel" || tools {
		t.Errorf("a part: %v, %v", names, tools)
	}
	if rest, ok := without([]string{"qemu", "args", "kernel"}, "args"); !ok || strings.Join(rest, " ") != "qemu kernel" {
		t.Errorf("without = %v, %v", rest, ok)
	}
	if rest, ok := without([]string{"qemu"}, "args"); ok || len(rest) != 1 {
		t.Errorf("without, absent = %v, %v", rest, ok)
	}
}

// e2fsprogsDocker is a docker whose e2fsprogs build leaves the tools, answering version.
func e2fsprogsDocker(version string) string {
	return `dest=""; for a; do case "$a" in type=local,dest=*) dest=${a#type=local,dest=};; esac; done
[ -n "$dest" ] || exit 0
mkdir -p "$dest/bin" "$dest/e2fsprogs"; : > "$dest/e2fsprogs/mke2fs.conf"
for b in mkfs.ext4 e2fsck dumpe2fs debugfs; do printf '#!/bin/sh\necho mke2fs ` + version + `\n' > "$dest/bin/$b"; chmod +x "$dest/bin/$b"; done`
}

func TestBuildMakesWhatIsStaleAndSaysWhatItMade(t *testing.T) {
	dir := checkout(t)
	put(t, filepath.Join(dir, "e2fsprogs/Dockerfile"), "FROM x", 0o644)
	w, _ := open()
	e, _ := w.pins.Get("e2fsprogs")
	calls := fakes(t, map[string]string{"docker": e2fsprogsDocker(e.Version), "go": ""})

	stdout, stderr, status := verb(t, "build", "e2fsprogs", "spin-machine", "--json", "--jobs", "2", "--cache-registry", "--cache-export=false")
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	var results []build.Result
	if err := json.Unmarshal([]byte(stdout), &results); err != nil || len(results) != 2 || !results[0].Built || results[1].Part != "spin-machine" {
		t.Errorf("--json said %q (%v)", stdout, err)
	}
	got := read(t, calls)
	if !strings.Contains(got, "docker buildx build --file e2fsprogs/Dockerfile --target extract --platform linux/amd64 --cache-from type=registry,ref="+build.CacheRegistry+":e2fsprogs --build-arg") ||
		strings.Contains(got, "--cache-to") {
		t.Errorf("docker ran %q", got)
	}
	if !strings.Contains(got, "go build -ldflags -s -w -o "+filepath.Join(dir, "_output/bin/spin-machine")+" ./cmd/spin-machine") {
		t.Errorf("spin-machine was not built: %q", got)
	}
	if !strings.Contains(stderr, "✓ e2fsprogs") || !strings.Contains(stderr, "✓ spin-machine") {
		t.Errorf("what was built was not said: %q", stderr)
	}
	_, stderr, status = verb(t, "build", "e2fsprogs")
	if status != 0 || !strings.Contains(stderr, "e2fsprogs: current") || strings.Contains(stderr, "✓ e2fsprogs") {
		t.Errorf("a current part again: exit %d, %q", status, stderr)
	}
	if _, _, status := verb(t, "build", "nope"); status == 0 {
		t.Error("a part that is not one was built")
	}
	fakes(t, map[string]string{"docker": "exit 1"})
	if _, stderr, status := verb(t, "build", "e2fsprogs", "--rebuild"); status != 2 || !strings.Contains(stderr, "building e2fsprogs") {
		t.Errorf("a build that failed: exit %d, %q", status, stderr)
	}
	fakes(t, map[string]string{"go": "exit 1"})
	if _, stderr, status := verb(t, "build", "spin-machine"); status != 2 || !strings.Contains(stderr, "building spin-machine") {
		t.Errorf("a spin-machine that did not build: exit %d, %q", status, stderr)
	}
}

// recordedSchema is taken before any test moves to a checkout of its own.
var recordedSchema, _ = filepath.Abs(filepath.Join("..", "..", "internal", "qmpdef", "testdata", "qmp-schema.json"))

// qemuSource is a QEMU tree that verifies, the way build's tests make one. Its TCG binary answers
// on QMP too, at descriptor 3, with the schema recorded from the pinned QEMU, which every
// definition the module links agrees with (TestQEMUKnowsEveryCommand).
func qemuSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	monitor := `printf '{"QMP": {"version": {"qemu": {"micro": 90, "minor": 1, "major": 11}, "package": ""}, "capabilities": []}}\n' >&3
		while read -r line <&3; do
			id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
			case "$line" in
			*qmp_capabilities*) printf '{"return": {}, "id": %s}\n' "$id" >&3 ;;
			*query-qmp-schema*) { printf '{"return": '; tr -d '\n' <` + recordedSchema + `; printf ', "id": %s}\n' "$id"; } >&3 ;;
			esac
		done`
	answer := func(accels string) string {
		return `case "$1 $2" in "-machine none") ` + monitor + `;; esac
case "$1" in --version) echo QEMU;; -accel) printf '` + accels + `\n';; -machine) echo "` + machine.Type + ` q35";; esac`
	}
	put(t, filepath.Join(src, "bin/qemu-system-x86_64"), "#!/bin/sh\n"+answer("kvm")+"\n", 0o755)
	put(t, filepath.Join(src, "bin/qemu-system-x86_64-tcg"), "#!/bin/sh\n"+answer("kvm\\ntcg")+"\n", 0o755)
	put(t, filepath.Join(src, "bin/qemu-img"), "#!/bin/sh\necho qemu-img\n", 0o755)
	for _, f := range build.Firmware {
		size := 1
		if f == "qboot.bin" {
			size = 64 << 10
		}
		put(t, filepath.Join(src, "qemu", f), strings.Repeat("x", size), 0o644)
	}
	return src
}

func TestBuildingQEMUSaysItsVersionAndWhatItIs(t *testing.T) {
	dir := checkout(t)
	src := qemuSource(t)
	fakes(t, map[string]string{"docker": `dest=""; for a; do case "$a" in type=local,dest=*) dest=${a#type=local,dest=};; esac; done
[ -n "$dest" ] && cp -r ` + src + `/bin ` + src + `/qemu "$dest"/; exit 0`})
	put(t, filepath.Join(dir, "qemu/Dockerfile"), "FROM x", 0o644)
	if _, stderr, status := verb(t, "build", "qemu"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	w, _ := open()
	q, _ := w.pins.Get("qemu")
	if got := read(t, os.Getenv("GITHUB_OUTPUT")); got != "qemu_version="+q.Version+"\n" {
		t.Errorf("the step's outputs are %q", got)
	}
	if got := read(t, os.Getenv("GITHUB_STEP_SUMMARY")); !strings.HasPrefix(got, "qemu sha256: ") {
		t.Errorf("the run's summary is %q", got)
	}
}

func TestFetchIsThePublishedQEMU(t *testing.T) {
	checkout(t)
	src := qemuSource(t)
	calls := fakes(t, map[string]string{"docker": `case "$1" in create) echo cid;; cp) f=${2#cid:}; case "$f" in /usr/local/bin/*) cp ` + src + `/bin/${f##*/} "$3";; *) cp -r ` + src + `/qemu "$3";; esac;; esac`})
	if _, stderr, status := verb(t, "fetch", "qemu"); status != 0 || !strings.Contains(stderr, "✓ qemu, fetched") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if !strings.Contains(read(t, calls), "docker pull --quiet --platform linux/amd64 "+build.PublishedQEMU+":") {
		t.Errorf("docker ran %q", read(t, calls))
	}
	if _, _, status := verb(t, "fetch", "kernel"); status == 0 {
		t.Error("fetch took what it cannot fetch")
	}
	fakes(t, map[string]string{"docker": "exit 1"})
	if _, _, status := verb(t, "fetch", "qemu"); status != 2 {
		t.Errorf("a fetch that failed exited %d", status)
	}
}

func TestPublishIsUnderTheWorkflowsRepository(t *testing.T) {
	checkout(t)
	calls := fakes(t, map[string]string{"docker": ""})
	t.Setenv("GITHUB_REPOSITORY", "")
	if _, stderr, status := verb(t, "publish", "qemu"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	_, _, _ = verb(t, "publish", "qemu")
	got := strings.Split(strings.TrimSpace(read(t, calls)), "\n")
	if len(got) != 2 || !strings.Contains(got[0], "--tag ghcr.io/spin-stack/spin-machine/qemu:") || !strings.Contains(got[1], "--tag ghcr.io/o/r/qemu:") {
		t.Errorf("docker ran %q", got)
	}
	fakes(t, map[string]string{"docker": "exit 1"})
	if _, _, status := verb(t, "publish", "qemu"); status != 2 {
		t.Errorf("a push that failed exited %d", status)
	}
}

func TestVerifySaysWhatIsMissingAndAsksQEMUForTheArguments(t *testing.T) {
	dir := checkout(t)
	_, stderr, status := verb(t, "verify", "qemu", "kernel")
	if status != 1 || !strings.Contains(stderr, "qemu: missing") || !strings.Contains(stderr, "kernel: no kernel") {
		t.Errorf("verify of nothing built: exit %d, %q", status, stderr)
	}
	if _, stderr, status := verb(t, "verify"); status != 1 || !strings.Contains(stderr, "image: missing") {
		t.Errorf("verify of every part: exit %d, %q", status, stderr)
	}
	if _, stderr, status := verb(t, "verify", "args"); status != 2 || !strings.Contains(stderr, "fetch qemu") {
		t.Errorf("args with no QEMU: exit %d, %q", status, stderr)
	}
	put(t, filepath.Join(dir, "_output/bin/qemu-system-x86_64-tcg"), "", 0o755)
	calls := fakes(t, map[string]string{"go": `echo "SPIN_MACHINE_OUTPUT=$SPIN_MACHINE_OUTPUT" >> ` + filepath.Join(dir, "env")})
	t.Setenv("SPIN_MACHINE_OUTPUT", "before")
	if _, stderr, status := verb(t, "verify", "args"); status != 0 {
		t.Fatalf("args: exit %d, %q", status, stderr)
	}
	if got := read(t, calls); got != "go test ./machine -run TestQEMUAcceptsEveryArgument|TestAChainOverDescriptors -count=1 -v\n" {
		t.Errorf("go ran %q", got)
	}
	if read(t, filepath.Join(dir, "env")) != "SPIN_MACHINE_OUTPUT="+filepath.Join(dir, "_output")+"\n" || os.Getenv("SPIN_MACHINE_OUTPUT") != "before" {
		t.Errorf("the test was not pointed at _output, or the setting outlived it: %q", read(t, filepath.Join(dir, "env")))
	}
	_ = os.Unsetenv("SPIN_MACHINE_OUTPUT")
	_, _, _ = verb(t, "verify", "args")
	if _, set := os.LookupEnv("SPIN_MACHINE_OUTPUT"); set {
		t.Error("a setting that was not there was left behind")
	}
	fakes(t, map[string]string{"go": "exit 1"})
	if _, _, status := verb(t, "verify", "args"); status != 1 {
		t.Errorf("a QEMU that refuses an argument exited %d", status)
	}
	if _, _, status := verb(t, "verify", "nope"); status == 0 {
		t.Error("verify took a part that is not one")
	}
}

func TestChangedSaysWhetherAPartBuilds(t *testing.T) {
	checkout(t)
	stdout, _, status := verb(t, "changed", "--pins", "e2fsprogs", "e2fsprogs")
	if status != 0 || stdout != "build=true\n" || read(t, os.Getenv("GITHUB_OUTPUT")) != "build=true\n" {
		t.Errorf("no base: exit %d, %q, %q", status, stdout, read(t, os.Getenv("GITHUB_OUTPUT")))
	}
	if _, stderr, status := verb(t, "changed", "e2fsprogs"); status != 2 || !strings.Contains(stderr, "--pins") {
		t.Errorf("no pins: exit %d, %q", status, stderr)
	}
	if _, _, status := verb(t, "changed", "--pins", "e2fsprogs"); status == 0 {
		t.Error("no paths was taken")
	}
}

// gitRepo makes dir a repository with one commit, tagged with tags.
func gitRepo(t *testing.T, dir string, tags ...string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_COMMITTER_DATE=1763510400 +0000", "GIT_AUTHOR_DATE=1763510400 +0000")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "c")
	for _, tag := range tags {
		run("tag", tag)
	}
}

func TestReleaseVersionIsGivenOrTheNextOfToday(t *testing.T) {
	dir := checkout(t)
	gitRepo(t, dir, "v20200101.01")
	stdout, stderr, status := verb(t, "release", "version", "--given", "v20261119.05")
	if status != 0 || stdout != "v20261119.05\n" || !strings.Contains(stderr, "given by the caller") {
		t.Errorf("given: exit %d, %q, %q", status, stdout, stderr)
	}
	if read(t, os.Getenv("GITHUB_OUTPUT")) != "version=v20261119.05\n" {
		t.Errorf("the step's outputs are %q", read(t, os.Getenv("GITHUB_OUTPUT")))
	}
	if stdout, _, status := verb(t, "release", "version"); status != 0 || !strings.HasSuffix(stdout, ".01\n") {
		t.Errorf("today's first: exit %d, %q", status, stdout)
	}
	if _, _, status := verb(t, "release", "version", "--given", "v1; rm"); status != 2 {
		t.Errorf("a version that is not one exited %d", status)
	}
}

// packed lays out a built _output, for release pack.
func packed(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "_output")
	for _, f := range []string{"LICENSE", "NOTICE", "qemu/qboot/COPYING", "qemu/qboot/a.patch", "qemu/patches/0001-a.patch"} {
		put(t, filepath.Join(dir, f), f, 0o644)
	}
	for _, b := range append(append([]string{}, build.QEMUBinaries...), build.E2fsprogsBinaries...) {
		put(t, filepath.Join(out, "bin", b), b, 0o755)
	}
	for _, f := range build.Firmware {
		put(t, filepath.Join(out, "qemu", f), f, 0o644)
	}
	for _, f := range []string{"e2fsprogs/mke2fs.conf", "kernel/bzImage", "kernel/vmlinux", "kernel/kernel-config", "kernel/sources",
		"kernel/patches/0001-k.patch", "image/rootfs.qcow2", "licenses.txt"} {
		put(t, filepath.Join(out, f), f, 0o644)
	}
	put(t, filepath.Join(out, "packages.txt"), "bash 5.2-1 amd64\n", 0o644)
	apk := func(only string) string {
		var b strings.Builder
		for _, p := range strings.Split(only, ",") {
			b.WriteString("P:" + p + "\nV:1-r0\nA:x86_64\n\n")
		}
		return b.String()
	}
	put(t, filepath.Join(out, "sbom/qemu.apk"), apk("glib-static,pcre2-static,libffi-dev,pixman-static,zlib-static,libseccomp-static,libcap-ng-static,liburing-dev,libaio-dev,gettext-static,musl-dev,gcc"), 0o644)
	put(t, filepath.Join(out, "sbom/e2fsprogs.apk"), apk("libarchive-static,acl-static,zlib-static,xz-static,zstd-static,bzip2-static,lz4-static,expat-static,openssl-libs-static,musl-dev,gcc"), 0o644)
	return out
}

func TestReleasePackPacksWhatOutputHolds(t *testing.T) {
	dir := checkout(t)
	out := packed(t, dir)
	gitRepo(t, dir)
	t.Setenv("SOURCE_DATE_EPOCH", "")
	stdout, stderr, status := verb(t, "release", "pack", "--version", "v20261119.09", "--json")
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	var p struct {
		Tarball  string            `json:"tarball"`
		Manifest map[string]string `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(stdout), &p); err != nil || p.Tarball != filepath.Join(out, "release", "spin-machine-v20261119.09-linux-x86_64.tar.gz") ||
		p.Manifest["qemu_sha256"] == "" || len(p.Manifest["source_commit"]) != 40 {
		t.Errorf("--json said %q (%v)", stdout, err)
	}
	// A version from git describe, when none is given: the commit's own name here, untagged.
	if stdout, stderr, status := verb(t, "release", "pack", "--json"); status != 0 || !strings.Contains(stdout, "spin-machine-") {
		t.Errorf("no version: exit %d, %s", status, stderr)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "soon")
	if _, stderr, status := verb(t, "release", "pack", "--version", "v1"); status != 2 || !strings.Contains(stderr, "SOURCE_DATE_EPOCH") {
		t.Errorf("a clock that is not one: exit %d, %q", status, stderr)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "1763510400")
	_ = os.Remove(filepath.Join(out, "image/rootfs.qcow2"))
	if _, stderr, status := verb(t, "release", "pack", "--version", "v1"); status != 2 || !strings.Contains(stderr, "the base image") {
		t.Errorf("a tree with the image missing: exit %d, %q", status, stderr)
	}
}

func TestOutsideARepositoryAPackIsDevAndItsClockTheEpoch(t *testing.T) {
	dir := checkout(t)
	packed(t, dir)
	t.Setenv("SOURCE_DATE_EPOCH", "")
	t.Setenv("GIT_DIR", filepath.Join(dir, "no-repository"))
	stdout, stderr, status := verb(t, "release", "pack", "--json")
	if status != 0 || !strings.Contains(stdout, "spin-machine-dev-linux-x86_64.tar.gz") || !strings.Contains(stdout, `"source_commit": "unknown"`) {
		t.Errorf("no git: exit %d, %q, %s", status, stdout, stderr)
	}
}

func TestReleaseBuildsThenPacks(t *testing.T) {
	dir := checkout(t)
	put(t, filepath.Join(dir, "qemu/Dockerfile"), "FROM x", 0o644)
	fakes(t, map[string]string{"docker": "exit 1"})
	if _, stderr, status := verb(t, "release", "--version", "v1"); status != 2 || !strings.Contains(stderr, "building qemu") {
		t.Errorf("a release whose build fails: exit %d, %q", status, stderr)
	}
}

func TestReleaseNotesSayWhatChanged(t *testing.T) {
	dir := checkout(t)
	share := filepath.Join(dir, "_output/release/spin-machine-v2-linux-x86_64/usr/share/spin-stack")
	put(t, filepath.Join(share, "machine.env"), "version=v2\nqemu_sha256=q1\n", 0o644)
	put(t, filepath.Join(share, "bin/qemu-system-x86_64"), "#!/bin/sh\necho '"+machine.Type+" q35'\n", 0o755)
	gitRepo(t, dir, "v1")
	fakes(t, map[string]string{"gh": `[ "$1 $2" = "release list" ] && printf 'v2\nv1\n'; exit 0`})
	stdout, stderr, status := verb(t, "release", "notes", "--version", "v2")
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	for _, want := range []string{"The machine as it stood on this date.", "version=v2\nqemu_sha256=q1\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the notes lack %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "The first release") {
		t.Errorf("notes against v1 say there is none:\n%s", stdout)
	}
	if got := read(t, os.Getenv("GITHUB_OUTPUT")); !strings.HasPrefix(got, "notes<<EOF_") {
		t.Errorf("the notes are not on the step's outputs: %q", got)
	}
	fakes(t, map[string]string{"gh": `[ "$1 $2" = "release list" ] && echo v2; exit 0`})
	if stdout, _, _ := verb(t, "release", "notes", "--version", "v2"); !strings.HasPrefix(stdout, "The first release: nothing before it") {
		t.Errorf("the first release's notes:\n%s", stdout)
	}
	fakes(t, map[string]string{"gh": "exit 1"})
	if _, _, status := verb(t, "release", "notes", "--version", "v2"); status != 2 {
		t.Errorf("a listing that failed exited %d: it would announce a first release", status)
	}
	if _, _, status := verb(t, "release", "notes"); status != 2 {
		t.Errorf("no version exited %d", status)
	}
	if _, _, status := verb(t, "release", "notes", "--version", "v9"); status != 2 {
		t.Errorf("a release not packed exited %d", status)
	}
}

func TestProvenanceIsRefusedOutsideAWorkflow(t *testing.T) {
	dir := checkout(t)
	for _, k := range []string{"GITHUB_ACTIONS", "GITHUB_REPOSITORY", "GITHUB_SHA", "GITHUB_SERVER_URL", "GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID"} {
		t.Setenv(k, "")
	}
	if _, stderr, status := verb(t, "release", "provenance", "--version", "v1"); status != 2 || !strings.Contains(stderr, "not a GitHub Actions run") {
		t.Errorf("outside a workflow: exit %d, %q", status, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "_output/provenance.json")); err == nil {
		t.Error("provenance was written anyway")
	}
}

func TestSignChecksEveryBundleAndThatNothingIsLeftUnattested(t *testing.T) {
	dir := checkout(t)
	rel := filepath.Join(dir, "_output/release")
	for _, f := range []string{"r.tar.gz", "r.tar.gz.sha256", "machine.env", "r.cdx.json"} {
		put(t, filepath.Join(rel, f), f, 0o644)
	}
	// cosign writes each bundle it is asked for.
	calls := fakes(t, map[string]string{"cosign": `while [ $# -gt 0 ]; do [ "$1" = --bundle ] && : > "$2"; shift; done`})
	if _, stderr, status := verb(t, "release", "sign", "--identity", "me"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	got := read(t, calls)
	for _, want := range []string{
		"cosign sign-blob --yes --bundle " + filepath.Join(rel, "machine.env.sigstore.json"),
		"cosign verify-blob --bundle " + filepath.Join(rel, "r.tar.gz.sigstore.json") + " --certificate-identity me --certificate-oidc-issuer " + release.OIDCIssuer,
		"cosign attest-blob --yes --type cyclonedx --predicate " + filepath.Join(rel, "r.cdx.json"),
		"cosign verify-blob-attestation --type slsaprovenance1",
		"--digestAlg sha256 --certificate-identity me",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cosign was not asked %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "cosign sign-blob") != 4 || strings.Count(got, "cosign attest-blob --yes --type slsaprovenance1") != 4 {
		t.Errorf("not every file was signed and attested:\n%s", got)
	}
	bundles, _ := filepath.Glob(filepath.Join(rel, "*.sigstore.json"))
	for _, b := range bundles {
		_ = os.Remove(b)
	}
	fakes(t, map[string]string{"cosign": ""})
	if _, stderr, status := verb(t, "release", "sign", "--identity", "me"); status != 1 || !strings.Contains(stderr, "not attested") {
		t.Errorf("bundles never written: exit %d, %q", status, stderr)
	}
	for _, step := range []string{"sign-blob", "attest-blob", "verify-blob", "verify-blob-attestation"} {
		fakes(t, map[string]string{"cosign": `[ "$1" = ` + step + ` ] && exit 1; while [ $# -gt 0 ]; do [ "$1" = --bundle ] && : > "$2"; shift; done`})
		if _, _, status := verb(t, "release", "sign", "--identity", "me"); status != 1 {
			t.Errorf("a %s that failed exited %d", step, status)
		}
	}
	fakes(t, map[string]string{"cosign": `[ "$1 $2 $3 $4" = "attest-blob --yes --type cyclonedx" ] && exit 1; while [ $# -gt 0 ]; do [ "$1" = --bundle ] && : > "$2"; shift; done`})
	if _, _, status := verb(t, "release", "sign", "--identity", "me"); status != 1 {
		t.Errorf("an SBOM attestation that failed exited %d", status)
	}
	if _, _, status := verb(t, "release", "sign"); status != 2 {
		t.Errorf("no identity exited %d", status)
	}
}

func TestTagIsThroughTheAPI(t *testing.T) {
	dir := checkout(t)
	gitRepo(t, dir)
	calls := fakes(t, map[string]string{"gh": "echo refs/tags/v1", "git": ""})
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	if _, stderr, status := verb(t, "release", "tag", "--version", "v1"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	got := read(t, calls)
	if !strings.Contains(got, "gh api repos/o/r/git/refs -f ref=refs/tags/v1 -f sha=") || !strings.Contains(got, "git fetch --quiet origin refs/tags/v1:refs/tags/v1") {
		t.Errorf("ran %q", got)
	}
	fakes(t, map[string]string{"gh": "exit 1"})
	if _, _, status := verb(t, "release", "tag", "--version", "v1"); status != 1 {
		t.Errorf("a tag that exists exited %d", status)
	}
	t.Setenv("GITHUB_REPOSITORY", "")
	if _, _, status := verb(t, "release", "tag", "--version", "v1"); status != 2 {
		t.Errorf("no repository exited %d", status)
	}
}

func TestOSVFindingsAreOneLineEach(t *testing.T) {
	report := `{"results":[{"packages":[{"package":{"name":"curl","version":"8.5"},"vulnerabilities":[{"id":"USN-1","summary":"s"},{"id":"CVE-2"}]},
		{"package":{"name":"curl","version":"8.5"},"vulnerabilities":[{"id":"USN-1","summary":"s"}]}]}]}`
	got, err := OSVFindings(strings.NewReader(report))
	if err != nil || strings.Join(got, "|") != "CVE-2\tcurl@8.5\t|USN-1\tcurl@8.5\ts" {
		t.Errorf("OSVFindings = %q, %v", got, err)
	}
	if _, err := OSVFindings(strings.NewReader("{")); err == nil {
		t.Error("a report that is not JSON was read")
	}
}

// ghRelease is a gh that knows one release and downloads what it is asked for.
const ghRelease = `case "$1 $2" in
"release view") echo v20261119.01;;
"release download") d=""; while [ $# -gt 0 ]; do case "$1" in -D) d=$2;; -p) mkdir -p "$d"; echo "$2" > "$d/$2";; esac; shift; done;;
"issue list") echo '[]';;
"issue create") echo https://x/issues/1;;
esac`

// cosignPasses begins a fake go: release.Signed's go run of cosign says the signature is the
// release's, and the rest of the body is what go does otherwise (osv-scanner).
const cosignPasses = `case "$2" in github.com/sigstore/cosign/*) exit 0;; esac; `

func TestOSVWritesTheFindingsOfTheNewestRelease(t *testing.T) {
	dir := checkout(t)
	report := `{"results":[{"packages":[{"package":{"name":"curl","version":"8.5"},"vulnerabilities":[{"id":"USN-1","summary":"s"}]}]}]}`
	calls := fakes(t, map[string]string{"gh": ghRelease, "go": cosignPasses + "echo '" + report + "'; exit 1"})
	_, stderr, status := verb(t, "osv")
	if status != 0 || !strings.Contains(stderr, "v20261119.01: 1 finding(s)") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, filepath.Join(dir, "_output/osv.tsv")); got != "USN-1\tcurl@8.5\ts\n" {
		t.Errorf("osv.tsv holds %q", got)
	}
	if got := read(t, calls); !strings.Contains(got, "/cmd/cosign@") || !strings.Contains(got, "--certificate-identity "+release.Identity) ||
		!strings.Contains(got, "go run github.com/google/osv-scanner/v2/cmd/osv-scanner@") {
		t.Errorf("ran %q", got)
	}
	if _, stderr, status := verb(t, "osv", "--issues"); status != 0 || !strings.Contains(read(t, calls), "gh issue create") {
		t.Errorf("--issues: exit %d, %s", status, stderr)
	}
	fakes(t, map[string]string{"gh": ghRelease, "go": cosignPasses + "echo '{\"results\":[]}'"})
	if _, _, status := verb(t, "osv"); status != 0 || read(t, filepath.Join(dir, "_output/osv.tsv")) != "" {
		t.Errorf("no findings: exit %d, %q", status, read(t, filepath.Join(dir, "_output/osv.tsv")))
	}
	for name, p := range map[string]map[string]string{
		"gh":          {"gh": "exit 1", "go": cosignPasses + "echo '{}'"},
		"cosign":      {"gh": ghRelease, "go": "exit 1"},
		"osv-scanner": {"gh": ghRelease, "go": cosignPasses + "exit 2"},
		"osv JSON":    {"gh": ghRelease, "go": cosignPasses + "echo nope"},
	} {
		fakes(t, p)
		if _, _, status := verb(t, "osv"); status != 2 {
			t.Errorf("a %s that failed exited %d", name, status)
		}
	}
	fakes(t, map[string]string{"gh": strings.Replace(ghRelease, `"issue list") echo '[]';;`, `"issue list") exit 1;;`, 1), "go": cosignPasses + "echo '" + report + "'"})
	if _, _, status := verb(t, "osv", "--issues"); status != 1 {
		t.Errorf("issues that could not be listed exited %d", status)
	}
}

// cveRecord is one record, as the kernel CNA publishes it: a fix in 7.4 to a.c.
const cveRecord = `{"cveMetadata":{"cveId":"CVE-1"},"containers":{"cna":{"title":"t","affected":[{"defaultStatus":"affected","programFiles":["a.c"],"versions":[` +
	`{"version":"6.1","status":"affected"},{"version":"0","lessThan":"6.1","status":"unaffected","versionType":"semver"},` +
	`{"version":"7.4","lessThanOrEqual":"*","status":"unaffected","versionType":"original_commit_for_fix"}]}]}}}`

func TestCVEsJudgeAKernelByWhatItCompiled(t *testing.T) {
	dir := checkout(t)
	put(t, filepath.Join(dir, "_output/kernel/sources"), "a.c\n", 0o644)
	// git clone makes the records' repository; the rest is fetching it again.
	calls := fakes(t, map[string]string{"git": `[ "$1" = clone ] && mkdir -p "$7/.git" "$7/cve/published/2026" && echo '` + cveRecord + `' > "$7/cve/published/2026/CVE-1.json"; exit 0`})
	stdout, stderr, status := verb(t, "cves", "--local")
	if status != 0 || !strings.Contains(stdout, "| Apply") {
		t.Fatalf("exit %d: %q %s", status, stdout, stderr)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "_output/cves/summary.md")), "CVE-1") || !strings.Contains(read(t, os.Getenv("GITHUB_STEP_SUMMARY")), "CVE-1") {
		t.Error("the summary was not kept")
	}
	if got := read(t, calls); !strings.Contains(got, "git clone -q --depth 1 --sparse https://git.kernel.org/pub/scm/linux/security/vulns.git") ||
		!strings.Contains(got, "sparse-checkout set cve/published") || !strings.Contains(got, "reset -q --hard FETCH_HEAD") {
		t.Errorf("git ran %q", got)
	}
	_, _, _ = verb(t, "cves", "--local")
	if strings.Count(read(t, calls), "git clone") != 1 {
		t.Error("records already there were cloned again")
	}
	_ = os.Remove(filepath.Join(dir, "_output/kernel/sources"))
	if _, _, status := verb(t, "cves", "--local"); status != 1 {
		t.Errorf("no sources list exited %d", status)
	}
	for _, step := range []string{"clone", "sparse-checkout", "fetch", "reset"} {
		_ = os.RemoveAll(filepath.Join(dir, "_output/cves"))
		fakes(t, map[string]string{"git": `for a; do [ "$a" = ` + step + ` ] && exit 1; done; [ "$1" = clone ] && mkdir -p "$7/.git"; exit 0`})
		if _, _, status := verb(t, "cves", "--local"); status == 0 {
			t.Errorf("a git %s that failed passed", step)
		}
	}
}

func TestCVEsOfAReleaseAreOfItsSignedTarball(t *testing.T) {
	dir := checkout(t)
	tar := t.TempDir()
	share := filepath.Join(tar, "spin-machine-v20261119.01-linux-x86_64/usr/share/spin-stack")
	put(t, filepath.Join(share, "machine.env"), "kernel_version=7.3.3\n", 0o644)
	put(t, filepath.Join(share, "kernel/sources"), "a.c\n", 0o644)
	tarball := filepath.Join(t.TempDir(), "spin-machine-v20261119.01-linux-x86_64.tar.gz")
	if out, err := exec.Command("tar", "-czf", tarball, "-C", tar, "spin-machine-v20261119.01-linux-x86_64").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	sum, _ := build.FileSHA256(tarball)
	gh := `case "$1 $2" in
"release view") echo v20261119.01;;
"release download") d=""; while [ $# -gt 0 ]; do [ "$1" = -D ] && d=$2; shift; done; mkdir -p "$d"
  cp ` + tarball + ` "$d"/; echo "SUM  spin-machine-v20261119.01-linux-x86_64.tar.gz" > "$d/spin-machine-v20261119.01-linux-x86_64.tar.gz.sha256";;
esac`
	records := `[ "$1" = clone ] && mkdir -p "$7/.git" "$7/cve/published/2026" && echo '` + cveRecord + `' > "$7/cve/published/2026/CVE-1.json"; exit 0`
	fakes(t, map[string]string{"gh": strings.Replace(gh, "SUM", sum, 1), "go": "", "git": records})
	stdout, stderr, status := verb(t, "cves")
	if status != 0 || !strings.Contains(stdout, "| Apply") {
		t.Fatalf("exit %d: %q %s", status, stdout, stderr)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "_output/cves/summary.md")), "7.3.3") {
		t.Error("the release's kernel version was not the one judged")
	}
	fakes(t, map[string]string{"gh": strings.Replace(gh, "SUM", sum, 1), "go": "exit 1", "git": records})
	if _, stderr, status := verb(t, "cves"); status != 2 || !strings.Contains(stderr, "signature") {
		t.Errorf("a tarball not signed by the release: exit %d, %q", status, stderr)
	}
	fakes(t, map[string]string{"gh": "exit 1", "git": records})
	if _, _, status := verb(t, "cves"); status != 2 {
		t.Errorf("no release exited %d", status)
	}
	// A manifest from before kernel_version names no kernel to judge.
	put(t, filepath.Join(share, "machine.env"), "qemu_version=11.1.2\n", 0o644)
	if out, err := exec.Command("tar", "-czf", tarball, "-C", tar, "spin-machine-v20261119.01-linux-x86_64").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	sum, _ = build.FileSHA256(tarball)
	fakes(t, map[string]string{"gh": strings.Replace(gh, "SUM", sum, 1), "go": "", "git": records})
	if _, stderr, status := verb(t, "cves"); status != 2 || !strings.Contains(stderr, "is not a kernel version") {
		t.Errorf("a release with no kernel version: exit %d, %q", status, stderr)
	}
	// A release from before kernel/Dockerfile wrote the sources list has none to judge by.
	_ = os.Remove(filepath.Join(share, "kernel/sources"))
	if out, err := exec.Command("tar", "-czf", tarball, "-C", tar, "spin-machine-v20261119.01-linux-x86_64").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	sum, _ = build.FileSHA256(tarball)
	fakes(t, map[string]string{"gh": strings.Replace(gh, "SUM", sum, 1), "go": "", "git": records})
	if _, stderr, status := verb(t, "cves"); status != 2 || !strings.Contains(stderr, "carries no kernel/sources") {
		t.Errorf("a release with no sources list: exit %d, %q", status, stderr)
	}
}

func TestOutdatedAndBumpAreGoToolsVersions(t *testing.T) {
	checkout(t)
	calls := fakes(t, map[string]string{"go": `[ "$3" = check ] && exit 1; exit 0`})
	if _, _, status := verb(t, "outdated"); status != 1 {
		t.Errorf("a pin behind exited %d", status)
	}
	if _, _, status := verb(t, "bump", "kernel", "7.3.9"); status != 0 {
		t.Errorf("a bump exited %d", status)
	}
	if got := read(t, calls); got != "go tool versions check\ngo tool versions bump kernel 7.3.9\n" {
		t.Errorf("go ran %q", got)
	}
	if _, _, status := verb(t, "bump"); status == 0 {
		t.Error("a bump of nothing ran")
	}
	fakes(t, map[string]string{"go": "exit 1"})
	if _, _, status := verb(t, "bump", "kernel"); status != 1 {
		t.Errorf("a bump that failed exited %d", status)
	}
}

func TestAQEMUBumpSaysWhatNewerTypesChange(t *testing.T) {
	dir := checkout(t)
	w, _ := open()
	q, _ := w.pins.Get("qemu")
	src := t.TempDir()
	put(t, filepath.Join(src, "qemu-"+q.Version, "hw/core/machine.c"), "GlobalProperty hw_compat_99_0[] = {};\n", 0o644)
	put(t, filepath.Join(src, "qemu-"+q.Version, "hw/i386/pc.c"), "GlobalProperty pc_compat_99_0[] = {};\n", 0o644)
	tarball := filepath.Join(t.TempDir(), "q.tar.xz")
	if out, err := exec.Command("tar", "-cJf", tarball, "-C", src, "qemu-"+q.Version).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	fakes(t, map[string]string{"go": "", "curl": "cp " + tarball + " \"$4\""})
	// The pin is the real QEMU's, so this tarball does not have its sha256: refused.
	if _, stderr, status := verb(t, "bump", "qemu"); status != 2 || !strings.Contains(stderr, "sha256") {
		t.Errorf("a source not the pin's: exit %d, %q", status, stderr)
	}
	sum, _ := build.FileSHA256(tarball)
	raw := read(t, filepath.Join(dir, "versions.yaml"))
	put(t, filepath.Join(dir, "versions.yaml"), strings.Replace(raw, q.Pin, sum, 1), 0o644)
	if stdout, stderr, status := verb(t, "bump", "qemu"); status != 0 || !strings.Contains(stdout, "is the newest type in this QEMU") {
		t.Errorf("a QEMU with nothing newer: exit %d, %q %s", status, stdout, stderr)
	}
	fakes(t, map[string]string{"go": "", "curl": "exit 22"})
	if _, _, status := verb(t, "bump", "qemu"); status != 2 {
		t.Errorf("a source that could not be fetched exited %d", status)
	}
}

func TestRefreshProposesThroughVersionsOrTheBuilders(t *testing.T) {
	checkout(t)
	calls := fakes(t, map[string]string{"go": ""})
	for _, what := range []string{"kernel", "systemd"} {
		if _, _, status := verb(t, "refresh", what); status != 0 {
			t.Errorf("refresh %s exited %d", what, status)
		}
	}
	if got := read(t, calls); got != "go tool versions refresh -propose -label kernel kernel\ngo tool versions refresh -propose -label systemd systemd-source\n" {
		t.Errorf("go ran %q", got)
	}
	if _, _, status := verb(t, "refresh", "nope"); status == 0 {
		t.Error("refresh took what it does not refresh")
	}
}

// fakeDocker is a docker whose every image holds musl at version, and whose pushed builders have
// the digests aaaa… and bbbb…; with noPinned, the pinned builders cannot be read.
func fakeDocker(version string, noPinned ...bool) string {
	pinned := ""
	if len(noPinned) > 0 {
		pinned = `case "$*" in *@sha256:*) exit 1;; esac; `
	}
	return `case "$1" in
run) ` + pinned + `printf 'P:musl\nV:` + version + `\n';;
inspect) case "$4" in
  *builder-qemu*) echo ghcr.io/spin-stack/spin-machine/builder-qemu@sha256:` + strings.Repeat("a", 64) + `;;
  *) echo ghcr.io/spin-stack/spin-machine/builder-e2fsprogs@sha256:` + strings.Repeat("b", 64) + `;;
  esac;;
esac`
}

// builderCheckout is a checkout with both apk.lists, committed on main, with a bare origin.
func builderCheckout(t *testing.T) string {
	t.Helper()
	dir := checkout(t)
	put(t, filepath.Join(dir, "qemu/apk.list"), "musl\n", 0o644)
	put(t, filepath.Join(dir, "e2fsprogs/apk.list"), "musl\n", 0o644)
	gitRepo(t, dir)
	bare := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--bare", bare}, {"-C", dir, "branch", "-M", "main"}, {"-C", dir, "remote", "add", "origin", bare}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
	return dir
}

// On a branch, a builder made again is published and its pin committed there; made again with
// the same packages, it is the one pinned.
func TestRefreshBuildersPinsABranchsOwnBuilder(t *testing.T) {
	dir := builderCheckout(t)
	// No GITHUB_REF_NAME, as on a person's machine: the branch is the one checked out.
	t.Setenv("GITHUB_REF_NAME", "")
	if out, err := exec.Command("git", "-C", dir, "switch", "-q", "-c", "work").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	calls := fakes(t, map[string]string{"docker": fakeDocker("1.2.5-r10")})
	if _, stderr, status := verb(t, "refresh", "builders"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	w, _ := open()
	q, _ := w.pins.Get("qemu-builder")
	e, _ := w.pins.Get("e2fsprogs-builder")
	if q.Pin != "sha256:"+strings.Repeat("a", 64) || e.Pin != "sha256:"+strings.Repeat("b", 64) || len(q.Version) != 12 {
		t.Errorf("pinned %s and %s", q.Ref(), e.Ref())
	}
	got := read(t, calls)
	for _, want := range []string{
		"docker buildx build --no-cache --load --platform linux/amd64 --file builder/Dockerfile",
		"--build-arg PART=qemu",
		"docker push --quiet ghcr.io/spin-stack/spin-machine/builder-qemu:" + q.Version,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("docker ran %q, without %q", got, want)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "ls-remote", "--exit-code", "--heads", "origin", "work").CombinedOutput(); err != nil {
		t.Errorf("nothing pushed to work: %v: %s", err, out)
	}
	// The pinned builder holds what the new one does: what moved is said, and it is nothing.
	if got := read(t, os.Getenv("GITHUB_STEP_SUMMARY")); !strings.Contains(got, "### qemu-builder "+q.Version+"\n\nno package moved") {
		t.Errorf("the run's summary is %q", got)
	}
	if _, stderr, status := verb(t, "refresh", "builders"); status != 0 || !strings.Contains(stderr, "the builders are current") {
		t.Errorf("the same builders again: exit %d, %q", status, stderr)
	}
}

// From main, the builders are proposed, the proposal they supersede closed, and a set proposed
// once is not proposed again.
func TestRefreshBuildersProposesFromMain(t *testing.T) {
	dir := builderCheckout(t)
	t.Setenv("GITHUB_REF_NAME", "main")
	calls := fakes(t, map[string]string{"docker": fakeDocker("1.2.6-r0", true), "gh": `case "$1 $2" in
"pr create") echo https://x/pull/8;;
"pr list") echo 7;;
esac`})
	if _, stderr, status := verb(t, "refresh", "builders"); status != 0 || !strings.Contains(stderr, "https://x/pull/8") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	got := read(t, calls)
	if !strings.Contains(read(t, os.Getenv("GITHUB_STEP_SUMMARY")), "no builder was pinned to compare with") {
		t.Error("a pinned builder that could not be read is not said")
	}
	if !strings.Contains(got, "gh pr create --base main --head refresh/builders-") || !strings.Contains(got, "gh pr close 7 --delete-branch --comment Superseded by https://x/pull/8.") {
		t.Errorf("gh ran %q", got)
	}
	if out, err := exec.Command("git", "-C", dir, "checkout", "-q", "-f", "main").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, stderr, status := verb(t, "refresh", "builders"); status != 0 || !strings.Contains(stderr, "was proposed already") {
		t.Errorf("the same builders again: exit %d, %q", status, stderr)
	}
	fakes(t, map[string]string{"docker": "exit 1"})
	if _, _, status := verb(t, "refresh", "builders"); status != 2 {
		t.Errorf("a builder that could not be made exited %d", status)
	}
}

func TestShellBootsTheBaseAndChecksItAfter(t *testing.T) {
	dir := checkout(t)
	out := filepath.Join(dir, "_output")
	if _, stderr, status := verb(t, "shell"); status != 2 || !strings.Contains(stderr, "missing") {
		t.Errorf("nothing built: exit %d, %q", status, stderr)
	}
	for _, f := range []string{"bin/qemu-system-x86_64", "kernel/bzImage", "image/rootfs.qcow2"} {
		put(t, filepath.Join(out, f), "x", 0o644)
	}
	log := filepath.Join(dir, "boot")
	put(t, filepath.Join(out, "bin/spin-machine"), "#!/bin/sh\necho \"$*\" > "+log+"\n", 0o755)
	if _, stderr, status := verb(t, "shell", "--init", "/bin/sh", "--memory", "512", "--cpus", "1"); status != 0 || !strings.Contains(stderr, "rootfs.qcow2 unchanged") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, log); got != "boot --release "+out+" --memory 512 --cpus 1 --init /bin/sh\n" {
		t.Errorf("spin-machine ran %q", got)
	}
	put(t, filepath.Join(out, "bin/spin-machine"), "#!/bin/sh\necho written >> "+filepath.Join(out, "image/rootfs.qcow2")+"\n", 0o755)
	if _, stderr, status := verb(t, "shell"); status != 1 || !strings.Contains(stderr, "the base image changed") {
		t.Errorf("a session that wrote the base: exit %d, %q", status, stderr)
	}
	put(t, filepath.Join(out, "bin/spin-machine"), "#!/bin/sh\nrm "+filepath.Join(out, "image/rootfs.qcow2")+"\n", 0o755)
	if _, _, status := verb(t, "shell"); status != 2 {
		t.Errorf("a session that removed the base exited %d", status)
	}
}

func TestMenuconfigEditsTheConfigInPlace(t *testing.T) {
	dir := checkout(t)
	calls := fakes(t, map[string]string{"docker": ""})
	t.Setenv("TERM", "")
	if _, stderr, status := verb(t, "menuconfig"); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	w, _ := open()
	k, _ := w.pins.Get("kernel")
	series := strings.Join(strings.SplitN(k.Version, ".", 3)[:2], ".")
	got := read(t, calls)
	for _, want := range []string{"docker buildx build --file kernel/Dockerfile --target kernel-build-base --platform linux/amd64 --cache-from type=local,src=" + filepath.Join(dir, ".cache/buildkit/kernel"),
		"--build-arg KERNEL_SERIES=" + series, "--output type=docker,name=spin-machine-menuconfig .",
		"docker run --rm -it -v " + filepath.Join(dir, "kernel") + ":/config -w /usr/src/linux -e KCONFIG_CONFIG=/config/config-" + series + "-x86_64 -e TERM=xterm-256color spin-machine-menuconfig make ARCH=x86_64 menuconfig"} {
		if !strings.Contains(got, want) {
			t.Errorf("docker was not asked %q:\n%s", want, got)
		}
	}
	fakes(t, map[string]string{"docker": "exit 1"})
	if _, _, status := verb(t, "menuconfig"); status != 1 {
		t.Errorf("a build that failed exited %d", status)
	}
}

func TestCleanRemovesTheOutputAndTheCachesWhenAsked(t *testing.T) {
	dir := checkout(t)
	for _, f := range []string{"_output/x", ".cache/buildkit/qemu/x", ".cache/mkosi/x", ".cache/other/x"} {
		put(t, filepath.Join(dir, f), "", 0o644)
	}
	if _, _, status := verb(t, "clean"); status != 0 {
		t.Fatal(status)
	}
	if _, err := os.Stat(filepath.Join(dir, "_output")); err == nil {
		t.Error("_output survived")
	}
	if _, err := os.Stat(filepath.Join(dir, ".cache/mkosi")); err != nil {
		t.Error("a cache went without --cache")
	}
	if _, _, status := verb(t, "clean", "--cache"); status != 0 {
		t.Fatal(status)
	}
	for f, gone := range map[string]bool{".cache/buildkit": true, ".cache/mkosi": true, ".cache/other": false} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) == gone {
			t.Errorf("%s: gone %v, want %v", f, err != nil, gone)
		}
	}
	if os.Getuid() != 0 {
		put(t, filepath.Join(dir, "_output/d/f"), "", 0o644)
		_ = os.Chmod(filepath.Join(dir, "_output/d"), 0o500)
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "_output/d"), 0o755) })
		if _, _, status := verb(t, "clean"); status != 2 {
			t.Errorf("what could not be removed exited %d", status)
		}
	}
}

func TestEmitWritesJSONOnlyWhenAsked(t *testing.T) {
	var b bytes.Buffer
	globals.json = false
	if emit(&b, map[string]int{"a": 1}) != nil || b.Len() != 0 {
		t.Error("JSON without --json")
	}
	globals.json = true
	t.Cleanup(func() { globals.json = false })
	if emit(&b, map[string]int{"a": 1}) != nil || b.String() != "{\n  \"a\": 1\n}\n" {
		t.Errorf("--json wrote %q", b.String())
	}
}

// A workflow's summary passes on jobs that passed or were skipped, and on nothing else - not on a
// failure, a cancellation, or a NEEDS it cannot read.
func TestNeedsPassesOnlyWhatPassedOrWasSkipped(t *testing.T) {
	checkout(t)
	for needs, want := range map[string]int{
		`{"changed":{"result":"success"},"qemu":{"result":"skipped"},"kernel":{"result":"success"}}`: 0,
		`{"changed":{"result":"success"},"qemu":{"result":"failure"}}`:                               1,
		`{"changed":{"result":"success"},"image":{"result":"cancelled"}}`:                            1,
		`{"changed":{"result":"failure"},"qemu":{"result":"skipped"}}`:                               1,
		`{}`:       2,
		``:         2,
		`not json`: 2,
	} {
		t.Setenv("NEEDS", needs)
		if _, stderr, status := verb(t, "needs"); status != want {
			t.Errorf("NEEDS=%s: exit %d, want %d: %s", needs, status, want, stderr)
		}
	}
	t.Setenv("NEEDS", `{"qemu":{"result":"failure"},"kernel":{"result":"success"}}`)
	if _, stderr, _ := verb(t, "needs"); !strings.Contains(stderr, "kernel: success") || !strings.Contains(stderr, "not every job passed: qemu failure") {
		t.Errorf("said %q", stderr)
	}
}
