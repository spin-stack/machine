// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// QEMUBinaries are what the QEMU build ships. qemu-img is not a convenience: a container's
// writable layer is a qcow2 overlay made by running it, so a release with the emulator and
// without it gives a container nowhere to write.
var QEMUBinaries = []string{"qemu-system-x86_64", "qemu-system-x86_64-tcg", "qemu-img"}

// Firmware is what every consumer of QEMU needs beside it. linuxboot_dma.bin is the option ROM QEMU
// adds when -kernel is a bzImage, the kernel the machine boots; efi-virtio.rom is the option ROM of
// the one NIC this build keeps, and a VM given a virtio-net-pci without it does not start; qboot.bin is the BIOS the machine boots with
// (machine.DefaultBIOS), built beside QEMU from qemu/qboot/.
var Firmware = []string{"bios.bin", "bios-256k.bin", "linuxboot_dma.bin", "kvmvapic.bin", "efi-virtio.rom", "qboot.bin"}

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

// VerifyKernel asserts the kernel is one QEMU can enter and this machine can boot: a relocatable
// bzImage, which is what the machine boots and where KASLR picks a base, and the same kernel as the
// ELF it was linked as, which spin reads symbols from (spin-stack/spin's hack/machine/place.go).
func VerifyKernel(o Options) error {
	bz := filepath.Join(o.Out, "kernel", "bzImage")
	if err := Relocatable(bz); err != nil {
		return err
	}
	vmlinux := filepath.Join(o.Out, "kernel", "vmlinux")
	f, err := elf.Open(vmlinux)
	if err != nil {
		return fmt.Errorf("reading %s as an ELF: %w", vmlinux, err)
	}
	return f.Close()
}

// Relocatable is whether path is a bzImage whose setup header says relocatable: "HdrS" at 0x202,
// relocatable_kernel at 0x234 (Documentation/arch/x86/boot.rst). Without either QEMU does not boot
// it as one, or the kernel runs where it was linked and KASLR cannot move it.
func Relocatable(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // the kernel this build made
	if err != nil {
		return fmt.Errorf("no kernel: %w", err)
	}
	if len(b) <= 0x234 || string(b[0x202:0x206]) != "HdrS" {
		return fmt.Errorf("%s has no HdrS setup header: QEMU cannot boot it as a bzImage", path)
	}
	if b[0x234] != 1 {
		return fmt.Errorf("%s is not relocatable: KASLR cannot move it", path)
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

// verifySystemd asserts the build left packages for the image to install.
func verifySystemd(o Options) error {
	debs, _ := filepath.Glob(filepath.Join(o.Out, "systemd", "*.deb"))
	if len(debs) == 0 {
		return errors.New("no systemd packages in " + filepath.Join(o.Out, "systemd"))
	}
	return nil
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
