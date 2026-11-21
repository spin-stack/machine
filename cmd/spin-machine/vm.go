// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spin-stack/spin-machine/machine"
)

// diskFlags is the one disk the commands that bind or attach one take.
type diskFlags struct {
	path     string
	format   string
	readonly bool
	serial   string
}

func (o *diskFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.path, "disk", "", "disk image and its backing chain (default: the base image under a throwaway overlay; - for no disk)")
	fs.StringVar(&o.format, "disk-format", "qcow2", "format of the disk image; never guessed")
	fs.BoolVar(&o.readonly, "disk-readonly", false, "open the disk read-only, and mount root ro")
	fs.StringVar(&o.serial, "disk-serial", "root", "serial the guest finds the disk by")
}

// open opens the disk's chain, top first, for a machine.Disk: the machine reads no image by
// path, so the chain the image's header names is read here (chain).
//
// No --disk is the base image under an overlay made in a directory of its own and unlinked
// once open: nothing to clean up after, and the base is never opened for writing (rule 1 in
// CLAUDE.md).
func (o diskFlags) open(release string) (machine.Disk, func(), error) {
	path, format := o.path, o.format
	if path == "" {
		rel, err := machine.OpenRelease(release)
		if err != nil {
			return machine.Disk{}, nil, err
		}
		base, err := rel.Rootfs()
		if err != nil {
			return machine.Disk{}, nil, err
		}
		dir, err := os.MkdirTemp("", "spin-machine-scratch")
		if err != nil {
			return machine.Disk{}, nil, fmt.Errorf("making a directory for the scratch overlay: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path, format = filepath.Join(dir, "overlay.qcow2"), "qcow2"
		// #nosec G204 -- the release's qemu-img and paths this command chose
		if out, err := exec.Command(rel.QEMUImg(), "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", base, path).CombinedOutput(); err != nil {
			return machine.Disk{}, nil, fmt.Errorf("making a scratch overlay over %s: %w: %s", base, err, out)
		}
	}
	links, err := chain(path, format)
	if err != nil {
		return machine.Disk{}, nil, err
	}
	d := machine.Disk{Serial: o.serial, Readonly: o.readonly, Locking: !o.readonly}
	closeAll := func() {
		for _, img := range d.Images {
			_ = img.File.Close()
		}
	}
	for j, img := range links {
		flag := os.O_RDONLY
		if j == 0 && !o.readonly {
			flag = os.O_RDWR
		}
		f, err := os.OpenFile(img.path, flag, 0) // mutate-exempt: the mode is read only with O_CREATE, which flag never has
		if err != nil {
			closeAll()
			return machine.Disk{}, nil, fmt.Errorf("opening %s: %w", img.path, err)
		}
		d.Images = append(d.Images, machine.Image{File: f, Format: img.format})
	}
	return d, closeAll, nil
}

// bindCommand is the command boot runs beside QEMU to boot it: not one for a person to type.
const bindCommand = "_bind"

// boot becomes QEMU: it returns only if the exec failed.
//
// Exec and not a child. A child leaves this process between whoever started the machine
// and the machine: a signal meant for the VM reaches a Go program that dies of it and
// leaves QEMU running, orphaned, holding its disks — the boot benchmarks once left one
// QEMU per boot that way. After an exec the pid the caller holds is QEMU's, its exit
// status is QEMU's, and the terminal, the process group and the signals are QEMU's
// without anything here to pass them on.
//
// QEMU starts held (Spec.Restore), so unless the caller asked for a held machine to boot
// later, something has to boot it: this command again as bindCommand, started beside the exec
// with the same flags and given a monitor of its own, which binds the disk, lets the vCPUs go
// and exits.
func boot(s machine.Spec, o machineFlags, argv []string) error {
	qemu, args, helper, err := bootCommand(s, o, argv)
	if err != nil {
		return err
	}
	if helper != nil {
		if err := helper.Start(); err != nil {
			return fmt.Errorf("starting what boots the machine: %w", err)
		}
	}
	// #nosec G204 -- a binary and arguments this caller chose
	if err := syscall.Exec(qemu, append([]string{qemu}, args...), os.Environ()); err != nil {
		return fmt.Errorf("executing %s: %w", qemu, err)
	}
	return nil
}

// bootCommand is what boot runs: QEMU's path and command line, and the helper that boots it
// unless the machine is to stay held. The command line and QEMU first: a machine that cannot
// start starts nothing beside it.
func bootCommand(s machine.Spec, o machineFlags, argv []string) (qemu string, args []string, helper *exec.Cmd, err error) {
	if (o.held || o.restore) && o.qmp == "" {
		return "", nil, nil, errors.New("boot: a machine started held is booted or restored over --qmp, and none was given")
	}
	var sock string
	if !o.held && !o.restore {
		dir, err := os.MkdirTemp("/tmp", "sm")
		if err != nil {
			return "", nil, nil, fmt.Errorf("making a directory for the boot's monitor: %w", err)
		}
		sock = filepath.Join(dir, "m")
		s.Monitors = append(s.Monitors, machine.Monitor{Socket: sock})
	}
	if args, err = s.Args(); err != nil {
		return "", nil, nil, err
	}
	if qemu, err = exec.LookPath(s.QEMU); err != nil {
		return "", nil, nil, fmt.Errorf("finding QEMU: %w", err)
	}
	if sock == "" {
		return qemu, args, nil, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", nil, nil, fmt.Errorf("finding this command to boot the machine with: %w", err)
	}
	// #nosec G204 -- this binary, with the flags it was given
	helper = exec.Command(self, append([]string{bindCommand, "--qmp", sock}, withoutQMP(argv)...)...)
	helper.Stderr = os.Stderr // mutate-exempt: where the helper's own error is printed, which only a person reads
	return qemu, args, helper, nil
}

// withoutQMP is the boot's flags without its own --qmp, which the helper's replaces.
func withoutQMP(argv []string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		switch a := argv[i]; {
		case a == "--qmp" || a == "-qmp":
			i++
		case len(a) > 6 && (a[:6] == "--qmp=" || a[:5] == "-qmp="):
		default:
			out = append(out, a)
		}
	}
	return out
}

// bindAndBoot is bindCommand: boot binds the machine it exec'd, then exits. A machine it could
// not boot is ended, so that boot fails where the cause is rather than as a QEMU held forever.
func bindAndBoot(o machineFlags) error {
	m, err := dial(o.qmp, 30*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	_ = os.RemoveAll(filepath.Dir(o.qmp))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err = bootWith(ctx, m, o.disks, o.release)
	if err != nil {
		_ = m.Quit(context.WithoutCancel(ctx))
	}
	return err
}

func bootWith(ctx context.Context, m machine.VM, d diskFlags, release string) error {
	var disks []machine.Disk
	if d.path != "-" {
		disk, closeAll, err := d.open(release)
		if err != nil {
			return err
		}
		defer closeAll()
		disks = append(disks, disk)
	}
	return m.Boot(ctx, disks...)
}

// dial connects to a machine's QMP socket, waiting for QEMU to have made it: a command run
// right after boot otherwise races QEMU's start.
func dial(socket string, wait time.Duration) (*machine.QEMU, error) {
	deadline := time.Now().Add(wait)
	for {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err == nil {
			m, err := machine.Attach(c)
			if err != nil {
				_ = c.Close()
				return nil, fmt.Errorf("talking to the machine at %s: %w", socket, err)
			}
			return m, nil
		}
		if time.Now().After(deadline) || !(errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)) {
			return nil, fmt.Errorf("connecting to the machine at %s: %w", socket, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type startFlags struct {
	qmp     string
	release string
	disks   diskFlags
	timeout time.Duration
}

func (o *startFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "QMP socket of a VM started with boot --held (required)")
	fs.StringVar(&o.release, "release", "_output", "the release tree: its base image, under a throwaway overlay, is the disk when --disk is not given")
	o.disks.register(fs)
	fs.DurationVar(&o.timeout, "timeout", time.Minute, "how long binding the disk may take")
}

// start boots a machine started ahead of it, with its disk: what a pool of machines does when
// one is asked for.
func start(o startFlags) error {
	if o.qmp == "" {
		return errors.New("start: --qmp is required")
	}
	m, err := dial(o.qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	return bootWith(ctx, m, o.disks, o.release)
}

type saveFlags struct {
	qmp      string
	to       string
	channels int
	timeout  time.Duration
}

func (o *saveFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "the running VM's QMP socket (required)")
	fs.StringVar(&o.to, "to", "", "where to write the state; what the machine needs besides it goes to <to>.json (required)")
	fs.IntVar(&o.channels, "channels", 0, fmt.Sprintf("how many threads write the guest's memory (QEMU's multifd); none given is %d, machine.DefaultChannels", machine.DefaultChannels))
	fs.DurationVar(&o.timeout, "timeout", 5*time.Minute, "how long the save may take")
}

// savedPath is where save writes what restore needs besides the state (machine.Saved).
func savedPath(state string) string { return state + ".json" }

// save stops a running VM, writes everything needed to resume it, and ends it: a checkpoint.
// A VM that kept running after its state was captured would have written to its disk, and
// the state would no longer describe it.
func save(o saveFlags) error {
	if o.qmp == "" || o.to == "" {
		return errors.New("save: --qmp and --to are required")
	}
	f, err := os.OpenFile(o.to, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s for the state: %w", o.to, err)
	}
	defer func() { _ = f.Close() }()
	m, err := dial(o.qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	saved, err := m.Checkpoint(ctx, f, machine.Save{Channels: o.channels}, func(context.Context) error { return nil })
	if err != nil {
		return err
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return fmt.Errorf("recording the checkpoint: %w", err)
	}
	if err := os.WriteFile(savedPath(o.to), b, 0o600); err != nil {
		return fmt.Errorf("recording the checkpoint: %w", err)
	}
	fmt.Fprintf(os.Stderr, "saved to %s\n", o.to)
	// The state is complete; what is left is a stopped VM nobody will resume in place.
	return m.Quit(ctx)
}

type restoreFlags struct {
	qmp      string
	from     string
	fast     bool
	channels int
	release  string
	disks    diskFlags
	timeout  time.Duration
}

func (o *restoreFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "QMP socket of a VM started with boot --restore (required)")
	fs.StringVar(&o.from, "from", "", "the state save wrote (required)")
	fs.BoolVar(&o.fast, "fast", false, "run the guest once its devices are in and read its memory as it touches it")
	fs.IntVar(&o.channels, "channels", 0, fmt.Sprintf("how many threads read the guest's memory (QEMU's multifd); any number reads a state saved with any, and none given is %d, machine.DefaultChannels, or one for --fast or a stream state", machine.DefaultChannels))
	fs.StringVar(&o.release, "release", "_output", "the release tree: its base image, under a throwaway overlay, is the disk when --disk is not given")
	o.disks.register(fs)
	fs.DurationVar(&o.timeout, "timeout", 5*time.Minute, "how long the load may take")
}

// restore loads a saved state into a machine started for one, with its disk, and runs it. A
// state with no <from>.json beside it is one a spin-machine before machine.Saved wrote, in the
// stream format.
func restore(o restoreFlags) error {
	if o.qmp == "" || o.from == "" {
		return errors.New("restore: --qmp and --from are required")
	}
	saved := machine.Saved{Format: machine.FormatStream}
	switch b, err := os.ReadFile(savedPath(o.from)); {
	case err == nil:
		if err := json.Unmarshal(b, &saved); err != nil {
			return fmt.Errorf("reading %s: %w", savedPath(o.from), err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("reading what the checkpoint needs besides its state: %w", err)
	}
	f, err := os.Open(o.from)
	if err != nil {
		return fmt.Errorf("opening the state: %w", err)
	}
	defer func() { _ = f.Close() }()
	m, err := dial(o.qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	var disks []machine.Disk
	if o.disks.path != "-" {
		disk, closeAll, err := o.disks.open(o.release)
		if err != nil {
			return err
		}
		defer closeAll()
		disks = append(disks, disk)
	}
	if err := m.Resume(ctx, f, saved, machine.Load{Fast: o.fast, Channels: o.channels}, disks...); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "restored %s\n", o.from)
	return nil
}

type attachFlags struct {
	qmp     string
	release string
	disks   diskFlags
}

func (o *attachFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "the running VM's QMP socket (required)")
	fs.StringVar(&o.release, "release", "_output", "the release tree: its base image, under a throwaway overlay, is the disk when --disk is not given")
	o.disks.register(fs)
}

// attach gives a running machine a disk, at the first target free on its hotplug controller.
// It returns when QEMU has the device; whether the guest saw it is read from the guest.
func attach(o attachFlags) error {
	if o.qmp == "" || o.disks.path == "" || o.disks.path == "-" {
		return errors.New("attach: --qmp and --disk are required")
	}
	d, closeAll, err := o.disks.open(o.release)
	if err != nil {
		return err
	}
	defer closeAll()
	m, err := dial(o.qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	if err := m.AttachDisk(context.Background(), d); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "attached %s as %s\n", o.disks.path, d.Serial)
	return nil
}

type detachFlags struct {
	qmp     string
	serial  string
	timeout time.Duration
}

func (o *detachFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "the running VM's QMP socket (required)")
	fs.StringVar(&o.serial, "disk-serial", "", "serial of the disk attach gave the VM (required)")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "how long QEMU has to report the disk gone")
}

// detach takes a disk back and waits for QEMU to say it has gone. A device QEMU never reports
// gone is the failure worth seeing, so it is an error and not a return on the request.
func detach(o detachFlags) error {
	if o.qmp == "" || o.serial == "" {
		return errors.New("detach: --qmp and --disk-serial are required")
	}
	m, err := dial(o.qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	start := time.Now()
	if err := m.DetachDisk(ctx, o.serial); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "detached %s in %.1fs\n", o.serial, time.Since(start).Seconds())
	return nil
}

type memoryFlags struct {
	qmp     string
	totalMB int
	timeout time.Duration
}

func (o *memoryFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "the running VM's QMP socket (required)")
	fs.IntVar(&o.totalMB, "to", 0, "MiB the guest is to have in all, between its --memory and its --max-memory (required)")
	fs.DurationVar(&o.timeout, "timeout", 60*time.Second, "how long the guest has to get there")
}

// memory asks for a size and waits for the guest to reach it. It prints the size the guest had
// when it stopped and how long that took, also when it did not get there: a guest that cannot
// give memory back - it sits in blocks the kernel will not offline - is the answer to the
// question a shrink asks, and it is an error only because the size asked for was not reached.
func memory(o memoryFlags, w io.Writer) error {
	if o.qmp == "" || o.totalMB <= 0 {
		return errors.New("memory: --qmp and --to are required")
	}
	m, err := dial(o.qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	start := time.Now()
	if err := m.SetMemory(ctx, o.totalMB); err != nil {
		return err
	}
	for {
		have, err := m.Memory(ctx)
		if err != nil {
			fmt.Fprintf(w, "the guest had not reached %d MiB after %d ms\n", o.totalMB, time.Since(start).Milliseconds())
			return err
		}
		if have == o.totalMB {
			fmt.Fprintf(w, "%d MiB in %d ms\n", have, time.Since(start).Milliseconds())
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cpuReport prints the CPU a machine shows its guest and the named models its host can run,
// which is what an installation's hosts report to pick a fleet's model from (machine.Baseline).
func cpuReport(qmp string, w io.Writer) error {
	if qmp == "" {
		return errors.New("cpu: --qmp is required")
	}
	m, err := dial(qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cpu, err := m.CPU(ctx)
	if err != nil {
		return err
	}
	models, err := m.Models(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		CPU    machine.CPU     `json:"cpu"`
		Models []machine.Model `json:"models"`
	}{cpu, models})
}
