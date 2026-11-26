// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bytes"
	"cmp"
	"crypto/x509"
	"debug/elf"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spin-stack/machine/internal/modsign"
	"github.com/spin-stack/machine/machine"
)

// QEMUBinaries are what the QEMU build ships. qemu-img is not a convenience: a container's
// writable layer is a qcow2 overlay made by running it, so a release with the emulator and
// without it gives a container nowhere to write.
var QEMUBinaries = []string{"qemu-system-x86_64", "qemu-system-x86_64-tcg", "qemu-img"}

// SWTPMBinary is the TPM's emulator the QEMU build ships beside it: machine.Start runs one beside
// each QEMU whose machine has a TPM.
const SWTPMBinary = "swtpm"

// PasstBinary is the network of a NIC on passt, which the QEMU build ships beside it: machine.Start
// runs one beside each QEMU whose machine has one.
const PasstBinary = "passt"

// Firmware is what every consumer of QEMU needs beside it. pvh.bin is the option ROM QEMU adds
// when -kernel is an ELF kernel, the vmlinux the machine boots, and linuxboot_dma.bin the one it
// adds for a bzImage, which a release from before #645 boots; efi-virtio.rom is the option ROM of
// the one NIC this build keeps, and a VM given a virtio-net-pci without it does not start; qboot.bin is the BIOS the machine boots with
// (machine.DefaultBIOS), built beside QEMU from qemu/qboot/.
var Firmware = []string{"bios.bin", "bios-256k.bin", "linuxboot_dma.bin", "pvh.bin", "kvmvapic.bin", "efi-virtio.rom", "qboot.bin"}

// qbootSize is the ROM window qboot is linked for.
const qbootSize = 64 << 10

// E2fsprogsBinaries are the e2fsprogs tools the image is made and read with.
var E2fsprogsBinaries = []string{"mkfs.ext4", "e2fsck", "dumpe2fs", "debugfs"}

// VerifyQEMU asserts the QEMU tree carries what every consumer needs, that it runs here, and that
// only the CI binary can emulate. Backwards, the last is a fleet running ~17 times slower or
// a lane that cannot boot at all; it is not visible in a file listing.
func VerifyQEMU(o Options) error {
	for _, b := range QEMUBinaries {
		if err := executable(filepath.Join(o.Out, "bin", b)); err != nil {
			return err
		}
	}
	for _, f := range Firmware {
		if _, err := os.Stat(filepath.Join(o.Out, "qemu", f)); err != nil {
			return fmt.Errorf("missing firmware %s: %w", f, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(o.Out, "qemu", "qboot.bin")); fi.Size() != qbootSize {
		return fmt.Errorf("qboot.bin is %d bytes, not the %d of the ROM window it is linked for", fi.Size(), qbootSize)
	}
	// Run, not only present: a binary that cannot start on the host that extracted it is the
	// failure static linking exists to remove, and it costs one exec to notice.
	for _, b := range QEMUBinaries {
		if _, err := output(filepath.Join(o.Out, "bin", b), "--version"); err != nil {
			return fmt.Errorf("%s does not run on this host: %w", b, err)
		}
	}
	for _, c := range []struct {
		binary string
		tcg    bool
	}{{"qemu-system-x86_64", false}, {"qemu-system-x86_64-tcg", true}} {
		accels, err := output(filepath.Join(o.Out, "bin", c.binary), "-accel", "help")
		if err != nil {
			return fmt.Errorf("asking %s for its accelerators: %w", c.binary, err)
		}
		if has := hasLine(accels, "tcg"); has != c.tcg {
			if has {
				return fmt.Errorf("%s has TCG compiled in: a host with no /dev/kvm would emulate", c.binary)
			}
			return fmt.Errorf("%s cannot emulate, so a runner without /dev/kvm has nothing to boot with", c.binary)
		}
	}
	if err := verifyTPM(o); err != nil {
		return err
	}
	if err := verifyPasst(o); err != nil {
		return err
	}
	// The versioned q35 every checkpoint is saved on, offered and not deprecated by both binaries.
	// A newer one is said, and is not a failure: moving to it is a decision, not a bump's side effect.
	for _, b := range QEMUBinaries[:2] {
		notice, err := MachineType(filepath.Join(o.Out, "bin", b))
		if err != nil {
			return err
		}
		if notice != "" {
			say(o.Stderr, "%s", notice)
		}
	}
	return nil
}

// verifyPasst asks passt for what machine.Start hands it: a connected socket by --fd. A passt that
// took the socket only by a path at -s would start and wait for a client that never comes.
func verifyPasst(o Options) error {
	passt := filepath.Join(o.Out, "bin", PasstBinary)
	if err := executable(passt); err != nil {
		return err
	}
	help, err := output(passt, "--help")
	if err != nil {
		return fmt.Errorf("asking passt for its options: %w", err)
	}
	if !strings.Contains(help, "--fd FD") {
		return fmt.Errorf("passt takes no --fd, which a NIC on passt is handed its socket by: %s", help)
	}
	return nil
}

// verifyTPM asks both halves of a machine's TPM what they can do: QEMU for its device and the
// emulator backend, and swtpm for a TPM 2.0 that makes the RSA-3072 primaries shirudo's init makes.
// A swtpm built without one of libtpms's algorithms still builds and starts, and the guest's
// first TPM2_CreatePrimary is what fails.
func verifyTPM(o Options) error {
	qemu := filepath.Join(o.Out, "bin", QEMUBinaries[0])
	devices, err := output(qemu, "-device", "help")
	if err != nil {
		return fmt.Errorf("asking %s for its devices: %w", QEMUBinaries[0], err)
	}
	if !strings.Contains(devices, `name "`+machine.TPMDevice+`"`) {
		return fmt.Errorf("%s has no %s: a machine with a TPM does not start", QEMUBinaries[0], machine.TPMDevice)
	}
	backends, err := output(qemu, "-tpmdev", "help")
	if err != nil {
		return fmt.Errorf("asking %s for its TPM backends: %w", QEMUBinaries[0], err)
	}
	if !slices.Contains(strings.Fields(backends), "emulator") {
		return fmt.Errorf("%s has no emulator TPM backend, which swtpm is reached through: %s", QEMUBinaries[0], backends)
	}
	swtpm := filepath.Join(o.Out, "bin", SWTPMBinary)
	if err := executable(swtpm); err != nil {
		return err
	}
	caps, err := output(swtpm, "socket", "--tpm2", "--print-capabilities")
	if err != nil {
		return fmt.Errorf("asking swtpm what its TPM can do: %w", err)
	}
	for _, c := range []string{"tpm-2.0", "rsa-keysize-3072"} {
		if !strings.Contains(caps, `"`+c+`"`) {
			return fmt.Errorf("swtpm does not offer %s: %s", c, caps)
		}
	}
	return nil
}

// VerifyKernel asserts the kernel is the vmlinux the machine enters through PVH, which spin also
// reads symbols from (spin-stack/spin's hack/machine/place.go), able to put itself at a random
// base (SelfRandomizing), that its modules came out beside it, and that it trusts the module
// certificate the tree holds (TrustsModuleCertificate). A release carries no bzImage since #660.
func VerifyKernel(o Options) error {
	vmlinux := filepath.Join(o.Out, "kernel", "vmlinux")
	if err := SelfRandomizing(vmlinux); err != nil {
		return err
	}
	if _, err := modsign.Modules(filepath.Join(o.Out, modulesDir)); err != nil {
		return err
	}
	return TrustsModuleCertificate(o.Root, vmlinux)
}

// ModuleCertificate is where the tree keeps the certificate of the key the modules are signed with,
// PEM: spin-stack/ci's KMS key's public half, which `spin-machine-dev certificate` makes once and
// the kernel builds in as its one trusted key (kernel/Dockerfile, SYSTEM_TRUSTED_KEYS).
const ModuleCertificate = "kernel/module-signing.crt"

// ReadModuleCertificate is root's module certificate.
func ReadModuleCertificate(root string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(filepath.Join(root, ModuleCertificate))
	if err != nil {
		return nil, fmt.Errorf("reading the module certificate: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s is not a PEM certificate", ModuleCertificate)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ModuleCertificate, err)
	}
	return cert, nil
}

// TrustsModuleCertificate asserts the vmlinux at path carries root's module certificate, whose DER
// the kernel keeps whole in its builtin list: a kernel built without it loads no module the release
// signs, and boots all the same. A tree with no certificate yet has nothing to look for (#746).
func TrustsModuleCertificate(root, path string) error {
	cert, err := ReadModuleCertificate(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	vmlinux, err := os.ReadFile(path) //nolint:gosec // the kernel this build made
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if !bytes.Contains(vmlinux, cert.Raw) {
		return fmt.Errorf("%s does not carry %s: it would load no module the release signs", path, ModuleCertificate)
	}
	return nil
}

// SelfRandomizing is whether the vmlinux at path can be put at a random base with no
// decompressor (#645): the Xen notes QEMU enters it and places it at a random physical base by
// (machine.PVH, qemu/patches/0008), and a filled relocation table in .data.reloc, which it moves
// its own virtual base with (kernel/patches/0014-0017). The table's last word is a relocation
// the relocs tool wrote; an unfilled table ends in the 0 it was reserved with, and the kernel
// stays where it was linked, booting all the same. boot:kaslr asks the kernel that boots.
func SelfRandomizing(path string) error {
	if err := machine.PVH(path); err != nil {
		return err
	}
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("reading %s as an ELF: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	sec := f.Section(".data.reloc")
	if sec == nil || sec.Size < 4 {
		return fmt.Errorf("%s has no .data.reloc: the kernel cannot move its virtual base", path)
	}
	last := make([]byte, 4)
	if _, err := sec.ReadAt(last, int64(sec.Size)-4); err != nil {
		return fmt.Errorf("reading %s's .data.reloc: %w", path, err)
	}
	if f.ByteOrder.Uint32(last) == 0 {
		return fmt.Errorf("%s's .data.reloc is empty: the relocs tool did not fill it, and the kernel would run where it was linked", path)
	}
	return nil
}

// VerifyE2fsprogs asserts the tools are there and are the pinned version: the version the binary
// answers, not the one the Dockerfile says, because a stale _output from an earlier pin passes
// every existence check.
func VerifyE2fsprogs(o Options) error {
	for _, b := range E2fsprogsBinaries {
		if err := executable(filepath.Join(o.Out, "bin", b)); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(o.Out, "e2fsprogs", "mke2fs.conf")); err != nil {
		return fmt.Errorf("missing mke2fs.conf: %w", err)
	}
	e, err := o.Pins.Get("e2fsprogs")
	if err != nil {
		return err
	}
	said, _ := output(filepath.Join(o.Out, "bin", "mkfs.ext4"), "-V") // mke2fs -V exits 0 or 1 by version; what it says is the answer
	if !strings.Contains(said, "mke2fs "+e.Version) {
		first, _, _ := strings.Cut(said, "\n")
		return fmt.Errorf("mkfs.ext4 is not e2fsprogs %s: %s", e.Version, first)
	}
	return nil
}

// verifyDebs asserts that part's package build (makeDebs) left packages for the image to install.
func verifyDebs(part string) func(Options) error {
	return func(o Options) error {
		debs, _ := filepath.Glob(filepath.Join(o.Out, part, "*.deb"))
		if len(debs) == 0 {
			return errors.New("no " + part + " packages in " + filepath.Join(o.Out, part))
		}
		return nil
	}
}

// verifyImage asserts the image and what describes it are there; image/build.sh asserts what is
// inside it, in the container that made it.
func verifyImage(o Options) error {
	for _, f := range []string{"image/rootfs.qcow2", "packages.txt", "licenses.txt"} {
		if _, err := os.Stat(filepath.Join(o.Out, f)); err != nil {
			return fmt.Errorf("missing %s: %w", f, err)
		}
	}
	return nil
}

// executable is whether path is a file this user can run.
func executable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("missing %s: %w", filepath.Base(path), err)
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

// output is what name prints, both streams, as text.
func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec // a binary this build made
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

// hasLine is whether text has a line that is exactly line.
func hasLine(text, line string) bool {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).MatchString(text)
}

// TraceableConfig holds the traceable kernel's config, at traceable, to the release kernel's, at
// release: lockdown forced to none where the release forces integrity, and every other option
// as the release has it. A ledger is read on that kernel as if it were the release's, and an
// option that moved with the swap would make it another kernel. traceable is the .config the
// kernel was compiled with, which kernel/Dockerfile keeps after olddefconfig; release is the
// series' committed file, which that Dockerfile refuses to build a release kernel from unless
// olddefconfig leaves it as it is.
func TraceableConfig(release, traceable string) error {
	r, err := kernelConfig(release)
	if err != nil {
		return err
	}
	t, err := kernelConfig(traceable)
	if err != nil {
		return err
	}
	swapped := map[string]struct{ release, traceable string }{
		"CONFIG_LOCK_DOWN_KERNEL_FORCE_INTEGRITY": {"y", "n"},
		"CONFIG_LOCK_DOWN_KERNEL_FORCE_NONE":      {"n", "y"},
	}
	keys := slices.Collect(maps.Keys(r))
	for k := range t {
		if _, ok := r[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var wrong []string
	for _, k := range keys {
		rv, tv := cmp.Or(r[k], "n"), cmp.Or(t[k], "n")
		if s, ok := swapped[k]; ok {
			if rv != s.release || tv != s.traceable {
				wrong = append(wrong, fmt.Sprintf("%s is %s in the release kernel and %s in the traceable one, not %s and %s", k, rv, tv, s.release, s.traceable))
			}
			continue
		}
		if rv != tv {
			wrong = append(wrong, fmt.Sprintf("%s is %s in the release kernel and %s in the traceable one", k, rv, tv))
		}
	}
	if len(wrong) > 0 {
		return fmt.Errorf("%s is not %s with lockdown forced to none: %s", traceable, release, strings.Join(wrong, "; "))
	}
	return nil
}

// kernelConfig is the options a kernel .config sets, each to its value, and to n where it says
// one is not set.
func kernelConfig(path string) (map[string]string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the config a kernel this build made was compiled with
	if err != nil {
		return nil, fmt.Errorf("reading the kernel config %s: %w", path, err)
	}
	options := map[string]string{}
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if k, ok := strings.CutSuffix(strings.TrimPrefix(line, "# "), " is not set"); ok && strings.HasPrefix(k, "CONFIG_") {
			options[k] = "n"
		} else if k, v, ok := strings.Cut(line, "="); ok {
			options[k] = v
		}
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("the kernel config %s sets no option", path)
	}
	return options, nil
}
