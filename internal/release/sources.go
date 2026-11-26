// SPDX-License-Identifier: Apache-2.0

package release

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/versions"
)

// Sources is where every part of the release came from.
//
// The licence on this repository covers the recipes. What they build is other people's software -
// QEMU and Linux are GPL-2.0, the userland is thousands of files under a dozen licences - and
// shipping their binaries carries obligations a LICENSE in a source tree does not discharge. This
// answers them: what was built, from exactly which source, and how to get that source. Written
// from versions.yaml, the values the builds checked the tarballs against, and never from a list
// kept by hand; each URL is the pin's, never one written here, which once named a mirror the
// kernel was not on.
func Sources(version string, pins *versions.Versions) (string, error) {
	get := func(name string) (versions.Entry, error) { return pins.Get(name) }
	q, err := get("qemu")
	if err != nil {
		return "", err
	}
	k, err := get("kernel")
	if err != nil {
		return "", err
	}
	e, err := get("e2fsprogs")
	if err != nil {
		return "", err
	}
	b, err := get("qboot")
	if err != nil {
		return "", err
	}
	s, err := get("systemd-source")
	if err != nil {
		return "", err
	}
	o, err := get("openssh-source")
	if err != nil {
		return "", err
	}
	w, err := get("swtpm")
	if err != nil {
		return "", err
	}
	l, err := get("libtpms")
	if err != nil {
		return "", err
	}
	j, err := get("json-glib")
	if err != nil {
		return "", err
	}
	p, err := get("passt")
	if err != nil {
		return "", err
	}
	sh, err := get("shirudo")
	if err != nil {
		return "", err
	}
	ssl, err := get("openssl")
	if err != nil {
		return "", err
	}
	c, err := get("curl")
	if err != nil {
		return "", err
	}
	tss, err := get("tpm2-tss")
	if err != nil {
		return "", err
	}
	us, err := get("ubuntu-snapshot")
	if err != nil {
		return "", err
	}
	ll, err := get("llvm")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(sourcesText,
		version,
		q.Version, q.URL(), q.Pin,
		b.Pin, b.Pin, b.Archive,
		w.Version, w.URL(), w.Pin, l.Version, l.URL(), l.Pin, j.Version, j.URL(), j.Pin,
		p.Version, p.URL(), p.Pin,
		e.Version, e.URL(), e.Pin,
		k.Version, k.URL(), k.Pin,
		sh.Pin, sh.Source, sh.Pin, us.Version, ll.Version,
		ssl.Version, ssl.URL(), ssl.Pin, c.Version, c.URL(), c.Pin, tss.Version, tss.Source, tss.Pin,
		version,
		s.Version, s.Version, s.URL(), s.Pin,
		o.Version, o.Version, o.URL(), o.Pin,
	), nil
}

const sourcesText = `spin-machine %s
Sources for the third-party software in this release.

This release contains software under the GNU General Public License, the GNU Lesser
General Public License and other licences; see NOTICE for what is under which. The
complete corresponding source for the GPL and LGPL components is available as set out
below. The statically linked libraries in the QEMU, swtpm, passt, e2fsprogs and shirudo binaries are covered by the same offer,
which satisfies section 6 of the LGPL by making a relinkable build reproducible from source.

QEMU %s
  binaries: usr/share/spin-stack/bin/qemu-system-x86_64
            usr/share/spin-stack/bin/qemu-system-x86_64-tcg
            usr/share/spin-stack/bin/qemu-img
  firmware: usr/share/spin-stack/qemu/*, except patches/, qboot.bin and the qboot files below
  source:   %s
  sha256:   %s
  built by: qemu/Dockerfile, with usr/share/spin-stack/qemu/patches applied to that source
            (each says what it changes and why) and the configure flags in that file

qboot %s
  binary:   usr/share/spin-stack/qemu/qboot.bin
  source:   https://github.com/bonzini/qboot, commit %s; the sha256 of what
            ` + "`git archive`" + ` writes for it is %s
  licence:  GPL-2.0, usr/share/spin-stack/qemu/qboot-COPYING
  built by: qemu/Dockerfile's qboot stage, with usr/share/spin-stack/qemu/qboot-*.patch applied
            to that source in the order that file applies them, and its compiler flags

swtpm %s
  binary:   usr/share/spin-stack/bin/swtpm
  source:   %s
  sha256:   %s
  licence:  BSD-3-Clause
  built by: qemu/Dockerfile's swtpm stage, with usr/share/spin-stack/qemu/swtpm-*.patch applied
            to that source in name order, statically linked against libtpms and json-glib below and the Alpine libraries QEMU's builder installs (qemu/apk.list),
            whose sources are the Alpine package sources for that release

libtpms %s, linked into swtpm
  source:   %s
  sha256:   %s
  licence:  BSD-3-Clause
  built by: qemu/Dockerfile's swtpm stage, unpatched

json-glib %s, linked into swtpm
  source:   %s
  sha256:   %s
  licence:  LGPL-2.1-or-later
  built by: qemu/Dockerfile's swtpm stage, unpatched

passt %s
  binary:   usr/share/spin-stack/bin/passt
  source:   %s
  sha256:   %s
  licence:  GPL-2.0-or-later
  built by: qemu/Dockerfile's passt stage, unpatched, statically linked against the musl libc of
            QEMU's builder (qemu/apk.list), whose source is the Alpine package source for that
            release

e2fsprogs %s
  binaries: usr/share/spin-stack/bin/mkfs.ext4, debugfs, e2fsck, dumpe2fs
  config:   usr/share/spin-stack/e2fsprogs/mke2fs.conf, upstream's, from the same tarball
  source:   %s
  sha256:   %s
  built by: e2fsprogs/Dockerfile, unpatched, statically linked against the Alpine libraries
            named there; their sources are the Alpine package sources for that release

Linux %s
  binary:   usr/share/spin-stack/kernel/vmlinux
  source:   %s
  sha256:   %s
  built by: kernel/Dockerfile, with the patches shipped alongside the binary in
            usr/share/spin-stack/kernel/patches applied to that source (each says what
            it changes and why), and the configuration shipped as
            usr/share/spin-stack/kernel/kernel-config

shirudo %s
  binary:   usr/share/spin-stack/guest/shirudo, for a guest: the shirudo the kernel's hooks above
            (kernel/patches 0026-0031) come from, which an operator builds its initrd and runtime
            image around
  source:   %s, commit %s
  licence:  BSD-2-Clause; with what is linked into it, usr/share/spin-stack/shirudo-licenses.txt
  built by: shirudo/Dockerfile: shirudo's own static target build (make target) in its build
            environment, Ubuntu 26.04 with its archive as snapshot.ubuntu.com serves it at %s
            and LLVM's clang %s, its BPF objects compiled against the BTF of the kernel above,
            unpatched, statically linked against OpenSSL, curl and TPM2-TSS below and the Ubuntu
            static archives of glibc, libelf, zlib, zstd, LuaJIT and libgcc that
            sbom/machine.cdx.json names with their versions, whose sources are those Ubuntu
            source packages (apt-get source <package>=<version>)

OpenSSL %s, linked into shirudo
  source:   %s
  sha256:   %s
  licence:  Apache-2.0
  built by: shirudo's Makefile.target-deps, unpatched, static

curl %s, linked into shirudo
  source:   %s
  sha256:   %s
  licence:  curl
  built by: shirudo's Makefile.target-deps, unpatched, static, against the OpenSSL above

TPM2-TSS %s, linked into shirudo
  source:   %s, commit %s
  licence:  BSD-2-Clause
  built by: shirudo's Makefile.target-deps, unpatched, static, against the OpenSSL above

spin-machine %s
  binary:   usr/share/spin-stack/bin/spin-machine, the tool that boots this release by hand
  source:   this repository, at the commit machine.env's source_commit names
  licence:  Apache-2.0 (LICENSE), statically linked with Go's standard library and the Go
            modules usr/share/spin-stack/go-licenses.txt names, whose licence texts it carries
  built by: go build -trimpath -buildvcs=false with CGO_ENABLED=0, by the toolchain go.mod names

Ubuntu userland
  image:    usr/share/spin-stack/image/rootfs.qcow2
  contents: usr/share/spin-stack/packages.txt — every package and its exact version
  licences: usr/share/spin-stack/licenses.txt — the copyright and licence text of each
  source:   the Ubuntu archive. For any package listed there whose version does not end
            in +spinN:
                apt-get source <package>=<version>
  built by: image/mkosi.conf and the scripts beside it

systemd %s+spin1
  packages: those in usr/share/spin-stack/packages.txt at %s+spin1
            (systemd, systemd-tpm, systemd-sysv, udev, libsystemd0, libsystemd-shared,
            libudev1, libpam-systemd and the rest of that source's binary packages the image
            installs)
  source:   Ubuntu's source package: %s
            and the tarballs it names beside it, with usr/share/spin-stack/systemd/*.patch,
            when it holds any, applied to it after Ubuntu's own (each says what it changes
            and why)
  sha256:   %s (the .dsc, which holds the tarballs' checksums)
  built by: image/systemd/Dockerfile: Debian's stage1 build profile, SELinux disabled and
            systemd-executor, udevd, systemctl and journalctl linked statically against
            libsystemd-shared, with a changelog entry for the +spin1 version; systemd-tpm,
            which stage1 leaves out, and systemd-analyze from a second build of the same source
            with TPM2, OpenSSL and the boot loader enabled (image/systemd/tpm2.mk)

OpenSSH 1:%s+spin1
  packages: those in usr/share/spin-stack/packages.txt at 1:%s+spin1
            (openssh-server, openssh-client, openssh-sftp-server)
  source:   Ubuntu's source package: %s
            and the tarballs it names beside it, with usr/share/spin-stack/openssh/*.patch
            applied to it after Ubuntu's own (each says what it changes and why)
  sha256:   %s (the .dsc, which holds the tarballs' checksums)
  built by: image/openssh/Dockerfile: Ubuntu's build, with a changelog entry for the +spin1
            version

Written offer
  For three years from the date of this release, the copyright holders will provide, to
  anyone who asks, the complete corresponding machine-readable source for the GPL and
  LGPL software in it, on a medium customarily used for software interchange, for no more
  than the cost of performing the distribution. Requests go to the issue tracker of the
  repository this release was built from.
`

// calver is a version as releases are named: vYYYYMMDD.NN, which sorts as a string - .02 after
// .01, and both before the next day's .01 - everywhere.
var calver = regexp.MustCompile(`^v[0-9]{8}\.[0-9]{2}$`)

// Version is the version a release is: given, when the caller named one, or the next of today's,
// counted from the tags today already has. It says where it came from.
func Version(given string, tags []string, now time.Time) (version, origin string, err error) {
	if given != "" {
		version, origin = given, "given by the caller"
	} else {
		day := now.UTC().Format("20060102")
		taken := 0
		for _, t := range tags {
			if strings.HasPrefix(t, "v"+day+".") {
				taken++
			}
		}
		version = fmt.Sprintf("v%s.%02d", day, taken+1)
		origin = fmt.Sprintf("generated for today (%d already taken)", taken)
	}
	if !calver.MatchString(version) {
		return "", "", fmt.Errorf("%q is not vYYYYMMDD.NN", version)
	}
	return version, origin, nil
}
