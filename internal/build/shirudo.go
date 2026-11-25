// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spin-stack/go-tools/versions"
)

// shirudoPatches is where shirudo keeps the kernel patches it needs, in its repository.
const shirudoPatches = "testing/kernel/patches"

// shirudoNote marks a kernel patch carried from shirudo, in the note this repository adds to it.
const shirudoNote = "from shirudo's testing/kernel/patches"

// shirudoDeclined is each patch of shirudo's not carried here, by Subject, and why. An upstream
// patch neither carried nor here fails `bump shirudo` until it is one or the other.
var shirudoDeclined = map[string]string{
	"kbuild: drop a .0 sublevel and the -rc suffix from the kernel release": "it names shirudo's nodes' packages and module directory; this kernel has no modules and no packages, and its release keeps the sublevel its tarball names (7.4.0-rc4), which libbpf's get_kernel_version reads as three numbers",
	"riscv: build without LLVM's GlobalMerge pass":                          "riscv only; this machine is x86-64",
}

// A kernelPatch is one patch by its Subject and the change it makes: the files it touches and
// the lines it adds and removes, without context, hunk positions or blob ids, so a patch carried
// onto another tree with only its context moved is the same change.
type kernelPatch struct {
	file, subject, change string
}

var subjectLine = regexp.MustCompile(`(?m)^Subject: \[PATCH\] (.*(?:\n .*)*)`)

func parseKernelPatch(file, text string) (kernelPatch, error) {
	m := subjectLine.FindStringSubmatch(text)
	if m == nil {
		return kernelPatch{}, fmt.Errorf("%s: no Subject: [PATCH] line", file)
	}
	var change []string
	inDiff := false
	for _, l := range strings.Split(text, "\n") {
		switch {
		case l == "-- ":
			// format-patch's signature, which only some of shirudo's patches carry.
			inDiff = false
		case strings.HasPrefix(l, "diff --git "):
			inDiff = true
			change = append(change, l)
		case !inDiff:
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, "-"):
			change = append(change, l)
		}
	}
	if len(change) == 0 {
		return kernelPatch{}, fmt.Errorf("%s: no diff", file)
	}
	return kernelPatch{file: file, subject: strings.Join(strings.Fields(m[1]), " "), change: strings.Join(change, "\n")}, nil
}

// ShirudoPatches compares shirudo's kernel patches at the pinned commit with the ones
// kernel/patches carries from it, under root. It answers one line for each, and fails naming
// every one that needs a person: a carried patch whose change upstream rewrote or no longer has,
// and an upstream patch neither carried nor in shirudoDeclined.
func ShirudoPatches(ctx context.Context, root string, pins *versions.Versions) (string, error) {
	e, err := pins.Get("shirudo")
	if err != nil {
		return "", err
	}
	upstream, err := fetchShirudoPatches(ctx, e.Source, e.Pin)
	if err != nil {
		return "", err
	}
	carried, err := carriedFromShirudo(filepath.Join(root, "kernel", "patches"))
	if err != nil {
		return "", err
	}
	return compareShirudo(upstream, carried, e.Pin)
}

func compareShirudo(upstream, carried []kernelPatch, pin string) (string, error) {
	bySubject := map[string]kernelPatch{}
	for _, p := range upstream {
		bySubject[p.subject] = p
	}
	var lines, act []string
	seen := map[string]bool{}
	for _, c := range carried {
		u, ok := bySubject[c.subject]
		seen[c.subject] = true
		switch {
		case !ok:
			act = append(act, fmt.Sprintf("%s: shirudo at %s no longer has %q: merged upstream or dropped; take it out or say why it stays", c.file, pin, c.subject))
		case u.change != c.change:
			act = append(act, fmt.Sprintf("%s: shirudo's %s changes it: carry upstream's change again", c.file, u.file))
		default:
			lines = append(lines, fmt.Sprintf("%s: as shirudo's %s", c.file, u.file))
		}
	}
	for _, u := range upstream {
		if seen[u.subject] {
			continue
		}
		if why, ok := shirudoDeclined[u.subject]; ok {
			lines = append(lines, fmt.Sprintf("shirudo's %s: not carried, %s", u.file, why))
			continue
		}
		act = append(act, fmt.Sprintf("shirudo's %s: neither carried nor declined (internal/build/shirudo.go)", u.file))
	}
	out := strings.Join(lines, "\n")
	if len(act) > 0 {
		return out, fmt.Errorf("shirudo's kernel patches at %s:\n%s", pin, strings.Join(act, "\n"))
	}
	return out, nil
}

// carriedFromShirudo is every patch in dir whose note says it is shirudo's.
func carriedFromShirudo(dir string) ([]kernelPatch, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.patch"))
	if err != nil {
		return nil, err
	}
	var carried []kernelPatch
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- a patch in this repository
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		if !strings.Contains(string(b), shirudoNote) {
			continue
		}
		p, err := parseKernelPatch(filepath.Base(f), string(b))
		if err != nil {
			return nil, err
		}
		carried = append(carried, p)
	}
	if len(carried) == 0 {
		return nil, fmt.Errorf("no patch in %s says it is %s", dir, strings.TrimPrefix(shirudoNote, "from "))
	}
	return carried, nil
}

// fetchShirudoPatches reads shirudo's kernel patches at commit, fetched by its id: git checks
// that what it got is that commit.
func fetchShirudoPatches(ctx context.Context, source, commit string) ([]kernelPatch, error) {
	dir, err := os.MkdirTemp("", "shirudo")
	if err != nil {
		return nil, fmt.Errorf("making a place for shirudo's repository: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output() // #nosec G204 -- git with fixed verbs
		if err != nil {
			var stderr string
			if x, ok := err.(*exec.ExitError); ok {
				stderr = strings.TrimSpace(string(x.Stderr))
			}
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr)
		}
		return string(out), nil
	}
	if _, err := git("init", "-q"); err != nil {
		return nil, err
	}
	if _, err := git("fetch", "-q", "--depth", "1", source, commit); err != nil {
		return nil, fmt.Errorf("fetching shirudo at %s: %w", commit, err)
	}
	names, err := git("ls-tree", "--name-only", commit, shirudoPatches+"/")
	if err != nil {
		return nil, err
	}
	var patches []kernelPatch
	for _, name := range strings.Fields(names) {
		if path.Ext(name) != ".patch" {
			continue
		}
		text, err := git("show", commit+":"+name)
		if err != nil {
			return nil, err
		}
		p, err := parseKernelPatch(path.Base(name), text)
		if err != nil {
			return nil, err
		}
		patches = append(patches, p)
	}
	if len(patches) == 0 {
		return nil, fmt.Errorf("shirudo at %s has no patch in %s", commit, shirudoPatches)
	}
	slices.SortFunc(patches, func(a, b kernelPatch) int { return strings.Compare(a.file, b.file) })
	return patches, nil
}
