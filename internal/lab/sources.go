// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// from is the tree a spec starts from and how a manifest names it.
func (s Sources) from(from string) (dir, source string, err error) {
	switch {
	case from == "" || from == "release" || strings.HasPrefix(from, "release:"):
		tag := strings.TrimPrefix(strings.TrimPrefix(from, "release"), ":")
		if tag == "" {
			if tag, err = s.GitHub.LatestRelease(); err != nil {
				return "", "", err
			}
		}
		dir, err := s.release(tag)
		return dir, "release " + tag, err
	default:
		abs, err := filepath.Abs(from)
		if err != nil {
			return "", "", fmt.Errorf("resolving %s: %w", from, err)
		}
		return abs, abs, nil
	}
}

// release is the unpacked tree of the release tag, downloaded the first time it is asked for.
// mutate-lane: TestTheLabTalksToGitHub
func (s Sources) release(tag string) (string, error) {
	cache := filepath.Join(s.Cache, "releases", tag)
	dir := filepath.Join(cache, "spin-machine-"+tag+"-linux-x86_64", "usr", "share", "spin-stack")
	if _, err := os.Stat(filepath.Join(dir, partFiles["qemu"])); err == nil {
		return dir, nil
	}
	if err := os.RemoveAll(cache); err != nil {
		return "", fmt.Errorf("clearing %s: %w", cache, err)
	}
	if err := s.GitHub.DownloadRelease(tag, "*.tar.gz", cache); err != nil {
		return "", err
	}
	tarballs, err := filepath.Glob(filepath.Join(cache, "*.tar.gz"))
	if err != nil || len(tarballs) != 1 {
		return "", fmt.Errorf("release %s: want one tarball, found %v (%v)", tag, tarballs, err)
	}
	if out, err := exec.Command("tar", "-xzf", tarballs[0], "-C", cache).CombinedOutput(); err != nil {
		return "", fmt.Errorf("unpacking %s: %w: %s", tarballs[0], err, out)
	}
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("release %s's tarball has no %s: %w", tag, dir, err)
	}
	return dir, nil
}

// run is the artifacts of workflow run id that match pattern, downloaded the first time.
// mutate-lane: TestTheLabTalksToGitHub
func (s Sources) run(id, pattern string) (string, error) {
	for _, c := range id {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("a run id is a number, not %q", id)
		}
	}
	dir := filepath.Join(s.Cache, "runs", id, strings.Trim(pattern, "*-"))
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := s.GitHub.DownloadRun(id, pattern, dir+".part"); err != nil {
		return "", err
	}
	if err := os.Rename(dir+".part", dir); err != nil {
		return "", fmt.Errorf("keeping run %s's %s: %w", id, pattern, err)
	}
	return dir, nil
}

// one is the one file under dir whose path ends in suffix.
func one(dir, suffix string) (string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, string(filepath.Separator)+suffix) {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("looking for %s under %s: %w", suffix, dir, err)
	}
	if len(found) != 1 {
		return "", fmt.Errorf("want one %s under %s, found %d", suffix, dir, len(found))
	}
	return found[0], nil
}

// placeKernel puts the kernel what names into the tree at dir and says where it came from.
//
// A bzImage, always, which is what the machine boots: a vmlinux would be entered through PVH,
// without the decompressor KASLR runs in, and the comparison would be of two ways in rather than
// two kernels. A Kernel run from before 2026-11-19 uploaded none, and is refused by name.
func (s Sources) placeKernel(what, dir string) (string, error) {
	bz, source := what, what
	if id, ok := strings.CutPrefix(what, "run:"); ok {
		d, err := s.run(id, "vmlinux-*")
		if err != nil {
			return "", err
		}
		if bz, err = one(d, "bzImage"); err != nil {
			return "", fmt.Errorf("kernel run %s: %w (a run from before 2026-11-19 built no bzImage)", id, err)
		}
		source = "run " + id
	}
	if err := isBzImage(bz); err != nil {
		return "", err
	}
	if err := replace(bz, filepath.Join(dir, "kernel", "bzImage"), 0o644); err != nil {
		return "", err
	}
	// The vmlinux beside it when there is one, for what reads symbols; without one the release's
	// would be left describing another kernel.
	vmlinux := filepath.Join(dir, "kernel", "vmlinux")
	if err := os.Remove(vmlinux); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("removing %s: %w", vmlinux, err)
	}
	if v := filepath.Join(filepath.Dir(bz), "vmlinux"); fileExists(v) {
		if err := replace(v, vmlinux, 0o644); err != nil {
			return "", err
		}
	}
	return source, nil
}

// isBzImage refuses a file that is not a bzImage: its setup header's magic, "HdrS" at 0x202.
func isBzImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening kernel %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := f.ReadAt(magic, 0x202); err != nil || string(magic) != "HdrS" {
		return fmt.Errorf("%s is not a bzImage (no HdrS at 0x202): a tree boots the bzImage, and a vmlinux would be entered another way", path)
	}
	return nil
}

// placeQEMU puts QEMU and the firmware beside it - a QEMU run's artifact, or a directory holding
// bin/qemu-system-x86_64 and qemu/ - into the tree at dir.
func (s Sources) placeQEMU(what, dir string) (string, error) {
	root, source := what, what
	if id, ok := strings.CutPrefix(what, "run:"); ok {
		d, err := s.run(id, "qemu-*-linux-amd64")
		if err != nil {
			return "", err
		}
		bin, err := one(d, filepath.Join("bin", "qemu-system-x86_64"))
		if err != nil {
			return "", fmt.Errorf("QEMU run %s: %w", id, err)
		}
		root, source = filepath.Dir(filepath.Dir(bin)), "run "+id
	}
	if err := replace(filepath.Join(root, "bin", "qemu-system-x86_64"), filepath.Join(dir, partFiles["qemu"]), 0o755); err != nil {
		return "", err
	}
	firmware := filepath.Join(dir, "qemu")
	if err := os.RemoveAll(firmware); err != nil {
		return "", fmt.Errorf("removing %s: %w", firmware, err)
	}
	if out, err := exec.Command("cp", "-r", filepath.Join(root, "qemu"), firmware).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copying %s's firmware: %w: %s", root, err, out)
	}
	return source, nil
}

// buildSpinMachine builds this checkout's spin-machine at to, static as a release's is.
func (s Sources) buildSpinMachine(to string) (string, error) {
	if err := os.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("removing %s: %w", to, err)
	}
	cmd := exec.Command("go", "build", "-o", to, "./cmd/spin-machine")
	cmd.Dir, cmd.Env = s.Checkout, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building this checkout's spin-machine: %w: %s", err, out)
	}
	// Named by its commit when the checkout is a git one; a copy of it (mutate's) is named by its
	// directory, which is what there is to name.
	if rev, err := exec.Command("git", "-C", s.Checkout, "describe", "--always", "--dirty").Output(); err == nil {
		return "built from " + strings.TrimSpace(string(rev)), nil
	}
	return "built from " + s.Checkout, nil
}

// systemd builds systemd with the experiment patch on this host, puts its packages under the
// tree at dir, and returns the edit that installs them into an overlay.
// mutate-lane: TestEveryProbeMeasuresATree
func (s Sources) systemd(patch, dir string) (Edit, error) {
	// --rebuild: the packages under _output/systemd are whatever was last built, and an
	// experiment's are not the release's.
	cmd := exec.Command("go", "run", "./cmd/spin-machine-dev", "build", "systemd",
		"--systemd-experiment", patch, "--cache-export=false", "--rebuild")
	cmd.Dir, cmd.Stdout, cmd.Stderr = s.Checkout, s.Log, s.Log
	if err := cmd.Run(); err != nil {
		return Edit{}, fmt.Errorf("building systemd with the experiment %s: %w", patch, err)
	}
	debs := filepath.Join(dir, "systemd")
	if err := os.MkdirAll(debs, 0o755); err != nil {
		return Edit{}, fmt.Errorf("making %s: %w", debs, err)
	}
	built, err := filepath.Glob(filepath.Join(s.Checkout, "_output", "systemd", "*.deb"))
	if err != nil || len(built) == 0 {
		return Edit{}, fmt.Errorf("the systemd build left no packages in _output/systemd (%v)", err)
	}
	for _, d := range built {
		if err := replace(d, filepath.Join(debs, filepath.Base(d)), 0o644); err != nil {
			return Edit{}, err
		}
	}
	return systemdPackages(debs), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
