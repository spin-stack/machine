// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/elftest"
	"github.com/spin-stack/machine/machine"
)

// compatDir is a release or a machine whose QEMU says it is version, with a bzImage and what
// machine.OpenRelease asks of a release that boots one.
func compatDir(t *testing.T, dir, version string) {
	t.Helper()
	for _, f := range []string{"kernel/bzImage", "bin/qemu-img", "qemu/linuxboot_dma.bin", "qemu/" + machine.DefaultBIOS} {
		write(t, filepath.Join(dir, f), []byte(f))
	}
	write(t, filepath.Join(dir, "bin", "qemu-system-x86_64"), []byte("#!/bin/sh\necho 'QEMU emulator version "+version+" (v"+version+")'\n"))
}

// compatReleases is a checkout whose origin holds, for each tag, a commit whose spin-machine prints
// the tag, and the Sources whose release tag is a tree made from that tag's commit, its QEMU saying
// it is the version given.
func compatReleases(t *testing.T, versions map[string]string) (src Sources, rel func(tag string) string) {
	t.Helper()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	upstream := t.TempDir()
	git(upstream, "init", "-q")
	checkout := t.TempDir()
	git(checkout, "init", "-q")
	git(checkout, "remote", "add", "origin", upstream)
	src = Sources{Checkout: checkout, Cache: t.TempDir()}
	rel = func(tag string) string {
		return filepath.Join(src.Cache, "releases", tag, "spin-machine-"+tag+"-linux-x86_64", "usr", "share", "spin-stack")
	}
	for tag, version := range versions {
		write(t, filepath.Join(upstream, "go.mod"), []byte("module example.com/m\n\ngo 1.24\n"))
		write(t, filepath.Join(upstream, "cmd", "spin-machine", "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Print(\""+tag+"\") }\n"))
		git(upstream, "add", "-A")
		git(upstream, "commit", "-q", "-m", tag)
		compatDir(t, rel(tag), version)
		write(t, filepath.Join(rel(tag), "machine.env"), []byte("version="+tag+"\nsource_commit="+git(upstream, "rev-parse", "HEAD")+"\n"))
	}
	return src, rel
}

// whose is what the spin-machine at cli says it is: the tag of the release it was built for.
func whose(t *testing.T, cli string) string {
	t.Helper()
	out, err := exec.Command(cli).Output()
	if err != nil {
		t.Errorf("running %s: %v", cli, err)
	}
	return string(out)
}

// Compat resumes every release until this machine refuses one, each started by its own
// spin-machine, says which did not go on running and why the refused one was, and holds CanResume
// to QEMU's one-way promise on the way.
func TestCompatResumesEveryReleaseUntilOneIsRefused(t *testing.T) {
	here := t.TempDir()
	compatDir(t, here, "11.1.90")
	src, rel := compatReleases(t, map[string]string{"v3": "11.1.3", "v2": "11.1.3", "v1": "11.0.0", "v0": "11.0.0", "v4": "12.0.0"})
	var resumed []string
	resume := func(previous, previousCLI string) (string, error) {
		resumed = append(resumed, previous)
		if tag := whose(t, previousCLI); rel(tag) != previous {
			t.Errorf("the machine of %s started by %s's spin-machine", previous, tag)
		}
		switch previous {
		case rel("v2"):
			return "", errors.New("never went on counting")
		case rel("v1"):
			// A refusal comes with the test's own exit: it skipped, and said why.
			return "ram pc.bios: checkpoint 262144, here 65536 (same)", errors.New("exit status 0")
		}
		return "", nil
	}
	rows, err := Compat(here, []string{"v3", "v2", "v1", "v0"}, src, resume, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []CompatRow{
		{Release: "v3", Resumed: true},
		{Release: "v2", Why: "never went on counting"},
		{Release: "v1", Moved: true, Why: "ram pc.bios: checkpoint 262144, here 65536 (same)"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows\n%+v\nwant\n%+v", rows, want)
	}
	if !reflect.DeepEqual(resumed, []string{rel("v3"), rel("v2"), rel("v1")}) {
		t.Errorf("resumed %v: not past the refused release", resumed)
	}
	table := CompatTable(rows)
	for _, s := range []string{"| v3 | resumed |", "| v2 | **failed**: never went on counting |",
		"| v1 | not meant to: its machine is not this one (ram pc.bios: checkpoint 262144, here 65536 (same)); older releases neither |"} {
		if !strings.Contains(table, s) {
			t.Errorf("no %q in:\n%s", s, table)
		}
	}

	// A release whose QEMU is newer than here's: its checkpoints cannot resume here, which CanResume
	// says before a boot does.
	if _, err := Compat(here, []string{"v4"}, src, resume, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "v4: a checkpoint of qemu 12.0.0 cannot resume on this machine's qemu 11.1.90") {
		t.Errorf("a release with a newer QEMU: %v", err)
	}
	if _, err := Compat(t.TempDir(), nil, src, resume, io.Discard); err == nil || !strings.Contains(err.Error(), "asking") {
		t.Errorf("a machine with no QEMU: %v", err)
	}
	// A release whose spin-machine cannot be built has no machine to take a checkpoint on.
	write(t, filepath.Join(rel("v3"), "machine.env"), []byte("version=v3\n"))
	resumed = nil
	if _, err := Compat(here, []string{"v3"}, src, resume, io.Discard); err == nil || !strings.Contains(err.Error(), "names no source_commit") || resumed != nil {
		t.Errorf("a release with no spin-machine: %v, resumed %v", err, resumed)
	}
}

// A release from before v20261119.02 carries a vmlinux that runs where it was linked and no
// bzImage, and no spin-machine since boots it: Compat stops there with a row that says so, rather
// than failing on what it cannot open. A release from #660 on carries no bzImage either, and a
// vmlinux that randomizes itself, which the machine boots: it is checked.
func TestCompatStopsAtTheFirstReleaseTheMachineDoesNotBoot(t *testing.T) {
	here := t.TempDir()
	compatDir(t, here, "11.1.90")
	src, rel := compatReleases(t, map[string]string{"v2": "11.1.3", "v1": "11.1.3"})
	_ = os.Remove(filepath.Join(rel("v2"), "kernel", "bzImage"))
	write(t, filepath.Join(rel("v2"), "kernel", "vmlinux"), elftest.Kernel([]uint32{18, 19}, []byte{0x10, 0, 0, 0}))
	write(t, filepath.Join(rel("v2"), "qemu", "pvh.bin"), []byte("pvh"))
	if err := os.Rename(filepath.Join(rel("v1"), "kernel", "bzImage"), filepath.Join(rel("v1"), "kernel", "vmlinux")); err != nil {
		t.Fatal(err)
	}
	var resumed []string
	resume := func(previous, _ string) (string, error) {
		resumed = append(resumed, previous)
		return "", nil
	}
	rows, err := Compat(here, []string{"v2", "v1", "v0"}, src, resume, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 2 && !strings.Contains(rows[1].Why, "bzImage") {
		t.Errorf("v1's row does not say what is missing: %q", rows[1].Why)
	}
	if len(rows) == 2 {
		rows[1].Why = ""
	}
	want := []CompatRow{{Release: "v2", Resumed: true}, {Release: "v1", Unbootable: true}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows\n%+v\nwant\n%+v", rows, want)
	}
	if !reflect.DeepEqual(resumed, []string{rel("v2")}) {
		t.Errorf("resumed %v: not past the release without a bzImage", resumed)
	}
	if table := CompatTable(rows); !strings.Contains(table, "| v1 | not checked: this machine boots neither") {
		t.Errorf("no row for v1 in:\n%s", table)
	}
}

func TestCanResumeKeepsQEMUsOneWayPromise(t *testing.T) {
	for _, tc := range []struct {
		here, release, want string
	}{
		{"qemu 11.1.90", "qemu 11.1.3", ""},
		{"qemu 11.1.3", "qemu 11.1.3", ""},
		{"qemu 11.1.3", "qemu 11.1.90", "a checkpoint of qemu 11.1.90 cannot resume on this machine's qemu 11.1.3"},
	} {
		err := oneWay(tc.here, tc.release)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("oneWay(%s, %s) = %v, want %q", tc.here, tc.release, err, tc.want)
		}
	}
}

func TestQEMUsVersionIsWhatACheckpointRecords(t *testing.T) {
	dir := t.TempDir()
	compatDir(t, dir, "11.1.90")
	if v, err := qemuVersion(filepath.Join(dir, "bin", "qemu-system-x86_64")); err != nil || v != "qemu 11.1.90" {
		t.Errorf("qemuVersion = %q, %v", v, err)
	}
	write(t, filepath.Join(dir, "bin", "liar"), []byte("#!/bin/sh\necho 'not QEMU'\n"))
	if _, err := qemuVersion(filepath.Join(dir, "bin", "liar")); err == nil || !strings.Contains(err.Error(), "not its version") {
		t.Errorf("a binary that is not QEMU: %v", err)
	}
	if _, err := qemuVersion(filepath.Join(dir, "none")); err == nil {
		t.Error("a QEMU that is not there")
	}
}

// TestHelperQEMU is not a test: it is the QEMU a fake spin-machine's `args` prints, run as this
// binary. It makes its monitor's socket, and is a guest whose console is its stdin and stdout: the counter compat starts prints TICKs, a restored one goes on from far past any
// tick saved, and the disk read answers.
func TestHelperQEMU(t *testing.T) {
	if os.Getenv("LAB_FAKE_QEMU") != "1" {
		t.Skip("run as a fake spin-machine's QEMU")
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	fmt.Fprint(os.Stderr, os.Getenv("LAB_FAKE_QEMU_SAYS"))
	if _, err := net.Listen("unix", args[0]); err != nil {
		os.Exit(2)
	}
	f, err := os.OpenFile(os.Getenv("LAB_FAKE_QEMU_ARGS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
	_ = f.Close()
	_ = os.WriteFile(os.Getenv("LAB_FAKE_QEMU_ARGS")+"."+args[1], []byte(strconv.Itoa(os.Getpid())), 0o644)
	if args[1] == "restore" {
		for i := 1; i <= 1000; i++ {
			fmt.Printf("TICK-%d\n", i)
		}
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		switch {
		case strings.Contains(in.Text(), "TICK-$i"):
			go func() {
				for i := 1; ; i++ {
					fmt.Printf("TICK-%d\n", i)
					time.Sleep(20 * time.Millisecond)
				}
			}()
		case strings.Contains(in.Text(), "READ-"):
			fmt.Println("READ-386")
		}
	}
	select {}
}

// compatHere is a built release whose spin-machine runs TestHelperQEMU as its QEMU, whose save
// writes saved beside the state and whose target prints fakeTarget; it records each command it runs
// to the file it returns.
func compatHere(t *testing.T, saved machine.Saved) (here, asked string) {
	t.Helper()
	here = t.TempDir()
	asked = filepath.Join(here, "asked")
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(here, "saved.json"), raw)
	if raw, err = json.Marshal(fakeTarget); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(here, "target.json"), raw)
	write(t, filepath.Join(here, "kernel", "vmlinux"), []byte("vmlinux"))
	write(t, filepath.Join(here, "image", "rootfs.qcow2"), []byte("base"))
	write(t, filepath.Join(here, "bin", "qemu-img"), []byte("#!/bin/sh\nfor a; do o=$a; done; : > \"$o\"\n"))
	write(t, filepath.Join(here, "bin", "spin-machine"), []byte(`#!/bin/sh
echo "$@" >> `+asked+`
verb=$1; sock=""; mode=boot; to=""
while [ $# -gt 0 ]; do case "$1" in --qmp) sock=$2;; --to) to=$2;; --restore) mode=restore;; esac; shift; done
case "$verb" in
args) printf '  %s \\\n  -test.run=^TestHelperQEMU$ \\\n  -- \\\n  %s \\\n  %s\n' "`+os.Args[0]+`" "$sock" "$mode";;
save) cp `+filepath.Join(here, "saved.json")+` "$to.json";;
target) cat `+filepath.Join(here, "target.json")+`;;
esac
`))
	t.Setenv("LAB_FAKE_QEMU", "1")
	t.Setenv("LAB_FAKE_QEMU_ARGS", filepath.Join(here, "qemu-args"))
	return here, asked
}

// fakeTarget is the record of the machine a compatHere tree holds.
var fakeTarget = machine.Target{VMM: "qemu 11.2.0", Formats: []string{machine.FormatMappedRAM, machine.FormatStream}}

// A checkpoint the release before saved is resumed here when this machine takes it - the counter
// goes on and the disk answers - its machine started by its own spin-machine and held and restored
// by this tree's, at spin's shape; and when this machine refuses it, the refusal is every
// difference and nothing is restored.
func TestACheckpointOfTheReleaseBeforeResumesHere(t *testing.T) {
	saved := machine.Saved{Format: machine.FormatMappedRAM, From: fakeTarget}
	previous, previousAsked := compatHere(t, saved)
	previousCLI := filepath.Join(previous, "bin", "spin-machine")
	here, asked := compatHere(t, saved)
	var log strings.Builder
	refused, err := ResumeHere(t.Context(), here, previous, previousCLI, &log)
	if err != nil || refused != "" {
		t.Fatalf("ResumeHere = %q, %v", refused, err)
	}
	if !strings.Contains(log.String(), "went on counting on this machine") {
		t.Errorf("the log says %q", log.String())
	}
	got, previousGot := read(t, asked), read(t, previousAsked)
	for _, want := range []string{
		"args --release " + previous + " --held --cpus 1 --max-cpus 16 --memory 128 --max-memory 8192 --hotplug-disks 16 --qmp ",
		"start --release " + previous + " --qmp ",
	} {
		if !strings.Contains(previousGot, want) || strings.Contains(got, want) {
			t.Errorf("the release's spin-machine ran\n%s\nand here's\n%s\nwant %q from the release's alone", previousGot, got, want)
		}
	}
	for _, want := range []string{
		"args --release " + here + " --cpus 1 --max-cpus 16 --memory 128 --max-memory 8192 --hotplug-disks 16 --qmp ",
		"--disk-format qcow2 --init /bin/sh --restore\n",
		"save --qmp ",
		"restore --release " + here + " --qmp ",
		"--disk-format qcow2 --disk-under " + filepath.Join(previous, "image", "rootfs.qcow2") + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("spin-machine ran\n%s\nwithout %q", got, want)
		}
	}
	// QEMU runs the command line args printed, and each machine has a NIC of its own as spin gives one.
	ran := strings.Split(strings.TrimSpace(read(t, filepath.Join(here, "qemu-args"))), "\n")
	for i, mac := range []string{"mac=52:54:00:00:01:01,", "mac=52:54:00:00:01:02,"} {
		if i >= len(ran) || !strings.HasPrefix(ran[i], "-test.run=^TestHelperQEMU$ -- ") || !strings.Contains(ran[i], "-netdev hubport,id=net0,hubid=0 -device virtio-net-pci,netdev=net0,"+mac) {
			t.Errorf("QEMU ran %q, want machine %d with its own NIC (%s)", ran, i+1, mac)
		}
	}
	// Neither QEMU outlives the check.
	for _, mode := range []string{"boot", "restore"} {
		pid, err := strconv.Atoi(read(t, filepath.Join(here, "qemu-args."+mode)))
		if err != nil || syscall.Kill(pid, 0) == nil {
			t.Errorf("the machine that ran %s is still there (pid %d, %v)", mode, pid, err)
		}
	}
	if n := strings.Count(got+previousGot, "start --release"); n != 1 {
		t.Errorf("started %d machines, want the first alone: the one held for the checkpoint is restored, not started", n)
	}

	saved.From.VMM = "qemu 99.0.0"
	here, asked = compatHere(t, saved)
	log.Reset()
	refused, err = ResumeHere(t.Context(), here, here, filepath.Join(here, "bin", "spin-machine"), &log)
	if err != nil || !strings.Contains(refused, "qemu 99.0.0") || !strings.Contains(log.String(), "is not this one") {
		t.Errorf("a checkpoint of a newer QEMU: %q, %v, %q", refused, err, log.String())
	}
	if strings.Contains(read(t, asked), "restore --release") {
		t.Error("a refused checkpoint was restored")
	}
}

// Nothing boots on a tree missing a part, and a step that fails says which.
func TestACheckpointThatCannotBeTakenSaysWhy(t *testing.T) {
	saved := machine.Saved{Format: machine.FormatMappedRAM, From: fakeTarget}
	here, _ := compatHere(t, saved)
	_ = os.Remove(filepath.Join(here, "kernel", "vmlinux"))
	if _, err := ResumeHere(t.Context(), here, here, filepath.Join(here, "bin", "spin-machine"), io.Discard); err == nil || !strings.Contains(err.Error(), "no kernel/vmlinux in "+here) {
		t.Errorf("a tree with no kernel: %v", err)
	}
	for name, tc := range map[string]struct{ tool, script, says string }{
		"an overlay that cannot be made": {"qemu-img", "exit 1", "an overlay of the previous base image"},
		"a command line it cannot print": {"spin-machine", "exit 1", "spin-machine args"},
	} {
		here, _ := compatHere(t, saved)
		write(t, filepath.Join(here, "bin", tc.tool), []byte("#!/bin/sh\n"+tc.script+"\n"))
		if _, err := ResumeHere(t.Context(), here, here, filepath.Join(here, "bin", "spin-machine"), io.Discard); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	here, _ = compatHere(t, saved)
	cli := filepath.Join(here, "bin", "spin-machine")
	script := strings.Replace(read(t, cli), "#!/bin/sh\n", "#!/bin/sh\n[ \"$1\" = target ] && { echo 'no monitor here' >&2; exit 1; }\n", 1)
	write(t, cli, []byte(script))
	if _, err := ResumeHere(t.Context(), here, here, filepath.Join(here, "bin", "spin-machine"), io.Discard); err == nil || !strings.Contains(err.Error(), "spin-machine target: exit status 1: no monitor here") {
		t.Errorf("a record that cannot be read: %v", err)
	}
	// A QEMU that ends before start lets it go says why on its console, and the failure carries it.
	here, _ = compatHere(t, saved)
	cli = filepath.Join(here, "bin", "spin-machine")
	script = strings.Replace(read(t, cli), "#!/bin/sh\n", "#!/bin/sh\n[ \"$1\" = start ] && { while [ ! -S \"$5\" ]; do sleep 0.01; done; echo 'the monitor connection ended'; exit 1; }\n", 1)
	write(t, cli, []byte(script))
	t.Setenv("LAB_FAKE_QEMU_SAYS", "Property 'virtio-scsi-pci.max_target' not found")
	if _, err := ResumeHere(t.Context(), here, here, cli, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "spin-machine start: exit status 1: the monitor connection ended") || !strings.Contains(err.Error(), "max_target' not found") {
		t.Errorf("a machine that would not start: %v", err)
	}
}
