// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/release"
	"github.com/spin-stack/machine/machine"
)

// from is the tree a spec starts from and how a manifest names it.
func (s Sources) from(from string) (dir, source string, err error) {
	from = cmp.Or(from, "release")
	switch {
	case from == "release" || strings.HasPrefix(from, "release:"):
		tag, err := s.releaseTag(from)
		if err != nil {
			return "", "", err
		}
		dir, err := s.Release(tag)
		return dir, "release " + tag, err
	default:
		abs, err := filepath.Abs(from)
		if err != nil {
			return "", "", fmt.Errorf("resolving %s: %w", from, err)
		}
		return abs, abs, nil
	}
}

// releaseTag is the tag "release:TAG" names, or the newest release's for "release".
//
// mutate-lane: TestTheLabTalksToGitHub, with SPIN_LAB_RELEASE set.
func (s Sources) releaseTag(from string) (string, error) {
	if tag := strings.TrimPrefix(from, "release:"); tag != from {
		return tag, nil
	}
	return s.GitHub.LatestRelease()
}

// Release is the unpacked usr/share/spin-stack of the release tag, downloaded and verified the
// first time it is asked for, in Cache/releases/TAG: the cache holds only what passed, and every
// verb that reads a release on a host shares its download.
//
// mutate-lane: TestTheLabTalksToGitHub
func (s Sources) Release(tag string) (string, error) {
	cache := filepath.Join(s.Cache, "releases", tag)
	dir := filepath.Join(cache, release.Name(tag), "usr", "share", "spin-stack")
	if _, err := os.Stat(filepath.Join(dir, partFiles["qemu"])); err == nil {
		return dir, nil
	}
	if err := os.RemoveAll(cache); err != nil {
		return "", fmt.Errorf("clearing %s: %w", cache, err)
	}
	pins, err := versions.Load(filepath.Join(s.Checkout, versions.File))
	if err != nil {
		return "", fmt.Errorf("reading the pins release %s is verified with: %w", tag, err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", fmt.Errorf("making %s: %w", cache, err)
	}
	return release.Fetch(context.Background(), pins, tag, cache)
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

// traceable is release tag's traceable kernel, downloaded and its signature checked the first time
// it is asked for, in Cache/traceable/TAG: the vmlinux a release publishes from #697 on
// (release.TraceableVmlinux), or the bzImage one published before it (release.TraceableBzImage).
// A release publishes one of the two, so whichever the cache holds is the release's.
//
// mutate-lane: TestTheLabTalksToGitHub, with SPIN_LAB_TRACEABLE set to a release that has one.
func (s Sources) traceable(tag string) (string, error) {
	dir := filepath.Join(s.Cache, "traceable", tag)
	names := []string{release.TraceableVmlinux(tag), release.TraceableBzImage(tag)}
	for _, n := range names {
		if p := filepath.Join(dir, n); fileExists(p) {
			return p, nil
		}
	}
	published, err := release.Assets(context.Background(), tag)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(names, func(n string) bool { return slices.Contains(published, n) })
	if i < 0 {
		return "", fmt.Errorf("release %s publishes no traceable kernel, neither %s nor %s (published beside releases from #536 on)", tag, names[0], names[1])
	}
	pins, err := versions.Load(filepath.Join(s.Checkout, versions.File))
	if err != nil {
		return "", fmt.Errorf("reading the pins release %s's traceable kernel is verified with: %w", tag, err)
	}
	if err := os.RemoveAll(dir + ".part"); err != nil {
		return "", fmt.Errorf("clearing %s.part: %w", dir, err)
	}
	if err := os.MkdirAll(dir+".part", 0o755); err != nil {
		return "", fmt.Errorf("making %s.part: %w", dir, err)
	}
	if _, err := release.Signed(context.Background(), pins, tag, dir+".part", names[i]); err != nil {
		return "", fmt.Errorf("release %s's traceable kernel: %w", tag, err)
	}
	if err := os.Rename(dir+".part", dir); err != nil {
		return "", fmt.Errorf("keeping release %s's traceable kernel: %w", tag, err)
	}
	return filepath.Join(dir, names[i]), nil
}

// placeKernel puts the kernel what names into the tree at dir and says where it came from and how
// the machine enters it.
//
// A vmlinux that randomizes itself (machine.PVH) is entered through PVH, as a release's is from
// v20261123.07 on (#645): it is put at kernel/vmlinux and the tree's bzImage is removed, which
// would be another kernel. Otherwise the kernel is a bzImage, entered through its decompressor,
// with the vmlinux beside it when there is one, for what reads symbols. A run's artifact or a
// path given may hold both, and the vmlinux is what the machine boots then (machine.OpenRelease).
// A Kernel run from before 2026-11-19 built neither, and is refused by name.
func (s Sources) placeKernel(what, dir string) (string, error) {
	var bz, vmlinux string
	source := what
	switch {
	case strings.HasPrefix(what, "traceable:"):
		tag := strings.TrimPrefix(what, "traceable:")
		k, err := s.traceable(tag)
		if err != nil {
			return "", err
		}
		bz, vmlinux, source = k, k, "release "+tag+"'s traceable kernel"
	case strings.HasPrefix(what, "run:"):
		id := strings.TrimPrefix(what, "run:")
		d, err := s.run(id, "vmlinux-*")
		if err != nil {
			return "", err
		}
		vmlinux, _ = one(d, "vmlinux")
		if bz, err = one(d, "bzImage"); err != nil && machine.PVH(vmlinux) != nil {
			return "", fmt.Errorf("kernel run %s: %w, and no vmlinux the machine enters through PVH (a run from before 2026-11-19 built neither)", id, err)
		}
		// Its modules, for --edit modules, when the run has them (#736 on).
		if modules := filepath.Join(filepath.Dir(vmlinux), "modules"); vmlinux != "" && fileExists(modules) {
			if out, err := exec.Command("cp", "-a", modules, filepath.Join(dir, "kernel", "modules")).CombinedOutput(); err != nil {
				return "", fmt.Errorf("kernel run %s: copying its modules: %w: %s", id, err, out)
			}
		}
		source = "run " + id
	default:
		bz, vmlinux = what, filepath.Join(filepath.Dir(what), "vmlinux")
		if machine.PVH(what) == nil {
			vmlinux = what
		}
	}
	tree := filepath.Join(dir, "kernel", "vmlinux")
	if machine.PVH(vmlinux) == nil {
		if err := replace(vmlinux, tree, 0o644); err != nil {
			return "", err
		}
		if err := removeIfThere(filepath.Join(dir, "kernel", "bzImage")); err != nil {
			return "", err
		}
		return source + ", a vmlinux entered through PVH", nil
	}
	if err := isBzImage(bz); err != nil {
		return "", err
	}
	if err := replace(bz, filepath.Join(dir, "kernel", "bzImage"), 0o644); err != nil {
		return "", err
	}
	// Without a vmlinux of its own, the release's would be left describing another kernel.
	if err := removeIfThere(tree); err != nil {
		return "", err
	}
	if fileExists(vmlinux) && vmlinux != bz {
		if err := replace(vmlinux, tree, 0o644); err != nil {
			return "", err
		}
	}
	return source + ", a bzImage entered through its decompressor", nil
}

// removeIfThere removes path, and is content when there is nothing to remove.
func removeIfThere(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

// placeQEMU puts QEMU, swtpm and the firmware beside them - a QEMU run's artifact, or a directory holding
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
	// swtpm is built beside QEMU and comes with it; a QEMU from before #719 has none, and its tree
	// keeps no swtpm of another build's.
	swtpm := filepath.Join(dir, "bin", "swtpm")
	if err := removeIfThere(swtpm); err != nil {
		return "", err
	}
	if fileExists(filepath.Join(root, "bin", "swtpm")) {
		if err := replace(filepath.Join(root, "bin", "swtpm"), swtpm, 0o755); err != nil {
			return "", err
		}
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

// releaseSpinMachine builds, at to, the spin-machine of the commit the release tree at base was
// made from (machine.env's source_commit): the machine as that release defined it - its devices,
// its places, its checkpoints - which this checkout's would not be.
//
// It builds that commit's tree, fetched alone into a repository of its own from wherever this
// checkout's origin points. It used go install through the module proxy, which fetches a module
// by the path its go.mod declares: after the module became github.com/spin-stack/machine (#376),
// the proxy could fetch no release from before it, and none after it until GitHub had the new
// name. A fetch by commit names neither. The checkout is only read: CI's is shallow, and a fetch
// into it would deepen it, or make a full clone shallow.
func (s Sources) releaseSpinMachine(base, to string) (string, error) {
	env, err := readEnv(filepath.Join(base, "machine.env"))
	if err != nil {
		return "", err
	}
	commit := env["source_commit"]
	if commit == "" {
		return "", fmt.Errorf("%s names no source_commit to build its spin-machine from", filepath.Join(base, "machine.env"))
	}
	if err := s.buildAt(commit, env["version"]+"'s spin-machine", to); err != nil {
		return "", err
	}
	return "built from " + commit + ", " + env["version"] + "'s", nil
}

// commitSpinMachine builds, at to, the spin-machine of one commit of this repository, fetched as
// a release's is. Two trees with two commits' spin-machines boot interleaved in one run, which is
// how a change since a release is found commit by commit (#438). GitHub refuses a fetch by an
// abbreviated name, so the commit is named whole.
func (s Sources) commitSpinMachine(commit, to string) (string, error) {
	if len(commit) != 40 || strings.Trim(commit, "0123456789abcdef") != "" {
		return "", fmt.Errorf("--spin-machine commit:%s: a commit is named by its 40 hexadecimal digits", commit)
	}
	if err := s.buildAt(commit, "the spin-machine of "+commit, to); err != nil {
		return "", err
	}
	return "built from " + commit, nil
}

// buildAt builds cmd/spin-machine at commit, fetched alone from this checkout's origin, to to.
// what names it in an error.
func (s Sources) buildAt(commit, what, to string) error {
	origin, err := exec.Command("git", "-C", s.Checkout, "remote", "get-url", "origin").Output()
	if err != nil {
		return fmt.Errorf("finding %s's origin, to fetch %s from: %w", s.Checkout, what, err)
	}
	tree, err := os.MkdirTemp(filepath.Dir(to), ".source")
	if err != nil {
		return fmt.Errorf("making a directory to build %s in: %w", what, err)
	}
	defer func() { _ = os.RemoveAll(tree) }()
	for _, args := range [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth=1", strings.TrimSpace(string(origin)), commit},
		{"-c", "advice.detachedHead=false", "checkout", "-q", commit},
	} {
		if out, err := exec.Command("git", append([]string{"-C", tree}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("checking out %s at %s: %w: %s", what, commit, err, out)
		}
	}
	if err := os.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", to, err)
	}
	cmd := exec.Command("go", "build", "-o", to, "./cmd/spin-machine")
	cmd.Dir, cmd.Env = tree, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building %s at %s: %w: %s", what, commit, err, out)
	}
	return nil
}

// readEnv reads a KEY=VALUE file such as machine.env.
func readEnv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	env := map[string]string{}
	for line := range strings.Lines(string(b)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			env[k] = v
		}
	}
	return env, nil
}

// systemd builds systemd with the experiment patch on this host, puts its packages under the
// tree at dir, and returns the edit that installs them into an overlay.
// mutate-lane: TestEveryProbeMeasuresATree
func (s Sources) systemd(patch, dir string) (NamedEdit, error) {
	// --rebuild: the packages under _output/systemd are whatever was last built, and an
	// experiment's are not the release's.
	debs, err := s.packages(dir, "systemd", "--systemd-experiment", patch, "--rebuild")
	if err != nil {
		return NamedEdit{}, fmt.Errorf("building systemd with the experiment %s: %w", patch, err)
	}
	return NamedEdit{Name: "systemd " + patch, Edit: packagesEdit(debs, systemdIsRebuilt)}, nil
}

// addOpenSSH builds OpenSSH as this checkout does on this host, puts its packages under the tree
// at dir, and adds the edit that installs them into an overlay to m.
// mutate-lane: TestEveryProbeMeasuresATree, with SPIN_LAB_OPENSSH=1.
func (s Sources) addOpenSSH(m *Manifest, dir string) error {
	debs, err := s.packages(dir, "openssh")
	if err != nil {
		return fmt.Errorf("building this checkout's OpenSSH: %w", err)
	}
	m.Edits = append(m.Edits, NamedEdit{Name: "openssh", Edit: packagesEdit(debs, opensshIsRebuilt)})
	return nil
}

// packages builds part's packages in this checkout with flags and copies them into dir/part.
// mutate-lane: TestEveryProbeMeasuresATree
func (s Sources) packages(dir, part string, flags ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"run", "./cmd/spin-machine-dev", "build", part, "--cache-export=false"}, flags...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = s.Checkout, s.Log, s.Log
	if err := cmd.Run(); err != nil {
		return "", err
	}
	debs := filepath.Join(dir, part)
	if err := os.MkdirAll(debs, 0o755); err != nil {
		return "", fmt.Errorf("making %s: %w", debs, err)
	}
	built, err := filepath.Glob(filepath.Join(s.Checkout, "_output", part, "*.deb"))
	if err != nil || len(built) == 0 {
		return "", fmt.Errorf("the %s build left no packages in _output/%s (%v)", part, part, err)
	}
	for _, d := range built {
		if err := replace(d, filepath.Join(debs, filepath.Base(d)), 0o644); err != nil {
			return "", err
		}
	}
	return debs, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isBzImage refuses a kernel that is not a bzImage, "HdrS" at 0x202, when the tree is made: the
// machine refuses it too (machine.Spec.Args), but only once the tree boots, and a tree is made to
// be booted many times somewhere else.
func isBzImage(path string) error {
	f, err := os.Open(path) //nolint:gosec // the kernel the tree spec names
	if err != nil {
		return fmt.Errorf("opening kernel %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := f.ReadAt(magic, 0x202); err != nil || string(magic) != "HdrS" {
		return fmt.Errorf("%s is not a bzImage (no HdrS at 0x202), nor a vmlinux the machine enters through PVH: the machine boots one of the two", path)
	}
	return nil
}
