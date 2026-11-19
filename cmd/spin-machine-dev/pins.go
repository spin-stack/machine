// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/spin-machine/internal/apklock"
	"github.com/spin-stack/spin-machine/internal/machinetype"
	"github.com/spin-stack/spin-machine/machine"
)

func init() {
	verbs = append(verbs, outdatedCmd, bumpCmd, lockCmd, refreshCmd)
}

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

A QEMU bump keeps the machine type (machine.MachineType), and prints what QEMU's newer types change
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
			out, err := qemuCompat(c.Context(), w)
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprintln(c.OutOrStdout(), out)
			return nil
		},
	}
}

// qemuCompat is what QEMU's newer machine types change against the pinned one, read from the
// pinned QEMU's source.
func qemuCompat(ctx context.Context, w workspace) (string, error) {
	e, err := w.pins.Get("qemu")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "qemu-compat")
	if err != nil {
		return "", fmt.Errorf("making a place for QEMU's source: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tarball := filepath.Join(dir, "qemu.tar.xz")
	if out, err := exec.CommandContext(ctx, "curl", "-fsSL", e.URL(), "-o", tarball).CombinedOutput(); err != nil { //nolint:gosec // the pin's URL
		return "", fmt.Errorf("fetching %s: %w: %s", e.URL(), err, out)
	}
	raw, err := os.ReadFile(tarball) //nolint:gosec // what was just fetched
	if err != nil {
		return "", fmt.Errorf("reading QEMU's source: %w", err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != e.Pin {
		return "", fmt.Errorf("%s does not have the sha256 versions.yaml pins", e.URL())
	}
	prefix := "qemu-" + e.Version + "/"
	files := []string{prefix + "hw/core/machine.c", prefix + "hw/i386/pc.c"}
	if out, err := exec.CommandContext(ctx, "tar", append([]string{"-xJf", tarball, "-C", dir}, files...)...).CombinedOutput(); err != nil { //nolint:gosec // the pin's files
		return "", fmt.Errorf("unpacking QEMU's machine types: %w: %s", err, out)
	}
	var src []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f)) //nolint:gosec // a file of the source just checked
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", f, err)
		}
		src = append(src, string(b))
	}
	out, err := machinetype.Compat(strings.Join(src, "\n"), machine.MachineType)
	if errors.Is(err, machinetype.ErrNoArray) {
		return machine.MachineType + " is the newest type in this QEMU: nothing newer to differ from it", nil
	}
	return out, err
}

func lockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Pin every package QEMU's and e2fsprogs' builders install at what the pinned Alpine's mirror has today",
		Long: `Pin every package QEMU's and e2fsprogs' builders install (each part's apk.list) at what the pinned
Alpine's mirror serves today, into its apk.lock: a fresh install of the list on the pinned Alpine,
its database read back. A new lock is a new QEMU or e2fsprogs build: the release notes say what
moved, and boot:compat that checkpoints still resume on it. Needs docker.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if err := lockAPK(c.Context(), c, w); err != nil {
				return couldNot(err)
			}
			return nil
		},
	}
}

// apkParts are the parts whose builders install from Alpine, each with an apk.list and an apk.lock.
var apkParts = []string{"qemu", "e2fsprogs"}

// lockAPK writes each part's apk.lock from a fresh install of its apk.list.
func lockAPK(ctx context.Context, c *cobra.Command, w workspace) error {
	alpine, err := w.pins.Get("alpine")
	if err != nil {
		return err
	}
	for _, part := range apkParts {
		list := filepath.Join(w.root, part, "apk.list")
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-v", list+":/apk.list:ro", alpine.Ref(), //nolint:gosec // the pinned image
			"sh", "-c", `apk add --no-cache $(grep -v "^#" /apk.list) > /dev/null && cat /lib/apk/db/installed`)
		var db, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &db, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installing %s's apk.list: %w: %s", part, err, bytes.TrimSpace(stderr.Bytes()))
		}
		lock, err := apklock.Lock(db.String())
		if err != nil {
			return fmt.Errorf("%s's packages: %w", part, err)
		}
		if err := os.WriteFile(filepath.Join(w.root, part, "apk.lock"), []byte(lock), 0o644); err != nil { //nolint:gosec // a file of the tree
			return fmt.Errorf("writing %s/apk.lock: %w", part, err)
		}
		say(c, "%s/apk.lock: %d packages", part, strings.Count(lock, "\n"))
	}
	return nil
}

func refreshCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "refresh kernel|systemd|apk",
		Short:  "Propose what upstream moved as a pull request: the kernel's series, the systemd source, the apk locks",
		Hidden: true,
		Long: `Propose, as a pull request, what upstream moved since the pin:

  kernel   the newest release of the kernel's series, and never past it (versions.yaml's note);
           most of what a stable release fixes is fixed before it is called a CVE
  systemd  the systemd source package the archive publishes now
  apk      the apk locks at what the pinned Alpine's mirror has today: the mirror serves only the
           newest build of each package, so a lock that resolved yesterday fails a build today,
           which a release found three times (6e7ba47, 8d495c5, run 43157106109)

Each branch is named for what it proposes, so a set proposed once is not proposed again, closed or
not: a person who closed it said no to it; a newer one closes the one it supersedes. Nothing is
proposed when the pins are current. refresh.yml runs it every day, as the release app.`,
		ValidArgs: []string{"kernel", "systemd", "apk"},
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
			}
			return proposeLocks(c.Context(), c, w)
		},
	}
	return c
}

// locksBody is the apk locks' pull request's body.
const locksBody = `The apk locks at what the pinned Alpine's mirror serves today, proposed by refresh.yml: the
mirror drops the builds a lock names, and a build from a stale lock fails. qemu.yml and image.yml
build it on this pull request; boot:compat after merge says checkpoints resume on the new QEMU.`

// proposeLocks writes the locks and proposes them, when they moved and were not proposed already.
func proposeLocks(ctx context.Context, c *cobra.Command, w workspace) error {
	if err := lockAPK(ctx, c, w); err != nil {
		return couldNot(err)
	}
	locks := []string{"qemu/apk.lock", "e2fsprogs/apk.lock"}
	diff := exec.CommandContext(ctx, "git", append([]string{"diff", "--quiet", "--"}, locks...)...)
	diff.Dir = w.root
	if diff.Run() == nil {
		say(c, "the apk locks are current")
		return summary("the apk locks are current\n")
	}
	h := sha256.New()
	for _, l := range locks {
		b, err := os.ReadFile(filepath.Join(w.root, l)) //nolint:gosec // a lock of the tree
		if err != nil {
			return couldNot(err)
		}
		h.Write(b)
	}
	branch := "refresh/apk-" + hex.EncodeToString(h.Sum(nil))[:12]
	run := func(name string, args ...string) error { return command(ctx, c, w.root, name, args...) }
	if exec.CommandContext(ctx, "git", "-C", w.root, "ls-remote", "--exit-code", "--heads", "origin", branch).Run() == nil {
		say(c, "%s was proposed already", branch)
		return nil
	}
	title := "apk: the locks at what the mirror has today"
	for _, step := range [][]string{
		{"git", "switch", "-c", branch},
		append([]string{"git", "commit", "-q", "-m", title, "--"}, locks...),
		{"git", "push", "origin", branch},
	} {
		if err := run(step[0], step[1:]...); err != nil {
			return err
		}
	}
	url, err := gh(ctx, "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", locksBody)
	if err != nil {
		return couldNot(err)
	}
	say(c, "%s", url)
	if err := summary(url + "\n"); err != nil {
		return couldNot(err)
	}
	open, err := gh(ctx, "pr", "list", "--state", "open", "--json", "number,headRefName", "--jq",
		`.[] | select((.headRefName | startswith("refresh/apk-")) and .headRefName != "`+branch+`") | .number`)
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
