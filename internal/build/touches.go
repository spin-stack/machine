// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"os/exec"
	"slices"

	"github.com/spin-stack/go-tools/versions"
)

// Touches is whether the commits since base can change what is built from pins and paths: true
// when anything under paths changed, or when versions.yaml hands those pins other build arguments
// than it did at base.
//
// It exists because versions.yaml has to be in the path filter of every workflow that builds from
// it, and a path filter cannot say which lines of a file changed: without it a bump of e2fsprogs
// rebuilds QEMU and the kernel, tens of minutes each. The comparison is of the build arguments,
// exactly what a Dockerfile is handed, so a note edited in versions.yaml builds nothing.
//
// It errs towards building. No base (a dispatch, a branch's first push), a base this clone does
// not have, or a versions.yaml at base that cannot be read for these pins (before the file or the
// entry existed) is true; so is a git that cannot answer.
func Touches(ctx context.Context, root, base string, now *versions.Versions, pins, paths []string) bool {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		return cmd.Output()
	}
	if base == "" {
		return true
	}
	if _, err := git("cat-file", "-e", base+"^{commit}"); err != nil {
		return true
	}
	// --quiet exits 1 for a difference and 0 for none; anything else is git failing, and building.
	diff := exec.CommandContext(ctx, "git", append([]string{"diff", "--quiet", base, "HEAD", "--"}, paths...)...)
	diff.Dir = root
	if err := diff.Run(); err != nil {
		return true
	}
	raw, err := git("show", base+":versions.yaml")
	if err != nil {
		return true
	}
	was, err := versions.Parse(raw)
	if err != nil {
		return true
	}
	before, err := Args(was, pins...)
	if err != nil {
		return true
	}
	after, err := Args(now, pins...)
	if err != nil {
		return true
	}
	return !slices.Equal(before, after)
}
