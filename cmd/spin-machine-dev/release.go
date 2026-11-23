// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spin-stack/go-tools/attestgate"
	"github.com/spin-stack/go-tools/provenance"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/internal/release"
)

func releaseCmd() *cobra.Command {
	var f buildFlags
	var version string
	c := &cobra.Command{
		Use:   "release",
		Short: "Build everything that is missing or stale and pack one versioned tarball, refusing one with anything missing",
		Long: `Build every part (as build does), then pack them into _output/release: one tarball, one
version - QEMU and its firmware, the kernel, the base image and the tools that make and read its
filesystem - with LICENSE, NOTICE, every patch each part was built with, machine.env (the
manifest: versions and checksums), SOURCES (where every part came from) and a CycloneDX SBOM,
inside the tarball and beside it.

A release missing a piece is worse than none: it installs, and what it left out fails somewhere
else. So every file is required, and a patch directory ships whole, never as a list.

--version names it; by default it is git describe's.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			o := f.options(c, w)
			if _, err := build.Make(c.Context(), o, build.Parts); err != nil {
				return couldNot(err)
			}
			packed, err := pack(c.Context(), c, w, version)
			if err != nil {
				return couldNot(err)
			}
			say(c, "✓ %s", packed.Tarball)
			return emit(c.OutOrStdout(), packed)
		},
	}
	f.register(c)
	c.Flags().StringVar(&version, "version", "", "the release's version (default: git describe --tags --always --dirty)")
	c.AddCommand(releasePackCmd(), releaseVersionCmd(), releaseNotesCmd(), releaseProvenanceCmd(), releaseSignCmd(), releaseTagCmd(), releaseTraceableKernelCmd(), releaseUploadCmd())
	return c
}

func releasePackCmd() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:    "pack",
		Short:  "Pack what _output holds as the release, building nothing",
		Hidden: true,
		Long: `Pack what _output holds into _output/release, as release does after it builds - for a tree built
by other means, or packed again. Refuses a tree with anything missing, as release does.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			packed, err := pack(c.Context(), c, w, version)
			if err != nil {
				return couldNot(err)
			}
			say(c, "✓ %s", packed.Tarball)
			return emit(c.OutOrStdout(), packed)
		},
	}
	c.Flags().StringVar(&version, "version", "", "the release's version (default: git describe --tags --always --dirty)")
	return c
}

// pack packs what _output holds as the release version.
func pack(ctx context.Context, c *cobra.Command, w workspace, version string) (release.Packed, error) {
	if version == "" {
		version = git(ctx, w.root, "describe", "--tags", "--always", "--dirty")
		if version == "" {
			version = "dev"
		}
	}
	commit := git(ctx, w.root, "rev-parse", "HEAD")
	if commit == "" {
		commit = "unknown"
	}
	epoch, err := sourceDateEpoch(ctx, w.root)
	if err != nil {
		return release.Packed{}, err
	}
	return release.Pack(ctx, release.Options{
		Root: w.root, Out: w.out, Version: version, Pins: w.pins, Commit: commit, Epoch: epoch, Now: time.Now(),
		Stderr: c.ErrOrStderr(),
	})
}

// sourceDateEpoch is SOURCE_DATE_EPOCH, or the commit's time: the SBOM's clock, so a release
// built twice from one commit carries the same bytes.
func sourceDateEpoch(ctx context.Context, root string) (time.Time, error) {
	s := os.Getenv("SOURCE_DATE_EPOCH")
	if s == "" {
		s = git(ctx, root, "log", "-1", "--format=%ct")
	}
	if s == "" {
		return time.Unix(0, 0), nil
	}
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q is not a number of seconds", s)
	}
	return time.Unix(sec, 0), nil
}

// git is what git prints in root, trimmed; nothing when it fails.
func git(ctx context.Context, root string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gh is what the GitHub CLI prints; it reads its token and repository from the environment.
func gh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args[:min(len(args), 2)], " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return strings.TrimSpace(string(out)), nil
}

func releaseTraceableKernelCmd() *cobra.Command {
	var f buildFlags
	var version string
	c := &cobra.Command{
		Use:    "traceable-kernel --version <version>",
		Short:  "Build the lab's traceable kernel of a release into _output/release, beside it and never in it",
		Hidden: true,
		Long: `Build the release's kernel (as build kernel does, into _output) and a second one from the same
source and config with lockdown forced to none instead of confidentiality, into _output/traceable,
then hold the second's config to the first's: that one option swapped and every other as the
release has it. Its bzImage is put in _output/release as the release's traceable kernel, to be
signed and published beside the release for the lab's ledger, which reads a scheduler trace that
lockdown takes away (kernel/README.md). It enters no tarball and nothing spin boots.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if version == "" {
				return couldNot(errors.New("--version names the release whose traceable kernel this is"))
			}
			kernel, err := build.Plan([]string{"kernel"})
			if err != nil {
				return couldNot(err)
			}
			o := f.options(c, w)
			if _, err := build.Make(c.Context(), o, kernel); err != nil {
				return couldNot(err)
			}
			t := o
			t.Out, t.Traceable = filepath.Join(w.out, "traceable"), true
			if _, err := build.Make(c.Context(), t, kernel); err != nil {
				return couldNot(err)
			}
			if err := build.TraceableConfig(filepath.Join(o.Out, "kernel", "kernel-config"), filepath.Join(t.Out, "kernel", "kernel-config")); err != nil {
				return couldNot(err)
			}
			to := filepath.Join(w.out, "release", release.TraceableKernel(version))
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return couldNot(fmt.Errorf("making %s: %w", filepath.Dir(to), err))
			}
			if err := copyFile(filepath.Join(t.Out, "kernel", "bzImage"), to); err != nil {
				return couldNot(err)
			}
			say(c, "✓ %s", to)
			return nil
		},
	}
	f.register(c)
	c.Flags().StringVar(&version, "version", "", "the release's version")
	return c
}

func releaseUploadCmd() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:    "upload --version <version>",
		Short:  "Add every file of _output/release to the published release",
		Hidden: true,
		Long: `Add every file of _output/release to the release --version, published already: release.yml's
traceable job publishes the release's traceable kernel and its bundles this way, after the release
itself. A file the release has already is refused, as gh release upload refuses it. Needs gh.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if version == "" {
				return couldNot(errors.New("--version names the release the files are added to"))
			}
			files, err := filepath.Glob(filepath.Join(w.out, "release", "*"))
			if err != nil || len(files) == 0 {
				return couldNot(fmt.Errorf("nothing in %s to add to release %s (%v)", filepath.Join(w.out, "release"), version, err))
			}
			if _, err := gh(c.Context(), append([]string{"release", "upload", version}, files...)...); err != nil {
				return exitError{code: 1, err: err}
			}
			return nil
		},
	}
	c.Flags().StringVar(&version, "version", "", "the release's version")
	return c
}

func releaseVersionCmd() *cobra.Command {
	var given string
	c := &cobra.Command{
		Use:    "version",
		Short:  "Say the version a release is: the one given, or the next of today's",
		Hidden: true,
		Long: `Say the version a release is: --given, or a new one for today counted from the tags today
already has (vYYYYMMDD.NN, two digits so that .02 sorts after .01 and both before the next day's,
as strings, everywhere). Written to the step's outputs as version=. Needs every tag: a shallow
clone counts none and hands out .01 twice.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			tags := strings.Fields(git(c.Context(), root, "tag", "-l", "v*"))
			version, origin, err := release.Version(given, tags, time.Now())
			if err != nil {
				return exitError{code: 2, err: err}
			}
			say(c, "Releasing %s — %s", version, origin)
			_, _ = fmt.Fprintln(c.OutOrStdout(), version)
			return output("version", version)
		},
	}
	c.Flags().StringVar(&given, "given", "", "the version the caller asked for")
	return c
}

func releaseNotesCmd() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:    "notes --version <version>",
		Short:  "Write the release's notes: what changed since the one before",
		Hidden: true,
		Long: `Write the notes of the release packed in _output/release: what reached the machine since the
newest release before it (the commits by part, the pins, the kernel options, the patches), whether
the QEMU shipped offers a newer machine type, and its machine.env. Which releases' checkpoints
resume on it is lab compat's to say, in the release's compat job. On stdout, and as notes= on the
step's outputs. Needs gh.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if version == "" {
				return couldNot(errors.New("--version names the release whose notes these are"))
			}
			notes, err := notesOf(c.Context(), w, version)
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprint(c.OutOrStdout(), notes)
			return output("notes", notes)
		},
	}
	c.Flags().StringVar(&version, "version", "", "the release's version")
	return c
}

// notesOf is the notes of the release version packed in w.out.
func notesOf(ctx context.Context, w workspace, version string) (string, error) {
	share := filepath.Join(w.out, "release", release.Name(version), "usr", "share", "spin-stack")
	raw, err := os.ReadFile(filepath.Join(share, "machine.env"))
	if err != nil {
		return "", fmt.Errorf("reading the release's manifest: %w", err)
	}
	// The newest published release that is not this one, so a re-run of a version that already
	// published compares against its predecessor, and not withdrawn (releases withdraw): what
	// changed is said against what a consumer has, and none has a withdrawn release. A listing
	// that fails fails: read as "no releases", it would announce a first release that is not one.
	listed, err := gh(ctx, "release", "list", "--exclude-pre-releases", "--limit", "30", "--json", "tagName", "-q", ".[].tagName")
	if err != nil {
		return "", err
	}
	previous := ""
	for _, t := range strings.Fields(listed) {
		if t != version {
			previous = t
			break
		}
	}
	notes := "The first release: nothing before it to say what changed against.\n"
	if previous != "" {
		if notes, err = release.Changes(w.root, previous); err != nil {
			return "", fmt.Errorf("what changed since %s: %w", previous, err)
		}
	}
	// Whether the QEMU shipped offers a machine type newer than the one every checkpoint is saved
	// on: said, never acted on (machine.Type).
	offer, err := build.MachineType(filepath.Join(share, "bin", "qemu-system-x86_64"))
	if err != nil {
		return "", err
	}
	if offer != "" {
		notes += "\n" + offer + "\n"
	}
	return notes + "\nThe machine as it stood on this date.\n\n```\n" + string(raw) + "```\n", nil
}

func releaseProvenanceCmd() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:    "provenance --version <version>",
		Short:  "Write who built the release and from what, as SLSA provenance, to _output/provenance.json",
		Hidden: true,
		Long: `Write who built the release and from what - this workflow at this commit, every pin in
versions.yaml - as SLSA provenance in _output/provenance.json. It refuses an environment that is
not a GitHub Actions run: provenance with no builder in it says nothing.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			p, err := provenance.Build(os.Getenv, version, w.pins, nil)
			if err != nil {
				return couldNot(err)
			}
			b, err := p.JSON()
			if err != nil {
				return couldNot(err)
			}
			if err := os.MkdirAll(w.out, 0o755); err != nil {
				return couldNot(err)
			}
			if err := os.WriteFile(filepath.Join(w.out, "provenance.json"), b, 0o644); err != nil { //nolint:gosec // published beside its files
				return couldNot(fmt.Errorf("writing the provenance: %w", err))
			}
			return nil
		},
	}
	c.Flags().StringVar(&version, "version", "", "the release's version")
	return c
}

func releaseSignCmd() *cobra.Command {
	var identity string
	c := &cobra.Command{
		Use:    "sign --identity <workflow@ref>",
		Short:  "Sign and attest every file of _output/release, and check each bundle against the identity",
		Hidden: true,
		Long: `Sign and attest, with cosign, every file of _output/release as --identity - the workflow at its
ref - and check each bundle against it: a bundle nothing verifies is a step that can quietly stop
working. Each file gets a signature and the provenance; the tarball also its SBOM. Then nothing in
the directory may lack an attestation (attestgate). Needs cosign and an OIDC token.

An attestation is checked by the file's digest, not the file: verify-blob-attestation reads a blob
whole and refuses one over 128 MiB, and the tarball is over 300 MB (v20261013.01 failed there).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if identity == "" {
				return couldNot(errors.New("--identity names the workflow every bundle is checked against"))
			}
			return sign(c.Context(), c, filepath.Join(w.out, "release"), filepath.Join(w.out, "provenance.json"), identity)
		},
	}
	c.Flags().StringVar(&identity, "identity", "", "the certificate identity: the workflow at its ref")
	return c
}

// sign signs and attests every published file of dir, checking each bundle as it goes.
func sign(ctx context.Context, c *cobra.Command, dir, predicate, identity string) error {
	cosign := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "cosign", args...)
		cmd.Stdout, cmd.Stderr = nil, c.ErrOrStderr()
		return status(cmd.Run(), "cosign "+args[0])
	}
	attested := func(kind, bundle, file string) error {
		sum, err := build.FileSHA256(file)
		if err != nil {
			return couldNot(err)
		}
		return cosign("verify-blob-attestation", "--type", kind, "--bundle", bundle, "--digest", sum, "--digestAlg", "sha256",
			"--certificate-identity", identity, "--certificate-oidc-issuer", release.OIDCIssuer)
	}
	var files []string
	for _, g := range []string{"*.tar.gz", "*.tar.gz.sha256", "machine.env", "*.cdx.json", release.TraceableKernel("*")} {
		found, _ := filepath.Glob(filepath.Join(dir, g))
		files = append(files, found...)
	}
	for _, f := range files {
		steps := [][]string{
			{"sign-blob", "--yes", "--bundle", f + ".sigstore.json", f},
			{"attest-blob", "--yes", "--type", "slsaprovenance1", "--predicate", predicate, "--bundle", f + ".provenance.sigstore.json", f},
			{"verify-blob", "--bundle", f + ".sigstore.json", "--certificate-identity", identity, "--certificate-oidc-issuer", release.OIDCIssuer, f},
		}
		for _, s := range steps {
			if err := cosign(s...); err != nil {
				return err
			}
		}
		if err := attested("slsaprovenance1", f+".provenance.sigstore.json", f); err != nil {
			return err
		}
	}
	// The tarball's SBOM, attested over it: what it is made of, by the workflow that built it.
	tarballs, _ := filepath.Glob(filepath.Join(dir, "*.tar.gz"))
	for _, f := range tarballs {
		if err := cosign("attest-blob", "--yes", "--type", "cyclonedx", "--predicate", strings.TrimSuffix(f, ".tar.gz")+".cdx.json",
			"--bundle", f+".cdx.sigstore.json", f); err != nil {
			return err
		}
		if err := attested("cyclonedx", f+".cdx.sigstore.json", f); err != nil {
			return err
		}
	}
	for _, g := range []struct{ require, except []string }{
		{[]string{"provenance"}, nil},
		{[]string{"cdx"}, []string{"*.sha256", "machine.env", "*.cdx.json", release.TraceableKernel("*")}},
	} {
		missing, err := attestgate.Files(dir, g.require, g.except)
		if err != nil {
			return couldNot(err)
		}
		if len(missing) > 0 {
			return exitError{code: 1, err: fmt.Errorf("not attested: %s", strings.Join(missing, ", "))}
		}
	}
	return nil
}

func releaseTagCmd() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:    "tag --version <version>",
		Short:  "Tag this commit as the release's version on GitHub; a version tagged already is refused",
		Hidden: true,
		Long: `Tag this commit as --version on GitHub, through the API and not git push: GitHub refuses a pushed
ref from GITHUB_TOKEN when the commits under it change .github/workflows/, a permission that token
cannot hold (v20261119.01). Creating the ref is contents: write, which the release has, and
creating one that exists fails, so a version is tagged once. Needs gh and GITHUB_REPOSITORY.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			if version == "" || os.Getenv("GITHUB_REPOSITORY") == "" {
				return couldNot(errors.New("--version and GITHUB_REPOSITORY say what to tag where"))
			}
			commit := git(c.Context(), root, "rev-parse", "HEAD")
			if _, err := gh(c.Context(), "api", "repos/"+os.Getenv("GITHUB_REPOSITORY")+"/git/refs",
				"-f", "ref=refs/tags/"+version, "-f", "sha="+commit, "--jq", ".ref"); err != nil {
				return exitError{code: 1, err: err}
			}
			return command(c.Context(), c, root, "git", "fetch", "--quiet", "origin", "refs/tags/"+version+":refs/tags/"+version)
		},
	}
	c.Flags().StringVar(&version, "version", "", "the release's version")
	return c
}
