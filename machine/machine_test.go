// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/machine/internal/elftest"
)

// spec returns a minimal valid spec pointing at files that exist: Args asks the kernel whether
// it is a bzImage or an ELF kernel entered through PVH.
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
	return Spec{
		QEMU:     write("qemu-system-x86_64", "qemu"),
		Kernel:   write("bzImage", strings.Repeat("\x00", 0x202)+"HdrS"),
		Firmware: dir,
		BootCPUs: 2,
		Memory:   Memory{BaseMB: 512},
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

// -smp carries maxcpus= only for a ceiling above the boot count: none, or one equal to it, adds
// no vCPU slots.
func TestTheVCPUCeilingIsOnTheCommandLineOnlyAboveTheBootCount(t *testing.T) {
	for _, tc := range []struct {
		boot, max int
		want      string
	}{
		{2, 0, "2"},
		{2, 2, "2"},
		{1, 16, "1,maxcpus=16"},
	} {
		s := spec(t)
		s.BootCPUs, s.MaxCPUs = tc.boot, tc.max
		args, err := s.Args()
		if err != nil {
			t.Fatal(err)
		}
		if got := argValue(args, "-smp"); got != tc.want {
			t.Errorf("-smp for %d vCPUs of %d = %q, want %q", tc.boot, tc.max, got, tc.want)
		}
	}
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
		absent: []string{"memory-backend", "virtio-mem", "-incoming", "vhost-vsock", "-tpmdev", "tpm-crb", "-fw_cfg"},
	}, {
		// The TPM's backend reaches swtpm over the control channel Start made, a descriptor, so
		// QEMU opens no socket by path.
		name: "a TPM",
		set:  func(s *Spec) { s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 3, Kernel: [32]byte{0xab}} },
		want: []string{"-chardev socket,id=tpm,fd=3 -tpmdev emulator,id=tpm0,chardev=tpm -device tpm-crb,tpmdev=tpm0 "},
	}, {
		// What qboot measures, from the events the machine names: the kernel's digest in PCR 4.
		name: "a TPM's measurements for the firmware",
		set:  func(s *Spec) { s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 3, Kernel: [32]byte{0xab}} },
		want: []string{"-fw_cfg name=opt/org.spin-stack/tpm-events,string=04 0000000d ab" + strings.Repeat("00", 31) + " 4c696e7578206b65726e656c "},
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
		// One place is enough for the group its node is in.
		name: "one place",
		set:  func(s *Spec) { s.Places = []Place{{Serial: "root"}} },
		want: []string{"-object throttle-group,id=places "},
	}, {
		name: "fixed slots",
		set: func(s *Spec) {
			s.VsockCID = 7
			s.Places = []Place{{Serial: "root"}, {Serial: "home", Readonly: true}}
			s.NICs = []NIC{nic()}
		},
		want: []string{
			"vhost-vsock-pci,guest-cid=7,disable-legacy=on,addr=0x2 ",
			"virtio-rng-pci,disable-legacy=on,addr=0x3 ",
			// The only way a running VM gives memory back. Without it the answer to
			// "how is memory reclaimed?" is "the VM exits".
			"virtio-balloon-pci,id=balloon0,free-page-reporting=on,deflate-on-oom=on,disable-legacy=on,addr=0x4 ",
			"virtio-blk-pci,id=disk0,drive=slot0,serial=root,disable-legacy=on,addr=0x5 ",
			"virtio-blk-pci,id=disk1,drive=slot1,serial=home,disable-legacy=on,addr=0x6 ",
			"virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01,romfile=,disable-legacy=on,addr=0x10 ",
			// Without this QEMU copies every packet through userspace, and nothing reports it.
			"tap,id=net0,fd=3,vhost=on ",
			// A place for a disk is a filter node over an empty placeholder until one is bound,
			// read-only when the place is: the device takes its permissions at exec. A throttle
			// node in a group with no limits, so that it is a filter (placeNode).
			"-object throttle-group,id=places ",
			`{"driver":"null-co","node-name":"slot0-empty","read-zeroes":true,"size":0} `,
			`{"driver":"throttle","node-name":"slot0","file":"slot0-empty","throttle-group":"places","read-only":false,"discard":"unmap"} `,
			`{"driver":"throttle","node-name":"slot1","file":"slot1-empty","throttle-group":"places","read-only":true,"discard":"unmap"} `,
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
		// A NIC on passt is a stream of frames on the descriptor Start made, with no vhost, and the
		// guest's side of it on the kernel command line: the .network and resolv.conf as tmpfiles
		// lines, and the udev rule that starts networkd, each a credential (passtGuest).
		name: "a NIC on passt",
		set:  func(s *Spec) { s.NICs = []NIC{{Passt: "/passt", PasstFD: 4, MAC: "52:54:00:12:34:56"}} },
		want: []string{
			"-netdev stream,id=net0,server=off,addr.type=fd,addr.str=4 -device virtio-net-pci,netdev=net0,mac=52:54:00:12:34:56,romfile=,disable-legacy=on,addr=0x10 ",
			"systemd.set_credential_binary=tmpfiles.extra:" + base64.StdEncoding.EncodeToString([]byte(
				"d /run/systemd/network 0755 - - -\n"+
					`f+ /run/systemd/network/50-passt.network 0644 - - - [Match]\nName=eth0\n[Network]\nAddress=10.0.2.15/24\nGateway=10.0.2.2\n`+"\n"+
					`f+ /etc/resolv.conf 0644 - - - nameserver 10.0.2.3\n`+"\n")) + " ",
			"systemd.set_credential_binary=udev.rules.50-passt.rules:" + base64.StdEncoding.EncodeToString([]byte(
				`SUBSYSTEM=="net", ACTION=="add", KERNEL=="eth0", TAG+="systemd", ENV{SYSTEMD_WANTS}+="systemd-networkd.service"`+"\n")) + " ",
		},
		absent: []string{"vhost", "tap,"},
	}, {
		// The guest's side names the NIC passt is on: the second NIC is eth1.
		name: "a NIC on passt after a TAP",
		set:  func(s *Spec) { s.NICs = []NIC{nic(), {Passt: "/passt", PasstFD: 4, MAC: "52:54:00:12:34:56"}} },
		want: []string{"-netdev tap,id=net0,fd=3,vhost=on ", "-netdev stream,id=net1,server=off,addr.type=fd,addr.str=4 ",
			"systemd.set_credential_binary=udev.rules.50-passt.rules:" + base64.StdEncoding.EncodeToString([]byte(
				`SUBSYSTEM=="net", ACTION=="add", KERNEL=="eth1", TAG+="systemd", ENV{SYSTEMD_WANTS}+="systemd-networkd.service"`+"\n")) + " "},
	}, {
		// A machine with no NIC on passt has nothing of it on its command line.
		name:   "a TAP's guest side is not the machine's",
		set:    func(s *Spec) { s.NICs = []NIC{nic()} },
		absent: []string{"set_credential", "stream"},
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
		// Disks that arrive later are added to one controller, whatever their number, which
		// tells the guest to scan as many targets as there are places.
		name:   "hotplug disks",
		set:    func(s *Spec) { s.HotplugDisks = 4 },
		want:   []string{"-device virtio-scsi-pci,id=scsi0,max_target=3,disable-legacy=on,addr=0x1a "},
		absent: []string{"pcie-root-port"},
	}, {
		// One disk is a controller too, with the one target.
		name: "one hotplug disk",
		set:  func(s *Spec) { s.HotplugDisks = 1 },
		want: []string{"-device virtio-scsi-pci,id=scsi0,max_target=0,disable-legacy=on,addr=0x1a "},
	}, {
		// Growing a VM is virtio-mem, not ACPI DIMM slots: -m carries the ceiling and
		// no slots=, and the device holds the region between boot memory and ceiling
		// with nothing plugged until somebody asks.
		name: "a memory ceiling",
		set:  func(s *Spec) { s.Memory.BaseMB, s.Memory.CeilingMB = 2048, 8192 },
		want: []string{
			"-m 2048,maxmem=8192M ",
			"memory-backend-ram,id=mem.growth,size=6144M ",
			"virtio-mem-pci,id=vmem0,memdev=mem.growth,requested-size=0,disable-legacy=on,addr=0x1e ",
		},
		absent: []string{"slots="},
	}, {
		name:   "fixed memory",
		set:    func(s *Spec) { s.Memory.BaseMB = 2048 },
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
			"-machine pc-q35-11.2,kernel-irqchip=on,", "-accel kvm,device=/dev/fdset/1 ",
			"addr=0x2,vhostfd=5 ", "tap,id=net0,fd=3,vhost=on,vhostfd=6 ",
		},
		absent: []string{"accel=kvm"},
	}, {
		// A restore's userfaultfd comes as a descriptor too, so QEMU's user needs no access
		// to the device (qemu/patches/0010).
		name: "userfaultfd handed over",
		set: func(s *Spec) {
			s.Restore = true
			s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 4}}}}
			s.UserfaultFDSet = 1
		},
		want: []string{"-userfaultfd /dev/fdset/1 -incoming defer"},
	}, {
		// A restore that was handed no device leaves QEMU to its own.
		name:   "userfaultfd not handed",
		set:    func(s *Spec) { s.Restore = true },
		want:   []string{"-incoming defer"},
		absent: []string{"-userfaultfd"},
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
		args, err := s.Args()
		if err != nil {
			t.Fatal(err)
		}
		if got := argValue(args, "-cpu"); !strings.HasPrefix(got, want) {
			t.Errorf("-accel %s: -cpu %q, want %q first", accel, got, want)
		}
	}
}

// What a pool takes away from host goes after what every machine takes away, and the machine is
// named by nothing else: exec passes an empty element as an argument of its own, which QEMU reads
// as a file name or refuses.
func TestTheCPUTakesAwayWhatAPoolDoesNotShare(t *testing.T) {
	s := spec(t)
	s.CPUWithout = []string{"avx512f", "sse4.1", "kvm-pv-eoi"}
	args, err := s.Args()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := argValue(args, "-cpu"), "host,migratable=on,-vmx,-svm,x-consistent-cache=off,x-legacy-cpuid2-cache=off,-avx512f,-sse4.1,-kvm-pv-eoi"; got != want {
		t.Errorf("-cpu %q, want %q", got, want)
	}
	if i := slices.Index(args, ""); i >= 0 {
		t.Errorf("argument %d is empty: %q", i, args)
	}
	if slices.Contains(args, "-name") {
		t.Errorf("the machine is named: %q", args)
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
	shirudo := DefaultCmdline()
	shirudo.Lockdown = LockdownShirudo
	kernel := DefaultCmdline()
	kernel.Lockdown = LockdownKernel

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
			// The kernel forces only integrity (kernel/README.md); a guest that does not ask
			// for less reads none of the kernel's memory.
			"lockdown=confidentiality",
		},
		// No root named, no root= at all: an empty one would be a kernel looking for "".
		// Both keyrings sealed: nothing at confidentiality can enroll in them.
		absent: []string{"root=", "keyring_unsealed"},
	}, {
		// A consumer that builds its own Cmdline and has never heard of lockdown gets
		// confidentiality too.
		name: "zero value",
		c:    Cmdline{},
		want: []string{"lockdown=confidentiality"},
	}, {
		// shirudo's programs read kernel memory, so it runs at the kernel's floor, which no command
		// line can lower, and its init enrolls its certificates in both keyrings: sealed, they would
		// be sealed empty.
		name:   "shirudo",
		c:      shirudo,
		want:   []string{"bpf.keyring_unsealed=1", "dm_verity.keyring_unsealed=1"},
		absent: []string{"lockdown="},
	}, {
		// The lab's traceable kernel stays at the none it forces, and nothing enrolls in its keyrings.
		name:   "the kernel's own",
		c:      kernel,
		absent: []string{"lockdown=", "keyring_unsealed"},
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

// A Lockdown is named by what spin-machine's --lockdown reads, and a name that is none of them,
// integrity alone among them, is refused rather than read as the zero value.
func TestLockdownIsReadByName(t *testing.T) {
	for _, l := range []Lockdown{LockdownConfidentiality, LockdownShirudo, LockdownKernel} {
		b, err := l.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var got Lockdown = -1
		if err := got.UnmarshalText(b); err != nil || got != l {
			t.Errorf("%q read back as %v, %v", b, got, err)
		}
	}
	var l Lockdown
	if err := l.UnmarshalText([]byte("integrity")); err == nil || !strings.Contains(err.Error(), "none of confidentiality, kernel, shirudo") {
		t.Errorf("integrity alone: %v", err)
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
		// QEMU tries anything else as a multiboot or an ELF kernel, and this machine enters neither.
		{"a kernel that is not a bzImage", func(s *Spec) { s.Kernel = s.QEMU }},
		{"a kernel that is not there", func(s *Spec) { s.Kernel += ".absent" }},
		{"no firmware", func(s *Spec) { s.Firmware = "" }},
		// Each is written into -cpu, where a comma or an '=' is another option.
		{"a feature to take away that is two", func(s *Spec) { s.CPUWithout = []string{"avx2,+avx512f"} }},
		{"a feature to take away that sets one", func(s *Spec) { s.CPUWithout = []string{"pmu=on"} }},
		{"a feature to take away with no name", func(s *Spec) { s.CPUWithout = []string{"avx2", ""} }},
		{"no boot CPUs", func(s *Spec) { s.BootCPUs = 0 }},
		{"a vCPU ceiling below the boot count", func(s *Spec) { s.BootCPUs, s.MaxCPUs = 4, 2 }},
		{"no memory", func(s *Spec) { s.Memory.BaseMB = 0 }},
		// A ceiling below the boot size: memoryArg drops it, so without this the
		// caller gets a machine that cannot grow having asked for one that can.
		{"a memory ceiling below the boot size", func(s *Spec) { s.Memory.BaseMB, s.Memory.CeilingMB = 2048, 512 }},
		{"more disks than the slot range holds", func(s *Spec) {
			s.Places = make([]Place, maxDisks+1)
			for i := range s.Places {
				s.Places[i] = Place{Serial: fmt.Sprintf("d%d", i)}
			}
		}},
		{"more NICs than the slot range holds", func(s *Spec) {
			s.NICs = make([]NIC, maxNICs+1)
			for i := range s.NICs {
				s.NICs[i] = nic()
			}
		}},
		{"more hotplug disks than the controller addresses", func(s *Spec) { s.HotplugDisks = maxHotplugDisks + 1 }},
		{"a negative number of hotplug disks", func(s *Spec) { s.HotplugDisks = -1 }},
		// A disk is bound to its place by serial, and the serial names its nodes.
		{"a place for a disk with no serial", func(s *Spec) { s.Places = []Place{{}} }},
		{"a serial QEMU would take as more options", func(s *Spec) { s.Places = []Place{{Serial: "x,addr=0x2"}} }},
		{"a serial longer than virtio-blk's 20 bytes", func(s *Spec) { s.Places = []Place{{Serial: strings.Repeat("a", 21)}} }},
		{"two places with one serial", func(s *Spec) { s.Places = []Place{{Serial: "a"}, {Serial: "a"}} }},
		// Held until a monitor says what to do: with none it never starts.
		{"no monitor", func(s *Spec) { s.Monitors = nil }},
		// A TPM is its state: a binary or a channel without one is a TPM asked for by halves.
		{"a TPM with no state", func(s *Spec) { s.TPM = TPM{SWTPM: "/swtpm", FD: 5} }},
		{"a TPM's channel with no state", func(s *Spec) { s.TPM = TPM{FD: 5} }},
		{"a TPM with no swtpm", func(s *Spec) { s.TPM = TPM{State: "/tpm", FD: 5} }},
		{"a TPM whose channel is stderr", func(s *Spec) { s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 2, Kernel: [32]byte{1}} }},
		// A TPM measures what boots, from digests the caller made: none is a boot it cannot measure.
		{"a TPM with no digest of the kernel", func(s *Spec) { s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 5} }},
		{"a TPM with no digest of the initrd", func(s *Spec) {
			s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 5, Kernel: [32]byte{1}}
			s.Initrd = "/initrd"
		}},
		{"a restore's digest of an initrd it does not boot", func(s *Spec) {
			s.Restore = true
			s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 5, Initrd: [32]byte{1}}
		}},
		{"a digest of an initrd the machine does not boot", func(s *Spec) {
			s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 5, Kernel: [32]byte{1}, Initrd: [32]byte{1}}
		}},
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
		// The kernel keeps the higher of two lockdown levels and the last of two keyring parameters,
		// so one in Extra would make the machine half of one Lockdown and half of another.
		{"an extra lockdown, raising a shirudo machine", func(s *Spec) {
			s.Cmdline.Lockdown = LockdownShirudo
			s.Cmdline.Extra = []string{"quiet lockdown=confidentiality"}
		}},
		{"an extra lockdown, claiming a level below confidentiality", func(s *Spec) { s.Cmdline.Extra = []string{"lockdown=integrity"} }},
		{"an extra keyring unsealed at confidentiality", func(s *Spec) { s.Cmdline.Extra = []string{"bpf.keyring_unsealed=1"} }},
		{"an extra keyring sealed again on a shirudo machine", func(s *Spec) {
			s.Cmdline.Lockdown = LockdownShirudo
			s.Cmdline.Extra = []string{"dm_verity.keyring_unsealed=0"}
		}},
		// The kernel reads a dash in a parameter's name as an underscore.
		{"an extra keyring unsealed with a dash", func(s *Spec) {
			s.Cmdline.Lockdown = LockdownKernel
			s.Cmdline.Extra = []string{"dm-verity.keyring_unsealed"}
		}},
		{"a Lockdown that is none of the machine's", func(s *Spec) { s.Cmdline.Lockdown = LockdownKernel + 1 }},
		{"a command line the kernel would truncate", func(s *Spec) {
			s.Cmdline.Extra = []string{strings.Repeat("x", maxCmdline)}
		}},
		{"/dev/kvm in a set nobody gave", func(s *Spec) { s.KVMFDSet = 1 }},
		{"/dev/kvm for an emulator", func(s *Spec) {
			s.Accel, s.KVMFDSet = "tcg", 1
			s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 3}}}}
		}},
		{"/dev/userfaultfd in a set nobody gave", func(s *Spec) { s.Restore, s.UserfaultFDSet = true, 1 }},
		{"/dev/userfaultfd for a boot, which never loads fast", func(s *Spec) {
			s.UserfaultFDSet = 1
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
		// A NIC is a TAP or passt: both is a NIC whose backend is a guess.
		{"a NIC on passt and a TAP", func(s *Spec) { s.NICs = []NIC{{TapFD: 3, Passt: "/passt", PasstFD: 4, MAC: "52:54:00:00:00:01"}} }},
		{"a NIC on passt with /dev/vhost-net", func(s *Spec) { s.NICs = []NIC{{VhostFD: 3, Passt: "/passt", PasstFD: 4, MAC: "52:54:00:00:00:01"}} }},
		{"a NIC on passt whose socket is stderr", func(s *Spec) { s.NICs = []NIC{{Passt: "/passt", PasstFD: 2, MAC: "52:54:00:00:00:01"}} }},
		{"a passt socket with no passt", func(s *Spec) { s.NICs = []NIC{{TapFD: 3, PasstFD: 4, MAC: "52:54:00:00:00:01"}} }},
		// Each passt gives its guest the same address and route.
		{"two NICs on passt", func(s *Spec) {
			s.NICs = []NIC{{Passt: "/passt", PasstFD: 3, MAC: "52:54:00:00:00:01"}, {Passt: "/passt", PasstFD: 4, MAC: "52:54:00:00:00:02"}}
		}},
		{"a NIC on passt with no MAC", func(s *Spec) { s.NICs = []NIC{{Passt: "/passt", PasstFD: 3}} }},
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

	// A line too long says how much of it the kernel keeps: COMMAND_LINE_SIZE less its NUL.
	long := spec(t)
	long.Cmdline.Extra = []string{strings.Repeat("x", maxCmdline)}
	if err := long.validate(); err == nil || !strings.Contains(err.Error(), "the kernel keeps 2047 ") {
		t.Errorf("an over-long command line: %v", err)
	}

	// A kernel refused is named, for whoever reads the error and not the code.
	s := spec(t)
	s.Kernel = s.QEMU
	if _, err := s.Args(); err == nil || !strings.Contains(err.Error(), s.QEMU+" is neither a bzImage") {
		t.Errorf("Args of a kernel that is not a bzImage: %v", err)
	}
	// Long enough to hold a setup header and without its magic.
	s = spec(t)
	s.Kernel = filepath.Join(t.TempDir(), "kernel")
	if err := os.WriteFile(s.Kernel, make([]byte, 0x400), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Args(); err == nil || !strings.Contains(err.Error(), "is neither a bzImage") {
		t.Errorf("Args of a kernel with no HdrS at 0x202: %v", err)
	}
	// An ELF kernel QEMU would enter where it was linked, with no relocation note.
	if err := os.WriteFile(s.Kernel, elftest.Kernel([]uint32{xenPhys32Entry}, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Args(); err == nil || !strings.Contains(err.Error(), "no Xen ELF note 19") {
		t.Errorf("Args of a vmlinux that cannot move: %v", err)
	}
	// Its relocation note cut short by the segment that holds it, as QEMU would not read it.
	k := elftest.Kernel([]uint32{xenPhys32Entry, xenPhys32Reloc}, nil)
	binary.LittleEndian.PutUint64(k[64+32:], 28+20) // PT_NOTE's p_filesz: the first note whole, the second's descriptor 8 bytes short
	if err := os.WriteFile(s.Kernel, k, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Args(); err == nil || !strings.Contains(err.Error(), "no Xen ELF note 19") {
		t.Errorf("Args of a vmlinux whose relocation note is truncated: %v", err)
	}

	// And the specs the cases above are made from have to pass, or every one of them
	// would pass for the wrong reason.
	s = spec(t)
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
	s.Memory.BaseMB = 1
	s.BootCPUs = 1
	s.VsockCID, s.VsockFD = 3, 3
	s.NICs = []NIC{{TapFD: 3, VhostFD: 3, MAC: "52:54:00:00:00:01"}, {Passt: "/passt", PasstFD: 3, MAC: "52:54:00:00:00:02"}}
	if err := s.validate(); err != nil {
		t.Fatalf("the least each bound allows does not validate: %v", err)
	}

	// A console is a path or a descriptor set, and either alone is not half of a conflict.
	s = spec(t)
	s.Console = "stdio"
	if err := s.validate(); err != nil {
		t.Fatalf("a console by path does not validate: %v", err)
	}
	s = spec(t)
	s.FDSets = []FDSet{{ID: 1, FDs: []FD{{Num: 10}}}}
	s.ConsoleFDSet = 1
	if err := s.validate(); err != nil {
		t.Fatalf("a console by descriptor does not validate: %v", err)
	}
}

// The kernel prints to the console the spec names: a chardev by path, a descriptor set, or none.
func TestTheConsoleIsWhatTheSpecNames(t *testing.T) {
	for _, tc := range []struct {
		console string
		fdset   int
		want    string
	}{
		{"file:/run/console.log", 0, "file:/run/console.log"},
		{"", 1, "chardev:serial0"},
		{"", 0, "none"},
	} {
		s := spec(t)
		s.Console, s.ConsoleFDSet = tc.console, tc.fdset
		if tc.fdset != 0 {
			s.FDSets = []FDSet{{ID: tc.fdset, FDs: []FD{{Num: 10}}}}
		}
		args, err := s.Args()
		if err != nil {
			t.Fatal(err)
		}
		if got := argValue(args, "-serial"); got != tc.want {
			t.Errorf("console %q, descriptor set %d: -serial %q, want %q", tc.console, tc.fdset, got, tc.want)
		}
	}
}

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

// What a machine with a TPM measures: the kernel's digest in PCR 4, and in PCR 9 the command line QEMU hands the kernel and the initrd,
// tagged as Linux's EFI stub tags them. A machine without a TPM measures nothing.
func TestATPMMeasuresTheKernelItsCommandLineAndItsInitrd(t *testing.T) {
	s := spec(t)
	if got := s.TPMEvents(); got != nil {
		t.Errorf("a machine without a TPM measures %v", got)
	}
	// A restore's firmware never runs: its TPM needs no digests, and QEMU is handed no events.
	restore := s
	restore.Restore = true
	restore.Initrd = "/initrd"
	restore.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 3}
	if args, err := restore.Args(); err != nil || argValue(args, "-fw_cfg") != "" || restore.TPMEvents() != nil {
		t.Errorf("a restore with a TPM and no digests: %v, -fw_cfg %q, events %v", err, argValue(args, "-fw_cfg"), restore.TPMEvents())
	}
	s.TPM = TPM{SWTPM: "/swtpm", State: "/tpm", FD: 3, Kernel: [32]byte{1}, Initrd: [32]byte{2}}
	s.Initrd = "/initrd"
	args, err := s.Args()
	if err != nil {
		t.Fatal(err)
	}
	events := s.TPMEvents()
	if len(events) != 3 {
		t.Fatalf("%d events, want the kernel, the command line and the initrd: %v", len(events), events)
	}
	want := []struct {
		pcr, typ uint32
		digest   [32]byte
		data     string
	}{
		{4, EvIPL, [32]byte{1}, "Linux kernel"},
		{9, EvEventTag, sha256.Sum256([]byte(argValue(args, "-append"))), "\xed\x22\x3b\x8f\x1a\x00\x00\x00LOADED_IMAGE::LoadOptions\x00"},
		{9, EvEventTag, [32]byte{2}, "\xec\x22\x3b\x8f\x0d\x00\x00\x00Linux initrd\x00"},
	}
	for i, w := range want {
		e := events[i]
		if e.PCR != w.pcr || e.Type != w.typ || e.Digest != w.digest || string(e.Data) != w.data {
			t.Errorf("event %d is %+v, want PCR %d, type %#x, digest %x, data %q", i, e, w.pcr, w.typ, w.digest, w.data)
		}
	}
	if !strings.Contains(argValue(args, "-fw_cfg"), fmt.Sprintf("09 00000006 %x ", sha256.Sum256([]byte(argValue(args, "-append"))))) {
		t.Errorf("-fw_cfg %s does not hand qboot the command line's event", argValue(args, "-fw_cfg"))
	}
}

// A file's digest is SHA-256 of its bytes, and a file that cannot be read says which.
func TestFileSHA256IsTheDigestOfTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(p, []byte("a kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := FileSHA256(p); err != nil || got != sha256.Sum256([]byte("a kernel")) {
		t.Errorf("FileSHA256 = %x, %v", got, err)
	}
	if _, err := FileSHA256(p + ".missing"); err == nil || !strings.Contains(err.Error(), "vmlinux.missing") {
		t.Errorf("a missing file: %v", err)
	}
	if _, err := FileSHA256(filepath.Dir(p)); err == nil || !strings.Contains(err.Error(), "hashing") {
		t.Errorf("a directory: %v", err)
	}
}
