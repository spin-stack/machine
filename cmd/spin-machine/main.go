// SPDX-License-Identifier: Apache-2.0

// Command spin-machine starts a VM of the machine this repository defines, and
// prints what that machine is.
//
// It exists for two reasons. The first is that a definition nothing executes
// drifts: the package next door says which chipset, which slots and which kernel
// command line, and this is what proves those still boot. The second is that it
// gives this repository a way to run its own artefacts without borrowing a
// runtime from somewhere else — `boot` is what `spin-machine-dev shell` runs.
//
// It is also how a check of this machine is done by hand. Whatever a caller does
// to a running machine through the contract this repository promises — a disk given
// while it runs, a save to a file — is a command here, so trying it needs no
// script of its own.
//
// It is not a container runtime and does not want to become one. `boot` starts QEMU
// through machine.Start and stays beside it until it exits, and the commands that act on
// a running VM are each one machine.VM call, over its monitor.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spin-stack/machine/machine"
)

// main is the only place the process ends. A command returns what happened and main decides
// what it means: help asked for is not a failure, and boot's exitCode is QEMU's.
func main() {
	err := run(os.Args[1:])
	var code exitCode
	if errors.As(err, &code) {
		os.Exit(int(code))
	}
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "spin-machine: %v\n", err)
		os.Exit(1)
	}
}

const usage = `spin-machine - the machine this repository builds

Usage:
  spin-machine boot    [flags]   start a VM and wait for it; with --held or --restore, start it held
  spin-machine args    [flags]   print the QEMU command line boot would run
  spin-machine start   [flags]   boot a VM started --held, with its disk
  spin-machine restore [flags]   load a saved state into a VM started --restore, if it is the machine the state was saved on
  spin-machine save    [flags]   stop a running VM and write its state, and the machine it was saved on, to files
  spin-machine attach  [flags]   give a running VM a disk while it runs
  spin-machine detach  [flags]   take it back, once the guest has let it go
  spin-machine memory  [flags]   grow or shrink a running VM between its memory and its ceiling
  spin-machine cpu     [flags]   print the CPU the guest is shown and the named models this host runs
  spin-machine target  [flags]   print the machine's record as JSON, which restore --target takes

spin-machine <command> -h lists a command's flags.
`

func run(argv []string) error {
	if len(argv) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}
	cmd, argv := argv[0], argv[1:]

	switch cmd {
	case "boot", "args":
		var o machineFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		spec, err := o.spec()
		if err != nil {
			return err
		}
		if cmd == "boot" {
			return boot(spec, o)
		}
		// The machine's command line as the flags make it, which a caller with --qmp can run
		// as it is. boot hands QEMU the same machine with its monitors, devices and console
		// as descriptors, and a monitor of machine.Start's own: shown at descriptor 3 when
		// the flags name no monitor, since a machine has to have one.
		if len(spec.Monitors) == 0 {
			spec.Monitors = []machine.Monitor{{FD: 3}}
		}
		args, err := spec.Args()
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(append([]string{spec.QEMU}, args...), " \\\n  "))
		return nil
	case "start":
		var o startFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		return start(o)
	case "cpu":
		var qmp string
		if err := parse(cmd, argv, func(fs *flag.FlagSet) {
			fs.StringVar(&qmp, "qmp", "", "a machine's QMP socket (required)")
		}); err != nil {
			return err
		}
		return cpuReport(qmp, os.Stdout)
	case "target":
		var qmp string
		if err := parse(cmd, argv, func(fs *flag.FlagSet) {
			fs.StringVar(&qmp, "qmp", "", "a machine's QMP socket (required)")
		}); err != nil {
			return err
		}
		return targetReport(qmp, os.Stdout)
	case "attach":
		var o attachFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		return attach(o)
	case "detach":
		var o detachFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		return detach(o)
	case "memory":
		var o memoryFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		return memory(o, os.Stdout)
	case "save":
		var o saveFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		return save(o)
	case "restore":
		var o restoreFlags
		if err := parse(cmd, argv, o.register); err != nil {
			return err
		}
		return restore(o)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// parse gives each command its own flags, so `save -h` lists what save takes and not
// what a boot does.
//
// The flag package prints a parse error itself and then returns it, and main prints
// what it is returned: the same error twice, the second time under a usage listing
// nobody asked for. So the package prints nothing, help goes to stdout because it was
// asked for, and an error is returned once with the command it belongs to.
func parse(cmd string, argv []string, register func(*flag.FlagSet)) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	register(fs)
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(os.Stdout)
			fs.Usage()
			return err
		}
		return fmt.Errorf("%s: %w (spin-machine %s -h lists its flags)", cmd, err, cmd)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%s: unexpected argument %q", cmd, fs.Arg(0))
	}
	return nil
}

// machineFlags is everything boot and args let a caller choose. It is
// deliberately thin: the machine's shape comes from the package, and what is left is
// which release to boot, how big, and what to put in it.
type machineFlags struct {
	release string

	qemu     string
	kernel   string
	initrd   string
	firmware string
	bios     string

	disks diskFlags

	memoryMB     int
	maxMemMB     int
	cpuModel     string
	accel        string
	cpus         int
	maxCPUs      int
	hotplugDisks int

	vsockCID int
	qmp      string
	console  string
	held     bool
	restore  bool

	init    string
	root    string
	profile bool
	extra   string
}

func (o *machineFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.release, "release", "_output", "an unpacked release tree; every path below defaults out of it")
	fs.StringVar(&o.qemu, "qemu", "", "QEMU binary (default: <release>/bin/qemu-system-x86_64)")
	fs.StringVar(&o.kernel, "kernel", "", "kernel image, a bzImage (default: <release>/kernel/bzImage)")
	fs.StringVar(&o.initrd, "initrd", "", "initrd (default: none)")
	fs.StringVar(&o.firmware, "firmware", "", "firmware directory (default: <release>/qemu)")
	fs.StringVar(&o.bios, "bios", "", "the BIOS, a file in the firmware directory (default: "+machine.DefaultBIOS+"; bios-256k.bin is SeaBIOS)")

	o.disks.register(fs)

	fs.IntVar(&o.memoryMB, "memory", 2048, "guest memory in MiB")
	fs.IntVar(&o.maxMemMB, "max-memory", 0, "ceiling this VM may grow to and shrink back from, in MiB, through virtio-mem (0: fixed memory)")
	fs.StringVar(&o.cpuModel, "cpu", "", "CPU model shown to the guest (default: host; name one, e.g. Skylake-Server-v4, to let VMs move between machines)")
	fs.StringVar(&o.accel, "accel", "", "kvm (default) or tcg; tcg runs the release's TCG build unless --qemu names another")
	fs.IntVar(&o.cpus, "cpus", 2, "boot vCPUs")
	fs.IntVar(&o.maxCPUs, "max-cpus", 0, "vCPU hotplug ceiling (0: no hotplug)")
	fs.IntVar(&o.hotplugDisks, "hotplug-disks", 0, "disks attach can give the VM at once while it runs")

	fs.IntVar(&o.vsockCID, "vsock-cid", 0, "give the machine a vhost-vsock device with this context id")
	fs.StringVar(&o.qmp, "qmp", "", "listen for QMP on this Unix socket; attach, detach and save connect to it")
	fs.BoolVar(&o.held, "held", false, "start held, and boot only when start is run (needs --qmp)")
	fs.BoolVar(&o.restore, "restore", false, "start held for a checkpoint, which restore loads (needs --qmp)")
	fs.StringVar(&o.console, "console", "mon:stdio",
		"QEMU chardev for the serial console, e.g. file:/path or unix:/path,server=on,wait=off; empty for none")

	fs.StringVar(&o.init, "init", "", "what the kernel runs as PID 1 inside the guest (default: the kernel's, /sbin/init)")
	fs.StringVar(&o.root, "root", "/dev/vda", "block device the kernel mounts as root; empty to stay on the initrd")
	fs.BoolVar(&o.profile, "profile", false, "boot with initcall profiling: silent console, full ring buffer")
	fs.StringVar(&o.extra, "append", "", "extra kernel command line arguments")
}

// spec turns the flags into a machine, filling in every path from the release
// tree so that the common case is no flags at all.
//
// The tree is opened rather than assumed, so a release missing a part says so
// here, naming the file — and not three seconds later as a QEMU that exits for
// want of an option ROM.
func (o *machineFlags) spec() (machine.Spec, error) {
	rel, err := machine.OpenRelease(o.release)
	if err != nil {
		return machine.Spec{}, err
	}
	or := func(v, fallback string) string {
		if v != "" {
			return v
		}
		return fallback
	}

	s, err := rel.Spec(o.accel)
	if err != nil {
		return machine.Spec{}, err
	}
	s.QEMU = or(o.qemu, s.QEMU)
	s.Kernel = or(o.kernel, s.Kernel)
	s.Firmware = or(o.firmware, s.Firmware)
	s.BIOS = o.bios
	s.Initrd = o.initrd
	s.CPU = o.cpuModel
	s.BootCPUs = o.cpus
	s.MaxCPUs = o.maxCPUs
	s.Memory = machine.Memory{
		BaseMB:    o.memoryMB,
		CeilingMB: o.maxMemMB,
	}
	s.HotplugDisks = o.hotplugDisks
	s.VsockCID = o.vsockCID
	if o.qmp != "" {
		s.Monitors = []machine.Monitor{{Socket: o.qmp}}
	}
	s.Restore = o.restore
	s.Console = o.console

	hasDisk := o.disks.path != "-"
	if hasDisk {
		s.Places = []machine.Place{{Serial: o.disks.serial, Readonly: o.disks.readonly}}
	}

	c := machine.DefaultCmdline()
	c.Init = o.init
	if hasDisk {
		c.Root = o.root
		c.RootReadonly = o.disks.readonly
	}
	if o.profile {
		c = c.Profiling()
	}
	if o.extra != "" {
		c.Extra = append(c.Extra, strings.Fields(o.extra)...)
	}
	// No console for the caller to read is no console for the kernel to print to.
	if o.console == "" {
		c.Console = ""
	}
	s.Cmdline = c

	return s, nil
}
