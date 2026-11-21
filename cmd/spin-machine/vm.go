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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// diskFlags is the one disk the commands that bind or attach one take.
type diskFlags struct {
	path     string
	format   string
	readonly bool
	serial   string
	under    []string
}

func (o *diskFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.path, "disk", "", "disk image and its backing chain (default: the base image under a throwaway overlay; - for no disk)")
	fs.StringVar(&o.format, "disk-format", "qcow2", "format of the disk image; never guessed")
	fs.BoolVar(&o.readonly, "disk-readonly", false, "open the disk read-only, and mount root ro")
	fs.StringVar(&o.serial, "disk-serial", "root", "serial the guest finds the disk by")
	fs.Func("disk-under", "a file or directory the disk's backing images may be in, never opened for writing; repeatable (default: the release's base image)", func(s string) error {
		o.under = append(o.under, s)
		return nil
	})
}

// open opens the disk's chain, top first, for a machine.Disk (machine.OpenDisk).
//
// No --disk is the base image under an overlay made in a directory of its own and unlinked
// once open: nothing to clean up after, and the base is never opened for writing (rule 1 in
// CLAUDE.md).
func (o diskFlags) open(release string) (machine.Disk, error) {
	d := machine.DiskPath{Serial: o.serial, Readonly: o.readonly, Path: o.path, Format: o.format, Under: o.under}
	if d.Path != "" {
		if len(d.Under) == 0 {
			// The release's base when there is one: a disk with no backing file, raw or one
			// attached by hand, needs no release at all.
			if base, err := baseOf(release); err == nil {
				d.Under = []string{base}
			}
		}
		return machine.OpenDisk(d)
	}
	rel, err := machine.OpenRelease(release)
	if err != nil {
		return machine.Disk{}, err
	}
	base, err := rel.Rootfs()
	if err != nil {
		return machine.Disk{}, err
	}
	d.Under = []string{base}
	dir, err := os.MkdirTemp("", "spin-machine-scratch")
	if err != nil {
		return machine.Disk{}, fmt.Errorf("making a directory for the scratch overlay: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	d.Path, d.Format = filepath.Join(dir, "overlay.qcow2"), "qcow2"
	// #nosec G204 -- the release's qemu-img and paths this command chose
	if out, err := exec.Command(rel.QEMUImg(), "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", base, d.Path).CombinedOutput(); err != nil {
		return machine.Disk{}, fmt.Errorf("making a scratch overlay over %s: %w: %s", base, err, out)
	}
	return machine.OpenDisk(d)
}

// baseOf is the base image of the release at dir.
func baseOf(dir string) (string, error) {
	rel, err := machine.OpenRelease(dir)
	if err != nil {
		return "", err
	}
	return rel.Rootfs()
}

// boot starts the machine and stays beside it until QEMU exits, then exits as QEMU did
// (exitCode). It boots the machine unless the caller asked for one held, which start or restore
// lets go over --qmp.
//
// A parent and not an exec, because machine.Start is how a machine is started: it hands QEMU its
// monitors, devices and console as descriptors and knows how the process ended. A signal meant
// for the VM reaches this process, which stops QEMU (machine.Process.Stop) before it exits, and
// a SIGKILL of this process alone reaches QEMU through Pdeathsig: no QEMU is left orphaned
// holding its disks, as the boot benchmarks once left one per boot.
func boot(s machine.Spec, o machineFlags) error {
	if (o.held || o.restore) && o.qmp == "" {
		return errors.New("boot: a machine started held is booted or restored over --qmp, and none was given")
	}
	signals, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	ctx, cancelStart := context.WithTimeout(signals, time.Minute)
	defer cancelStart()
	p, err := machine.Start(ctx, s, machine.StartOptions{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Cmd: func(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }, // mutate-exempt: TestAKilledBootTakesItsQEMU holds it, from a child whose coverage mutate does not see
	})
	if err != nil {
		return err
	}
	if !o.held && !o.restore {
		if err := bootWith(ctx, p, o.disks, o.release); err != nil {
			p.Stop(context.WithoutCancel(ctx))
			return err
		}
	}
	cancelStart()
	select {
	case <-p.Exited():
	case <-signals.Done():
		p.Stop(context.Background())
	}
	return exitOf(p.Wait())
}

// exitCode is a command's end that main makes the process's exit status, saying nothing: what
// QEMU had to say it has said on stderr already.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

// exitOf is QEMU's end as this process's: its status, or 128 and the signal that ended it, as a
// shell has it.
func exitOf(e machine.Exit) error {
	if ws, ok := e.State.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return exitCode(128 + int(ws.Signal()))
	}
	if code := e.State.ExitCode(); code != 0 {
		return exitCode(code)
	}
	return nil
}

func bootWith(ctx context.Context, m machine.VM, d diskFlags, release string) error {
	var disks []machine.Disk
	if d.path != "-" {
		disk, err := d.open(release)
		if err != nil {
			return err
		}
		defer func() { _ = disk.Close() }() // the machine holds its own copies
		disks = append(disks, disk)
	}
	return m.Boot(ctx, disks...)
}

// handshakeTimeout bounds QEMU's greeting and qmp_capabilities. boot binds --qmp before QEMU
// runs, so a client that connects early waits here for QEMU's start, a few hundred milliseconds.
const handshakeTimeout = 30 * time.Second

// dial connects to a machine's QMP socket, waiting for boot to have bound it: a command run right
// after boot otherwise races boot's own start.
func dial(socket string, wait time.Duration) (*machine.QEMU, error) {
	deadline := time.Now().Add(wait)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
		m, err := machine.Dial(ctx, socket, qmp.Observer{})
		cancel()
		if err == nil || time.Now().After(deadline) || !(errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)) {
			return m, err
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
	var told timeline
	saved, cost, err := m.Checkpoint(ctx, f, machine.SaveOptions{Channels: o.channels, Phases: told.tell}, func(context.Context) error { return nil })
	told.print(os.Stderr)
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
	fmt.Fprintf(os.Stderr, "saved to %s: %d MiB of RAM in %d ms, the guest paused %d ms\n",
		o.to, cost.RAM>>20, cost.Total.Milliseconds(), cost.Paused.Milliseconds()) // mutate-exempt: a line for a person, whose MiB no caller reads
	// The state is complete; what is left is a stopped VM nobody will resume in place.
	return m.Quit(ctx)
}

// timeline is what a checkpoint or a resume told, printed for a person once it is over: printed
// as it happens, a resume's lines would be on restore-first's clock.
type timeline []machine.Phase

func (l *timeline) tell(p machine.Phase) { *l = append(*l, p) }

// print writes each phase on a line, at its time from the first.
func (l timeline) print(w io.Writer) {
	for _, p := range l {
		at := float64(p.When().Sub(l[0].When()).Microseconds()) / 1000
		what := strings.TrimPrefix(fmt.Sprintf("%T", p), "machine.")
		switch p := p.(type) {
		case machine.RAMWritten:
			what += fmt.Sprintf(" %d MiB", p.Bytes>>20) // mutate-exempt: a line for a person, whose MiB no caller reads
		case machine.Sealed:
			what += fmt.Sprintf(" in %s", p.Took)
		}
		fmt.Fprintf(w, "%9.2f ms  %s\n", at, what)
	}
}

type restoreFlags struct {
	qmp      string
	from     string
	target   string
	fast     bool
	channels int
	release  string
	disks    diskFlags
	timeout  time.Duration
}

func (o *restoreFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.qmp, "qmp", "", "QMP socket of a VM started with boot --restore (required)")
	fs.StringVar(&o.from, "from", "", "the state save wrote (required)")
	fs.StringVar(&o.target, "target", "", "this machine's record, as spin-machine target prints it, trusted as given (default: asked of the machine before the load)")
	fs.BoolVar(&o.fast, "fast", false, "run the guest once its devices are in and read its memory as it touches it")
	fs.IntVar(&o.channels, "channels", 0, fmt.Sprintf("how many threads read the guest's memory (QEMU's multifd); any number reads a state saved with any, and none given is %d, machine.DefaultChannels, or one for --fast or a stream state", machine.DefaultChannels))
	fs.StringVar(&o.release, "release", "_output", "the release tree: its base image, under a throwaway overlay, is the disk when --disk is not given")
	o.disks.register(fs)
	fs.DurationVar(&o.timeout, "timeout", 5*time.Minute, "how long the load may take")
}

// restore loads a saved state into a machine started for one, with its disk, and runs it: the
// state and what save wrote beside it, <from>.json, which says the machine it was taken on. A
// machine that is not that one refuses it before reading the state, and each difference is said
// on a line of its own.
//
// The machine's own record is --target when given, as spin hands Resume its host's, read once:
// asked of the machine here, its 8 round trips are on the restore's clock (machine.QEMU.Resume).
func restore(o restoreFlags) error {
	if o.qmp == "" || o.from == "" {
		return errors.New("restore: --qmp and --from are required")
	}
	var saved machine.Saved
	b, err := os.ReadFile(savedPath(o.from))
	if err != nil {
		return fmt.Errorf("reading what save wrote beside the state: %w", err)
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return fmt.Errorf("reading %s: %w", savedPath(o.from), err)
	}
	var here machine.Target
	if o.target != "" {
		b, err := os.ReadFile(o.target)
		if err != nil {
			return fmt.Errorf("reading the machine's record: %w", err)
		}
		if err := json.Unmarshal(b, &here); err != nil {
			return fmt.Errorf("reading %s: %w", o.target, err)
		}
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
		disk, err := o.disks.open(o.release)
		if err != nil {
			return err
		}
		defer func() { _ = disk.Close() }() // the machine holds its own copies
		disks = append(disks, disk)
	}
	if o.target == "" {
		if here, err = m.Target(ctx); err != nil {
			return err
		}
	}
	var told timeline
	err = m.Resume(ctx, f, saved, here, machine.LoadOptions{Fast: o.fast, Channels: o.channels, Phases: told.tell}, disks...)
	told.print(os.Stderr)
	var r *machine.Refusal
	if errors.As(err, &r) {
		lines := []string{"restore: the checkpoint's machine is not this one:"}
		for _, d := range r.Differences {
			lines = append(lines, "  "+d.String())
		}
		return errors.New(strings.Join(lines, "\n"))
	}
	if err != nil {
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
	d, err := o.disks.open(o.release)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }() // the machine holds its own copies
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
		if have.Now == o.totalMB {
			fmt.Fprintf(w, "%d MiB in %d ms\n", have.Now, time.Since(start).Milliseconds())
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// targetReport prints the machine's record (machine.QEMU.Target) as JSON: what restore --target
// reads, and what a caller that resumes many checkpoints into machines of one shape reads once,
// from one machine started held, and keeps.
func targetReport(qmp string, w io.Writer) error {
	if qmp == "" {
		return errors.New("target: --qmp is required")
	}
	m, err := dial(qmp, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	here, err := m.Target(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(here)
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
	models, err := m.CPUModels(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		CPU    machine.CPU        `json:"cpu"`
		Models []machine.CPUModel `json:"models"`
	}{cpu, models})
}
