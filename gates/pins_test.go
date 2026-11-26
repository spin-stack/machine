// SPDX-License-Identifier: Apache-2.0

// Package gates holds the whole repository to the rules no one package owns: every pin written
// once, in versions.yaml; every workflow that publishes attesting first; lab.yml offering every
// probe the lab has.
package gates_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/go-tools/versions/gate"
)

// root is the repository's: a test runs in its package's directory.
const root = ".."

// A version is written once: in versions.yaml. The gate is go-tools': no digest or commit
// anywhere else, no Dockerfile pin with a default of its own, no entry nothing reads.
func TestVersionsYAMLIsTheOnlyPin(t *testing.T) {
	g := gate.Gate{Root: root, Elsewhere: []string{
		// What Go writes of its own modules.
		"go.sum",
		// The qboot commit the patch was cut against and the licence it carries. A bump of qboot
		// has to rebase the patch and restate both; versions.yaml's note says so.
		"NOTICE",
		"qemu/qboot/README.md",
	},
		// And spin-machine-dev's Go, which reads versions.yaml with go-tools' package: a part's pins
		// as one literal of names (internal/build), and one entry by its name.
		Reads: append(slices.Clone(gate.DefaultReads),
			regexp.MustCompile(`strings\.Fields\("([a-z0-9 -]+)"\)`),
			regexp.MustCompile(`\.Get\("([a-z0-9-]+)"\)`)),
	}
	if err := g.Check(); err != nil {
		t.Error(err)
	}
}

// mkosi.conf's MinimumVersion is the version image/Dockerfile installs, not a floor with room
// above it: mkosi decides what ends up in the tree. It cannot be handed in, so it is held to the
// pin here instead.
func TestMkosiConfIsThePinnedMkosi(t *testing.T) {
	v, err := versions.Load(filepath.Join(root, versions.File))
	if err != nil {
		t.Fatal(err)
	}
	e, err := v.Get("mkosi")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "image", "mkosi.conf"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^MinimumVersion=(.*)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("image/mkosi.conf has no MinimumVersion")
	}
	if string(m[1]) != e.Version {
		t.Errorf("image/mkosi.conf says MinimumVersion=%s, and %s pins mkosi %s", m[1], versions.File, e.Version)
	}
}

// The kernel's tarball is the one its pin was taken of. kernel/Dockerfile fetches a release
// candidate as the gzip tarball git.kernel.org makes of torvalds' tag and a release from
// cdn.kernel.org, and versions.yaml's source is what the release's SBOM, its SOURCES and the
// CodeQL tree fetch: a pin moved across the two with its source left behind is the hash of one
// file and the URL of another.
func TestTheKernelsSourceIsTheTarballTheBuildFetches(t *testing.T) {
	v, err := versions.Load(filepath.Join(root, versions.File))
	if err != nil {
		t.Fatal(err)
	}
	e, err := v.Get("kernel")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "kernel", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	want := "https://cdn.kernel.org/pub/linux/kernel/v{major}.x/linux-{version}.tar.xz"
	built := "https://cdn.kernel.org/pub/linux/kernel/v${KERNEL_MAJOR}.x/linux-${KERNEL_VERSION}.tar.xz"
	if regexp.MustCompile(`-rc[1-9][0-9]*$`).MatchString(e.Version) {
		want = "https://git.kernel.org/torvalds/t/linux-{version}.tar.gz"
		built = "https://git.kernel.org/torvalds/t/linux-${KERNEL_VERSION}.tar.gz"
	}
	if e.Source != want {
		t.Errorf("%s's kernel %s has source %s; kernel/Dockerfile fetches %s", versions.File, e.Version, e.Source, want)
	}
	if !strings.Contains(string(dockerfile), `url="`+built+`"`) {
		t.Errorf("kernel/Dockerfile no longer fetches %s for kernel %s", built, e.Version)
	}
}

// Every entry's track is one `spin-machine-dev outdated` reads (go-tools' versions/upstream):
// swtpm's `github-release stefanberger/swtpm` passed versions.Parse and read "not a GitHub
// repository" in outdated, so its pin drifted with nothing to say so (#780). Each kind's argument
// is held to the form upstream.newest gives it; a with track is Parse's to check.
func TestEveryTrackIsOneOutdatedReads(t *testing.T) {
	v, err := versions.Load(filepath.Join(root, versions.File))
	if err != nil {
		t.Fatal(err)
	}
	url := func(s string) bool { return strings.HasPrefix(s, "https://") }
	none := func(arg []string) bool { return len(arg) == 0 }
	reads := map[string]func(arg []string) bool{
		"github-release": func(arg []string) bool {
			return len(arg) >= 1 && len(arg) <= 2 && strings.HasPrefix(arg[0], "https://github.com/")
		},
		"tags":    func(arg []string) bool { return len(arg) == 1 && url(arg[0]) },
		"page":    func(arg []string) bool { return len(arg) == 1 && url(arg[0]) },
		"commit":  func(arg []string) bool { return len(arg) == 2 && url(arg[0]) },
		"follows": func(arg []string) bool { return len(arg) > 0 },
		"with":    func([]string) bool { return true },
		"branch":  none, "digest": none, "kernel": none, "today": none, "go": none, "pypi": none,
	}
	for _, e := range v.Entries {
		kind, arg, _ := strings.Cut(e.Track, " ")
		ok, known := reads[kind]
		if !known || !ok(strings.Fields(arg)) {
			t.Errorf("%s: track %q is not one outdated reads", e.Name, e.Track)
		}
	}
}
