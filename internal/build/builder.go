// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spin-stack/spin-machine/internal/apklock"
)

// Builders are the parts compiled on an Alpine builder image of their own (builder/Dockerfile),
// each from its apk.list and pinned as versions.yaml's <part>-builder.
var Builders = []string{"qemu", "e2fsprogs"}

// BuilderEntry is the versions.yaml entry that pins part's builder.
func BuilderEntry(part string) string { return part + "-builder" }

// Builder is a part's builder made from this tree, loaded into docker under Local.
type Builder struct {
	Part, Local string
	// Tag is what it is published as: a hash of the Alpine it was made on, the list it installed
	// and every package that install left, so the same builder made twice is one tag and a tag
	// never names two images.
	Tag string
	// Packages is every package installed, as name=version lines.
	Packages string
}

// MakeBuilder makes part's builder from its apk.list on the pinned Alpine, from the mirror as it is
// now: no layer cache, which would answer with the packages of the day it was filled.
func MakeBuilder(ctx context.Context, o Options, part string) (Builder, error) {
	alpine, err := o.Pins.Get("alpine")
	if err != nil {
		return Builder{}, err
	}
	list, err := os.ReadFile(filepath.Join(o.Root, part, "apk.list")) //nolint:gosec // a file of the tree
	if err != nil {
		return Builder{}, fmt.Errorf("reading %s's apk.list: %w", part, err)
	}
	e, err := o.Pins.Get(BuilderEntry(part))
	if err != nil {
		return Builder{}, err
	}
	b := Builder{Part: part, Local: "spin-machine/builder-" + part + ":candidate"}
	if err := docker(ctx, o, "buildx", "build", "--no-cache", "--load", "--platform", "linux/amd64",
		"--file", "builder/Dockerfile",
		"--build-arg", "ALPINE_IMAGE="+alpine.Ref(), "--build-arg", "PART="+part,
		"--label", "org.opencontainers.image.source=https://github.com/"+sourceRepo(e.Source),
		"--tag", b.Local, "."); err != nil {
		return Builder{}, fmt.Errorf("making %s's builder: %w", part, err)
	}
	if b.Packages, err = installed(ctx, o, b.Local); err != nil {
		return Builder{}, fmt.Errorf("%s's builder: %w", part, err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s", alpine.Pin, list, b.Packages)
	b.Tag = hex.EncodeToString(h.Sum(nil))[:12]
	return b, nil
}

// PinnedPackages is every package the pinned builder of part has, as name=version lines.
func PinnedPackages(ctx context.Context, o Options, part string) (string, error) {
	e, err := o.Pins.Get(BuilderEntry(part))
	if err != nil {
		return "", err
	}
	return installed(ctx, o, e.Ref())
}

// PublishBuilder pushes b under its entry's source, tagged b.Tag, and answers its digest: the pin.
func PublishBuilder(ctx context.Context, o Options, b Builder) (string, error) {
	e, err := o.Pins.Get(BuilderEntry(b.Part))
	if err != nil {
		return "", err
	}
	remote := e.Source + ":" + b.Tag
	if err := docker(ctx, o, "tag", b.Local, remote); err != nil {
		return "", err
	}
	if err := docker(ctx, o, "push", "--quiet", remote); err != nil {
		return "", fmt.Errorf("publishing %s: %w", remote, err)
	}
	var out strings.Builder
	inspect := o
	inspect.Stdout = &out
	if err := docker(ctx, inspect, "inspect", "--format", `{{range .RepoDigests}}{{println .}}{{end}}`, remote); err != nil {
		return "", err
	}
	for line := range strings.Lines(out.String()) {
		if digest, ok := strings.CutPrefix(strings.TrimSpace(line), e.Source+"@"); ok {
			return digest, nil
		}
	}
	return "", fmt.Errorf("docker says no digest of %s in %s after pushing it", remote, e.Source)
}

// installed is image's apk database as name=version lines.
func installed(ctx context.Context, o Options, image string) (string, error) {
	var db strings.Builder
	run := o
	run.Stdout = &db
	if err := docker(ctx, run, "run", "--rm", "--platform", "linux/amd64", "--entrypoint", "cat", image, "/lib/apk/db/installed"); err != nil {
		return "", fmt.Errorf("reading the packages of %s: %w", image, err)
	}
	return apklock.Lock(db.String())
}

// sourceRepo is the GitHub repository a ghcr.io image of it is under: ghcr.io/owner/repo/name.
func sourceRepo(source string) string {
	return filepath.Dir(strings.TrimPrefix(source, "ghcr.io/"))
}
