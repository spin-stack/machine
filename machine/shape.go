// SPDX-License-Identifier: Apache-2.0

package machine

import "fmt"

// cpuArg is -cpu: the model, the options that make it mean what it says, and the features taken
// away from it.
func (s Spec) cpuArg() string {
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
	// CPUID leaf 2 with QEMU's own cache descriptors, where QEMU's default answers 0xFF ("see
	// leaf 4"). Every process glibc starts asks for its cache sizes 12 times, and each 0xFF sends
	// it round leaf 4's subleaves, ~32 CPUID exits a process. On the bench (nested), spin's shape,
	// v20261122.06: CPUID exits from boot to the first login 3727 → 2176, first login −10.3
	// [−14.2, −4.0], 40 boots a side, restored login within noise (labs 45083473855, 45083473790,
	// 45083754801, 2026-11-22, #565).
	//
	// x-legacy-cpuid2-cache=off (qemu/patches/0006) makes leaf 2 describe the caches leaf 4 does.
	// Under host legacy-cache is on, and x-consistent-cache=off alone describes
	// legacy_intel_cpuid2_cache_info in leaf 2: the guest's glibc read a 2 MiB 8-way L2 where
	// leaf 4 and the guest's kernel say 4 MiB 16-way (lab 45100482399, 2026-11-23, #586).
	// legacy-cache=off, which would take both from the model, is refused under host, and
	// host-cache-info=on passes the host's leaf 2 through, 0xFF again. Both are x- properties, so a
	// QEMU that renames one fails qemu_accepts_test, not a boot.
	cpu += ",x-consistent-cache=off,x-legacy-cpuid2-cache=off"
	for _, f := range s.CPUWithout {
		cpu += ",-" + f
	}
	return cpu
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
// "-machine accel=" options are incompatible" (11.1.1). One form for every machine is one string,
// whose options a machine's record reads back (Target.Hardware).
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
// refusing — measured against 11.1.1 — so the string is one and not two.
const machineOpts = Type + ",kernel-irqchip=on,hpet=off,acpi=on,sata=off,smbus=off"

// Type is the versioned q35 this machine is, and never the bare "q35", which is an
// alias of whichever pc-q35-X.Y the QEMU running it is newest at.
//
// The version is what QEMU's migration promise is made of: a newer QEMU loads the state an
// older one saved when both run the same pc-q35-X.Y with the same devices, because the newer
// type's changes to defaults (hw_compat_X_Y in hw/core/machine.c, pc_compat_X_Y in
// hw/i386/pc.c) are switched back off for the older one. Under the alias a QEMU bump to a new
// minor is a new machine; under the version it is a new emulator of the same one, and a
// checkpoint resumes across it (CanResume). Moving this is moving every guest to new
// hardware: a decision, made here, that every checkpoint taken before it stops resuming.
// `spin-machine-dev verify qemu` says when the QEMU built offers a newer one, and fails once this one is
// deprecated (QEMU deprecates a versioned type after three years and removes it after six).
const Type = "pc-q35-11.1"

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
