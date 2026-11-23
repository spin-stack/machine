// SPDX-License-Identifier: Apache-2.0

// Package patched makes the source trees the machine's C patches change, as its builds make
// them, for an analysis that reads only what the patches touch: CodeQL, in
// .github/workflows/codeql.yml (#653). A tree is the pinned upstream source with the part's
// patches applied in name order, without fuzz, and every C file the patches do not touch taken
// out, so an analysis that compiles each C file it finds compiles only the touched ones. The
// headers stay, for the touched files to include. Beside it are the touched files as upstream
// has them, to say which lines the patches added.
package patched

import (
	"bufio"
	"bytes"
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
	"github.com/spin-stack/go-tools/versions/upstream"
)

// A Part is a source the machine carries patches for, the versions.yaml entry it is built from
// and where its patches are, a glob from the checkout's root.
type Part struct {
	Name, Pin, Patches string
}

// Parts is every part with C patches, where AGENTS.md says each part's patches are kept.
var Parts = []Part{
	{Name: "kernel", Pin: "kernel", Patches: "kernel/patches/*.patch"},
	{Name: "qemu", Pin: "qemu", Patches: "qemu/patches/*.patch"},
	{Name: "qboot", Pin: "qboot", Patches: "qemu/qboot/*.patch"},
	{Name: "systemd", Pin: "systemd-source", Patches: "image/systemd/*.patch"},
	{Name: "openssh", Pin: "openssh-source", Patches: "image/openssh/*.patch"},
}

// Names is every part's name.
func Names() []string {
	names := make([]string, len(Parts))
	for i, p := range Parts {
		names[i] = p.Name
	}
	return names
}

// Find is the part named name.
func Find(name string) (Part, error) {
	for _, p := range Parts {
		if p.Name == name {
			return p, nil
		}
	}
	return Part{}, fmt.Errorf("no part %q with patches: one of %s", name, strings.Join(Names(), ", "))
}

// Tree makes p's patched tree in dir: dir/src the tree, dir/upstream the touched files before
// the patches, dir/touched their paths, one a line. root is the checkout the patches are read
// from. Downloads are kept in dir/download and checked against their pins every time.
func Tree(ctx context.Context, root string, pins *versions.Versions, p Part, dir string) error {
	patches, err := filepath.Glob(filepath.Join(root, p.Patches))
	if err != nil || len(patches) == 0 {
		return fmt.Errorf("%s: no patches at %s", p.Name, p.Patches)
	}
	e, err := pins.Get(p.Pin)
	if err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	src := filepath.Join(dir, "src")
	for _, d := range []string{src, filepath.Join(dir, "upstream")} {
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("%s: clearing %s: %w", p.Name, d, err)
		}
	}
	if err := unpack(ctx, e, filepath.Join(dir, "download"), src); err != nil {
		return fmt.Errorf("%s: unpacking %s %s: %w", p.Name, e.Name, e.Version, err)
	}
	touched, err := Touched(patches)
	if err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	for _, f := range touched {
		if err := keep(filepath.Join(src, f), filepath.Join(dir, "upstream", f)); err != nil {
			return fmt.Errorf("%s: keeping upstream's %s: %w", p.Name, f, err)
		}
	}
	for _, patch := range patches {
		if out, err := run(ctx, "patch", "-d", src, "-p1", "--forward", "--fuzz=0", "-i", patch); err != nil {
			return fmt.Errorf("%s: %s does not apply to %s %s: %w\n%s", p.Name, filepath.Base(patch), e.Name, e.Version, err, out)
		}
	}
	if err := prune(src, touched); err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	return os.WriteFile(filepath.Join(dir, "touched"), []byte(strings.Join(touched, "\n")+"\n"), 0o644)
}

// unpack puts e's source in src: a git entry cloned at its commit, a Debian source package
// extracted by dpkg-source with the distribution's own patches, or a tarball of one directory.
func unpack(ctx context.Context, e versions.Entry, download, src string) error {
	if e.Kind == versions.Git {
		if out, err := run(ctx, "git", "clone", "--quiet", "--branch", e.Version, e.Source, src); err != nil {
			return fmt.Errorf("cloning %s: %w\n%s", e.Source, err, out)
		}
		if out, err := run(ctx, "git", "-C", src, "checkout", "--quiet", e.Pin); err != nil {
			return fmt.Errorf("checking out %s: %w\n%s", e.Pin, err, out)
		}
		return nil
	}
	file, err := upstream.PinnedFile(ctx, e, download)
	if err != nil {
		return err
	}
	if strings.HasSuffix(file, ".dsc") {
		return debianSource(ctx, e, file, download, src)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	if out, err := run(ctx, "tar", "-xf", file, "-C", src, "--strip-components=1"); err != nil {
		return fmt.Errorf("extracting %s: %w\n%s", file, err, out)
	}
	return nil
}

// debianSource fetches the files a pinned .dsc names, each by the SHA-256 the .dsc gives it and
// from beside it, and extracts the package with dpkg-source, which applies debian/patches.
func debianSource(ctx context.Context, e versions.Entry, dsc, download, src string) error {
	raw, err := os.ReadFile(dsc)
	if err != nil {
		return err
	}
	files, err := dscFiles(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(dsc), err)
	}
	base, _ := strings.CutSuffix(e.URL(), "/"+filepath.Base(dsc))
	for name, sum := range files {
		f := versions.Entry{Name: name, Kind: versions.Download, Source: base + "/" + name, Pin: sum}
		if _, err := upstream.PinnedFile(ctx, f, download); err != nil {
			return err
		}
	}
	if out, err := run(ctx, "dpkg-source", "--no-copy", "-x", dsc, src); err != nil {
		return fmt.Errorf("dpkg-source -x %s: %w\n%s", filepath.Base(dsc), err, out)
	}
	return nil
}

// dscFiles is each file a .dsc's Checksums-Sha256 field names, with its sum.
func dscFiles(dsc []byte) (map[string]string, error) {
	files := map[string]string{}
	in := false
	s := bufio.NewScanner(bytes.NewReader(dsc))
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, " ") {
			in = line == "Checksums-Sha256:"
			continue
		}
		if f := strings.Fields(line); in && len(f) == 3 {
			files[f[2]] = f[0]
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no Checksums-Sha256 field naming a file")
	}
	return files, nil
}

// Touched is every file the patches change, as a path in the tree, each once and sorted: what a
// patch names after "+++ b/".
func Touched(patches []string) ([]string, error) {
	var files []string
	for _, p := range patches {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for line := range strings.Lines(string(raw)) {
			if f, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "+++ b/"); ok {
				files = append(files, strings.Fields(f)[0])
			}
		}
	}
	if len(files) == 0 {
		return nil, errors.New("the patches name no file they change")
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// keep copies a touched file as upstream has it. A file the patches create has no upstream
// copy, and every line of it is the patches'.
func keep(from, to string) error {
	raw, err := os.ReadFile(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, raw, 0o644)
}

// prune removes every C or C++ source file in src that the patches do not touch.
func prune(src string, touched []string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if !d.Type().IsRegular() || !slices.Contains([]string{".c", ".cc", ".cpp", ".cxx"}, filepath.Ext(p)) ||
			slices.Contains(touched, filepath.ToSlash(rel)) {
			return nil
		}
		return os.Remove(p)
	})
}

// run is a command's combined output, and its error.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
