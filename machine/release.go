// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A release is one directory tree, and this is the only description of its
// shape.
//
// It is here rather than in each consumer because the alternative was measured:
// the same four files had three layouts — the tarball's, the one a fetch script
// rearranged them into, and the one a program went looking for — with a
// translation step between each pair and a discovery function guessing which it
// had been handed. Every one of those was a place the answer could be wrong, and
// the way it went wrong was a path that existed and held the previous release's
// kernel. There is one layout now: whatever the tarball unpacks to, read as it
// lies.
const (
	qemuName    = "bin/qemu-system-x86_64"
	qemuTCGName = "bin/qemu-system-x86_64-tcg"
	qemuImgName = "bin/qemu-img"
	swtpmName   = "bin/swtpm"
	passtName   = "bin/passt"
	firmwareDir = "qemu"
	rootfsName  = "image/rootfs.qcow2"
	manifest    = "machine.env"

	// The kernel the machine boots, and the option ROM QEMU enters it with. A release whose
	// vmlinux randomizes itself (PVH) boots it through PVH, with no decompressor (#645); one
	// from v20261119.02 to #645 boots its bzImage, whose decompressor picks KASLR's base: the lab
	// and compat still open those (internal/lab), and a release from #660 on carries no bzImage. Until
	// 2026-11-19 the machine entered a vmlinux that could not move, through PVH, and _text was
	// ffffffff81000000 at every boot: such a release has neither, and is refused.
	vmlinuxName = "kernel/vmlinux"
	pvhROM      = "pvh.bin"
	bzImageName = "kernel/bzImage"
	bzImageROM  = "linuxboot_dma.bin"
)

// Release is an unpacked spin-machine release: the tree the tarball holds under
// usr/share/spin-stack.
//
// It answers where the parts of one machine are, and nothing else. It does not
// download, unpack, verify a signature or decide which version to use — a
// release arrives by whatever means its consumer already has, and by the time
// this opens one the question is only whether it is whole.
type Release struct {
	dir string
	env map[string]string
	// kernel and rom are the kernel this release boots and the option ROM QEMU enters it with.
	kernel, rom string
}

// OpenRelease reads the release tree at dir and reports what is missing, if anything.
//
// It checks rather than trusting, because a release with one file absent is
// worse than no release: it installs, and the gap surfaces later as a firmware that
// cannot enter the kernel for want of an option ROM, or a kernel that is the previous version. The
// cost of finding out here is five stats.
//
// The TCG binary and the root filesystem are not required. A host that only ever
// runs guests under KVM needs neither, and refusing to start for the want of a
// 900 MB file it will not open would be a check that costs more than it saves —
// Rootfs and Spec("tcg") report their own absence to whoever asks for them.
func OpenRelease(dir string) (*Release, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving release directory %s: %w", dir, err)
	}

	r := &Release{dir: abs, kernel: bzImageName, rom: bzImageROM}
	if PVH(filepath.Join(abs, vmlinuxName)) == nil {
		r.kernel, r.rom = vmlinuxName, pvhROM
	}
	for _, want := range []string{qemuName, qemuImgName, r.kernel, filepath.Join(firmwareDir, r.rom), filepath.Join(firmwareDir, DefaultBIOS)} {
		p := filepath.Join(abs, want)
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("%s is not a whole machine: %w", abs, err)
		}
	}

	r.env, err = readEnv(filepath.Join(abs, manifest))
	if err != nil {
		return nil, err
	}
	return r, nil
}

// QEMUImg creates and rebases the qcow2 files a guest's disks are.
func (r *Release) QEMUImg() string { return filepath.Join(r.dir, qemuImgName) }

// Rootfs is the read-only base image every workspace overlays.
//
// Never opened for writing: many VMs map this one file through a qcow2 backing
// chain, and a write to it invalidates every overlay in existence silently —
// they keep working until they read a cluster that moved. A new image is written
// beside it and renamed over it.
func (r *Release) Rootfs() (string, error) { return r.optional(rootfsName, "the base image") }

// SWTPM is the swtpm that emulates a machine's TPM (Spec.TPM). Not every release carries one, and
// OpenRelease does not ask for it: a release from before #719 has none, and is still a machine
// without a TPM.
func (r *Release) SWTPM() (string, error) { return r.optional(swtpmName, "swtpm, the TPM's emulator") }

// Passt is the passt that is the backend of a NIC on passt (NIC.Passt). Not every release carries
// one, and OpenRelease does not ask for it: a release from before #751 has none, and is still a
// machine whose NICs are TAPs.
func (r *Release) Passt() (string, error) {
	return r.optional(passtName, "passt, a NIC's network without root")
}

func (r *Release) optional(name, what string) (string, error) {
	p := filepath.Join(r.dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s is not in this release: %w", what, err)
	}
	return p, nil
}

// Version is the release's own version, as machine.env records it. Empty if the
// tree has no manifest.
//
// It names a build for a human reading a log. It is not what decides whether a
// checkpoint may be resumed — the machine's record is (CanResume) — so two hosts agreeing
// on this string is evidence and not proof.
func (r *Release) Version() string { return r.env["version"] }

// ManifestKernelSHA256 is experiment #742's: machine.env's kernel_sha256.
func (r *Release) ManifestKernelSHA256() string { return r.env["kernel_sha256"] }

// Spec returns the parts of a machine this release supplies for the accelerator accel ("" or
// "kvm", or "tcg"): the QEMU that runs under it, the kernel it boots, the firmware directory
// QEMU loads the BIOS and the kernel's option ROM from, and Accel. It is the one way to a Spec's
// binaries, so that a binary and the accelerator it was built for cannot be paired wrong.
//
// Under "kvm" the QEMU is the ordinary build, which has no TCG compiled in. Under "tcg" it is
// the emulating build, for a machine with no /dev/kvm: a CI runner, a laptop outside the kvm
// group. It is a different binary and not a flag because a host serving tenants should not be
// able to fall back to software emulation by accident: that failure presents as a VM ~17 times
// slower (lab 44848049631, 2026-11-21), not as one that did not start. A release need not carry
// it (OpenRelease), so "tcg" from a release without it is an error naming the file.
//
// Any other accel is left for Spec.Args to refuse. The caller fills in everything that is about
// one VM rather than about the machine - memory, CPU count, disks, vsock, console - and an
// initrd, which a release does not carry because what runs as PID 1 is the caller's business.
func (r *Release) Spec(accel string) (Spec, error) {
	qemu := filepath.Join(r.dir, qemuName)
	if accel == "tcg" {
		var err error
		if qemu, err = r.optional(qemuTCGName, "the TCG build of QEMU"); err != nil {
			return Spec{}, err
		}
	}
	return Spec{
		QEMU:     qemu,
		Kernel:   filepath.Join(r.dir, r.kernel),
		Firmware: filepath.Join(r.dir, firmwareDir),
		Accel:    accel,
	}, nil
}

// readEnv parses machine.env: key=value lines, # comments, no quoting. A tree
// without one is not an error — a developer's build directory is a release for
// every purpose except being named. A line that is none of those is: spin-machine-dev release
// writes nothing else, so it is a manifest damaged on the way here, and reading
// past it would report the version of a file nobody wrote.
func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading the release manifest: %w", err)
	}
	defer func() { _ = f.Close() }()

	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%s:%d: %q is not key=value", path, n, line)
		}
		env[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return env, nil
}
