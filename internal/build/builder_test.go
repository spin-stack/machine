// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

// builderRoot is a tree with part's apk.list.
func builderRoot(t *testing.T, list string) string {
	t.Helper()
	root := t.TempDir()
	for _, part := range Builders {
		if err := os.MkdirAll(filepath.Join(root, part), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, part, "apk.list"), []byte(list), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A builder is made on the pinned Alpine from its list, with no cache, and its tag names the
// list and the packages: another of either is another tag, the same of both the same tag.
func TestABuildersTagIsWhatItHolds(t *testing.T) {
	v := pins(t)
	alpine, _ := v.Get("alpine")
	made := func(list, version string) (Builder, []string) {
		t.Helper()
		log := fakeDocker(t, `[ "$1" = run ] && printf 'P:musl\nV:`+version+`\n'; exit 0`)
		b, err := MakeBuilder(context.Background(), Options{Root: builderRoot(t, list), Pins: v}, "qemu")
		if err != nil {
			t.Fatal(err)
		}
		return b, calls(t, log)
	}
	b, got := made("musl\n", "1.2.5-r10")
	if b.Packages != "musl=1.2.5-r10\n" || len(b.Tag) != 12 || b.Local != "spin-machine/builder-qemu:candidate" {
		t.Errorf("made %+v", b)
	}
	for _, want := range []string{
		"buildx build --no-cache --load --platform linux/amd64 --file builder/Dockerfile --build-arg ALPINE_IMAGE=" + alpine.Ref() + " --build-arg PART=qemu",
		"--label org.opencontainers.image.source=https://github.com/spin-stack/spin-machine",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("built with %q, without %q", got[0], want)
		}
	}
	if want := "run --rm --platform linux/amd64 --entrypoint cat " + b.Local + " /lib/apk/db/installed"; got[1] != want {
		t.Errorf("read the packages with %q", got[1])
	}
	if again, _ := made("musl\n", "1.2.5-r10"); again.Tag != b.Tag {
		t.Error("the same builder made twice has two tags")
	}
	if other, _ := made("musl\n", "1.2.6-r0"); other.Tag == b.Tag {
		t.Error("a package moved and the tag did not")
	}
	if other, _ := made("# why\nmusl\n", "1.2.5-r10"); other.Tag == b.Tag {
		t.Error("the list changed and the tag did not")
	}
}

func TestABuilderThatCannotBeMadeOrReadIsNone(t *testing.T) {
	v := pins(t)
	o := Options{Root: builderRoot(t, "musl\n"), Pins: v}
	for name, script := range map[string]string{
		"a build that failed":  "exit 1",
		"a run that failed":    `[ "$1" = run ] && exit 1; exit 0`,
		"no package installed": "exit 0",
	} {
		fakeDocker(t, script)
		if _, err := MakeBuilder(context.Background(), o, "qemu"); err == nil {
			t.Errorf("%s: made", name)
		}
	}
	if _, err := MakeBuilder(context.Background(), Options{Root: t.TempDir(), Pins: v}, "qemu"); err == nil {
		t.Error("a part with no apk.list: made")
	}
	if _, err := MakeBuilder(context.Background(), o, "kernel"); err == nil {
		t.Error("a part with no builder entry: made")
	}
	if _, err := MakeBuilder(context.Background(), Options{Root: o.Root, Pins: pinsWithout(t, "alpine")}, "qemu"); err == nil {
		t.Error("no Alpine pinned: made")
	}
}

// What is published is the builder made, under its entry's source, and its pin is the digest the
// registry gave it there - not another repository's digest of it.
func TestAPublishedBuilderIsPinnedByItsDigest(t *testing.T) {
	v := pins(t)
	e, _ := v.Get("e2fsprogs-builder")
	digest := "sha256:" + strings.Repeat("c", 64)
	log := fakeDocker(t, `[ "$1" = inspect ] && { echo elsewhere.io/x@sha256:`+strings.Repeat("d", 64)+`; echo `+e.Source+`@`+digest+`; }; exit 0`)
	b := Builder{Part: "e2fsprogs", Local: "spin-machine/builder-e2fsprogs:candidate", Tag: "0123456789ab"}
	pin, err := PublishBuilder(context.Background(), Options{Pins: v}, b)
	if err != nil || pin != digest {
		t.Fatalf("pinned %q, %v", pin, err)
	}
	remote := e.Source + ":" + b.Tag
	got := calls(t, log)
	for i, want := range []string{"tag " + b.Local + " " + remote, "push --quiet " + remote} {
		if at(got, i) != want {
			t.Errorf("call %d = %q, want %q", i, at(got, i), want)
		}
	}
	for name, script := range map[string]string{
		"a tag that failed":       `[ "$1" = tag ] && exit 1; exit 0`,
		"a push that failed":      `[ "$1" = push ] && exit 1; exit 0`,
		"an inspect that failed":  `[ "$1" = inspect ] && exit 1; exit 0`,
		"no digest in the source": "echo elsewhere.io/x@sha256:" + strings.Repeat("d", 64),
	} {
		fakeDocker(t, script)
		if _, err := PublishBuilder(context.Background(), Options{Pins: v}, b); err == nil {
			t.Errorf("%s: published", name)
		}
	}
	if _, err := PublishBuilder(context.Background(), Options{Pins: v}, Builder{Part: "kernel"}); err == nil {
		t.Error("a part with no builder entry: published")
	}
}

func TestThePinnedBuildersPackagesAreReadFromItsDigest(t *testing.T) {
	v := pins(t)
	e, _ := v.Get("qemu-builder")
	log := fakeDocker(t, `printf 'P:zlib\nV:1.3-r0\n'`)
	got, err := PinnedPackages(context.Background(), Options{Pins: v}, "qemu")
	if err != nil || got != "zlib=1.3-r0\n" {
		t.Fatalf("read %q, %v", got, err)
	}
	if c := calls(t, log); !strings.Contains(c[0], " "+e.Ref()+" ") {
		t.Errorf("read %q, not the pinned %s", c[0], e.Ref())
	}
	if _, err := PinnedPackages(context.Background(), Options{Pins: v}, "kernel"); err == nil {
		t.Error("a part with no builder entry: read")
	}
}

// The installed packages are every one of them, name=version, sorted. A package with half its
// record is refused, and so is a database with none.
func TestTheInstalledPackagesAreEveryOneOfThem(t *testing.T) {
	db := "C:Q1abc=\nP:zlib-static\nV:1.3.1-r2\nA:x86_64\n\nP:glib-static\nV:2.88.1-r1\n\n"
	got, err := installedPackages(db)
	if err != nil || got != "glib-static=2.88.1-r1\nzlib-static=1.3.1-r2\n" {
		t.Fatalf("installed %q, %v", got, err)
	}
	for name, db := range map[string]string{
		"no version":          "P:glib\n",
		"no name":             "V:1\n",
		"nothing":             "\n",
		"one whole, one half": "P:musl\nV:1.2.5-r10\n\nP:glib\n",
	} {
		if _, err := installedPackages(db); err == nil {
			t.Errorf("%s: read as installed packages", name)
		}
	}
}

func TestPackagesMovedSaysWhatMoved(t *testing.T) {
	got := packagesMoved("a=1\nb=1\nc=1\n", "a=1\nb=2\nd=1\n")
	if got != "- b 1 → 2\n- d 1 (new)\n- c (gone)\n" {
		t.Errorf("moved %q", got)
	}
	if got := packagesMoved("a=1\n", "a=1\n"); !strings.Contains(got, "no package moved") {
		t.Errorf("nothing moved, said %q", got)
	}
}

// pinnedAt is versions.yaml with entry's version at version.
func pinnedAt(t *testing.T, entry, version string) *versions.Versions {
	t.Helper()
	v := pins(t)
	w := *v
	w.Entries = slices.Clone(v.Entries)
	for i := range w.Entries {
		if w.Entries[i].Name == entry {
			w.Entries[i].Version = version
		}
	}
	return &w
}

// A builder made again is moved when its tag is not the one pinned, and current when it is.
func TestABuilderMadeAgainHasMovedOrIsCurrent(t *testing.T) {
	root := builderRoot(t, "musl\n")
	fakeDocker(t, `[ "$1" = run ] && printf 'P:musl\nV:1.2.5-r10\n'; exit 0`)
	moved, current, err := MovedBuilders(context.Background(), Options{Root: root, Pins: pins(t)})
	if err != nil || len(moved) != 2 || len(current) != 0 {
		t.Fatalf("moved %v, current %v, %v", moved, current, err)
	}
	moved, current, err = MovedBuilders(context.Background(), Options{Root: root, Pins: pinnedAt(t, "qemu-builder", moved[0].Tag)})
	if err != nil || len(moved) != 1 || moved[0].Part != "e2fsprogs" || len(current) != 1 || current[0].Part != "qemu" {
		t.Errorf("one pinned at its tag: moved %v, current %v, %v", moved, current, err)
	}
	fakeDocker(t, "exit 1")
	if _, _, err := MovedBuilders(context.Background(), Options{Root: root, Pins: pins(t)}); err == nil {
		t.Error("a builder that could not be made: compared")
	}
}

// Each builder that moved is published and pinned by its digest, and says what moved in its
// packages against the builder pinned before it, or that there was none to read.
func TestABuilderThatMovedIsPublishedAndPinned(t *testing.T) {
	digest := map[string]string{"qemu": "sha256:" + strings.Repeat("a", 64), "e2fsprogs": "sha256:" + strings.Repeat("b", 64)}
	docker := func(pinned string) string {
		return `case "$1" in
run) ` + pinned + `printf 'P:musl\nV:1.2.5-r10\n';;
inspect) case "$4" in
  *builder-qemu*) echo ghcr.io/spin-stack/spin-machine/builder-qemu@` + digest["qemu"] + `;;
  *) echo ghcr.io/spin-stack/spin-machine/builder-e2fsprogs@` + digest["e2fsprogs"] + `;;
  esac;;
esac`
	}
	fakeDocker(t, docker(""))
	moved := []Builder{
		{Part: "qemu", Local: "q", Tag: "0123456789ab", Packages: "musl=1.2.6-r0\n"},
		{Part: "e2fsprogs", Local: "e", Tag: "ba9876543210", Packages: "musl=1.2.5-r10\n"},
	}
	file, pinned, err := PinBuilders(context.Background(), Options{Pins: pins(t)}, moved)
	if err != nil {
		t.Fatal(err)
	}
	now, err := versions.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range moved {
		e, _ := now.Get(BuilderEntry(b.Part))
		if e.Version != b.Tag || e.Pin != digest[b.Part] {
			t.Errorf("%s pinned at %s", b.Part, e.Ref())
		}
		if p := pinned[i]; p.Entry != BuilderEntry(b.Part) || p.Tag != b.Tag || p.Pin != digest[b.Part] {
			t.Errorf("pinned %+v", p)
		}
	}
	if pinned[0].Changes != "- musl 1.2.5-r10 → 1.2.6-r0\n" || !strings.Contains(pinned[1].Changes, "no package moved") {
		t.Errorf("changes %q and %q", pinned[0].Changes, pinned[1].Changes)
	}
	fakeDocker(t, docker(`case "$*" in *@sha256:*) exit 1;; esac; `))
	if _, pinned, err := PinBuilders(context.Background(), Options{Pins: pins(t)}, moved[:1]); err != nil || pinned[0].Changes != "no builder was pinned to compare with\n" {
		t.Errorf("no pinned builder to read: %+v, %v", pinned, err)
	}
	fakeDocker(t, `[ "$1" = push ] && exit 1; exit 0`)
	if _, _, err := PinBuilders(context.Background(), Options{Pins: pins(t)}, moved); err == nil {
		t.Error("a builder that could not be published: pinned")
	}
}
