// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"github.com/spin-stack/machine/qmp"
)

// cpuFeature is what a CPU feature's name is made of in QEMU: "avx512f", "sse4.1", "kvm-pv-eoi".
// Spec.CPUWithout writes each into -cpu, where a comma or an '=' would be another option.
var cpuFeature = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// Fixed PCI slot assignments on the q35 root complex (pcie.0).
//
// Every device sits directly on bus 0 — there are no PCIe root ports — which is
// what lets the kernel command line carry pci=lastbus=0 and skip scanning buses
// 1 through 255. Pinning each device to a slot rather than letting QEMU assign
// one makes guest enumeration order deterministic: it stops depending on the
// order of calls made here, so reordering code cannot silently renumber devices
// inside the guest.
//
// The machine owns both ends of the bus: 0x00 is the host bridge and 0x1f the
// ICH9 function block, which here is the LPC bridge alone — the SATA and SMBus
// functions beside it are turned off in the machine string, see machineOpts. 0x01 is
// left free — the q35 convention puts VGA there, and this machine has no
// display adapter at all.
const (
	slotVsock   = 0x02
	slotRNG     = 0x03
	slotBalloon = 0x04

	slotDiskBase = 0x05
	slotDiskMax  = 0x0f

	slotNICBase = 0x10
	slotNICMax  = 0x19

	// The SCSI controller disks that arrive while the machine runs are added to.
	// 0x1b-0x1d are free.
	slotHotplug = 0x1a

	slotMem = 0x1e
)

// maxDisks and maxNICs bound the fixed slot ranges above. Exceeding either is a
// configuration error, caught before a command line is built rather than by the
// guest not finding a device.
const (
	maxDisks = slotDiskMax - slotDiskBase + 1
	maxNICs  = slotNICMax - slotNICBase + 1

	// maxHotplugDisks bounds Spec.HotplugDisks: the targets a virtio-scsi controller
	// addresses, 0 to 255.
	maxHotplugDisks = 256
)

const (
	hotplugController = "scsi0"
	hotplugBus        = hotplugController + ".0"
)

// virtioModern forces virtio 1.0 (modern-only) on a PCI virtio device.
//
// disable-legacy=on drops the legacy I/O BAR and the transitional device ID, so
// the guest skips the legacy probe path entirely. The kernel this machine boots
// is virtio 1.0 capable, so the transitional mode QEMU would otherwise negotiate
// buys nothing. Measured neutral for boot time; the win is a smaller device
// surface, not speed.
const virtioModern = "disable-legacy=on"

// qemuOpt escapes a value for a QEMU option string, which splits on a single comma
// and reads a doubled one as a literal. Every free-form value a caller supplies goes
// through it: a disk path "a,readonly=off" is otherwise a second option, not part of
// the path. A value picked from a fixed set — a disk's format, the
// accelerator, a CPU model name — is not escaped but refused by validate when it is not
// one of the set, because an escaped "qcow2,,x" is still not a format.
func qemuOpt(v string) string { return strings.ReplaceAll(v, ",", ",,") }

// diskFormats are the formats a Disk or an Image may name: the two this machine's images
// are. Anything else is either a typo QEMU reports as a missing driver or a driver
// nobody here has thought about the safety of.
var diskFormats = []string{"qcow2", "raw"}

// memGrowthID names the memory a virtio-mem device hands out. It is a separate
// region from pc.ram, the memory the guest boots with: this one is empty until
// somebody asks for it.
const memGrowthID = "mem.growth"

// virtioMemID is the virtio-mem device's id: QEMU.SetMemory grows and shrinks the VM through
// /machine/peripheral/<virtioMemID>, its property requested-size.
const virtioMemID = "vmem0"

// balloonID is the balloon's id, which puts it at /machine/peripheral/<balloonID>.
const balloonID = "balloon0"

// Place is where a disk the machine is started with goes: a virtio-blk device on its fixed
// slot, over a raw node whose file is an empty placeholder until a disk is bound to it (Boot,
// Resume). The guest finds the disk by Serial.
//
// The serial and whether the disk is read-only are the place's and not the disk's because the
// device takes both when it is realized: QEMU refuses to change a realized virtio-blk's serial
// ("after it was realized"), and a writable device refuses a read-only node under it. So a
// machine started ahead of the workspace it will be is started with the places that workspace
// will have, and every workspace has the same: spin's root and agent home.
//
// A place no disk is bound to is a disk of no size: the null-co placeholder, which a guest
// that read it would find empty rather than full of zeroes.
type Place struct {
	Serial   string
	Readonly bool
}

// FDSet is descriptors QEMU is handed open rather than a path to open: a path
// of the form /dev/fdset/<ID> names the set wherever QEMU takes a file name.
type FDSet struct {
	ID  int
	FDs []FD
}

// FD is one descriptor in the QEMU process, so 3 or above.
type FD struct {
	Num int
	// Opaque is what QEMU reports for the descriptor in query-fdsets, and the
	// only way back from /dev/fdset/<ID> to what the caller opened: the path, in
	// practice, so that a caller asking which image a device has open gets an
	// answer it can compare.
	Opaque string
}

// NIC is one virtio-net device, backed by a TAP file descriptor the caller has
// already opened and passed to the QEMU process.
type NIC struct {
	// TapFD is the descriptor number in the QEMU process, so 3 or above.
	TapFD int
	// VhostFD is /dev/vhost-net already open, handed to QEMU as a descriptor, for a
	// QEMU that may open no device node. Zero has QEMU open the node itself.
	VhostFD int
	MAC     string

	// MTU is the largest frame the guest may send, announced through
	// VIRTIO_NET_F_MTU. Zero leaves the guest at its own default, 1500.
	//
	// Announced rather than configured, and that is the point of doing it here:
	// virtnet_probe assigns it to both dev->mtu and dev->max_mtu, so the guest
	// comes up with it before its init runs and the kernel refuses any attempt to
	// raise it. A value delivered any other way — DHCP option 26, a command-line
	// parameter the guest parses — is advice that root inside the machine can
	// ignore, and the tenant is root.
	MTU int

	// hub puts the NIC on a hub port in place of a TAP: ReadTarget's, whose machine never runs.
	// The record leaves the backend out (perVM), so the NIC reads the same either way.
	hub bool
}

// Memory describes guest RAM.
type Memory struct {
	BaseMB int
	// CeilingMB, when larger than BaseMB, is the ceiling this VM's memory may grow to
	// and shrink back from while it runs, through virtio-mem. Both numbers are in
	// the machine's shape.
	//
	// Growth is virtio-mem and not ACPI DIMM hotplug, which this machine offers
	// no slots for. A DIMM can be added and, in practice, not removed: unplugging
	// one needs the guest to offline a whole memory block, and a single unmovable
	// page in it makes that fail. virtio-mem plugs and unplugs in small blocks
	// inside one device, so a VM that grew for a build can give the memory back
	// afterwards — which is the half that made this worth having.
	//
	// Nothing is plugged at start-up. QEMU.SetMemory plugs more when the caller
	// wants it.
	CeilingMB int
}

// Monitor is one QMP monitor: a Unix socket QEMU creates and listens on, or one the
// caller already made and listens on, handed over as a descriptor for a QEMU that may
// not create a socket where the caller would connect to it. Set one or the other.
type Monitor struct {
	Socket string
	FD     int
}

func (m Monitor) validate() error {
	switch {
	case (m.Socket == "") == (m.FD == 0):
		return errors.New("a monitor is either a socket path or a descriptor, and this is both or neither")
	case m.FD != 0 && m.FD < 3:
		return fmt.Errorf("a monitor descriptor of %d is stdin, stdout or stderr", m.FD)
	}
	return nil
}

// Spec is one virtual machine.
type Spec struct {
	QEMU   string
	Kernel string
	Initrd string

	// Firmware is the directory QEMU loads option ROMs and BIOS blobs from. It
	// is the `qemu/` directory of a release; without it the machine cannot find
	// the BIOS, nor the option ROM QEMU enters the kernel with: pvh.bin for an ELF
	// kernel, linuxboot_dma.bin for a bzImage.
	Firmware string

	// BIOS is the firmware the machine runs, a file in Firmware: DefaultBIOS when
	// empty. Its size is the pc.bios block a saved machine carries, which the
	// machine's record holds (Target.Hardware): a checkpoint resumes only where the
	// block is the same length.
	// SeaBIOS (bios-256k.bin) is beside it, for comparing the two.
	BIOS string

	BootCPUs int

	// MaxCPUs is the hotplug ceiling, and it is not free at rest. QEMU declares one ACPI
	// object per vCPU slot in the DSDT whether or not the slot is ever filled, the guest
	// walks all of them, and nr_cpu_ids rises so every subsystem that allocates per
	// *possible* CPU allocates for the ceiling. Measured 2026-09-10 with `--cpus 2`, two
	// boots each, from the kernel's own clock to `Run /sbin/init`:
	//
	//     no ceiling      63.0 ms        maxcpus=16      68.1 ms        maxcpus=64   72.1 ms
	//     initcalls       38.6 ms                        43.1 ms                     44.2 ms
	//
	// So a ceiling of 16 costs 5 ms of kernel and 64 costs 9 ms. Only part of that is the
	// namespace walk — function_graph at depth 1 on acpi_bus_scan puts 8.4 / 8.7 / 13.2 ms
	// there — and the rest is everything else sized by the ceiling. A virtio-mem memory
	// ceiling is cheaper because it is a different mechanism, one device rather than a slot
	// per block: `--max-memory 16384` costs 1.0 ms.
	//
	// Worth knowing rather than worth avoiding: this is paid by a boot, and a machine resumed
	// from a checkpoint pays none of it. It is the number to reach for when somebody proposes
	// a generous ceiling "since it costs nothing when unused" — it does cost something, and
	// here is what.
	MaxCPUs int

	Memory Memory

	// CPU is the model the guest is shown. Empty means "host", and that is a
	// decision about whether VMs may move between machines.
	//
	// "host" exposes this host's own feature set, which is the fastest thing and
	// the least portable: a guest resumed on another host has already been told
	// through CPUID which instructions exist, and it does not ask again. Restore
	// it somewhere without AVX-512 and it executes an instruction that is not
	// there. A checkpoint therefore records the CPU its guest was shown
	// (Saved.From.CPU), and resumes only where the same CPU is shown (CanResume).
	//
	// Naming a model instead — "Skylake-Server-v4", "EPYC-Rome-v3", whichever is
	// the oldest microarchitecture in the fleet — gives every host the same guest
	// CPU, so checkpoints cross hosts. The cost is that guests never see anything
	// newer than the model names, on any machine, and a slower first login: 27 ms
	// on the bench for the newest model it could run (lab 44855890737, 2026-11-21).
	// CPUWithout is the other way to the same end.
	//
	// The portable choice is a named microarchitecture, and not an x86-64-v2/v3/v4
	// baseline: those are psABI levels that compilers target, and QEMU 11.1.1
	// defines no CPU model by those names — checked in target/i386/cpu.c, which
	// has zero occurrences of the string. The generic models it does define are
	// qemu64 and kvm64, both far older than anything worth running. Adding v3 as
	// a model would mean carrying a QEMU patch, which would cost forward-porting
	// it forever and buy nothing over naming the oldest microarchitecture in the
	// fleet — which is what libvirt and every cluster manager do.
	CPU string

	// CPUWithout are features the guest is not shown although its model has them: under
	// host, the features some host of a pool lacks (Unshared), so that every host of the
	// pool shows the same CPU and a checkpoint of one resumes on any (spin-stack/spin#1417).
	// Args takes each away from -cpu; what the guest is shown is read off the vCPU
	// (QEMU.CPU), not from this list.
	CPUWithout []string

	// Accel is what executes the guest's instructions. Empty is "kvm".
	//
	// "tcg" is software emulation, for a machine with no /dev/kvm: a CI runner, a laptop
	// outside the kvm group. It reaches the first login ~17 times slower (lab 44848049631, 2026-11-21), which is
	// the whole reason it is stated rather than fallen back to — a host that quietly
	// emulated would present as a fleet that is inexplicably slow, not as one that failed.
	//
	// Release.Spec sets it together with QEMU, the TCG build under "tcg": the ordinary build
	// has no TCG compiled in and refuses -accel tcg outright, which is the good failure, and
	// why validate does not guess a binary's build from its name. And "host" is not a CPU model
	// TCG can present — QEMU refuses it with "CPU model 'host' requires KVM or HVF" — so
	// the default below is "max" here and "host" under KVM.
	//
	// Checkpoints do not cross the two: the accelerator is in the machine's record, and a
	// guest's state under TCG is not a guest's state under KVM.
	Accel string

	// Places are where the disks the machine is started with go, on fixed slots in this
	// order. The disks themselves arrive later, bound by serial (VM.Boot, VM.Resume).
	Places []Place
	NICs   []NIC

	// HotplugDisks, when not zero, gives the machine the controller disks attached while it
	// runs go to (VM.AttachDisk), at the first of its 256 targets free.
	//
	// A disk that arrives later cannot go where the ones on the command line go: those
	// slots are on the q35 root complex, and QEMU refuses device_add there - "Bus 'pcie.0'
	// does not support hotplugging". What takes one here is a virtio-scsi controller, on
	// the command line whenever this is not zero: a disk added to it is a SCSI event the
	// guest acts on at once.
	//
	// It replaced a PCIe root port per disk, which cost more at every step, measured
	// 2026-09-29 with four: ~10 ms of each boot per port (QEMU realizing it, the guest
	// enumerating and probing it) against ~10 ms for the controller; ~135 ms for the guest
	// to see a disk against 1.5; and ~6 s for it to let one go - pciehp waits out an
	// attention button's cancel window - against under 1.
	//
	// The count is the controller's max_target, which a checkpoint resumes across when it
	// grows (mayHaveMore): raising it strands no checkpoint, and lowering it strands every
	// one taken with more.
	HotplugDisks int

	// VsockCID, when non-zero, gives the machine a vhost-vsock device with that
	// context id. It is how anything inside the guest is reached: this machine
	// has no serial port for a caller to drive and no network it is required to
	// have.
	VsockCID int
	// VsockFD is /dev/vhost-vsock already open, handed to QEMU as a descriptor, for a
	// QEMU that may open no device node: one whose root has no /dev. Zero has QEMU open
	// the node itself.
	VsockFD int

	// KVMFDSet is /dev/kvm as a descriptor set, for the same QEMU. Only under KVM, which
	// is the accelerator that opens a device. The accelerator is still kvm: where its
	// descriptor came from is not the machine a checkpoint is loaded into.
	KVMFDSet int

	// UserfaultFDSet is /dev/userfaultfd as a descriptor set, which a fast load makes the
	// userfaultfd it serves the guest's faults with from (qemu/patches/0010, -userfaultfd). Only
	// for a restore. Zero leaves QEMU to open the device by path or ask the system call, both of
	// which a host that grants neither to QEMU's user refuses.
	UserfaultFDSet int

	// Monitors are the QMP monitors this machine listens on. At least one is needed to
	// do anything to a running machine, including shutting it down.
	//
	// More than one because a QMP socket serves one client: QEMU's socket chardev
	// accepts one connection and the next one waits, so two components cannot share a
	// path. Two monitors are two chardevs, and QEMU serves both at once — each with its
	// own greeting, its own capabilities handshake and its own command stream. The case
	// that asked for it is a machine whose lifecycle and whose disk are owned by
	// different things: whoever launched it holds the first monitor for as long as it
	// runs, and whatever owns the storage under it has to be able to seal a layer
	// without asking the launcher to relay commands it does not understand.
	Monitors []Monitor

	// FDSets are the descriptor sets the command line names: ConsoleFDSet, KVMFDSet,
	// UserfaultFDSet. A disk's images are handed over when it is bound, through the monitor.
	FDSets []FDSet

	// ConsoleFDSet is the console as a descriptor set QEMU writes to, instead of
	// Console, for a QEMU that may open nothing by path. It is opened append-only:
	// QEMU otherwise truncates what it opens, and the usual console descriptor is
	// a FIFO, which cannot be truncated — "-serial file:/dev/fdset/<ID>" fails
	// with EINVAL on one (measured, QEMU 11.1.1).
	ConsoleFDSet int

	// Console is a QEMU chardev spec for the console — "file:/path/console.log",
	// "mon:stdio", or empty for no console at all. The kernel prints to the ISA
	// 16550 the machine has; nothing else uses it.
	Console string

	// Cmdline is the kernel command line. A typed value rather than a rendered string,
	// so that what String always adds — pci=lastbus=0 and the rest — cannot be left
	// out by a caller who built the line some other way, and so that validate can see
	// what the line is made of.
	Cmdline Cmdline

	// Restore starts the machine to load a checkpoint (VM.Resume) rather than to boot (VM.Boot).
	//
	// Either way QEMU starts held and does nothing until it is told over a monitor: -S for a
	// boot, -incoming defer for a restore. One QEMU cannot wait for both: under -incoming defer,
	// cont only notes that the machine is to run once a migration has arrived, and leaves it in
	// inmigrate (QEMU 11.2.0-rc0, 2026-11-19). So a machine started ahead of its workspace is
	// started for one or the other.
	Restore bool

	// TPM, when its State is set, gives the machine a TPM 2.0 (TPM).
	TPM TPM
}

// accelArg is -accel: the accelerator, and under KVM the descriptor set /dev/kvm is in
// when the caller opened it (validate refuses one for any other accelerator).
func accelArg(accel string, kvmFDSet int) string {
	if kvmFDSet == 0 {
		return accel
	}
	return fmt.Sprintf("%s,device=/dev/fdset/%d", accel, kvmFDSet)
}

// validate reports what is wrong with a spec, before a command line is built
// from it. Every check here is something that otherwise fails as a guest that
// boots and finds the world subtly wrong.
func (s Spec) validate() error {
	switch {
	case s.QEMU == "":
		return errors.New("no QEMU binary")
	case s.Kernel == "":
		return errors.New("no kernel")
	case s.Firmware == "":
		// The BIOS and the option ROM QEMU adds for the kernel are found under this
		// directory. Without it the failure is a rom-open error that reads like
		// something else entirely.
		return errors.New("no firmware directory: QEMU has no BIOS and no option ROM to enter the kernel through")
	case s.Accel != "" && s.Accel != "kvm" && s.Accel != "tcg":
		return fmt.Errorf("accelerator %q: this machine runs under kvm or tcg", s.Accel)
	// A model name, and only that. The options after it are the machine's (cpuArg), and a
	// comma here would add options to -cpu under the name of a model.
	case strings.ContainsAny(s.CPU, ", \t\n"):
		return fmt.Errorf("CPU model %q is not a model name: its options are the machine's", s.CPU)
	case slices.ContainsFunc(s.CPUWithout, func(f string) bool { return !cpuFeature.MatchString(f) }):
		return fmt.Errorf("CPUWithout %q: each is a CPU feature's name, of a-z, 0-9, '.', '_' and '-'", s.CPUWithout)
	case s.BootCPUs < 1:
		return fmt.Errorf("BootCPUs is %d", s.BootCPUs)
	case s.MaxCPUs != 0 && s.MaxCPUs < s.BootCPUs:
		return fmt.Errorf("MaxCPUs %d is below BootCPUs %d", s.MaxCPUs, s.BootCPUs)
	// memoryArg only writes maxmem= when the ceiling is above the boot size, so a
	// ceiling below it would be a machine that cannot grow, handed without complaint to a
	// caller that asked for one that can. Equal is not an error: it is how a machine says
	// it has no room to grow.
	case s.Memory.CeilingMB != 0 && s.Memory.CeilingMB < s.Memory.BaseMB:
		return fmt.Errorf("Memory.CeilingMB %d is below Memory.BaseMB %d",
			s.Memory.CeilingMB, s.Memory.BaseMB)
	case s.Memory.BaseMB < 1:
		return fmt.Errorf("memory is %d MB", s.Memory.BaseMB)
	case len(s.Places) > maxDisks:
		return fmt.Errorf("%d disks, and the slot range holds %d", len(s.Places), maxDisks)
	// Held until a monitor says what to do: with none it never starts.
	case len(s.Monitors) == 0:
		return errors.New("no monitor: the machine starts held, and is booted or resumed over one")
	case len(s.NICs) > maxNICs:
		return fmt.Errorf("%d NICs, and the slot range holds %d", len(s.NICs), maxNICs)
	case s.HotplugDisks < 0 || s.HotplugDisks > maxHotplugDisks:
		return fmt.Errorf("%d disks arriving later, and the controller addresses %d",
			s.HotplugDisks, maxHotplugDisks)
	}
	if err := bootable(s.Kernel); err != nil {
		return err
	}
	if err := s.Cmdline.validate(); err != nil {
		return fmt.Errorf("kernel command line: %w", err)
	}
	for i, m := range s.Monitors {
		if err := m.validate(); err != nil {
			return fmt.Errorf("monitor %d: %w", i, err)
		}
	}
	sets, err := s.fdSets()
	if err != nil {
		return err
	}
	switch {
	case s.ConsoleFDSet != 0 && s.Console != "":
		return errors.New("a console given both as a chardev and as a descriptor set")
	case s.ConsoleFDSet != 0 && !sets[s.ConsoleFDSet]:
		return fmt.Errorf("the console is descriptor set %d, which Spec.FDSets does not have", s.ConsoleFDSet)
	case s.KVMFDSet != 0 && s.Accel != "" && s.Accel != "kvm":
		return fmt.Errorf("a /dev/kvm descriptor for -accel %s, which opens no device", s.Accel)
	case s.KVMFDSet != 0 && !sets[s.KVMFDSet]:
		return fmt.Errorf("/dev/kvm is descriptor set %d, which Spec.FDSets does not have", s.KVMFDSet)
	case s.UserfaultFDSet != 0 && !s.Restore:
		return errors.New("a /dev/userfaultfd descriptor for a machine that boots: only a restore loads fast")
	case s.UserfaultFDSet != 0 && !sets[s.UserfaultFDSet]:
		return fmt.Errorf("/dev/userfaultfd is descriptor set %d, which Spec.FDSets does not have", s.UserfaultFDSet)
	case s.VsockFD != 0 && s.VsockCID == 0:
		return errors.New("a /dev/vhost-vsock descriptor for a machine with no vsock")
	case s.VsockFD != 0 && s.VsockFD < 3:
		return errors.New("a /dev/vhost-vsock descriptor below 3 is stdin, stdout or stderr")
	}
	if err := s.TPM.validate(); err != nil {
		return err
	}
	for i, n := range s.NICs {
		if err := n.validate(); err != nil {
			return fmt.Errorf("NIC %d: %w", i, err)
		}
	}
	seen := map[string]bool{}
	for i, d := range s.Places {
		switch {
		case !serialPattern.MatchString(d.Serial):
			return fmt.Errorf("disk %d: serial %q is not 1-20 of A-Z, a-z, 0-9, '-' and '_'", i, d.Serial)
		case seen[d.Serial]:
			return fmt.Errorf("disk %d: serial %q is another disk's: a disk is bound to its place by serial", i, d.Serial)
		}
		seen[d.Serial] = true
	}
	return nil
}

// fdSets indexes Spec.FDSets by id, refusing what QEMU would refuse later and less clearly.
func (s Spec) fdSets() (map[int]bool, error) {
	sets := make(map[int]bool, len(s.FDSets))
	for _, set := range s.FDSets {
		switch {
		case sets[set.ID]:
			return nil, fmt.Errorf("descriptor set %d is given twice", set.ID)
		case len(set.FDs) == 0:
			return nil, fmt.Errorf("descriptor set %d holds no descriptor", set.ID)
		}
		for _, fd := range set.FDs {
			if fd.Num < 3 {
				return nil, fmt.Errorf("descriptor set %d names descriptor %d, which is stdin, stdout or stderr", set.ID, fd.Num)
			}
		}
		sets[set.ID] = true
	}
	return sets, nil
}

func (n NIC) validate() error {
	switch {
	// TapFD has no "absent": a NIC is its TAP, and the zero value is stdin.
	case n.TapFD < 3 && !n.hub:
		return fmt.Errorf("a TAP descriptor of %d is stdin, stdout or stderr", n.TapFD)
	case n.VhostFD != 0 && n.VhostFD < 3:
		return errors.New("a /dev/vhost-net descriptor below 3 is stdin, stdout or stderr")
	}
	// Parsed, not only required: the MAC is written into a QEMU option string, and
	// anything net.ParseMAC accepts as 48 bits has no comma in it.
	if mac, err := net.ParseMAC(n.MAC); err != nil || len(mac) != 6 {
		return fmt.Errorf("MAC %q is not a 48-bit address", n.MAC)
	}
	return nil
}

// Args returns the QEMU command line, without the binary itself.
func (s Spec) Args() ([]string, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}

	args := make([]string, 0, 64)
	args = append(args,
		"-L", s.Firmware,
		"-bios", s.bios(),

		// Every device this machine has is named below. Without -nodefaults QEMU
		// adds a NIC, a display, a serial port and a floppy controller of its
		// own choosing, and a guest that enumerates them pays for all of them.
		"-nodefaults",
		"-nographic",

		// The binary is built with seccomp support, so all four restrictions
		// apply: obsolete syscalls denied; setuid/setgid denied, since QEMU
		// never changes user here; fork and exec denied, because nothing is
		// spawned — a TAP arrives as a file descriptor and no network helper is
		// compiled in; and sched_setaffinity denied, so vCPU pinning has to be
		// done from outside rather than from inside QEMU.
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",

		"-machine", machineOpts,
		"-accel", accelArg(s.accel(), s.KVMFDSet),
		"-cpu", s.cpuArg(),
		"-smp", smpArg(s.BootCPUs, s.MaxCPUs),
		"-m", memoryArg(s.Memory.BaseMB, s.Memory.CeilingMB),
	)
	args = appendChipset(args)
	args = s.appendBoot(args)
	args = s.appendDevices(args)
	args, err := s.appendDisks(args)
	if err != nil {
		return nil, err
	}
	args = s.appendNICs(args)
	args = s.appendTPM(args)
	args = s.appendConsole(args)
	args = s.appendMonitors(args)
	args = s.appendFDSets(args)
	args = s.appendHold(args)
	return args, nil
}

// appendChipset is the chipset's globals and what a reset does, which no spec changes.
func appendChipset(args []string) []string {
	// S3 and S4 are suspend states this machine cannot come back from and that a
	// guest can ask for by accident. Disabling them in the chipset means the
	// request never reaches the point of stopping the VM.
	args = append(args, "-global", "ICH9-LPC.disable_s3=1", "-global", "ICH9-LPC.disable_s4=1")

	// No ACPI PCI hotplug. Disks arrive on a SCSI controller and the machine has no bridge for
	// it to serve, and a q35 left to its default hands PCIe hotplug to ACPI, whose _OSC then
	// withholds native control from a guest kernel with no acpiphp: when this machine had root
	// ports, a disk added was seen only after a rescan and one asked to go never went.
	args = append(args, "-global", "ICH9-LPC.acpi-pci-hotplug-with-bridge-support=off")

	// A reset ends the process instead of starting the machine again.
	//
	// The kernel command line carries panic=1, whose stated purpose is that a
	// wedged guest becomes a process that exits rather than a VM sitting at a
	// prompt nobody is watching. Without this it does not: the guest panics,
	// reboots, panics again, and QEMU never exits — measured by booting an init
	// that returns immediately, which produced a reboot loop rather than the
	// expected exit. A VM here is cattle; something that wants a fresh machine
	// starts one.
	return append(args, "-no-reboot")
}

// appendBoot is what the machine boots: the kernel, the initrd, and its command line.
//
// QEMU copies the kernel's loaded segments into guest memory at every reset, 6.7 ms of a
// start (boot:firmware-stages, 2026-09-30), and mapping the ELF instead would not save it:
// read back at a login prompt, the guest had written 53% of the 8749 pages it was given and
// 86% of .text (return thunks and ENDBR sealing reach nearly every function), so a mapping
// pays a copy-on-write fault for most of them. What stays unwritten is .rodata, ORC and .BTF.
func (s Spec) appendBoot(args []string) []string {
	args = append(args, "-kernel", s.Kernel)
	if s.Initrd != "" {
		args = append(args, "-initrd", s.Initrd)
	}
	return append(args, "-append", s.Cmdline.String())
}

// appendDevices is every device this machine has whatever it is given: vmgenid, the
// RNG and the balloon, and the vsock, the virtio-mem region and the hotplug controller when
// the spec asks for them.
func (s Spec) appendDevices(args []string) []string {
	// The VM Generation ID, whose value QEMU randomises for every VM it starts.
	//
	// It exists for restores. A VM resumed from a checkpoint starts with the
	// checkpoint's memory, which includes the state of the guest's random pool: two
	// guests resumed from one checkpoint, or one resumed twice, would otherwise
	// produce the same "random" bytes until something reseeded them. The kernel
	// watches this device and reseeds when the value it sees differs from the one in
	// the memory it woke up with, which is exactly this case.
	args = append(args, "-device", "vmgenid,guid=auto")

	args = append(args, "-device",
		fmt.Sprintf("virtio-rng-pci,%s,addr=0x%x", virtioModern, slotRNG))

	// The balloon, and the only way a running VM here gives memory back.
	//
	// Without it the answer to "how is memory reclaimed?" is "the VM exits". A
	// guest that peaked at 8 GB during a build holds 8 GB of the host until it is
	// shut down, however little of it is still in use — which for a fleet of
	// long-lived development VMs is most of the host, most of the time.
	//
	// free-page-reporting rather than a host-driven target: the guest reports
	// pages it has genuinely freed and QEMU discards them, continuously and with
	// nobody deciding a number. The guest half is CONFIG_PAGE_REPORTING, which
	// this kernel has.
	//
	// deflate-on-oom is the safety valve for the other direction: a guest about
	// to kill a process for want of memory takes some back from the balloon
	// first. It costs nothing when nothing is inflated, and the alternative is a
	// build dying with the host holding memory this VM had already earned.
	//
	// The id is the path GuestMemory reads the guest's reports at. It is not the machine's: the
	// record names the balloon by its slot and leaves every id out (perVM), and the stream names
	// it by its PCI address, so a checkpoint of a balloon without one resumes into this.
	args = append(args, "-device",
		fmt.Sprintf("virtio-balloon-pci,id=%s,free-page-reporting=on,deflate-on-oom=on,%s,addr=0x%x",
			balloonID, virtioModern, slotBalloon))

	if s.VsockCID != 0 {
		dev := fmt.Sprintf("vhost-vsock-pci,guest-cid=%d,%s,addr=0x%x",
			s.VsockCID, virtioModern, slotVsock)
		if s.VsockFD != 0 {
			dev += fmt.Sprintf(",vhostfd=%d", s.VsockFD)
		}
		args = append(args, "-device", dev)
	}

	// virtio-mem, when this VM is allowed to grow: the region between its boot
	// memory and its ceiling, present as a device and empty.
	//
	// requested-size=0 — nothing is plugged until somebody asks over QMP. What is
	// plugged when a checkpoint is saved goes into it with the rest of the memory.
	if s.Memory.CeilingMB > s.Memory.BaseMB {
		growth := s.Memory.CeilingMB - s.Memory.BaseMB
		args = append(args,
			"-object", fmt.Sprintf("memory-backend-ram,id=%s,size=%dM", memGrowthID, growth),
			"-device", fmt.Sprintf("virtio-mem-pci,id=%s,memdev=%s,requested-size=0,%s,addr=0x%x",
				virtioMemID, memGrowthID, virtioModern, slotMem))
	}

	// The controller disks this machine is given while it runs are added to. See
	// Spec.HotplugDisks: the root complex takes no device_add, and a SCSI bus does.
	//
	// max_target is the last target a disk can arrive at (hotplugDisk). The guest scans every
	// target up to it at boot, one request each: all 256 were 10-11 ms of a 1-vCPU boot on
	// nested KVM (boot:initcalls, lab 44587603255). qemu/patches/0002 adds the property.
	if s.HotplugDisks > 0 {
		args = append(args, "-device", fmt.Sprintf("virtio-scsi-pci,id=%s,max_target=%d,%s,addr=0x%x",
			hotplugController, s.HotplugDisks-1, virtioModern, slotHotplug))
	}
	return args
}

// appendDisks is each place for a disk, on its fixed slot: the device, over a throttle node
// named slot<i> whose file is an empty placeholder until a disk is bound (Place).
func (s Spec) appendDisks(args []string) ([]string, error) {
	if len(s.Places) > 0 {
		// The group the places' nodes are in, with no limits: a throttle node is only how a
		// place is a filter (placeNode).
		args = append(args, "-object", "throttle-group,id="+placeGroup)
	}
	for i, d := range s.Places {
		empty, err := json.Marshal(map[string]any{
			"driver": "null-co", "node-name": emptyNode(i), "size": 0, "read-zeroes": true,
		})
		if err != nil {
			return nil, fmt.Errorf("encoding disk %d's placeholder: %w", i, err)
		}
		raw, err := json.Marshal(placeNode(i, emptyNode(i), d.Readonly))
		if err != nil {
			return nil, fmt.Errorf("encoding disk %d's node: %w", i, err)
		}
		args = append(args, "-blockdev", string(empty), "-blockdev", string(raw), "-device",
			fmt.Sprintf("virtio-blk-pci,id=%s,drive=%s,serial=%s,%s,addr=0x%x",
				slotDevice(i), slotNodePrefix+fmt.Sprint(i), d.Serial, virtioModern, slotDiskBase+i))
	}
	return args, nil
}

// slotNodePrefix names a place's node, slot<i>: the node the device's drive is, which a bind
// moves onto the disk.
const slotNodePrefix = "slot"

func slotDevice(i int) string { return fmt.Sprintf("disk%d", i) }
func emptyNode(i int) string  { return fmt.Sprintf("slot%d-empty", i) }

// placeGroup is the throttle group every place's node is in. It has no limits and is given none:
// a group's members share its limit, so one set here would hold every disk together. A disk's
// limit is its device's (SetDiskLimit).
const placeGroup = "places"

// placeNode is the node a place's device reads through: a pass-through over file, the
// placeholder at exec and the disk's top image once bound.
//
// It is a throttle node with no limits, not raw, because the node has to be a filter. spin's
// storage rewrites a bound image's backing file (change-backing-file, its compaction), which QEMU
// takes only on a root node whose chain contains the image, and it walks that chain through
// backing and filter links alone: under raw, which reaches the image through a plain file
// child, QEMU answered "'slot0' and image file are not in the same chain" (QEMU 11.2.0-rc0,
// 2026-11-19). copy-on-read, compress and preallocate are filters too, and each changes what
// is written; throttle with no limits changes nothing.
func placeNode(i int, file string, readonly bool) qmp.ThrottleNode {
	return qmp.ThrottleNode{NodeName: slotNodePrefix + fmt.Sprint(i), File: qmp.NodeRef(file),
		ThrottleGroup: placeGroup, ReadOnly: readonly, Discard: "unmap"}
}

// appendNICs is each NIC and its TAP backend, on its fixed slot.
func (s Spec) appendNICs(args []string) []string {
	for i, n := range s.NICs {
		// romfile= loads no option ROM. The card is only ever driven by a guest
		// that already has the driver compiled in, and it never boots from the
		// network, so the ROM is firmware nothing reads.
		// vhost=on, always. Without it every packet is copied into QEMU, out of
		// it, and back into the kernel; with it the kernel moves frames between
		// the tap and the guest's virtqueues and QEMU is not on the data path at
		// all. QEMU does not default it on, and there is no fallback: a host with
		// no /dev/vhost-net fails at start-up rather than quietly running slowly,
		// which is the right way round — a machine that cannot do this is a
		// different machine.
		dev := fmt.Sprintf("virtio-net-pci,netdev=net%d,mac=%s,romfile=,%s,addr=0x%x",
			i, n.MAC, virtioModern, slotNICBase+i)
		if n.MTU > 0 { // mutate-exempt: the guest driver refuses an announced MTU below 68 (virtnet_probe), so 0 is the only small value a caller means
			dev += fmt.Sprintf(",host_mtu=%d", n.MTU)
		}
		netdev := fmt.Sprintf("tap,id=net%d,fd=%d,vhost=on", i, n.TapFD)
		switch {
		case n.hub:
			netdev = fmt.Sprintf("hubport,id=net%d,hubid=0", i)
		case n.VhostFD != 0:
			netdev += fmt.Sprintf(",vhostfd=%d", n.VhostFD)
		}
		args = append(args, "-netdev", netdev, "-device", dev)
	}
	return args
}

// appendConsole is the serial console: a descriptor set, a chardev, or none.
func (s Spec) appendConsole(args []string) []string {
	switch {
	case s.ConsoleFDSet != 0:
		args = append(args,
			"-chardev", fmt.Sprintf("file,id=serial0,path=/dev/fdset/%d,append=on", s.ConsoleFDSet),
			"-serial", "chardev:serial0")
	case s.Console != "":
		args = append(args, "-serial", s.Console)
	default:
		args = append(args, "-serial", "none")
	}
	return args
}

// appendMonitors is each QMP monitor, in the order the spec lists them.
func (s Spec) appendMonitors(args []string) []string {
	for i, m := range s.Monitors {
		if m.Socket != "" {
			args = append(args, "-qmp", fmt.Sprintf("unix:%s,server=on,wait=off", qemuOpt(m.Socket)))
			continue
		}
		// A monitor on a socket the caller made and listens on: QEMU accepts on the descriptor
		// and never needs a place in the filesystem to put a socket.
		//
		// -object monitor-qmp and not -mon, which QEMU 11.1 warns is deprecated.
		id := fmt.Sprintf("qmp%d", i)
		args = append(args,
			"-chardev", fmt.Sprintf("socket,id=%s,fd=%d,server=on,wait=off", id, m.FD),
			"-object", fmt.Sprintf("monitor-qmp,id=mon-%s,chardev=%s", id, id))
	}
	return args
}

// appendFDSets is every descriptor set the command line names.
func (s Spec) appendFDSets(args []string) []string {
	// An opaque is a path in practice, so it is escaped like one.
	for _, set := range s.FDSets {
		for _, fd := range set.FDs {
			spec := fmt.Sprintf("fd=%d,set=%d", fd.Num, set.ID)
			if fd.Opaque != "" {
				spec += ",opaque=" + qemuOpt(fd.Opaque)
			}
			args = append(args, "-add-fd", spec)
		}
	}
	return args
}

// appendHold is what holds the machine until a monitor says what to do: -incoming defer for a
// restore, with the userfaultfd device it loads fast with when it was handed one, -S for a boot
// (Spec.Restore).
func (s Spec) appendHold(args []string) []string {
	if s.UserfaultFDSet != 0 {
		args = append(args, "-userfaultfd", fmt.Sprintf("/dev/fdset/%d", s.UserfaultFDSet))
	}
	if s.Restore {
		return append(args, "-incoming", "defer")
	}
	return append(args, "-S")
}
