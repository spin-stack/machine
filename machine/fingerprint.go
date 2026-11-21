// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Shape is the five arguments that decide what machine a guest sees: the chipset
// and its options, the accelerator, the CPU model, the vCPU count with its hotplug
// ceiling, and the memory size with its ceiling.
//
// It is one type with one constructor because it has two consumers that must
// never disagree: the command line QEMU is given, and the fingerprint that
// decides which checkpoints this machine may resume. If the fingerprint stopped
// describing the command line, a VM would resume a checkpoint of another machine
// and the failure would be silent. Args passes each field as it is; the only thing
// it adds is where /dev/kvm came from (KVMFDSet), which is the host's business and
// not the guest's.
type Shape struct {
	Machine string
	Accel   string
	CPU     string
	SMP     string
	Memory  string
}

// Shape returns the machine's shape, which is the one Args runs and Fingerprint hashes.
func (s Spec) Shape() Shape {
	accel := s.accel()
	cpu := s.CPU
	if cpu == "" {
		// "host" is the silicon underneath, which TCG does not have: it refuses the model
		// outright. "max" is the same idea for an emulator — every feature this QEMU can
		// emulate — and is derived the same way, so migratable=on applies to it too.
		cpu = "host"
		if accel != "kvm" {
			cpu = "max"
		}
	}
	// migratable=on drops features QEMU cannot save and reload, which is what a
	// restore does. It is not what makes a CPU portable between machines — a
	// common and expensive misreading. It filters by "can this feature be
	// migrated at all", not by "does the other host have it"; under model "host"
	// the other host is still required to be this one.
	//
	// And it exists only on the models that derive their features from the
	// silicon underneath, which is host and max. A named model has a fixed
	// feature set with nothing to filter, and asking anyway is not ignored:
	//
	//   can't apply global Skylake-Server-v4-x86_64-cpu.migratable=on:
	//   Property 'Skylake-Server-v4-x86_64-cpu.migratable' not found
	//
	// at start-up, in whichever lane first names a model.
	if derivedFromHost(cpu) {
		cpu += ",migratable=on"
	} else {
		// enforce=on, and without it naming a model does not mean what it says.
		//
		// QEMU's default is to warn about features the host cannot provide and
		// start anyway, having quietly removed them — so the same model name
		// yields a different guest CPU on different machines, which is the exact
		// thing naming a model was supposed to prevent. Measured here, asking a
		// Raptor Lake host for Skylake-Server-v4: five warnings about missing
		// AVX-512 and exit 0. A VM saved on a host that had them and resumed on
		// one that did not would fault on an instruction it had already been told
		// it has, long after the warning scrolled past.
		//
		// With this, a host that cannot provide the model refuses to start it:
		// "Host doesn't support requested features", exit 1, before the VM exists.
		cpu += ",enforce=on"
	}
	// No hardware virtualisation in the guest. The kernel has neither KVM nor modules, so
	// nothing in it can use VMX or SVM, and exposing them only hands a guest's root the
	// host's nested-virtualisation code to reach. It also kept the CPUs from agreeing:
	// firmware locks IA32_FEATURE_CONTROL with VMX on for the CPUs present at boot, and a
	// hot-added one arrives with it unlocked, which this kernel locks with VMX off - so the
	// boot CPU had vmx and every added one did not, and the first addition printed
	// "WARNING: arch/x86/kernel/cpu/cpuid-deps.c:127 at do_clear_cpu_cap". Measured
	// 2026-09-24 under KVM, -smp 1,maxcpus=4, three cores added over QMP: that warning and
	// vmx=1,0,0,0 before; no warning and vmx=0 on all four with it. Global properties of the
	// model, so a CPU added later is given them too.
	cpu += ",-vmx,-svm"

	return Shape{
		Machine: machineOpts,
		Accel:   accel,
		CPU:     cpu,
		SMP:     smpArg(s.BootCPUs, s.MaxCPUs),
		Memory:  memoryArg(s.Memory.SizeMB, s.Memory.MaxMB),
	}
}

func (s Spec) accel() string {
	if s.Accel == "" {
		return "kvm"
	}
	return s.Accel
}

// machineOpts is the -machine string. It never carries accel=: the accelerator is always
// its own -accel, because that is the only form that can also carry the /dev/kvm
// descriptor (KVMFDSet), and QEMU refuses the two together — "The -accel and
// "-machine accel=" options are incompatible" (11.1.1). One form for every machine is
// one string for the fingerprint to hash and the command line to carry.
//
// hpet=off: the HPET is a timer the guest would enumerate, initialise and
// then not use, because a KVM guest reads the TSC and the KVM clock.
// kernel-irqchip=on keeps interrupt delivery in the kernel rather than
// bouncing every one through userspace. acpi=on is not optional: vmgenid
// reaches the guest through an ACPI table, vCPU hotplug through the DSDT's
// processor objects, and the kernel finds the PCIe config space (MCFG) and
// the interrupt routing (_PRT) there. PCI hotplug is not ACPI's (see the
// ICH9-LPC globals below), nor is memory growth, which is virtio-mem.
//
// sata=off and smbus=off remove the two ICH9 functions a q35 builds beside
// the LPC bridge and this machine has no use for: an AHCI controller at
// 00:1f.2, on a kernel built without CONFIG_ATA, and an SMBus controller at
// 00:1f.3 with eight SPD EEPROMs behind it, on a kernel with no i2c bus
// driver to reach them. Both were on the bus with no driver bound, and the
// guest paid for enumerating them, for their BARs and for their I/O windows
// (0x0700-0x073f and 0xc040-0xc05f, gone from the guest's /proc/ioports).
//
// Kernel to init, medians of 400 boots each, measured 2026-09-08 on the KVM
// binary: 62.3 ms as it was, 61.3 ms with sata=off, 61.7 ms with smbus=off,
// 60.6 ms with both. Nothing else moved — the guest's PCI list loses exactly
// those two functions, the remaining BARs shift down by the page the AHCI
// controller had, and a boot onto the real root filesystem still reaches its
// init.
//
// Four more machine options were measured the same way and are deliberately
// not here, because each bought nothing (same date, 300 boots per variant,
// medians against a 62.9 ms baseline whose run-to-run spread was ±2 ms):
//
//   - usb=off, 63.0 ms, is a no-op twice over. This q35 already reports
//     /machine usb false, and the binary is built from qemu/devices.mak with
//     no USB controller in it at all, so usb=on is not even startable here:
//     "unknown type 'ich9-usb-ehci1'".
//   - vmport=off, 62.6 ms. The VMware backdoor port is emulated but never
//     touched: nothing in the guest's /proc/ioports claims it, because a
//     Linux guest decides whether to talk to it from CPUID and this one
//     finds KVM.
//   - smm=off, 62.3 ms. The scepticism it deserves is what makes it a
//     no-change: this machine entered a PVH ELF kernel directly then (a bzImage
//     through qboot since 2026-11-19), has no pflash and no OVMF, and the 0.6 ms is inside the noise, so the option
//     would be carrying an interaction with firmware nobody here has for a
//     number that cannot be told from zero.
//   - i8042=off, 62.5 ms. It does remove the PS/2 controller, its two ports
//     and port92 — 92 bytes off the DSDT is the whole visible effect — and
//     the kernel has no CONFIG_SERIO_I8042 to probe any of it with.
//
// kernel-irqchip=on stays under either accelerator. It names KVM's in-kernel interrupt
// controller and TCG has none, but QEMU accepts the option and ignores it rather than
// refusing — measured against 11.1.1 — so the shape is one string and not two.
const machineOpts = MachineType + ",kernel-irqchip=on,hpet=off,acpi=on,sata=off,smbus=off"

// MachineType is the versioned q35 this machine is, and never the bare "q35", which is an
// alias of whichever pc-q35-X.Y the QEMU running it is newest at.
//
// The version is what QEMU's migration promise is made of: a newer QEMU loads the state an
// older one saved when both run the same pc-q35-X.Y with the same devices, because the newer
// type's changes to defaults (hw_compat_X_Y in hw/core/machine.c, pc_compat_X_Y in
// hw/i386/pc.c) are switched back off for the older one. Under the alias a QEMU bump to a new
// minor is a new machine; under the version it is a new emulator of the same one, and a
// checkpoint resumes across it (Fingerprint). Moving this is moving every guest to new
// hardware: a decision, made here, that every checkpoint taken before it stops resuming.
// `spin-machine-dev verify qemu` says when the QEMU built offers a newer one, and fails once this one is
// deprecated (QEMU deprecates a versioned type after three years and removes it after six).
const MachineType = "pc-q35-11.1"

// smpArg is -smp. A ceiling of 0, or one equal to the boot count, adds no maxcpus=: validate
// refuses a ceiling below the boot count, so above it is the only ceiling that adds slots.
func smpArg(bootCPUs, maxCPUs int) string {
	if maxCPUs > bootCPUs {
		return fmt.Sprintf("%d,maxcpus=%d", bootCPUs, maxCPUs)
	}
	return fmt.Sprintf("%d", bootCPUs)
}

// memoryArg is -m with no slots=: slots are ACPI DIMM sockets, and this
// machine plugs no DIMMs. maxmem on its own is the address space reserved for
// memory devices, which is what virtio-mem needs and all it needs — verified by
// starting a machine with maxmem and no slots.
func memoryArg(sizeMB, maxMB int) string {
	if maxMB > sizeMB {
		return fmt.Sprintf("%d,maxmem=%dM", sizeMB, maxMB)
	}
	return fmt.Sprintf("%d", sizeMB)
}

// DefaultBIOS is the firmware a machine runs when Spec.BIOS names none: qboot with
// WRITE_POINTER, built beside QEMU (qemu/qboot/). 7.14 ms faster than SeaBIOS to a guest's init
// on the lab runner (2026-09-29, 20 boots interleaved), with vmgenid's table published and a
// restored guest reseeded.
const DefaultBIOS = "qboot.bin"

func (s Spec) bios() string {
	if s.BIOS == "" {
		return DefaultBIOS
	}
	return s.BIOS
}

// Fingerprint is the machine's contract: what a checkpoint saved on it needs of the machine it
// resumes into. Two machines with one fingerprint present the same hardware to a guest, and a
// checkpoint saved on one resumes on the other. QEMU does not check it, so across two that
// differ a resume is undefined rather than an error, and CanResume refuses it.
//
// It hashes the versioned machine type and the shape (Shape), the chipset's globals and every
// device as the command line gives it with what is one VM's taken out (Contract), and the size of
// the BIOS. Not the host's CPU: under model host, which CPU a checkpoint needs is in the
// checkpoint (Saved.CPU) and checked against the machine it is resumed into (CanResume), which
// is exact where a model name was not (hostCPU's removal, docs/log.md, 2026-11-19). Not the
// QEMU binary, the kernel, the initrd or the option ROM that entered it (linuxboot_dma.bin):
// QEMU's promise is per versioned machine type, so a newer QEMU of the same type is
// the same machine; a resumed guest runs the kernel in its saved memory, and the
// kernel, the initrd and the option ROM that entered it are read again only by a boot.
// boot:compat holds the promise: a checkpoint the newest release saved resumes on this tree's.
//
// A change to any of what it does hash is a change of hardware under every guest, and every
// checkpoint taken against the previous machine stops resuming. That is the design, and the
// release notes say so (internal/release's verdict).
func (s Spec) Fingerprint() (string, error) {
	h := sha256.New()

	// Length-prefixed, so that no two different machines can produce the same
	// byte stream by moving a delimiter into a value. Every value here is
	// something a caller supplies or a tool prints — a CPU model name, a device
	// list, a line out of /proc/cpuinfo — and none of them is guaranteed to be
	// free of the separator. Prefixing costs nothing and removes the question.
	write := func(key, value string) {
		_, _ = fmt.Fprintf(h, "%s=%d:%s\n", key, len(value), value)
	}

	// The BIOS by its size. It is the one ROM a file sizes: QEMU maps it as the RAM block
	// pc.bios, which a saved machine carries whole and a resume refuses at another length. Its
	// contents travel with the checkpoint, so a rebuilt qboot of the same size is the same
	// machine. Every other read-only block is the machine type's or the chipset's own, and
	// boot:roms fails when one appears that is neither (boot/roms_test.go).
	size, err := fileSize(filepath.Join(s.Firmware, s.bios()))
	if err != nil {
		return "", fmt.Errorf("fingerprinting the BIOS: %w", err)
	}
	write("bios-size", strconv.FormatInt(size, 10)) // mutate-exempt: the base the size is written in; any base names each size once

	shape := s.Shape()
	write("machine", shape.Machine)
	write("accel", shape.Accel)
	write("cpu", shape.CPU)
	write("smp", shape.SMP)
	write("memory", shape.Memory)
	for _, c := range s.Contract() {
		write("contract", c)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Contract is the chipset's globals and every device of the machine, each as the command line
// gives it (appendChipset, appendDevices, appendNICs) with what is one VM's taken out, and
// whether it has a serial port.
//
// From the command line itself and not a description kept beside it, because a resume loads
// device state into a machine that has to have the same devices with the same properties: a
// balloon added at slot 0x04, free-page-reporting turned on, a NIC's MTU announced
// (VIRTIO_NET_F_MTU, negotiated at probe and carried in the stream) are each a machine a
// checkpoint of the other cannot be loaded into, and each moves this the moment Args changes.
//
// What is one VM's, and so taken out, is what a resume is known to be allowed to differ in:
// an id, the backend a device is bound to (netdev, memdev), a NIC's MAC, the vsock's context
// id, which the migration stream does not carry and the guest re-reads when QEMU resets the
// transport, and a descriptor (vhostfd). The backends themselves (-netdev, -drive, -object,
// -chardev) are files and descriptors on the host, never the guest's hardware; the memory
// backend's size is the shape's.
//
// The disks are not here at all: they are the VM's and not the machine's. A host computes its
// fingerprint before any VM exists to say how many disks it has, and a checkpoint is resumed
// with the disks it was saved with, which is the resuming caller's part of the contract.
func (s Spec) Contract() []string {
	args := appendChipset(nil)
	args = s.appendDevices(args)
	args = s.appendNICs(args)
	var out []string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-global":
			out = append(out, "global "+args[i+1])
			i++
		case "-device":
			out = append(out, "device "+withoutTheVMs(args[i+1]))
			i++
		}
	}
	if s.Serial != "" || s.SerialFDSet != 0 {
		out = append(out, "serial isa-serial")
	}
	return out
}

// vmProperties are the device properties that are one VM's and not its machine's (Contract).
var vmProperties = []string{"id", "netdev", "memdev", "mac", "guest-cid", "vhostfd"}

// withoutTheVMs is a -device value with vmProperties taken out, the rest in the order Args
// wrote them. The values Contract reads are Args' own, with no escaped comma among them: the
// one that may carry one, a disk's serial, belongs to a disk, which is not in the contract.
func withoutTheVMs(dev string) string {
	parts := strings.Split(dev, ",")
	kept := parts[:1]
	for _, p := range parts[1:] {
		if k, _, _ := strings.Cut(p, "="); !slices.Contains(vmProperties, k) {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ",")
}

// fingerprintName is how Args names the machine: this, then its fingerprint.
const fingerprintName = "spin-machine:"

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
