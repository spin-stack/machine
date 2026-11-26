// SPDX-License-Identifier: Apache-2.0

// Package release packs the machine into one tarball with one version - QEMU, its firmware, the
// kernel, the base image and the tools that make and read its filesystem - and says what a
// release is: its version, its manifest, its sources, its SBOM and what changed since the one
// before it.
//
// One tarball, not because it is tidy: the parts are checked together (boot:compat, boot:record),
// and shipped separately they could drift apart and leave the last one to notice.
package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"github.com/spin-stack/go-tools/sbom"
	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/build"
)

// Name is the release's name, which its tarball and directory carry.
func Name(version string) string { return "spin-machine-" + version + "-linux-" + build.Arch }

// TraceableVmlinux is the name the lab's kernel of release version is published under beside the
// release: its vmlinux built from the same source and config with lockdown forced to none, so the
// ledger can read a scheduler trace (kernel/README.md), and entered through PVH as the release's is.
// Never in the tarball nor anything spin boots, and named lab-only for whoever reads the release's
// files.
func TraceableVmlinux(version string) string { return Name(version) + ".lab-only-traceable.vmlinux" }

// TraceableBzImage is the name the traceable kernel was published under until #697: a bzImage,
// which releases up to v20261123.07 boot, and the lab still fetches for a ledger of one of them.
func TraceableBzImage(version string) string { return Name(version) + ".lab-only-traceable.bzImage" }

// Options is what a release is packed from.
type Options struct {
	// Root is the repository: its LICENSE, NOTICE and patches ship.
	Root string
	// Out is the build's output, absolute; the release is packed into Out/release.
	Out     string
	Version string
	Pins    *versions.Versions
	// Commit is the commit the release was built from, and Epoch its time: the SBOM's clock, so
	// a release built twice from one commit carries the same bytes.
	Commit string
	Epoch  time.Time
	// Now is when the release was packed, for the manifest.
	Now    time.Time
	Stderr io.Writer
}

// Packed is what Pack left.
type Packed struct {
	Tarball  string            `json:"tarball"`
	Manifest map[string]string `json:"manifest"`
}

// file is one thing the release ships: from src, in the tree at dest, with mode.
type file struct {
	src, dest, what string
	mode            os.FileMode
}

// Pack copies every artefact into the release tree - every one required, because a release
// missing a piece is worse than no release: it installs, and the thing it left out fails somewhere
// else, a VM that will not start for want of an option ROM, a container with nowhere to write for
// want of qemu-img - and writes its manifest, SOURCES and SBOM, then the tarball and its sha256.
func Pack(ctx context.Context, o Options) (Packed, error) {
	name := Name(o.Version)
	rel := filepath.Join(o.Out, "release")
	share := filepath.Join(rel, name, "usr", "share", "spin-stack")
	if err := os.RemoveAll(rel); err != nil {
		return Packed{}, fmt.Errorf("emptying %s: %w", rel, err)
	}
	files, err := shipped(o, filepath.Join(rel, name), share)
	if err != nil {
		return Packed{}, err
	}
	for _, f := range files {
		if err := install(f); err != nil {
			return Packed{}, err
		}
	}
	say(o.Stderr, "packed %d files into %s", len(files), name)

	manifest, err := Manifest(o, share)
	if err != nil {
		return Packed{}, err
	}
	if err := os.WriteFile(filepath.Join(share, "machine.env"), []byte(FormatEnv(manifest)), 0o644); err != nil { //nolint:gosec // shipped, read by everyone
		return Packed{}, fmt.Errorf("writing the manifest: %w", err)
	}
	sources, err := Sources(o.Version, o.Pins)
	if err != nil {
		return Packed{}, err
	}
	if err := os.WriteFile(filepath.Join(share, "SOURCES"), []byte(sources), 0o644); err != nil { //nolint:gosec // shipped, read by everyone
		return Packed{}, fmt.Errorf("writing SOURCES: %w", err)
	}
	machineBOM, err := BOM(o, share)
	if err != nil {
		return Packed{}, err
	}
	if err := os.MkdirAll(filepath.Join(share, "sbom"), 0o755); err != nil {
		return Packed{}, fmt.Errorf("making the SBOM's directory: %w", err)
	}
	if err := sbom.Write(filepath.Join(share, "sbom", "machine.cdx.json"), machineBOM); err != nil {
		return Packed{}, err
	}

	tarball := filepath.Join(rel, name+".tar.gz")
	tar := exec.CommandContext(ctx, "tar", "-czf", tarball, "-C", rel, name)
	if out, err := tar.CombinedOutput(); err != nil {
		return Packed{}, fmt.Errorf("packing %s: %w: %s", tarball, err, out)
	}
	sum, err := build.FileSHA256(tarball)
	if err != nil {
		return Packed{}, err
	}
	if err := os.WriteFile(tarball+".sha256", []byte(sum+"  "+name+".tar.gz\n"), 0o644); err != nil { //nolint:gosec // published
		return Packed{}, fmt.Errorf("writing %s.sha256: %w", tarball, err)
	}
	// And beside it, of the tarball by its hash: what the release's attestation is made of.
	subject, err := hashed(name+".tar.gz", o.Version, tarball)
	if err != nil {
		return Packed{}, err
	}

	tarBOM, err := sbom.Merge(subject, o.Epoch, machineBOM)
	if err != nil {
		return Packed{}, err
	}
	if err := sbom.Check(tarBOM); err != nil {
		return Packed{}, fmt.Errorf("the tarball's SBOM: %w", err)
	}
	if err := sbom.Write(filepath.Join(rel, name+".cdx.json"), tarBOM); err != nil {
		return Packed{}, err
	}
	// The manifest again, beside the tarball rather than inside it, so it is published as an
	// asset of its own and read without the gigabyte around it.
	if err := os.WriteFile(filepath.Join(rel, "machine.env"), []byte(FormatEnv(manifest)), 0o644); err != nil { //nolint:gosec // published
		return Packed{}, fmt.Errorf("writing the manifest beside the tarball: %w", err)
	}
	return Packed{Tarball: tarball, Manifest: manifest}, nil
}

// shipped is every file the release carries, each one required.
func shipped(o Options, top, share string) ([]file, error) {
	at := func(dir string) string { return filepath.Join(share, dir) }
	fromOut := func(p string) string { return filepath.Join(o.Out, p) }
	// The licence travels with the artefacts: a tarball of GPL binaries and an Ubuntu userland
	// handed over without the terms is the one packaging mistake the recipient cannot recover from.
	files := []file{
		{filepath.Join(o.Root, "LICENSE"), filepath.Join(top, "LICENSE"), "the licence", 0o644},
		{filepath.Join(o.Root, "NOTICE"), filepath.Join(top, "NOTICE"), "the notice", 0o644},
	}
	for _, b := range build.QEMUBinaries {
		files = append(files, file{fromOut("bin/" + b), at("bin/" + b), "the " + b + " binary", 0o755})
	}
	files = append(files, file{fromOut("bin/" + build.SWTPMBinary), at("bin/" + build.SWTPMBinary), "the swtpm binary", 0o755})
	// mkfs.ext4 and the configuration it reads together: what an ext4 is made with is what both
	// default to. debugfs, e2fsck and dumpe2fs from the same build: the probes edit a copy of the
	// base image with this debugfs, because an older one refuses features it does not know.
	for _, b := range build.E2fsprogsBinaries {
		files = append(files, file{fromOut("bin/" + b), at("bin/" + b), "the " + b + " binary", 0o755})
	}
	files = append(files, file{fromOut("e2fsprogs/mke2fs.conf"), at("e2fsprogs/mke2fs.conf"), "mke2fs.conf", 0o644})
	for _, f := range build.Firmware {
		files = append(files, file{fromOut("qemu/" + f), at("qemu/" + f), "firmware " + f, 0o644})
	}
	// Every patch in each directory, never a list of them: mtrr.patch was added to the build and
	// not to a list here, and v20261001.01 shipped a qboot.bin without the change it was built with.
	// A changed GPL source is distributed with its changes.
	for _, d := range []struct{ glob, prefix, what string }{
		{filepath.Join(o.Root, "qemu/qboot/*.patch"), "qemu/qboot-", "qboot's patch"},
		{filepath.Join(o.Root, "qemu/patches/*.patch"), "qemu/patches/", "QEMU patch"},
		{fromOut("kernel/patches/*.patch"), "kernel/patches/", "kernel patch"},
		{filepath.Join(o.Root, "image/openssh/*.patch"), "openssh/", "OpenSSH patch"},
		{filepath.Join(o.Root, "image/systemd/*.patch"), "systemd/", "systemd patch"},
	} {
		found, _ := filepath.Glob(d.glob)
		if len(found) == 0 {
			return nil, fmt.Errorf("missing every %s: nothing matches %s", d.what, d.glob)
		}
		slices.Sort(found)
		for _, p := range found {
			files = append(files, file{p, at(d.prefix + filepath.Base(p)), d.what + " " + filepath.Base(p), 0o644})
		}
	}
	files = append(files,
		file{filepath.Join(o.Root, "qemu/qboot/COPYING"), at("qemu/qboot-COPYING"), "qboot's licence", 0o644},
		// vmlinux is what the machine boots, through PVH, the kernel moving itself to a random
		// base (#645), and what a reader of the kernel's symbols opens. No bzImage since #660.
		file{fromOut("kernel/vmlinux"), at("kernel/vmlinux"), "the guest kernel", 0o644},
		file{fromOut("kernel/kernel-config"), at("kernel/kernel-config"), "the kernel config", 0o644},
		// What the kernel compiled, one source or header a line: what `cves` judges a release's
		// kernel against the kernel CNA's CVEs with.
		file{fromOut("kernel/sources"), at("kernel/sources"), "the kernel's sources list", 0o644},
		file{fromOut("image/rootfs.qcow2"), at("image/rootfs.qcow2"), "the base image", 0o444},
		file{fromOut("packages.txt"), at("packages.txt"), "the image's package list", 0o644},
		file{fromOut("licenses.txt"), at("licenses.txt"), "the image's licence texts", 0o644},
	)
	return files, nil
}

// install copies f, refusing one that is not there.
func install(f file) error {
	in, err := os.Open(f.src)
	if err != nil {
		return fmt.Errorf("missing %s: %w (spin-machine-dev build makes it)", f.what, err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(f.dest), 0o755); err != nil {
		return fmt.Errorf("packing %s: %w", f.what, err)
	}
	out, err := os.OpenFile(f.dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.mode) //nolint:gosec // the release tree
	if err != nil {
		return fmt.Errorf("packing %s: %w", f.what, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("packing %s: %w", f.what, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("packing %s: %w", f.what, err)
	}
	return os.Chmod(f.dest, f.mode)
}

// ManifestKeys are machine.env's keys, in the order it is written.
var ManifestKeys = []string{"version", "source_commit", "qemu_version", "kernel_version", "e2fsprogs_version", "built",
	"qemu_sha256", "kernel_sha256", "bios_sha256", "image_sha256"}

// Manifest is what the release is, in a form something other than a human reads: the files'
// checksums say what it is made of, and decide nothing about checkpoints, which the machine's
// record does (machine.CanResume). source_commit says which tree it came out of, which the
// version alone does not: a manifest that cannot be read without the repository it came from is
// half a manifest.
func Manifest(o Options, share string) (map[string]string, error) {
	m := map[string]string{
		"version":       o.Version,
		"source_commit": o.Commit,
		"built":         o.Now.UTC().Format(time.RFC3339),
	}
	for _, p := range []string{"qemu", "kernel", "e2fsprogs"} {
		e, err := o.Pins.Get(p)
		if err != nil {
			return nil, err
		}
		m[p+"_version"] = e.Version
	}
	for k, path := range map[string]string{
		"qemu_sha256":   "bin/qemu-system-x86_64",
		"kernel_sha256": "kernel/vmlinux",
		"bios_sha256":   "qemu/qboot.bin",
		"image_sha256":  "image/rootfs.qcow2",
	} {
		sum, err := build.FileSHA256(filepath.Join(share, path))
		if err != nil {
			return nil, err
		}
		m[k] = sum
	}
	return m, nil
}

// FormatEnv is a manifest as machine.env is written: key=value lines, in ManifestKeys' order.
func FormatEnv(m map[string]string) string {
	var b strings.Builder
	for _, k := range ManifestKeys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	return b.String()
}

// ParseEnv reads a machine.env: key=value lines, the first of a key winning.
func ParseEnv(text string) map[string]string {
	m := map[string]string{}
	for line := range strings.Lines(text) {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\n"), "=")
		if _, seen := m[k]; ok && !seen {
			m[k] = v
		}
	}
	return m
}

// BOM is what the release is made of, as a scanner reads it: CycloneDX, one component per thing
// shipped and the version it was built from. Read from what the builds recorded - the apk
// databases the static binaries were linked in, the image's dpkg database, versions.yaml's pins -
// never from a list kept here. A component with no version fails it: a scanner cannot match it.
func BOM(o Options, share string) (*cdx.BOM, error) {
	version := func(name string) string { e, _ := o.Pins.Get(name); return e.Version }
	var parts []*cdx.BOM
	// The packages each static binary links, by the -static or -dev package that carries the .a,
	// with musl's libc and gcc's libgcc: the rest of each database built them and is formulation.
	for _, a := range []struct{ name, db, version, only string }{
		{"qemu-libraries", "qemu.apk", version("qemu"), "glib-static,pcre2-static,libffi-dev,pixman-static,zlib-static,libseccomp-static,libcap-ng-static,liburing-dev,libaio-dev,gettext-static,openssl-libs-static,musl-dev,gcc"},
		{"e2fsprogs-libraries", "e2fsprogs.apk", version("e2fsprogs"), "libarchive-static,acl-static,zlib-static,xz-static,zstd-static,bzip2-static,lz4-static,expat-static,openssl-libs-static,musl-dev,gcc"},
	} {
		raw, err := os.ReadFile(filepath.Join(o.Out, "sbom", a.db))
		if err != nil {
			return nil, fmt.Errorf("missing the apk database %s (spin-machine-dev build makes it): %w", a.db, err)
		}
		shippedPkgs, built, err := sbom.Apk(raw, "alpine", strings.Split(a.only, ","))
		if err != nil {
			return nil, err
		}
		parts = append(parts, sbom.New(subject(a.name, a.version), shippedPkgs, built, o.Epoch))
	}
	raw, err := os.ReadFile(filepath.Join(share, "packages.txt"))
	if err != nil {
		return nil, fmt.Errorf("reading the image's package list: %w", err)
	}
	image, err := sbom.Dpkg(raw, "ubuntu")
	if err != nil {
		return nil, err
	}
	if err := fromTheirRepositories(o.Pins, image); err != nil {
		return nil, err
	}
	parts = append(parts, sbom.New(subject("base-image", o.Version), image, nil, o.Epoch))
	// QEMU, qboot, the kernel, systemd and OpenSSH are built with this repository's patches: each is in its
	// component's pedigree, so the SBOM does not say upstream's source is what was compiled.
	pins, err := sbom.Pins(o.Pins, strings.Fields("qemu qboot swtpm libtpms json-glib e2fsprogs kernel ubuntu systemd-source openssh-source task git-lfs buildkit")...)
	if err != nil {
		return nil, err
	}
	patches := map[string]string{
		"qemu":           filepath.Join(o.Root, "qemu/patches"),
		"qboot":          filepath.Join(o.Root, "qemu/qboot"),
		"kernel":         filepath.Join(o.Out, "kernel/patches"),
		"openssh-source": filepath.Join(o.Root, "image/openssh"),
		"systemd-source": filepath.Join(o.Root, "image/systemd"),
	}
	for i, c := range pins {
		dir, ok := patches[c.Name]
		if !ok {
			continue
		}
		found, _ := filepath.Glob(filepath.Join(dir, "*.patch"))
		if len(found) == 0 {
			return nil, fmt.Errorf("%s has no *.patch in %s", c.Name, dir)
		}
		slices.Sort(found)
		if pins[i], err = sbom.Patched(c, found...); err != nil {
			return nil, err
		}
	}
	parts = append(parts, sbom.New(subject("sources", o.Version), pins, nil, o.Epoch))
	// The files themselves, by their hashes: the firmware is the QEMU tarball's own build of it.
	var each []struct{ path, version string }
	for _, b := range build.QEMUBinaries {
		each = append(each, struct{ path, version string }{"bin/" + b, version("qemu")})
	}
	for _, f := range build.Firmware {
		if f != "qboot.bin" {
			each = append(each, struct{ path, version string }{"qemu/" + f, version("qemu")})
		}
	}
	for _, b := range build.E2fsprogsBinaries {
		each = append(each, struct{ path, version string }{"bin/" + b, version("e2fsprogs")})
	}
	each = append(each, struct{ path, version string }{"bin/" + build.SWTPMBinary, version("swtpm")})
	qboot, err := o.Pins.Get("qboot")
	if err != nil {
		return nil, err
	}
	each = append(each,
		struct{ path, version string }{"qemu/qboot.bin", qboot.Pin},
		struct{ path, version string }{"kernel/vmlinux", version("kernel")},
		struct{ path, version string }{"image/rootfs.qcow2", o.Version},
	)
	for _, f := range each {
		c, err := sbom.File(filepath.Base(f.path), f.version, filepath.Join(share, f.path))
		if err != nil {
			return nil, err
		}
		parts = append(parts, sbom.New(c, nil, nil, o.Epoch))
	}
	// The machine, as the manifest describes it.
	machine, err := hashed("spin-machine", o.Version, filepath.Join(share, "machine.env"))
	if err != nil {
		return nil, err
	}

	b, err := sbom.Merge(machine, o.Epoch, parts...)
	if err != nil {
		return nil, err
	}
	if err := sbom.Check(b); err != nil {
		return nil, fmt.Errorf("the release's SBOM: %w", err)
	}
	return b, nil
}

// debRepositories are the apt repositories versions.yaml pins a .deb from, each with the vendor a
// package URL names for it.
var debRepositories = map[string]string{"https://download.docker.com/linux/ubuntu": "docker"}

// fromTheirRepositories names each package versions.yaml pins as a .deb by the repository it was
// downloaded from rather than as Ubuntu's: Docker's six (#609) as pkg:deb/docker, with that
// repository's URL and the .deb's SHA-256. Named pkg:deb/ubuntu, a scanner looks for them in
// Ubuntu's advisories, where they are not, and misses Docker's (#621). A pinned .deb the image
// did not record, or recorded at another version, is an image that did not install what
// versions.yaml says it did.
func fromTheirRepositories(v *versions.Versions, image []cdx.Component) error {
	for _, e := range v.Entries {
		file := filepath.Base(e.URL())
		if e.Kind != versions.Download || !strings.HasSuffix(file, ".deb") {
			continue
		}
		repository, _, _ := strings.Cut(e.URL(), "/dists/")
		vendor, ok := debRepositories[repository]
		if !ok {
			return fmt.Errorf("%s: %s is from no apt repository the SBOM knows a vendor for", e.Name, e.URL())
		}
		pkg, _, _ := strings.Cut(file, "_")
		i := slices.IndexFunc(image, func(c cdx.Component) bool { return c.Name == pkg })
		if i < 0 {
			return fmt.Errorf("%s: versions.yaml pins %s, and the image's package list does not have it", e.Name, pkg)
		}
		c := &image[i]
		// dpkg's version is the pinned one between an epoch and Docker's -1~ubuntu suffix.
		rest, ok := strings.CutPrefix(c.Version[strings.Index(c.Version, ":")+1:], e.Version)
		if !ok || !(rest == "" || rest[0] == '-' || rest[0] == '~') {
			return fmt.Errorf("%s: versions.yaml pins %s %s, and the image has %s", e.Name, pkg, e.Version, c.Version)
		}
		_, arch, _ := strings.Cut(c.PackageURL, "?arch=")
		c.PackageURL = "pkg:deb/" + vendor + "/" + pkg + "@" + c.Version + "?arch=" + arch + "&repository_url=" + repository
		c.Hashes = &[]cdx.Hash{{Algorithm: cdx.HashAlgoSHA256, Value: e.Pin}}
		c.ExternalReferences = &[]cdx.ExternalReference{{Type: cdx.ERTypeDistribution, URL: e.URL()}}
	}
	return nil
}

// subject is a BOM's subject as go-tools' sbom command names one.
func subject(name, version string) cdx.Component {
	return cdx.Component{Type: cdx.ComponentTypeApplication, Name: name, Version: version,
		PackageURL: "pkg:generic/" + name + "@" + version}
}

// hashed is a merged BOM's subject: the generic component, with path's hashes - the file the BOM
// describes.
func hashed(name, version, path string) (cdx.Component, error) {
	f, err := sbom.File(name, version, path)
	if err != nil {
		return cdx.Component{}, err
	}
	s := subject(name, version)
	s.Hashes = f.Hashes
	return s, nil
}

func say(w io.Writer, format string, a ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format+"\n", a...)
	}
}
