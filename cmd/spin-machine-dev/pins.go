// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/build"
)

func outdatedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outdated",
		Short: "Say which of versions.yaml's pins are behind their upstream (exits 1 when any is)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			return versionsTool(c.Context(), c, root, "check")
		},
	}
}

func bumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bump <entry> [version]",
		Short: "Pin one entry of versions.yaml at a version, the newest by default",
		Long: `Pin one entry of versions.yaml at a version, the newest by default. Nothing is built: the entry's
note says what the bump is checked with.

A QEMU bump keeps the machine type (machine.Type), and prints what QEMU's newer types change
against it - hw_compat_X_Y and pc_compat_X_Y, from the source of the version just pinned, checked
against its sha256 - for the bump's pull request.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			if err := versionsTool(c.Context(), c, root, append([]string{"bump"}, args...)...); err != nil {
				return err
			}
			if args[0] != "qemu" {
				return nil
			}
			w, err := open()
			if err != nil {
				return err
			}
			out, err := build.QEMUCompat(c.Context(), w.pins)
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprintln(c.OutOrStdout(), out)
			return nil
		},
	}
}

func refreshCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "refresh kernel|systemd|openssh|builders",
		Short:  "Propose what upstream moved as a pull request: the kernel's series, the systemd and openssh sources, the builders",
		Hidden: true,
		Long: `Propose, as a pull request, what upstream moved since the pin:

  kernel   the newest release of the kernel's series, and never past it (versions.yaml's note);
           most of what a stable release fixes is fixed before it is called a CVE
  systemd  the systemd source package the archive publishes now; parts.yml's image build on the
           pull request applies every patch in image/systemd to it without fuzz, as for openssh
  openssh  the openssh source package the archive publishes now; parts.yml's image build on the
           pull request applies every patch in image/openssh to it without fuzz, and fails there
           when one no longer applies
  builders QEMU's and e2fsprogs' builder images (builder/Dockerfile) made again from each apk.list
           on the pinned Alpine, published, and pinned, when a package in them moved; the
           pull request lists the packages that did. Run on a branch other than main, it
           commits the pins to that branch instead: how a changed apk.list gets its builder.
           Needs docker, logged in to ghcr.io with packages: write

Each branch is named for what it proposes, so a set proposed once is not proposed again, closed or
not: a person who closed it said no to it; a newer one closes the one it supersedes. Nothing is
proposed when the pins are current. refresh.yml runs it as the release app: kernel, systemd and
openssh every day, builders every week.`,
		ValidArgs: []string{"kernel", "systemd", "openssh", "builders"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(c *cobra.Command, args []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			switch args[0] {
			case "kernel":
				return versionsTool(c.Context(), c, w.root, "refresh", "-propose", "-label", "kernel", "kernel")
			case "systemd":
				return versionsTool(c.Context(), c, w.root, "refresh", "-propose", "-label", "systemd", "systemd-source")
			case "openssh":
				return versionsTool(c.Context(), c, w.root, "refresh", "-propose", "-label", "openssh", "openssh-source")
			}
			return proposeBuilders(c.Context(), c, w)
		},
	}
	return c
}

// buildersBody is the builders' pull request's body, before the packages that moved.
const buildersBody = `QEMU's and e2fsprogs' builder images made again from each apk.list on the pinned Alpine, proposed
by refresh.yml (spin-machine-dev refresh builders). Nothing forces it: a pinned builder keeps its
packages after Alpine's mirror drops them, so this is taken when its fixes are wanted. parts.yml
compiles on it here; boot:compat after merge says checkpoints resume on the new QEMU.
`

// proposeBuilders makes each builder again and, for the ones whose packages moved, publishes it and
// pins it: as a pull request from main, or as a commit on any other branch.
func proposeBuilders(ctx context.Context, c *cobra.Command, w workspace) error {
	o := build.Options{Root: w.root, Pins: w.pins, Stdout: c.ErrOrStderr(), Stderr: c.ErrOrStderr()}
	moved, current, err := build.MovedBuilders(ctx, o)
	if err != nil {
		return couldNot(err)
	}
	for _, b := range current {
		say(c, "%s is current (%s)", build.BuilderEntry(b.Part), b.Tag)
	}
	var tags []string
	for _, b := range moved {
		tags = append(tags, b.Tag)
	}
	if len(moved) == 0 {
		say(c, "the builders are current")
		return summary("the builders are current\n")
	}
	branch := cmp.Or(os.Getenv("GITHUB_REF_NAME"), currentBranch(ctx, w.root))
	onMain := branch == "main"
	if onMain {
		// Named for what it proposes, so a set proposed once is not proposed again, closed or
		// not: a person who closed it said no to it.
		branch = "refresh/builders-" + strings.Join(tags, "-")
		if exec.CommandContext(ctx, "git", "-C", w.root, "ls-remote", "--exit-code", "--heads", "origin", branch).Run() == nil { //nolint:gosec // a branch this names
			say(c, "%s was proposed already", branch)
			return nil
		}
	}
	file, pinned, err := build.PinBuilders(ctx, o, moved)
	if err != nil {
		return couldNot(err)
	}
	body := buildersBody
	for _, p := range pinned {
		body += "\n### " + p.Entry + " " + p.Tag + "\n\n" + p.Changes
		say(c, "%s: %s@%s", p.Entry, p.Tag, p.Pin)
	}
	if err := os.WriteFile(filepath.Join(w.root, versions.File), file, 0o644); err != nil { //nolint:gosec // the tree's own file
		return couldNot(fmt.Errorf("writing %s: %w", versions.File, err))
	}
	if err := summary(body); err != nil {
		return couldNot(err)
	}
	run := func(name string, args ...string) error { return command(ctx, c, w.root, name, args...) }
	title := "builders: QEMU's and e2fsprogs' packages at what Alpine has today"
	if !onMain {
		// A branch's own builder - what its apk.list asks for - committed where it was asked.
		for _, step := range [][]string{
			{"git", "commit", "-q", "-m", title + "\n\n" + body, "--", versions.File},
			{"git", "push", "origin", "HEAD:refs/heads/" + branch},
		} {
			if err := run(step[0], step[1:]...); err != nil {
				return err
			}
		}
		return nil
	}
	for _, step := range [][]string{
		{"git", "switch", "-c", branch},
		{"git", "commit", "-q", "-m", title, "--", versions.File},
		{"git", "push", "origin", branch},
	} {
		if err := run(step[0], step[1:]...); err != nil {
			return err
		}
	}
	url, err := gh(ctx, "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body)
	if err != nil {
		return couldNot(err)
	}
	say(c, "%s", url)
	if err := summary(url + "\n"); err != nil {
		return couldNot(err)
	}
	open, err := gh(ctx, "pr", "list", "--state", "open", "--json", "number,headRefName", "--jq",
		`.[] | select((.headRefName | startswith("refresh/builders-")) and .headRefName != "`+branch+`") | .number`)
	if err != nil {
		return couldNot(err)
	}
	for _, n := range strings.Fields(open) {
		if _, err := gh(ctx, "pr", "close", n, "--delete-branch", "--comment", "Superseded by "+url+"."); err != nil {
			return couldNot(err)
		}
	}
	return nil
}

// currentBranch is the branch checked out in root, or "" when none is.
func currentBranch(ctx context.Context, root string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
