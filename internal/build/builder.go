// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spin-stack/go-tools/versions"
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

// installed is image's apk database as name=version lines: a builder's tag is a hash of them, and a
// proposed builder says which of them moved (spin-machine-dev refresh builders).
func installed(ctx context.Context, o Options, image string) (string, error) {
	var db strings.Builder
	run := o
	run.Stdout = &db
	if err := docker(ctx, run, "run", "--rm", "--platform", "linux/amd64", "--entrypoint", "cat", image, "/lib/apk/db/installed"); err != nil {
		return "", fmt.Errorf("reading the packages of %s: %w", image, err)
	}
	return installedPackages(db.String())
}

// installedPackages is an apk installed database's packages as name=version lines, sorted.
func installedPackages(db string) (string, error) {
	var pins []string
	for _, block := range strings.Split(db, "\n\n") {
		var name, version string
		for _, line := range strings.Split(block, "\n") {
			switch k, v, _ := strings.Cut(line, ":"); k {
			case "P":
				name = v
			case "V":
				version = v
			}
		}
		switch {
		case name == "" && version == "":
			continue
		case name == "" || version == "":
			return "", fmt.Errorf("a package with name %q and version %q", name, version)
		}
		pins = append(pins, name+"="+version)
	}
	if len(pins) == 0 {
		return "", fmt.Errorf("no package installed")
	}
	slices.Sort(pins)
	return strings.Join(pins, "\n") + "\n", nil
}

// sourceRepo is the GitHub repository a ghcr.io image of it is under: ghcr.io/owner/repo/name.
func sourceRepo(source string) string {
	return filepath.Dir(strings.TrimPrefix(source, "ghcr.io/"))
}

// MovedBuilders makes each of Builders again and splits them by whether the tag made is the one
// versions.yaml pins: moved, to publish and pin, and current.
func MovedBuilders(ctx context.Context, o Options) (moved, current []Builder, err error) {
	for _, part := range Builders {
		b, err := MakeBuilder(ctx, o, part)
		if err != nil {
			return nil, nil, err
		}
		e, err := o.Pins.Get(BuilderEntry(part))
		if err != nil {
			return nil, nil, err
		}
		if e.Version == b.Tag {
			current = append(current, b)
		} else {
			moved = append(moved, b)
		}
	}
	return moved, current, nil
}

// Pinned is one builder published and pinned, and what moved in its packages against the builder
// pinned before it, for a person deciding whether to take it.
type Pinned struct {
	Entry, Tag, Pin string
	Changes         string
}

// PinBuilders publishes each of moved and pins it, and returns versions.yaml with every pin moved,
// which it does not write.
func PinBuilders(ctx context.Context, o Options, moved []Builder) ([]byte, []Pinned, error) {
	pins := o.Pins
	var file []byte
	var pinned []Pinned
	for _, b := range moved {
		changes := "no builder was pinned to compare with\n"
		if old, err := PinnedPackages(ctx, o, b.Part); err == nil {
			changes = packagesMoved(old, b.Packages)
		}
		pin, err := PublishBuilder(ctx, o, b)
		if err != nil {
			return nil, nil, err
		}
		entry := BuilderEntry(b.Part)
		if file, err = pins.Set(entry, b.Tag, pin); err != nil {
			return nil, nil, err
		}
		if pins, err = versions.Parse(file); err != nil {
			return nil, nil, err
		}
		pinned = append(pinned, Pinned{Entry: entry, Tag: b.Tag, Pin: pin, Changes: changes})
	}
	return file, pinned, nil
}

// packagesMoved is what moved from one builder's packages to another's, as name=version lines.
func packagesMoved(old, now string) string {
	was := map[string]string{}
	for line := range strings.Lines(old) {
		name, version, _ := strings.Cut(strings.TrimSpace(line), "=")
		was[name] = version
	}
	var lines []string
	for line := range strings.Lines(now) {
		name, version, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch before, ok := was[name]; {
		case !ok:
			lines = append(lines, "- "+name+" "+version+" (new)")
		case before != version:
			lines = append(lines, "- "+name+" "+before+" → "+version)
		}
		delete(was, name)
	}
	for _, name := range slices.Sorted(maps.Keys(was)) {
		lines = append(lines, "- "+name+" (gone)")
	}
	if len(lines) == 0 {
		return "no package moved: the Alpine or the list did\n"
	}
	return strings.Join(lines, "\n") + "\n"
}
