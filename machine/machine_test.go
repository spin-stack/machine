// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// spec returns a minimal valid spec pointing at files that exist, so
// Fingerprint has something to hash.
func spec(t *testing.T) Spec {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(DefaultBIOS, "qboot")
	write("bios-256k.bin", "seabios")
	write("linuxboot_dma.bin", "linuxboot")
	return Spec{
		QEMU:     write("qemu-system-x86_64", "qemu"),
		Kernel:   write("bzImage", "kernel"),
		Firmware: dir,
		BootCPUs: 2,
		Memory:   Memory{SizeMB: 512},
		Monitors: []Monitor{{Socket: "/run/qmp"}},
	}
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// nic is a NIC validate accepts, for a test about something else.
func nic() NIC { return NIC{TapFD: 3, MAC: "52:54:00:00:00:01"} }

// What Args puts on the command line, one machine per row. Each row is the whole
// line joined with spaces and a trailing one, so a want ending in a space pins the
// end of an argument.
//
// Every string here is something whose absence is invisible in a guest until it is
// slow or wrong — none of them fails loudly when it goes.
func TestArgs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(*Spec)
		want   []string
		absent []string
	}{{
		// A machine given no display, no default devices and no way to spawn a helper
		// is most of what makes this machine what it is.
		name: "machine-wide flags",
		want: []string{"-nodefaults", "-sandbox on,obsolete=deny", "vmgenid,guid=auto"},
		// Anonymous RAM names no backend; the virtio-mem device is only for a ceiling, and a
		// vsock only for a CID.
		absent: []string{"memory-backend", "virtio-mem", "-incoming", "vhost-vsock"},
	}, {
		// qboot unless told otherwise: SeaBIOS is QEMU's own default, and a Spec that left the
		// firmware to QEMU would boot it without saying so.
		name: "qboot by default",
		want: []string{"-bios " + DefaultBIOS},
	}, {
		name: "another BIOS when named",
		set:  func(s *Spec) { s.BIOS = "bios-256k.bin" },
		want: []string{"-bios bios-256k.bin"},
	}, {
		// The slot map is the reason the kernel can be told pci=lastbus=0. A device
		// that moved off its slot is a device the guest may not find, with no error.
		name: "fixed slots",
		set: func(s *Spec) {
			s.VsockCID = 7
			s.Disks = []DiskSlot{{Serial: "root"}, {Serial: "home", Readonly: true}}
			s.NICs = []NIC{nic()}
		},
		want: []string{
			"vhost-vsock-pci,guest-cid=7,disable-legacy=on,addr=0x2 ",
			"virtio-rng-pci,disable-legacy=on,addr=0x3 ",
			// The only way a running VM gives memory back. Without it the answer to
			// "how is memory reclaimed?" is "the VM exits".
			"virtio-balloon-pci,free-page-reporting=on,deflate-on-oom=on,disable-legacy=on,addr=0x4 ",
			"virtio-blk-pci,id=disk0,drive=slot0,serial=root,disable-legacy=on,addr=0x5 ",
			"virtio-blk-pci,id=disk1,drive=slot1,serial=home,disable-legacy=on,addr=0x6 ",
			"virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01,romfile=,disable-legacy=on,addr=0x10 ",
			// Without this QEMU copies every packet through userspace, and nothing reports it.
			"tap,id=net0,fd=3,vhost=on ",
			// A place for a disk is a raw node over an empty placeholder until one is bound,
			// read-only when the place is: the device takes its permissions at exec.
			`{"driver":"null-co","node-name":"slot0-empty","read-zeroes":true,"size":0} `,
			`{"driver":"raw","node-name":"slot0","file":"slot0-empty","read-only":false,"discard":"unmap"} `,
			`{"driver":"raw","node-name":"slot1","file":"slot1-empty","read-only":true,"discard":"unmap"} `,
		},
	}, {
		// host_mtu is how the guest is told what it may send, and the only way of
		// telling it that it cannot then ignore: virtnet_probe assigns the announced
		// value to both dev->mtu and dev->max_mtu. Root inside the machine can discard
		// a DHCP option or a kernel parameter; this it cannot.
		name: "an announced MTU",
		set: func(s *Spec) {
			n := nic()
			n.MTU = 1400
			s.NICs = []NIC{n}
		},
		want: []string{"virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01,romfile=,disable-legacy=on,addr=0x10,host_mtu=1400 "},
	}, {
		// And nothing to announce says nothing, rather than announcing zero — which
		// QEMU takes as "no MTU" but only after the guest has read a config field the
		// feature bit says is valid.
		name:   "no MTU",
		set:    func(s *Spec) { s.NICs = []NIC{nic()} },
		absent: []string{"host_mtu"},
	}, {
		// Each NIC on a slot of its own, counting up from the first.
		name: "two NICs",
		set: func(s *Spec) {
			second := nic()
			second.TapFD, second.MAC = 4, "52:54:00:00:00:02"
			s.NICs = []NIC{nic(), second}
		},
		want: []string{
			"netdev=net0,mac=52:54:00:00:00:01,romfile=,disable-legacy=on,addr=0x10 ",
			"netdev=net1,mac=52:54:00:00:00:02,romfile=,disable-legacy=on,addr=0x11 ",
		},
	}, {
		// Disks that arrive later are added to one controller, whatever their number.
		name:   "hotplug disks",
		set:    func(s *Spec) { s.HotplugDisks = 1 },
		want:   []string{"-device virtio-scsi-pci,id=scsi0,disable-legacy=on,addr=0x1a "},
		absent: []string{"pcie-root-port"},
	}, {
		// Growing a VM is virtio-mem, not ACPI DIMM slots: -m carries the ceiling and
		// no slots=, and the device holds the region between boot memory and ceiling
		// with nothing plugged until somebody asks.
		name: "a memory ceiling",
		set:  func(s *Spec) { s.Memory.SizeMB, s.Memory.MaxMB = 2048, 8192 },
		want: []string{
			"-m 2048,maxmem=8192M ",
			"memory-backend-ram,id=mem.growth,size=6144M ",
			"virtio-mem-pci,id=vmem0,memdev=mem.growth,requested-size=0,disable-legacy=on,addr=0x1e ",
		},
		absent: []string{"slots="},
	}, {
		name:   "fixed memory",
		set:    func(s *Spec) { s.Memory.SizeMB = 2048 },
		want:   []string{"-m 2048 "},
		absent: []string{"maxmem", "virtio-mem"},
	}, {
		// A device handed over as a descriptor is named by it on the command line, and
		// one that is not is left for QEMU to open.
		name: "devices QEMU opens",
		set: func(s *Spec) {
			s.VsockCID = 7
			s.NICs = []NIC{nic()}
		},
		want:   []string{"-accel kvm ", "vhost-vsock-pci,guest-cid=7,disable-legacy=on,addr=0x2 ", "tap,id=net0,fd=3,vhost=on "},
		absent: []string{"device=/dev/fdset", "vhostfd"},
	}, {
		// The accelerator is always its own -accel and never accel= in -machine: QEMU
		// refuses the two together, and only -accel can carry /dev/kvm's descriptor.
		name: "devices handed over",
		set: func(s *Spec) {
			s.VsockCID = 7
			s.NICs = []NIC{nic()}
			s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 4}}}}
			s.KVMFDSet, s.VsockFD, s.NICs[0].VhostFD = 1, 5, 6
		},
		want: []string{
			"-machine pc-q35-11.1,kernel-irqchip=on,", "-accel kvm,device=/dev/fdset/1 ",
			"addr=0x2,vhostfd=5 ", "tap,id=net0,fd=3,vhost=on,vhostfd=6 ",
		},
		absent: []string{"accel=kvm"},
	}, {
		// A value a caller supplies is a value, not more options. QEMU splits an option
		// string on a single comma, so "a,readonly=off" as a path was a path "a" and a
		// second option. Doubled, it is one value; TestQEMUAcceptsEveryArgument asks
		// QEMU to open such paths.
		name: "commas in values",
		set: func(s *Spec) {
			s.Monitors = []Monitor{{Socket: "/run/q,wait=on"}}
			s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 3, Opaque: "/o,p"}}}}
		},
		want: []string{
			"unix:/run/q,,wait=on,server=on,wait=off ",
			"opaque=/o,,p ",
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			s := spec(t)
			if tc.set != nil {
				tc.set(&s)
			}
			args, err := s.Args()
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(args, " ") + " "
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in %s", want, joined)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(joined, absent) {
					t.Errorf("%q in %s", absent, joined)
				}
			}
		})
	}
}

// An unnamed CPU is the silicon under KVM and everything QEMU can emulate under TCG,
// which refuses "host" outright.
func TestAnUnnamedCPUFollowsTheAccelerator(t *testing.T) {
	for accel, want := range map[string]string{"kvm": "host,", "tcg": "max,"} {
		s := spec(t)
		s.Accel = accel
		if got := s.Shape().CPU; !strings.HasPrefix(got, want) {
			t.Errorf("-accel %s: -cpu %q, want %q first", accel, got, want)
		}
	}
}

// The command line and the fingerprint read one Shape. If they read two, a VM could
// resume a checkpoint of a machine it is not.
func TestArgsCarriesTheShape(t *testing.T) {
	s := spec(t)
	args, err := s.Args()
	if err != nil {
		t.Fatal(err)
	}
	// exec passes an empty element as an argument of its own, which QEMU reads as a
	// file name or refuses.
	if i := slices.Index(args, ""); i >= 0 {
		t.Errorf("argument %d is empty: %q", i, args)
	}
	sh := s.Shape()
	for _, c := range []struct{ flag, want string }{
		{"-machine", sh.Machine},
		{"-accel", sh.Accel},
		{"-cpu", sh.CPU},
		{"-smp", sh.SMP},
		{"-m", sh.Memory},
	} {
		if got := argValue(args, c.flag); got != c.want {
			t.Errorf("%s = %q, want %q", c.flag, got, c.want)
		}
	}
}

// The fingerprint, one pair of machines per row: a is spec() changed by a, b is a
// changed by b, and the row says whether a checkpoint saved on one resumes on the other.
//
// It may only if the machine type, the shape, the devices present when state is loaded and the
// BIOS's size all agree — and must be able to when all that differs is the emulator, what a
// boot reads, or what is behind the devices, or a checkpoint resumes nowhere but on the release
// that saved it.
func TestFingerprint(t *testing.T) {
	rewrite := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withMTU := func(mtu int) func(*testing.T, *Spec) {
		return func(_ *testing.T, s *Spec) {
			n := nic()
			n.MTU = mtu
			s.NICs = []NIC{n}
		}
	}

	for _, tc := range []struct {
		name string
		a, b func(*testing.T, *Spec)
		same bool
	}{
		{name: "the same machine twice", same: true},

		// A path is not the machine: the same contents under another name is one.
		{name: "the same files at other paths", same: true, b: func(t *testing.T, s *Spec) {
			other := spec(t)
			s.QEMU, s.Kernel = other.QEMU, other.Kernel
		}},

		// The disks are the VM's, and a host fingerprints its machine before there is a
		// VM: a checkpoint is resumed with the disks it was saved with, by its caller.
		{name: "a disk", same: true, b: func(_ *testing.T, s *Spec) { s.Disks = []DiskSlot{{Serial: "root"}} }},

		// A descriptor number is which file the backend reads, the way a disk's path
		// is, and a MAC is a property of the device and not of the bus. A caller that
		// wants distinct MACs should not find that its checkpoints resume nowhere else.
		{name: "another TAP and MAC", same: true,
			a: func(_ *testing.T, s *Spec) { s.NICs = []NIC{nic()} },
			b: func(_ *testing.T, s *Spec) { s.NICs = []NIC{{TapFD: 9, MAC: "52:54:00:ff:ff:ff"}} }},

		// What is behind a device is not the machine: two VMs with one disk each are
		// the same machine whether that disk holds a database or a scratch overlay,
		// and whether it arrives by path or as a descriptor.
		{name: "everything handed over as descriptors", same: true,
			a: func(_ *testing.T, s *Spec) {
				s.Disks = []DiskSlot{{Serial: "aaa"}}
				s.NICs = []NIC{nic()}
				s.VsockCID = 7
				s.Serial = "file:/var/log/console"
			},
			b: func(_ *testing.T, s *Spec) {
				s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 10}}}, {ID: 2, FDs: []FD{{Num: 11}}}, {ID: 3, FDs: []FD{{Num: 13}}}}
				s.Disks = []DiskSlot{{Serial: "bbb"}}
				s.NICs = []NIC{{TapFD: 9, MAC: "52:54:00:00:00:02", VhostFD: 15}}
				s.VsockCID = 42
				s.Serial, s.SerialFDSet = "", 2
				s.Monitors = []Monitor{{FD: 12}}
				s.KVMFDSet, s.VsockFD = 3, 14
			}},

		// Naming a model is how a fleet gets VMs that move: every host shows the guest
		// the same CPU, so the host's own silicon drops out.

		{name: "more memory", b: func(_ *testing.T, s *Spec) { s.Memory.SizeMB = 1024 }},
		{name: "more vCPUs", b: func(_ *testing.T, s *Spec) { s.BootCPUs = 4 }},
		// A resumed guest runs the kernel in its saved memory: the file is read by a boot.
		{name: "a new kernel", same: true, b: func(t *testing.T, s *Spec) { rewrite(t, s.Kernel, "a different kernel") }},
		{name: "an initrd", same: true, b: func(t *testing.T, s *Spec) { s.Initrd = s.Kernel }},
		// Another QEMU of the same versioned machine type is another emulator of one machine:
		// QEMU's migration promise, which boot:compat holds a release to.
		{name: "a new QEMU", same: true, b: func(t *testing.T, s *Spec) { rewrite(t, s.QEMU, "a different qemu") }},
		// The option ROM QEMU adds for the kernel reaches the guest through fw_cfg at a boot
		// and is no block a resume loads.
		{name: "a new linuxboot_dma.bin", same: true, b: func(t *testing.T, s *Spec) {
			rewrite(t, filepath.Join(s.Firmware, "linuxboot_dma.bin"), "a different linuxboot")
		}},
		// The BIOS is the pc.bios block a saved machine carries whole, contents included: one
		// of another length is refused, and a rebuild of the same length is the same machine.
		{name: "a BIOS rebuilt at its size", same: true, b: func(t *testing.T, s *Spec) {
			rewrite(t, filepath.Join(s.Firmware, DefaultBIOS), "QBOOT")
		}},
		{name: "a BIOS of another size", b: func(t *testing.T, s *Spec) {
			rewrite(t, filepath.Join(s.Firmware, DefaultBIOS), "a different qboot")
		}},
		{name: "SeaBIOS instead of qboot", b: func(_ *testing.T, s *Spec) { s.BIOS = "bios-256k.bin" }},
		{name: "the same firmware in another directory", same: true, b: func(t *testing.T, s *Spec) {
			s.Firmware = spec(t).Firmware
		}},
		{name: "the default BIOS named", same: true, b: func(_ *testing.T, s *Spec) { s.BIOS = DefaultBIOS }},
		// A guest's state under TCG is not a guest's state under KVM, and the
		// accelerator is in the shape.
		{name: "emulation", b: func(_ *testing.T, s *Spec) { s.Accel = "tcg" }},
		// A vsock and a memory ceiling are there on both sides of a restore.
		{name: "a vsock", b: func(_ *testing.T, s *Spec) { s.VsockCID = 7 }},
		{name: "a memory ceiling", b: func(_ *testing.T, s *Spec) { s.Memory.MaxMB = s.Memory.SizeMB * 2 }},
		// A NIC is on the command line, so it is present when state is loaded.
		{name: "a NIC", b: func(_ *testing.T, s *Spec) { s.NICs = []NIC{nic()} }},
		// The MTU is VIRTIO_NET_F_MTU, negotiated at probe and written into the
		// migration stream: a checkpoint saved under one MTU must not be loaded under
		// another. Announcing nothing is a third machine.
		{name: "an MTU announced", a: withMTU(0), b: withMTU(1500)},
		{name: "another MTU", a: withMTU(1500), b: withMTU(1400)},
		{name: "a named CPU instead of host", b: func(_ *testing.T, s *Spec) { s.CPU = "Skylake-Server-v4" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := spec(t)
			if tc.a != nil {
				tc.a(t, &a)
			}
			fa, err := a.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			b := a
			if tc.b != nil {
				tc.b(t, &b)
			}
			fb, err := b.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.same && fa != fb:
				t.Error("two machines a checkpoint may move between have different fingerprints")
			case !tc.same && fa == fb:
				t.Error("two machines a checkpoint may not move between share a fingerprint")
			}
		})
	}
}

// The kernel command line, one Cmdline per row.
func TestCmdline(t *testing.T) {
	withInit := DefaultCmdline()
	withInit.Init = "/sbin/custom-init"
	withRoot := DefaultCmdline()
	withRoot.Root = "/dev/vda"
	noConsole := DefaultCmdline()
	noConsole.Console = ""

	for _, tc := range []struct {
		name   string
		c      Cmdline
		want   []string
		absent []string
		suffix string
	}{{
		name: "default",
		c:    DefaultCmdline(),
		want: []string{
			// Without it every boot pays 8192 config reads looking for peer host bridges.
			"pci=lastbus=0",
			// Memory that arrives at runtime is no use to a guest that does not online
			// it, and the failure is silent: growth stops at the boot size.
			"memhp_default_state=online",
			// Two thirds of this machine's boot was systemd asking a serial port nobody
			// listens to two questions and waiting 334 ms for each answer. It looks like
			// a terminal preference, so it is what gets dropped while tidying; nothing
			// fails if it goes, the machine just takes five times as long.
			"TERM=dumb",
			"console=ttyS0",
			// The probes nothing before init waits for run beside the initcalls; the
			// root disk's is not among them, because the mount needs it.
			"driver_async_probe=virtio_rng,virtio_balloon,vmw_vsock_virtio_transport",
		},
		// No root named, no root= at all: an empty one would be a kernel looking for "".
		absent: []string{"root="},
	}, {
		name: "root",
		c:    withRoot,
		// Named, or the root mount tries ext3 and ext2 on the superblock first.
		want: []string{"root=/dev/vda rw rootfstype=ext4"},
	}, {
		name:   "no console",
		c:      noConsole,
		absent: []string{"console="},
	}, {
		name:   "init",
		c:      withInit,
		suffix: "init=/sbin/custom-init",
	}, {
		// A profiling boot goes silent, not verbose: registering a console replays the
		// whole ring into it inside an initcall, and the profile then measures itself.
		name:   "profiling",
		c:      DefaultCmdline().Profiling(),
		want:   []string{"loglevel=0", "initcall_debug", "log_buf_len=4M"},
		absent: []string{"quiet"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q: %s", want, got)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("%q in %s", absent, got)
				}
			}
			if !strings.HasSuffix(got, tc.suffix) {
				t.Errorf("init must come last, with its args after --: %s", got)
			}
		})
	}
}

// Profiling returns a new Cmdline, and the one it was made from stays the caller's.
// Extra is a slice: appending the profiling flags into spare capacity the caller's
// Extra had wrote them into the caller's array, so the caller's next append
// overwrote the profile's first flag.
func TestProfilingLeavesTheCallersCmdlineAlone(t *testing.T) {
	base := DefaultCmdline()
	base.Extra = make([]string, 0, 8)
	prof := base.Profiling()
	base.Extra = append(base.Extra, "caller's own")

	if got := prof.Extra[0]; got != "initcall_debug" {
		t.Errorf("the caller's append reached the profiling command line: Extra[0] = %q", got)
	}
}

// validate is the boundary, and every case in it is something that otherwise
// fails as a guest that boots and finds the world subtly wrong. A case added
// without a test is a check nobody notices stopped firing.
func TestValidateRefuses(t *testing.T) {
	for _, tc := range []struct {
		what   string
		breaks func(*Spec)
	}{
		{"no QEMU", func(s *Spec) { s.QEMU = "" }},
		{"no kernel", func(s *Spec) { s.Kernel = "" }},
		{"no firmware", func(s *Spec) { s.Firmware = "" }},
		{"a BIOS given as a path", func(s *Spec) { s.BIOS = "/elsewhere/qboot.bin" }},
		{"no boot CPUs", func(s *Spec) { s.BootCPUs = 0 }},
		{"a vCPU ceiling below the boot count", func(s *Spec) { s.BootCPUs, s.MaxCPUs = 4, 2 }},
		{"no memory", func(s *Spec) { s.Memory.SizeMB = 0 }},
		// A ceiling below the boot size: memoryArg drops it, so without this the
		// caller gets a machine that cannot grow having asked for one that can.
		{"a memory ceiling below the boot size", func(s *Spec) { s.Memory.SizeMB, s.Memory.MaxMB = 2048, 512 }},
		{"more disks than the slot range holds", func(s *Spec) {
			s.Disks = make([]DiskSlot, maxDisks+1)
			for i := range s.Disks {
				s.Disks[i] = DiskSlot{Serial: fmt.Sprintf("d%d", i)}
			}
		}},
		{"more NICs than the slot range holds", func(s *Spec) {
			s.NICs = make([]NIC, maxNICs+1)
			for i := range s.NICs {
				s.NICs[i] = nic()
			}
		}},
		{"more hotplug disks than the controller addresses", func(s *Spec) { s.HotplugDisks = MaxHotplugDisks + 1 }},
		{"a negative number of hotplug disks", func(s *Spec) { s.HotplugDisks = -1 }},
		// A disk is bound to its place by serial, and the serial names its nodes.
		{"a place for a disk with no serial", func(s *Spec) { s.Disks = []DiskSlot{{}} }},
		{"a serial QEMU would take as more options", func(s *Spec) { s.Disks = []DiskSlot{{Serial: "x,addr=0x2"}} }},
		{"a serial longer than virtio-blk's 20 bytes", func(s *Spec) { s.Disks = []DiskSlot{{Serial: strings.Repeat("a", 21)}} }},
		{"two places with one serial", func(s *Spec) { s.Disks = []DiskSlot{{Serial: "a"}, {Serial: "a"}} }},
		// Held until a monitor says what to do: with none it never starts.
		{"no monitor", func(s *Spec) { s.Monitors = nil }},
		{"a descriptor set given twice", func(s *Spec) {
			s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 3}}}, {ID: 1, FDs: []FD{{Num: 4}}}}
		}},
		{"a descriptor set holding stdin", func(s *Spec) { s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 0}}}} }},
		{"a monitor given a path and a descriptor", func(s *Spec) { s.Monitors = []Monitor{{Socket: "/qmp", FD: 3}} }},
		{"a monitor given neither", func(s *Spec) { s.Monitors = []Monitor{{Socket: "/qmp"}, {}} }},
		{"a monitor on stderr", func(s *Spec) { s.Monitors = []Monitor{{FD: 2}} }},
		// A value from a fixed set is refused, not escaped: an escaped "qcow2,,x" is
		// still not a format, and an unescaped one is a second option.
		{"a CPU model with options", func(s *Spec) { s.CPU = "Skylake-Server-v4,enforce=off" }},
		{"an accelerator this machine does not run under", func(s *Spec) { s.Accel = "xen" }},
		// The kernel command line: each of these boots a guest that is told something
		// other than what the caller wrote.
		{"a root device with a space", func(s *Spec) { s.Cmdline.Root = "/dev/vda quiet" }},
		{"an extra parameter that ends the kernel's part", func(s *Spec) { s.Cmdline.Extra = []string{"quiet -- x"} }},
		{"a command line the kernel would truncate", func(s *Spec) {
			s.Cmdline.Extra = []string{strings.Repeat("x", maxCmdline)}
		}},
		{"/dev/kvm in a set nobody gave", func(s *Spec) { s.KVMFDSet = 1 }},
		{"/dev/kvm for an emulator", func(s *Spec) {
			s.Accel, s.KVMFDSet = "tcg", 1
			s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 3}}}}
		}},
		{"/dev/vhost-vsock with no vsock", func(s *Spec) { s.VsockCID, s.VsockFD = 0, 3 }},
		{"/dev/vhost-vsock on stdout", func(s *Spec) { s.VsockCID, s.VsockFD = 3, 1 }},
		{"/dev/vhost-net on stderr", func(s *Spec) {
			n := nic()
			n.VhostFD = 2
			s.NICs = []NIC{n}
		}},
		// A NIC is its TAP, and the zero value of a descriptor is stdin: NIC{} was a
		// machine whose network backend read the launcher's terminal.
		{"a NIC with no TAP", func(s *Spec) { s.NICs = []NIC{{MAC: "52:54:00:00:00:01"}} }},
		// The MAC goes into an option string: empty is "mac=,", and a comma in it
		// would be another option.
		{"a NIC with no MAC", func(s *Spec) { s.NICs = []NIC{{TapFD: 3}} }},
		{"a NIC with a MAC that is not one", func(s *Spec) { s.NICs = []NIC{{TapFD: 3, MAC: "52:54:00:00:00:01,romfile=x"}} }},
		{"a NIC with a 64-bit MAC", func(s *Spec) { s.NICs = []NIC{{TapFD: 3, MAC: "02:00:5e:10:00:00:00:01"}} }},
	} {
		t.Run(tc.what, func(t *testing.T) {
			s := spec(t)
			tc.breaks(&s)
			if err := s.validate(); err == nil {
				t.Fatalf("validate accepted %s", tc.what)
			}
			if _, err := s.Args(); err == nil {
				t.Fatalf("Args built a command line for %s", tc.what)
			}
		})
	}

	// And the specs the cases above are made from have to pass, or every one of them
	// would pass for the wrong reason.
	s := spec(t)
	if err := s.validate(); err != nil {
		t.Fatalf("the minimal spec does not validate: %v", err)
	}
	s.NICs = []NIC{nic()}
	if err := s.validate(); err != nil {
		t.Fatalf("the minimal spec with a NIC does not validate: %v", err)
	}

	// The least each bound allows: one megabyte, one vCPU, and the first descriptor that is
	// not stdin, stdout or stderr.
	s = spec(t)
	s.Memory.SizeMB = 1
	s.BootCPUs = 1
	s.VsockCID, s.VsockFD = 3, 3
	if err := s.validate(); err != nil {
		t.Fatalf("the least each bound allows does not validate: %v", err)
	}

	// A console is a path or a descriptor set, and either alone is not half of a conflict.
	s = spec(t)
	s.Serial = "stdio"
	if err := s.validate(); err != nil {
		t.Fatalf("a console by path does not validate: %v", err)
	}
	s = spec(t)
	s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 10}}}}
	s.SerialFDSet = 1
	if err := s.validate(); err != nil {
		t.Fatalf("a console by descriptor does not validate: %v", err)
	}
}

// What the contract says is present when a checkpoint's state is loaded, each device with its
// properties and its slot and without what is one VM's. Two machines that differ in one of these
// differ on the bus, so each has to be in the contract when the device is there and out of it
// when it is not; a check that is only "the two fingerprints differ" holds none of them, because
// an inverted test still differs.
func TestTheContractIsTheDevicesPresentAtRestore(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(*Spec)
		want   []string
		absent []string
	}{
		{name: "the machine's own", set: func(*Spec) {},
			want: []string{
				"device vmgenid,guid=auto",
				"device virtio-rng-pci,disable-legacy=on,addr=0x3",
				"device virtio-balloon-pci,free-page-reporting=on,deflate-on-oom=on,disable-legacy=on,addr=0x4",
				"global ICH9-LPC.disable_s3=1",
				"global ICH9-LPC.acpi-pci-hotplug-with-bridge-support=off",
			},
			absent: []string{"vhost-vsock-pci", "virtio-mem-pci", "isa-serial", "virtio-scsi-pci", "virtio-net-pci", "virtio-blk-pci"}},
		{name: "a vsock, without its context id or descriptor", set: func(s *Spec) { s.VsockCID, s.VsockFD = 7, 14 },
			want: []string{"device vhost-vsock-pci,disable-legacy=on,addr=0x2"}, absent: []string{"guest-cid", "vhostfd"}},
		{name: "a memory ceiling, without its backend", set: func(s *Spec) { s.Memory.MaxMB = s.Memory.SizeMB + 1 },
			want: []string{"device virtio-mem-pci,requested-size=0,disable-legacy=on,addr=0x1e"}, absent: []string{"memdev", ",id="}},
		{name: "a console by path", set: func(s *Spec) { s.Serial = "file:/log" }, want: []string{"serial isa-serial"}},
		{name: "a console by descriptor", set: func(s *Spec) { s.SerialFDSet = 1 }, want: []string{"serial isa-serial"}},
		{name: "a hotplug controller", set: func(s *Spec) { s.HotplugDisks = 1 },
			want: []string{"device virtio-scsi-pci,disable-legacy=on,addr=0x1a"}},
		{name: "a NIC, by its slot and MTU and not its MAC or backend", set: func(s *Spec) {
			n := nic()
			n.MTU, n.VhostFD = 1500, 15
			s.NICs = []NIC{n}
		}, want: []string{"device virtio-net-pci,romfile=,disable-legacy=on,addr=0x10,host_mtu=1500"}, absent: []string{"mac=", "netdev", "vhostfd"}},
		{name: "a disk is the VM's", set: func(s *Spec) { s.Disks = []DiskSlot{{Serial: "x"}} },
			absent: []string{"virtio-blk-pci", "serial=x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := spec(t)
			tc.set(&s)
			got := strings.Join(s.Contract(), "\n")
			for _, w := range tc.want {
				if !slices.Contains(s.Contract(), w) {
					t.Errorf("contract\n%s\nwant it to hold %q", got, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("contract\n%s\nwant no %q", got, a)
				}
			}
		})
	}
}

// A machine starts held, for a boot or for a checkpoint, and never both: under -incoming defer
// cont leaves the machine waiting for a migration, so a machine for a boot that carried it
// would never boot, and one for a restore without it would boot a fresh guest instead.
func TestAMachineStartsHeldForABootOrARestore(t *testing.T) {
	for _, tc := range []struct {
		restore      bool
		want, absent string
	}{
		{false, " -S ", "-incoming"},
		{true, " -incoming defer ", " -S "},
	} {
		s := spec(t)
		s.Restore = tc.restore
		args, err := s.Args()
		if err != nil {
			t.Fatal(err)
		}
		joined := " " + strings.Join(args, " ") + " "
		if !strings.Contains(joined, tc.want) || strings.Contains(joined, tc.absent) {
			t.Errorf("restore %t: want %q and no %q in %s", tc.restore, tc.want, tc.absent, joined)
		}
	}
}
