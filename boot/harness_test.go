// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// rawRoot is a private raw copy of a release's root filesystem, edited without mounts or NBD
// by the release's own debugfs: the one the image was made and checked with, not the host's,
// because an older one refuses a filesystem with features it does not know rather than editing
// it. debugfs exits 0 whether or not an edit happened - a write or a symlink into a directory
// that is not there included - so every edit here is read back.
type rawRoot struct {
	t       *testing.T
	path    string // the raw image, for a -drive
	debugfs string
	input   string
}

func newRawRoot(t *testing.T, out, base string) *rawRoot {
	t.Helper()
	dir := t.TempDir()
	r := &rawRoot{t: t, path: filepath.Join(dir, "rootfs.raw"), debugfs: filepath.Join(out, "bin/debugfs"),
		input: filepath.Join(dir, "input")}
	mustRun(t, filepath.Join(out, "bin/qemu-img"), "convert", "-f", "qcow2", "-O", "raw", base, r.path)
	return r
}

func (r *rawRoot) edit(request string) {
	r.t.Helper()
	mustRun(r.t, r.debugfs, "-w", "-R", request, r.path)
}

// mkdirParent makes p's parent. mkdir fails on a directory that is there, which is not a failure
// here; what is made in it is what is checked.
func (r *rawRoot) mkdirParent(p string) {
	_ = exec.Command(r.debugfs, "-w", "-R", "mkdir "+filepath.Dir(p), r.path).Run()
}

func (r *rawRoot) stat(p string) string {
	out, _ := exec.Command(r.debugfs, "-R", "stat "+p, r.path).CombinedOutput()
	return string(out)
}

func (r *rawRoot) write(p, content string) {
	r.t.Helper()
	if err := os.WriteFile(r.input, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.mkdirParent(p)
	r.edit("write " + r.input + " " + p)
	if b, err := exec.Command(r.debugfs, "-R", "cat "+p, r.path).Output(); err != nil || string(b) != content {
		r.t.Fatalf("%s is not in the guest's root as written (%v)", p, err)
	}
}

// remove fails on a path that is not there, so that an experiment removing something the image
// no longer has fails rather than measuring the image as built under another name.
func (r *rawRoot) remove(p string) {
	r.t.Helper()
	if !strings.Contains(r.stat(p), "Inode:") {
		r.t.Fatalf("%s is not in the image, so removing it tests nothing", p)
	}
	r.edit("rm " + p)
	if strings.Contains(r.stat(p), "Inode:") {
		r.t.Fatalf("%s is still in the guest's root", p)
	}
}

func (r *rawRoot) link(l, target string) {
	r.t.Helper()
	r.mkdirParent(l)
	r.edit("symlink " + l + " " + target)
	if !strings.Contains(r.stat(l), "Type: symlink") {
		r.t.Fatalf("%s is not a symlink in the guest's root", l)
	}
}

// releaseTree is _output, or SPIN_MACHINE_OUTPUT, without releaseDir's checks: a test that runs
// under TCG cannot ask for /dev/kvm.
func releaseTree(t *testing.T) string {
	t.Helper()
	out, err := filepath.Abs(cmp.Or(os.Getenv("SPIN_MACHINE_OUTPUT"), filepath.Join("..", "_output")))
	if err != nil {
		t.Fatalf("locating the release: %v", err)
	}
	return out
}

// releaseDir is the release tree the benchmarks boot: _output, or SPIN_MACHINE_OUTPUT - a tree
// laid out the same way, for measuring a kernel or an image built somewhere else without writing
// it over what `spin-machine-dev build` made.
func releaseDir(t *testing.T) string {
	t.Helper()
	out := releaseTree(t)
	for _, f := range []string{
		"bin/spin-machine", "bin/qemu-img", "kernel/vmlinux", "image/rootfs.qcow2",
	} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Skipf("no %s in %s — run: spin-machine-dev build", f, out)
		}
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm: these numbers are only meaningful under KVM")
	}
	return out
}

func envInt(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 {
		t.Fatalf("%s=%q is not a positive number", name, v)
	}
	return n
}

func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
