// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/build"
)

// Repo is the repository releases are published from.
const Repo = "spin-stack/machine"

// Identity matches the workflow every published file is signed by: release.yml, on main, under
// the name the repository had when it ran. It was spin-stack/spin-machine until 2026-11-21, which
// signed every release up to v20261120.04, so those still verify.
const Identity = `^https://github\.com/spin-stack/(spin-)?machine/\.github/workflows/release\.yml@refs/heads/main$`

// OIDCIssuer is who issues the token a release is signed with.
const OIDCIssuer = "https://token.actions.githubusercontent.com"

// Fetch downloads release tag's tarball into dir, checks it against the sha256 published beside it
// and its signature by Identity, unpacks it there and returns its usr/share/spin-stack. Nothing is
// unpacked that did not pass: what a caller boots or reads is what was released.
func Fetch(ctx context.Context, pins *versions.Versions, tag, dir string) (string, error) {
	name := Name(tag) + ".tar.gz"
	if err := download(ctx, tag, dir, name, name+".sha256", name+".sigstore.json"); err != nil {
		return "", err
	}
	tarball := filepath.Join(dir, name)
	sum, err := os.ReadFile(tarball + ".sha256")
	if err != nil {
		return "", fmt.Errorf("reading release %s's published sha256: %w", tag, err)
	}
	got, err := build.FileSHA256(tarball)
	if err != nil {
		return "", err
	}
	if want, _, _ := strings.Cut(strings.TrimSpace(string(sum)), " "); want != got {
		return "", fmt.Errorf("release %s: %s is %s, and its published sha256 says %q", tag, name, got, want)
	}
	if err := verify(ctx, pins, tarball); err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xzf", tarball, "-C", dir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("unpacking %s: %w: %s", tarball, err, bytes.TrimSpace(out))
	}
	share := filepath.Join(dir, Name(tag), "usr", "share", "spin-stack")
	if _, err := os.Stat(share); err != nil {
		return "", fmt.Errorf("release %s's tarball has no usr/share/spin-stack: %w", tag, err)
	}
	return share, nil
}

// Signed downloads the file name published with release tag into dir and returns its path, once
// its signature is Identity's: one file of a release, when the tarball is more than is needed.
func Signed(ctx context.Context, pins *versions.Versions, tag, dir, name string) (string, error) {
	if err := download(ctx, tag, dir, name, name+".sigstore.json"); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	return path, verify(ctx, pins, path)
}

// Assets are the names of the files published with release tag.
func Assets(ctx context.Context, tag string) ([]string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", "release", "view", tag, "-R", Repo, "--json", "assets", "-q", ".assets[].name")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing release %s's files: %w: %s", tag, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return strings.Fields(string(out)), nil
}

// download puts the files names of release tag in dir.
func download(ctx context.Context, tag, dir string, names ...string) error {
	args := []string{"release", "download", tag, "-R", Repo, "-D", dir}
	for _, n := range names {
		args = append(args, "-p", n)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("downloading %s of release %s: %w: %s", strings.Join(names, ", "), tag, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

// verify checks file against the sigstore bundle beside it, as signed by Identity, with the cosign
// versions.yaml pins, built by go run: one pin for a laptop and a runner alike, and nothing on the
// PATH to drift from it.
func verify(ctx context.Context, pins *versions.Versions, file string) error {
	cosign, err := pins.Get("cosign")
	if err != nil {
		return fmt.Errorf("verifying %s: %w", filepath.Base(file), err)
	}
	cmd := exec.CommandContext(ctx, "go", "run", cosign.Source+"@"+cosign.Version, "verify-blob",
		"--bundle", file+".sigstore.json", "--certificate-identity-regexp", Identity, "--certificate-oidc-issuer", OIDCIssuer, file)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s's signature is not %s's: %w: %s", filepath.Base(file), Identity, err, bytes.TrimSpace(out))
	}
	return nil
}

// Kernel fetches the release tag into dir, emptied first (Fetch), and says its kernel's version and
// where its sources list is: what spin-machine-dev cves judges CVEs against. The tarball and not a
// file of its own, because the sources list is in what a release signs already.
func Kernel(ctx context.Context, pins *versions.Versions, tag, dir string) (version, sources string, err error) {
	if err := os.RemoveAll(dir); err != nil {
		return "", "", fmt.Errorf("emptying %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("making %s: %w", dir, err)
	}
	share, err := Fetch(ctx, pins, tag, dir)
	if err != nil {
		return "", "", err
	}
	sources = filepath.Join(share, "kernel", "sources")
	if _, err := os.Stat(sources); err != nil {
		return "", "", fmt.Errorf("%s carries no kernel/sources: cves reads releases from the one after kernel/Dockerfile wrote it: %w", tag, err)
	}
	env, err := os.ReadFile(filepath.Join(share, "machine.env"))
	if err != nil {
		return "", "", fmt.Errorf("reading %s's manifest: %w", tag, err)
	}
	return ParseEnv(string(env))["kernel_version"], sources, nil
}
