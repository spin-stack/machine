// SPDX-License-Identifier: Apache-2.0

// Package lab measures this machine: trees to boot, probes that boot them, and experiments that
// interleave the two and write down what they found.
//
// A tree is a whole machine in a directory, and every part of it says where it came from. That is
// the point of the package: the probes this replaced took a second kernel, a second QEMU or an
// edit of the image each through an environment variable of its own, and a probe that did not
// read one measured the release twice under two names (boot:restore-first ignored a QEMU B on
// 2026-11-19), and a kernel built before the bzImage made the lab fail with no message. Here a
// probe is handed trees, and a tree that is not whole is refused before anything boots.
package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spin-stack/machine/machine"
)

// Tree is one machine to measure: a directory laid out as a release's usr/share/spin-stack, and
// the manifest that says what each part of it is.
type Tree struct {
	Dir      string
	Manifest Manifest
}

// Manifest is what a tree is made of, written beside it as manifest.json when it is made and
// read back before it is measured.
type Manifest struct {
	Name string `json:"name"`
	// Parts are where each part came from: a release, a CI run, a build of a checkout, a
	// directory. Every name in partFiles is here.
	Parts map[string]Part `json:"parts"`
	// Append is added to the kernel command line of every boot of the tree.
	Append string `json:"append,omitempty"`
	// Boot are spin-machine boot flags added to every boot of the tree, after the probe's and the
	// experiment's: a machine the release defines but does not default to, such as a named CPU
	// model (--cpu Skylake-Server-v4).
	Boot []string `json:"boot,omitempty"`
	// CPU is the named model a tree asked for as --cpu baseline, as the host that made the tree
	// chose it: the newest QEMU says it can run there (machine.QEMU.CPUModels, baseline).
	CPU string `json:"cpu,omitempty"`
	// Edits are made to every boot's own overlay, in order.
	Edits []NamedEdit `json:"edits,omitempty"`
	// Options are probe options (memory, plugged...) that replace the run's for this tree, so that
	// two shapes are two trees of one interleaved run: two runs are two machines, and the bench
	// moved ~8 ms between runs of one shape (#561).
	Options Options `json:"options,omitempty"`
}

// Part is one part of a tree and where it came from.
type Part struct {
	Source string `json:"source"`
	// SHA256 is the file's, for the parts that are one file: two trees whose kernels differ only
	// in the run they came from say so here.
	SHA256 string `json:"sha256,omitempty"`
}

// NamedEdit is an edit of every overlay of a tree, and the name it was asked for by.
type NamedEdit struct {
	Name string `json:"name"`
	Edit Edit   `json:"edit"`
}

// partFiles are the parts of a tree, and the file under it each is checked by. The kernel's is the
// one the tree boots, which machine.OpenRelease picks (partFile).
var partFiles = map[string]string{
	"qemu":         "bin/qemu-system-x86_64",
	"firmware":     "qemu/" + machine.DefaultBIOS,
	"kernel":       "kernel/vmlinux or kernel/bzImage",
	"image":        "image/rootfs.qcow2",
	"spin-machine": "bin/spin-machine",
}

// partFile is the file under dir that part name is checked by.
func partFile(dir, name string) (string, error) {
	if name != "kernel" {
		return filepath.Join(dir, partFiles[name]), nil
	}
	r, err := machine.OpenRelease(dir)
	if err != nil {
		return "", err
	}
	s, err := r.Spec("")
	if err != nil {
		return "", err
	}
	return s.Kernel, nil
}

const manifestName = "manifest.json"

// OpenTree reads the tree at dir and refuses one that is not whole: a part missing, a part the
// manifest does not name, a file that is not what the manifest says it is.
func OpenTree(dir string) (*Tree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving tree %s: %w", dir, err)
	}
	b, err := os.ReadFile(filepath.Join(abs, manifestName))
	if err != nil {
		return nil, fmt.Errorf("tree %s has no manifest, so nothing says what it is (make it with `spin-machine-dev lab tree`): %w", abs, err)
	}
	t := &Tree{Dir: abs}
	if err := json.Unmarshal(b, &t.Manifest); err != nil {
		return nil, fmt.Errorf("reading %s: %w", filepath.Join(abs, manifestName), err)
	}
	if err := t.check(); err != nil {
		return nil, err
	}
	return t, nil
}

// check is OpenTree's refusal, also run on a tree just made.
func (t *Tree) check() error {
	if _, err := machine.OpenRelease(t.Dir); err != nil {
		return fmt.Errorf("tree %s: %w", t.Manifest.Name, err)
	}
	for _, name := range slices.Sorted(maps.Keys(partFiles)) {
		p, ok := t.Manifest.Parts[name]
		if !ok || p.Source == "" {
			return fmt.Errorf("tree %s: the manifest does not say where its %s came from", t.Manifest.Name, name)
		}
		file, err := partFile(t.Dir, name)
		if err != nil {
			return fmt.Errorf("tree %s: %w", t.Manifest.Name, err)
		}
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("tree %s has no %s: %w", t.Manifest.Name, name, err)
		}
		if p.SHA256 == "" {
			continue
		}
		sum, err := fileSHA256(file)
		if err != nil {
			return err
		}
		if sum != p.SHA256 {
			return fmt.Errorf("tree %s: %s is %s, and its manifest says %s", t.Manifest.Name, file, sum, p.SHA256)
		}
	}
	return nil
}

// Name is what the tree is called in a table.
func (t *Tree) Name() string { return t.Manifest.Name }

// SpinMachine is the tree's spin-machine, which every boot of it runs.
func (t *Tree) SpinMachine() string { return filepath.Join(t.Dir, "bin", "spin-machine") }

// QEMUImg is the tree's qemu-img, which makes its overlays.
func (t *Tree) QEMUImg() string { return filepath.Join(t.Dir, "bin", "qemu-img") }

// Kernel is the kernel every boot of the tree boots: its vmlinux entered through PVH, or its bzImage.
func (t *Tree) Kernel() (string, error) { return partFile(t.Dir, "kernel") }

// Rootfs is the base image every overlay of the tree is backed by. It is never opened for writing.
func (t *Tree) Rootfs() string { return filepath.Join(t.Dir, "image", "rootfs.qcow2") }

// Cmdline is the kernel command line a boot of the tree that runs init starts from. The tree's
// own --append comes after, and the kernel takes the last init= it is given.
func (t *Tree) Cmdline(init string) string {
	return strings.TrimSpace("init=" + init + " " + t.Manifest.Append)
}

// TreeSpec is how a tree is asked for: what it starts from and what replaces a part of it.
type TreeSpec struct {
	Name string
	// From is the tree the rest is laid over: "release" (the newest), "release:vYYYYMMDD.NN", or
	// a directory laid out as a release (_output after `spin-machine-dev build`).
	From string
	// Kernel and QEMU replace those parts: "run:N" for a parts.yml run (or a Kernel or QEMU workflow run from before it), or a path
	// (a vmlinux that randomizes itself or a bzImage; a directory holding bin/qemu-system-x86_64 and
	// qemu/). Kernel is also "traceable:vYYYYMMDD.NN", the kernel published beside that release with
	// lockdown forced to none, which the ledger reads a trace from (Sources.traceable).
	Kernel, QEMU string
	// SpinMachine is "here" for this checkout's, built; "release" for the one built from the
	// commit the From tree's machine.env names, which is the machine that release defined: spin
	// builds the machine package of the release it pins, and a release ships no spin-machine
	// binary. Left out, it is the From tree's own when it has one (_output after a build) and this
	// checkout's otherwise. "commit:SHA" is the one built from that commit of this repository. The
	// manifest says whose.
	SpinMachine string
	Append      string
	Boot        []string
	// CPU is "baseline" for the newest named CPU model the host making the tree can run, chosen
	// there: a model picked on another machine is one a nested host may not show (#222).
	CPU   string
	Edits []string
	// Systemd is an image/systemd/experiments patch: systemd built with it, put into every overlay.
	Systemd string
	// OpenSSH is OpenSSH as this checkout builds it (image/openssh), put into every overlay.
	OpenSSH bool
	// Options are NAME=VALUE probe options for this tree's boots in place of the run's (Manifest.Options).
	Options []string
}

// Sources is where MakeTree finds what a spec names: this checkout, and the downloads.
type Sources struct {
	// Checkout is this repository's root: the spin-machine `here` builds, the image's lane
	// files, the systemd experiments, image/openssh.
	Checkout string
	// Cache is where releases and runs are downloaded and unpacked, once.
	Cache  string
	GitHub *GitHub
	Log    io.Writer
}

// MakeTree lays the tree spec asks for out under dir and returns it opened: the From tree linked
// file by file, and each part asked for copied over its link. Nothing is written through a link,
// so the tree it starts from - a release's, or _output - is never touched.
func MakeTree(dir string, spec TreeSpec, src Sources) (*Tree, error) {
	if spec.Name == "" {
		return nil, errors.New("a tree needs a name")
	}
	base, baseSource, err := src.from(spec.From)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", dir, err)
	}
	if err := linkTree(base, dir); err != nil {
		return nil, err
	}
	m := Manifest{Name: spec.Name, Parts: map[string]Part{}, Append: spec.Append, Boot: spec.Boot}
	for _, o := range spec.Options {
		name, value, ok := strings.Cut(o, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("tree %s: an option is NAME=VALUE, e.g. plugged=0, not %q", spec.Name, o)
		}
		if m.Options == nil {
			m.Options = Options{}
		}
		m.Options[name] = value
	}
	for name := range partFiles {
		m.Parts[name] = Part{Source: baseSource}
	}
	if spec.Kernel != "" {
		source, err := src.placeKernel(spec.Kernel, dir)
		if err != nil {
			return nil, err
		}
		m.Parts["kernel"] = Part{Source: source}
	}
	if spec.QEMU != "" {
		source, err := src.placeQEMU(spec.QEMU, dir)
		if err != nil {
			return nil, err
		}
		m.Parts["qemu"], m.Parts["firmware"] = Part{Source: source}, Part{Source: source}
	}
	switch spec.SpinMachine {
	case "here", "":
		if spec.SpinMachine == "" && fileExists(filepath.Join(dir, partFiles["spin-machine"])) {
			break
		}
		source, err := src.buildSpinMachine(filepath.Join(dir, partFiles["spin-machine"]))
		if err != nil {
			return nil, err
		}
		m.Parts["spin-machine"] = Part{Source: source}
	case "release":
		source, err := src.releaseSpinMachine(base, filepath.Join(dir, partFiles["spin-machine"]))
		if err != nil {
			return nil, err
		}
		m.Parts["spin-machine"] = Part{Source: source}
	default:
		commit, ok := strings.CutPrefix(spec.SpinMachine, "commit:")
		if !ok {
			return nil, fmt.Errorf("--spin-machine is here, release, commit:SHA, or left out, not %q", spec.SpinMachine)
		}
		source, err := src.commitSpinMachine(commit, filepath.Join(dir, partFiles["spin-machine"]))
		if err != nil {
			return nil, err
		}
		m.Parts["spin-machine"] = Part{Source: source}
	}
	for _, name := range spec.Edits {
		e, err := namedEdit(name, src.Checkout, dir)
		if err != nil {
			return nil, err
		}
		m.Edits = append(m.Edits, NamedEdit{Name: name, Edit: e})
	}
	if spec.Systemd != "" {
		if err := src.addSystemd(&m, spec.Systemd, dir); err != nil {
			return nil, err
		}
	}
	if spec.OpenSSH {
		if err := src.addOpenSSH(&m, dir); err != nil {
			return nil, err
		}
	}
	switch spec.CPU {
	case "":
	case "baseline":
		model, err := hostBaseline(dir)
		if err != nil {
			return nil, err
		}
		m.CPU = model
		m.Boot = append(m.Boot, "--cpu="+model)
	default:
		return nil, fmt.Errorf("--cpu is baseline or left out, not %q: a named model is --boot --cpu=NAME", spec.CPU)
	}
	if strings.HasPrefix(spec.Kernel, "traceable:") {
		if err := keepUnlocked(&m, dir); err != nil {
			return nil, fmt.Errorf("tree %s: %w", spec.Name, err)
		}
	}
	for _, name := range []string{"kernel", "qemu", "spin-machine"} {
		p := m.Parts[name]
		file, err := partFile(dir, name)
		if err != nil {
			return nil, fmt.Errorf("tree %s: %w", spec.Name, err)
		}
		if p.SHA256, err = fileSHA256(file); err != nil {
			return nil, err
		}
		m.Parts[name] = p
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("writing tree %s's manifest: %w", spec.Name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), append(b, '\n'), 0o644); err != nil {
		return nil, fmt.Errorf("writing tree %s's manifest: %w", spec.Name, err)
	}
	return OpenTree(dir)
}

// addSystemd builds systemd with the experiment patch and adds the edit that installs it to m.
//
// mutate-lane: TestEveryProbeMeasuresATree, with SPIN_LAB_SYSTEMD naming a patch.
func (s Sources) addSystemd(m *Manifest, patch, dir string) error {
	e, err := s.systemd(patch, dir)
	if err != nil {
		return err
	}
	m.Edits = append(m.Edits, e)
	return nil
}

// linkTree makes dst the tree at src as directories of symbolic links, one a file, as `cp -as`
// would: a part is replaced by removing its link, never by writing through it.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return fmt.Errorf("placing %s under %s: %w", p, src, err)
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			if err := os.MkdirAll(to, 0o755); err != nil {
				return fmt.Errorf("making %s: %w", to, err)
			}
			return nil
		}
		if err := os.Symlink(p, to); err != nil {
			return fmt.Errorf("linking %s to %s: %w", to, p, err)
		}
		return nil
	})
}

// replace puts a copy of the file from at to, where to may be a link into another tree.
func replace(from, to string, mode os.FileMode) error {
	if err := os.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", to, err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(to), err)
	}
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("opening %s: %w", from, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", to, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copying %s to %s: %w", from, to, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", to, err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Edited is the tree laid out again at dir with its edits made in its root filesystem, for what
// boots the tree's image as it is rather than through a probe's overlays (`lab check`): every part
// a link to the tree's, and image/rootfs.qcow2 a qcow2 overlay over the tree's base with the
// edits made in it. A tree without edits is itself.
// mutate-lane: TestEveryProbeMeasuresATree
func (t *Tree) Edited(dir string) (string, error) {
	if len(t.Manifest.Edits) == 0 {
		return t.Dir, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "image"), 0o755); err != nil {
		return "", fmt.Errorf("making the edited tree %s: %w", dir, err)
	}
	link := func(from, name string) error {
		entries, err := os.ReadDir(from)
		if err != nil {
			return fmt.Errorf("reading tree %s: %w", from, err)
		}
		for _, e := range entries {
			if e.Name() == name {
				continue
			}
			if err := os.Symlink(filepath.Join(from, e.Name()), filepath.Join(dir, strings.TrimPrefix(strings.TrimPrefix(from, t.Dir), "/"), e.Name())); err != nil {
				return fmt.Errorf("laying out the edited tree %s: %w", dir, err)
			}
		}
		return nil
	}
	if err := link(t.Dir, "image"); err != nil {
		return "", err
	}
	if err := link(filepath.Join(t.Dir, "image"), "rootfs.qcow2"); err != nil {
		return "", err
	}
	rootfs := filepath.Join(dir, "image", "rootfs.qcow2")
	if err := NewOverlay(t, rootfs); err != nil {
		return "", err
	}
	var edits []Edit
	for _, e := range t.Manifest.Edits {
		edits = append(edits, e.Edit)
	}
	if err := EditOverlay(rootfs, edits...); err != nil {
		return "", err
	}
	return dir, nil
}

// keepUnlocked keeps a tree that boots a traceable kernel at lockdown none. That kernel forces
// none, and a spin-machine from #734 on raises every machine to confidentiality with lockdown= on
// its command line unless asked not to, which would take the trace away again: the boots then pass
// --lockdown=kernel, which adds nothing and leaves the keyrings sealed. Not shirudo's, which also
// leaves lockdown= out: nothing in a traced boot enrolls a key, and an unsealed .bpf keyring
// changes which signed BPF programs the verifier accepts. A spin-machine from before #734 knows no
// such flag and passes no lockdown=. Which one the tree has is read from the command line its
// spin-machine prints (args), and that line is checked to have no lockdown= before anything boots.
func keepUnlocked(m *Manifest, dir string) error {
	line := func() (string, error) {
		args := append([]string{"args", "--release", dir, "--disk", "-"}, m.Boot...)
		out, err := exec.Command(filepath.Join(dir, "bin", "spin-machine"), args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("asking its spin-machine for the command line a boot runs: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	out, err := line()
	if err != nil || !strings.Contains(out, "lockdown=") {
		return err
	}
	m.Boot = append(m.Boot, "--lockdown=kernel")
	if out, err = line(); err != nil {
		return err
	}
	if strings.Contains(out, "lockdown=") {
		return fmt.Errorf("its traceable kernel would boot locked down, and its trace could not be read: %s", out)
	}
	return nil
}
