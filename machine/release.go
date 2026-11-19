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
	firmwareDir = "qemu"
	rootfsName  = "image/rootfs.qcow2"
	manifest    = "machine.env"

	// kernelName is the kernel the machine boots: a bzImage, entered through its setup code
	// and the decompressor that picks KASLR's random base, with linuxboot_dma.bin the option
	// ROM QEMU adds for it. Until 2026-11-19 the machine entered vmlinux through its PVH notes,
	// which skips that decompressor: _text was ffffffff81000000 at every boot.
	kernelName = "kernel/bzImage"
	kernelROM  = "linuxboot_dma.bin"

	// elfKernelName is the same kernel as the ELF it was linked as, and pvh.bin the ROM QEMU
	// enters it through. A release from before the bzImage carries only this, and a tree
	// without a bzImage boots it: boot:compat boots the newest release with this tree's
	// spin-machine, so this is read until a release carries a bzImage, and can go after.
	elfKernelName = "kernel/vmlinux"
	elfKernelROM  = "pvh.bin"
)

// Release is an unpacked spin-machine release: the tree the tarball holds under
// usr/share/spin-stack.
//
// It answers where the parts of one machine are, and nothing else. It does not
// download, unpack, verify a signature or decide which version to use — a
// release arrives by whatever means its consumer already has, and by the time
// this opens one the question is only whether it is whole.
type Release struct {
	dir    string
	kernel string
	env    map[string]string
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
// Rootfs and QEMUTCG report their own absence to whoever asks for them.
func OpenRelease(dir string) (*Release, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving release directory %s: %w", dir, err)
	}

	r := &Release{dir: abs, kernel: kernelName}
	rom := kernelROM
	if _, err := os.Stat(filepath.Join(abs, kernelName)); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(abs, elfKernelName)); err == nil {
			r.kernel, rom = elfKernelName, elfKernelROM
		}
	}
	for _, want := range []string{qemuName, qemuImgName, r.kernel, filepath.Join(firmwareDir, rom), filepath.Join(firmwareDir, DefaultBIOS)} {
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

// QEMU is the emulator every guest runs under: KVM only, no TCG.
func (r *Release) QEMU() string { return filepath.Join(r.dir, qemuName) }

// QEMUImg creates and rebases the qcow2 files a guest's disks are.
func (r *Release) QEMUImg() string { return filepath.Join(r.dir, qemuImgName) }

// Firmware is the directory QEMU loads option ROMs and BIOS blobs from, which is
// what Spec.Firmware wants: the BIOS, and the option ROM QEMU adds for the kernel it is
// given (linuxboot_dma.bin for a bzImage, pvh.bin for a vmlinux).
func (r *Release) Firmware() string { return filepath.Join(r.dir, firmwareDir) }

// Kernel is the guest kernel the machine boots: the bzImage, or the vmlinux of a release
// from before it.
func (r *Release) Kernel() string { return filepath.Join(r.dir, r.kernel) }

// QEMUTCG is the emulating build, for a machine with no /dev/kvm — a CI runner,
// a laptop outside the kvm group. It is a different binary and not a flag
// because a host serving tenants should not be able to fall back to software
// emulation by accident: that failure presents as a VM that is fifty times
// slower, not as one that did not start.
//
// It is not part of a machine's identity and must never run one that a KVM guest
// will later restore from.
func (r *Release) QEMUTCG() (string, error) { return r.optional(qemuTCGName, "the TCG build of QEMU") }

// Rootfs is the read-only base image every workspace overlays.
//
// Never opened for writing: many VMs map this one file through a qcow2 backing
// chain, and a write to it invalidates every overlay in existence silently —
// they keep working until they read a cluster that moved. A new image is written
// beside it and renamed over it.
func (r *Release) Rootfs() (string, error) { return r.optional(rootfsName, "the base image") }

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
// checkpoint may be resumed — Fingerprint, the machine's contract, is — so two hosts agreeing
// on this string is evidence and not proof.
func (r *Release) Version() string { return r.env["version"] }

// Spec returns the parts of a machine this release supplies: the three paths
// that come out of the tarball. The caller fills in everything that is about one
// VM rather than about the machine — memory, CPU count, disks, vsock, console —
// and an initrd, which a release does not carry because what runs as PID 1 is
// the caller's business.
func (r *Release) Spec() Spec {
	return Spec{
		QEMU:     r.QEMU(),
		Kernel:   r.Kernel(),
		Firmware: r.Firmware(),
	}
}

// readEnv parses machine.env: key=value lines, # comments, no quoting. A tree
// without one is not an error — a developer's build directory is a release for
// every purpose except being named. A line that is none of those is: hack/release
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
