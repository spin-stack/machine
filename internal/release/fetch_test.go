// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/build"
)

// published is a release v1 as GitHub would serve it, from a directory: its tarball, holding
// files, the tarball's sha256 (sum, or the tarball's own when sum is "") and a bundle beside it.
func published(t *testing.T, files map[string]string, sum string) string {
	t.Helper()
	tree := t.TempDir()
	for name, body := range files {
		put(t, filepath.Join(tree, Name("v1"), name), body, 0o644)
	}
	pub := t.TempDir()
	tarball := filepath.Join(pub, Name("v1")+".tar.gz")
	if out, err := exec.Command("tar", "-czf", tarball, "-C", tree, Name("v1")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if sum == "" {
		sum, _ = build.FileSHA256(tarball)
	}
	put(t, tarball+".sha256", sum+"  "+filepath.Base(tarball)+"\n", 0o644)
	put(t, tarball+".sigstore.json", "{}", 0o644)
	return pub
}

// github puts on the PATH a gh that serves release v1 from pub - each asset asked for that is
// there, and nothing else - and a go whose body is cosign's answer, and returns where the two
// record what they were asked.
func github(t *testing.T, pub, gh, cosign string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if gh == "" {
		gh = `d=""; while [ $# -gt 0 ]; do case "$1" in -D) d=$2;; -p) [ -f "` + pub + `/$2" ] && cp "` + pub + `/$2" "$d/";; esac; shift; done`
	}
	put(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"gh $*\" >> "+calls+"\n"+gh+"\n", 0o755)
	put(t, filepath.Join(bin, "go"), "#!/bin/sh\necho \"go $*\" >> "+calls+"\n"+cosign+"\n", 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAFetchedReleaseIsItsTarballOnceItsSumAndSignatureAreTheReleases(t *testing.T) {
	pub := published(t, map[string]string{"usr/share/spin-stack/machine.env": "version=v1\n"}, "")
	calls := github(t, pub, "", "")
	dir := t.TempDir()
	share, err := Fetch(context.Background(), pins(t), "v1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if share != filepath.Join(dir, Name("v1"), "usr", "share", "spin-stack") || read(t, filepath.Join(share, "machine.env")) != "version=v1\n" {
		t.Errorf("Fetch = %s", share)
	}
	cosign, _ := pins(t).Get("cosign")
	tarball := filepath.Join(dir, Name("v1")+".tar.gz")
	want := "gh release download v1 -R spin-stack/machine -D " + dir +
		" -p " + Name("v1") + ".tar.gz -p " + Name("v1") + ".tar.gz.sha256 -p " + Name("v1") + ".tar.gz.sigstore.json\n" +
		"go run " + cosign.Source + "@" + cosign.Version + " verify-blob --bundle " + tarball + ".sigstore.json" +
		" --certificate-identity-regexp " + Identity +
		" --certificate-oidc-issuer https://token.actions.githubusercontent.com " + tarball + "\n"
	if got := read(t, calls); got != want {
		t.Errorf("asked\n%s\nwant\n%s", got, want)
	}
}

// Nothing is unpacked that did not pass, and each refusal says what failed on which release.
func TestAFetchRefusesWhatIsNotTheRelease(t *testing.T) {
	share := map[string]string{"usr/share/spin-stack/machine.env": "version=v1\n"}
	for _, tc := range []struct {
		name, gh, cosign, says string
		pub                    func(t *testing.T) string
	}{
		{"gh fails", "echo no such release >&2; exit 1", "", "downloading " + Name("v1") + ".tar.gz, " + Name("v1") + ".tar.gz.sha256, " + Name("v1") + ".tar.gz.sigstore.json of release v1: exit status 1: no such release", func(t *testing.T) string { return published(t, share, "") }},
		{"no sha256 published", "", "", "reading release v1's published sha256", func(t *testing.T) string {
			p := published(t, share, "")
			_ = os.Remove(filepath.Join(p, Name("v1")+".tar.gz.sha256"))
			return p
		}},
		{"a sha256 that differs", "", "", "its published sha256 says \"0000\"", func(t *testing.T) string { return published(t, share, "0000") }},
		{"an empty sha256", "", "", "its published sha256 says \"\"", func(t *testing.T) string {
			p := published(t, share, "")
			put(t, filepath.Join(p, Name("v1")+".tar.gz.sha256"), "", 0o644)
			return p
		}},
		{"a signature not the release's", "", "echo no matching signatures; exit 1", "signature is not " + Identity + "'s: exit status 1: no matching signatures", func(t *testing.T) string { return published(t, share, "") }},
		{"no usr/share/spin-stack", "", "", "release v1's tarball has no usr/share/spin-stack", func(t *testing.T) string { return published(t, map[string]string{"README": "x"}, "") }},
		{"not a tarball", "", "", "unpacking", func(t *testing.T) string {
			p := t.TempDir()
			tarball := filepath.Join(p, Name("v1")+".tar.gz")
			put(t, tarball, "not gzip", 0o644)
			sum, _ := build.FileSHA256(tarball)
			put(t, tarball+".sha256", sum+"\n", 0o644)
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github(t, tc.pub(t), tc.gh, tc.cosign)
			dir := t.TempDir()
			if _, err := Fetch(context.Background(), pins(t), "v1", dir); err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("Fetch: %v, want an error saying %q", err, tc.says)
			}
			if _, err := os.Stat(filepath.Join(dir, Name("v1"))); err == nil && tc.name != "no usr/share/spin-stack" {
				t.Error("unpacked what did not pass")
			}
		})
	}
}

// A release is verified with the cosign versions.yaml pins, and with none there is nothing to
// verify it with.
func TestAFetchNeedsCosignsPin(t *testing.T) {
	github(t, published(t, map[string]string{"usr/share/spin-stack/machine.env": ""}, ""), "", "")
	none, err := versions.Parse([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), none, "v1", t.TempDir()); err == nil || !strings.Contains(err.Error(), "verifying "+Name("v1")+".tar.gz") {
		t.Errorf("Fetch with no cosign pinned: %v", err)
	}
}

func TestASignedFileIsTheOneAskedForOnceItsSignatureIsTheReleases(t *testing.T) {
	pub := t.TempDir()
	put(t, filepath.Join(pub, "r.cdx.json"), "{}", 0o644)
	put(t, filepath.Join(pub, "r.cdx.json.sigstore.json"), "{}", 0o644)
	calls := github(t, pub, "", "")
	dir := t.TempDir()
	path, err := Signed(context.Background(), pins(t), "v1", dir, "r.cdx.json")
	if err != nil || path != filepath.Join(dir, "r.cdx.json") {
		t.Fatalf("Signed = %s, %v", path, err)
	}
	if got := read(t, calls); !strings.HasPrefix(got, "gh release download v1 -R spin-stack/machine -D "+dir+" -p r.cdx.json -p r.cdx.json.sigstore.json\n") ||
		!strings.Contains(got, " verify-blob --bundle "+path+".sigstore.json ") || !strings.HasSuffix(got, " "+path+"\n") {
		t.Errorf("asked %s", got)
	}
	github(t, pub, "exit 1", "")
	if _, err := Signed(context.Background(), pins(t), "v1", dir, "r.cdx.json"); err == nil || !strings.Contains(err.Error(), "downloading r.cdx.json, r.cdx.json.sigstore.json of release v1") {
		t.Errorf("Signed with gh failing: %v", err)
	}
	github(t, pub, "", "exit 1")
	if _, err := Signed(context.Background(), pins(t), "v1", dir, "r.cdx.json"); err == nil || !strings.Contains(err.Error(), "r.cdx.json's signature is not") {
		t.Errorf("Signed with a signature not the release's: %v", err)
	}
}

// A release's kernel is its signed tarball's: the version its manifest names and the sources list
// it carries, in a dir emptied first. A release from before either is refused by what it lacks.
func TestAReleasesKernelIsItsSignedTarballs(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "stale"), "x", 0o644)
	github(t, published(t, map[string]string{
		"usr/share/spin-stack/machine.env":    "kernel_version=7.3.3\n",
		"usr/share/spin-stack/kernel/sources": "a.c\n",
	}, ""), "", "")
	version, sources, err := Kernel(context.Background(), pins(t), "v1", dir)
	if err != nil || version != "7.3.3" || read(t, sources) != "a.c\n" {
		t.Fatalf("Kernel = %q, %q, %v", version, sources, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); err == nil {
		t.Error("dir was not emptied first")
	}
	github(t, published(t, map[string]string{"usr/share/spin-stack/machine.env": "kernel_version=7.3.3\n"}, ""), "", "")
	if _, _, err := Kernel(context.Background(), pins(t), "v1", dir); err == nil || !strings.Contains(err.Error(), "v1 carries no kernel/sources") {
		t.Errorf("a release with no sources list: %v", err)
	}
	github(t, published(t, map[string]string{"usr/share/spin-stack/kernel/sources": "a.c\n"}, ""), "", "")
	if _, _, err := Kernel(context.Background(), pins(t), "v1", dir); err == nil || !strings.Contains(err.Error(), "reading v1's manifest") {
		t.Errorf("a release with no manifest: %v", err)
	}
	github(t, published(t, map[string]string{"usr/share/spin-stack/machine.env": "kernel_version=7.3.3\n"}, ""), "", "exit 1")
	if _, _, err := Kernel(context.Background(), pins(t), "v1", dir); err == nil {
		t.Error("a tarball not signed by the release: fetched")
	}
	if _, _, err := Kernel(context.Background(), pins(t), "v1", "/proc/version/x"); err == nil {
		t.Error("a dir that cannot be made: fetched")
	}
}
