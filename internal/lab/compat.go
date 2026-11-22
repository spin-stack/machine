// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spin-stack/machine/machine"
)

// CompatRow is what one release's checkpoint does on this machine.
type CompatRow struct {
	Release string
	// Resumed is whether a checkpoint the release saved went on running here; Moved, that this
	// machine refused it (machine.CanResume), so none of the release's checkpoints is meant to.
	// NoBzImage is a release from before v20261119.02, which carries only the vmlinux the machine
	// entered through PVH then: nothing here boots it, so nothing checks it.
	Resumed, Moved, NoBzImage bool
	// Why is the failure, or the refusal: every difference between the two machines.
	Why string
}

// Compat is boot:compat over the releases, newest first, until one whose machine is not this one:
// a host that skipped releases resumes a checkpoint from any of them, not only the one before. A
// release's checkpoint is taken at spin's shape on its own machine - its QEMU, kernel and firmware,
// started by the spin-machine of the commit it was made from - and this machine refuses it or
// resumes it (resume); the first it refuses, by its differences, is where the machine moved, and
// every older release is past it. It stops as well at the first release with no bzImage, which no
// spin-machine since v20261119.02 boots, and every older release has none either. For each release it also holds the other direction: a checkpoint written by here's
// QEMU is one CanResume refuses on a release whose QEMU is older (QEMU loads what an older version
// wrote, never the reverse).
//
// resume is ResumeHere on here with previous as the release before and previousCLI as its
// spin-machine: the refusal when this machine refused the checkpoint, or the failure when it did
// not go on running.
func Compat(here string, tags []string, src Sources, resume func(previous, previousCLI string) (refused string, err error), log io.Writer) ([]CompatRow, error) {
	hereQEMU, err := qemuVersion(filepath.Join(here, "bin", "qemu-system-x86_64"))
	if err != nil {
		return nil, err
	}
	var rows []CompatRow
	for _, tag := range tags {
		dir, err := src.Release(tag)
		if err != nil {
			return nil, err
		}
		if !fileExists(filepath.Join(dir, partFiles["kernel"])) {
			rows = append(rows, CompatRow{Release: tag, NoBzImage: true})
			break
		}
		relQEMU, err := qemuVersion(filepath.Join(dir, "bin", "qemu-system-x86_64"))
		if err != nil {
			return nil, err
		}
		if err := oneWay(hereQEMU, relQEMU); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		// The release's machine is the one its own spin-machine starts: here's command line is
		// here's machine, and it gave v20261120.02's QEMU a property only v20261120.03's has
		// (virtio-scsi-pci's max_target), which that QEMU refused (2026-11-21).
		cli := filepath.Join(src.Cache, "releases", tag, "spin-machine")
		if _, err := src.releaseSpinMachine(dir, cli); err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(log, "compat: a checkpoint of %s, resumed here\n", tag)
		refused, err := resume(dir, cli)
		switch {
		case refused != "":
			rows = append(rows, CompatRow{Release: tag, Moved: true, Why: refused})
			return rows, nil
		case err != nil:
			rows = append(rows, CompatRow{Release: tag, Why: err.Error()})
		default:
			rows = append(rows, CompatRow{Release: tag, Resumed: true})
		}
	}
	return rows, nil
}

// oneWay holds CanResume to QEMU's promise between here's QEMU and a release's: a release's
// checkpoint resumes here, and a checkpoint of here's newer QEMU is refused on the release's. The
// two machines are otherwise one, so the VMM is all that differs.
func oneWay(here, release string) error {
	on := func(vmm string) machine.Target {
		return machine.Target{VMM: vmm, Formats: []string{machine.FormatMappedRAM, machine.FormatStream}}
	}
	if err := machine.CanResume(machine.Saved{Format: machine.FormatMappedRAM, From: on(release)}, on(here)); err != nil {
		return fmt.Errorf("a checkpoint of %s cannot resume on this machine's %s: %w", release, here, err)
	}
	back := machine.CanResume(machine.Saved{Format: machine.FormatMappedRAM, From: on(here)}, on(release))
	if here != release && back == nil {
		return fmt.Errorf("CanResume lets a checkpoint of %s onto the older %s", here, release)
	}
	return nil
}

// qemuVersion is the QEMU at path as a checkpoint records it (machine.Saved.From.VMM): "qemu 11.1.90",
// from its own --version, which is what query-version answers.
func qemuVersion(path string) (string, error) {
	b, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("asking %s its version: %w", path, err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	v, ok := strings.CutPrefix(strings.TrimSpace(first), "QEMU emulator version ")
	if !ok {
		return "", fmt.Errorf("%s says %q, not its version", path, first)
	}
	v, _, _ = strings.Cut(v, " ")
	return "qemu " + v, nil
}

// CompatTable is the rows as the run's summary carries them.
func CompatTable(rows []CompatRow) string {
	var b strings.Builder
	b.WriteString("| release | a checkpoint of it, resumed here |\n|---|---|\n")
	for _, r := range rows {
		switch {
		case r.Moved:
			fmt.Fprintf(&b, "| %s | not meant to: its machine is not this one (%s); older releases neither |\n", r.Release, r.Why)
		case r.NoBzImage:
			fmt.Fprintf(&b, "| %s | not checked: it carries no bzImage, the only kernel this machine boots; older releases neither |\n", r.Release)
		case r.Resumed:
			fmt.Fprintf(&b, "| %s | resumed |\n", r.Release)
		default:
			fmt.Fprintf(&b, "| %s | **failed**: %s |\n", r.Release, r.Why)
		}
	}
	return b.String()
}

// ResumeHere says whether a checkpoint the release at previous saves resumes on the machine of the
// built release at here, and holds CanResume's answer to QEMU's. The guest is booted at spin's
// shape on previous's machine - its QEMU, kernel and firmware, on the command line its own
// spin-machine, previousCLI, prints and started by it - over an overlay of its base image, with a
// counter printing on its console; here's spin-machine saves it, which records the machine as that
// QEMU reports it. A machine of here's QEMU, held for a checkpoint over the same overlay, says
// whether it resumes it (machine.CanResume); if so it is restored with that record (restore
// --target), as spin hands Resume its host's, and the counter has to go on from where it was and
// the disk has to answer.
//
// When here's machine refuses the checkpoint, the machine moved and no checkpoint of previous is
// meant to resume: refused is every difference, and nothing is held.
//
// It was a test lab compat ran with `go test`, which a release tree missing a part or a renamed
// test passed: a skip, or "no tests to run", exits 0.
func ResumeHere(ctx context.Context, here, previous, previousCLI string, log io.Writer) (refused string, err error) {
	for _, f := range []string{"bin/spin-machine", "bin/qemu-img", "kernel/vmlinux", "image/rootfs.qcow2"} {
		if !fileExists(filepath.Join(here, f)) {
			return "", fmt.Errorf("no %s in %s: run spin-machine-dev build", f, here)
		}
	}
	cli := filepath.Join(here, "bin", "spin-machine")
	dir, err := os.MkdirTemp("", "compat")
	if err != nil {
		return "", fmt.Errorf("making compat's scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// The monitors in a short directory of their own: a socket's path fits in 108 bytes.
	socks, err := os.MkdirTemp("/tmp", "compat")
	if err != nil {
		return "", fmt.Errorf("making compat's socket directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(socks) }()
	overlay := filepath.Join(dir, "root.qcow2")
	if b, err := exec.CommandContext(ctx, filepath.Join(here, "bin", "qemu-img"), "create", "-q", "-f", "qcow2",
		"-b", filepath.Join(previous, "image", "rootfs.qcow2"), "-F", "qcow2", overlay).CombinedOutput(); err != nil {
		return "", fmt.Errorf("an overlay of the previous base image: %w: %s", err, b)
	}
	state := filepath.Join(dir, "state")
	cid := 3500000 + os.Getpid()%100000*10 // mutate-exempt: where this run's context ids start; any start clear of the host's own (0-2) and of another run's holds

	// machineOf is a machine of release on the overlay, its console on stdin and stdout: QEMU run
	// with the command line release's spin-machine, cli, prints, then let go by its start, or held
	// for a checkpoint with restore.
	machineOf := func(release, cli, sock string, n int, restore bool) (*console, error) {
		flags := append(spinsFlags(), "--qmp", sock, "--vsock-cid", fmt.Sprint(cid+n), // mutate-exempt: any two context ids apart, clear of the host's own (0-2), are two machines' own
			"--disk", overlay, "--disk-format", "qcow2", "--init", "/bin/sh")
		mode := []string{"args", "--release", release}
		if restore {
			flags = append(flags, "--restore")
		} else {
			mode = append(mode, "--held")
		}
		b, err := exec.CommandContext(ctx, cli, append(mode, flags...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("spin-machine args: %w", err)
		}
		var args []string
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			args = append(args, strings.TrimSuffix(strings.TrimPrefix(l, "  "), " \\"))
		}
		g, err := startConsole(append(args, spinsNIC(fmt.Sprintf("52:54:00:00:01:%02x", n))...))
		if err != nil || restore {
			return g, err
		}
		if o, err := exec.CommandContext(ctx, cli, "start", "--release", release, "--qmp", sock,
			"--disk", overlay, "--disk-format", "qcow2").CombinedOutput(); err != nil {
			g.kill()
			return nil, fmt.Errorf("spin-machine start: %w: %s\n%s", err, o, g.said())
		}
		return g, nil
	}

	aSock := filepath.Join(socks, "a")
	a, err := machineOf(previous, previousCLI, aSock, 1, false)
	if err != nil {
		return "", err
	}
	defer a.kill()
	if err := a.typeLine("mount -t proc proc /proc; ( i=0; while :; do i=$((i+1)); echo TICK-$i; sleep 0.2; done ) &"); err != nil {
		return "", err
	}
	if err := a.await(regexp.MustCompile(`TICK-5\b`), 60*time.Second); err != nil {
		return "", err
	}
	if o, err := exec.CommandContext(ctx, cli, "save", "--qmp", aSock, "--to", state).CombinedOutput(); err != nil {
		return "", fmt.Errorf("saving: %w: %s\n%s", err, o, a.said())
	}
	last := a.lastTick()
	a.kill()

	bSock := filepath.Join(socks, "b")
	b, err := machineOf(here, cli, bSock, 2, true)
	if err != nil {
		return "", err
	}
	defer b.kill()
	var saved machine.Saved
	if raw, err := os.ReadFile(state + ".json"); err != nil || json.Unmarshal(raw, &saved) != nil {
		return "", fmt.Errorf("what save wrote beside the state: %v", err)
	}
	// The record of the machine held, kept to hand to restore as spin hands Resume its host's.
	target := state + ".target"
	hereTarget, err := recordOf(ctx, cli, bSock, target, b)
	if err != nil {
		return "", err
	}
	var refusal *machine.Refusal
	if errors.As(machine.CanResume(saved, hereTarget), &refusal) {
		var ds []string
		for _, d := range refusal.Differences {
			ds = append(ds, d.String())
		}
		_, _ = fmt.Fprintf(log, "compat: the machine of %s is not this one:\n  %s\n", previous, strings.Join(ds, "\n  "))
		return strings.Join(ds, "; "), nil
	}
	// The overlay is over previous's base, not here's, which is where restore looks unless told.
	if o, err := exec.CommandContext(ctx, cli, "restore", "--release", here, "--qmp", bSock, "--from", state, "--target", target,
		"--disk", overlay, "--disk-format", "qcow2", "--disk-under", filepath.Join(previous, "image", "rootfs.qcow2")).CombinedOutput(); err != nil {
		return "", fmt.Errorf("restoring: %w: %s\n%s", err, o, b.said())
	}
	if err := b.await(regexp.MustCompile(fmt.Sprintf(`TICK-%d\b`, last+3)), 30*time.Second); err != nil { // mutate-exempt: how far past the saved tick; any number ahead of it says the counter went on
		return "", err
	}
	// Read through the disk the guest had before it was saved: a file it never opened, so the read
	// reaches the restored virtio-blk and the overlay behind it.
	if err := b.typeLine("echo READ-$(wc -c < /etc/os-release)"); err != nil {
		return "", err
	}
	if err := b.await(regexp.MustCompile(`READ-[1-9][0-9]*`), 30*time.Second); err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(log, "compat: a checkpoint of %s, saved at TICK-%d, went on counting on this machine\n", previous, last)
	return "", nil
}

// spinsFlags are the flags of spin's shape, shape's defaults, but for its vsock and NIC, which
// every machine here is given apart.
func spinsFlags() []string {
	var flags []string
	for _, o := range shape {
		switch o.Name {
		case "cpus", "max-cpus", "memory", "max-memory", "hotplug-disks":
			flags = append(flags, "--"+o.Name, o.Default)
		}
	}
	return flags
}

// spinsNIC is the NIC spin gives a workspace, as Spec.Args writes it with spin's MTU, on a hub port
// in place of spin's TAP: spin-machine has no flag for a NIC, and a TAP needs CAP_NET_ADMIN.
func spinsNIC(mac string) []string {
	return []string{"-netdev", "hubport,id=net0,hubid=0",
		"-device", "virtio-net-pci,netdev=net0,mac=" + mac + ",romfile=,disable-legacy=on,addr=0x10,host_mtu=1450"}
}

// recordOf is the record of the machine whose monitor is sock, once QEMU has made it, as cli's
// `spin-machine target` prints it, which is also written to path.
func recordOf(ctx context.Context, cli, sock, path string, g *console) (machine.Target, error) {
	for deadline := time.Now().Add(20 * time.Second); !fileExists(sock); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			return machine.Target{}, fmt.Errorf("QEMU never opened its monitor %s:\n%s", sock, g.said())
		}
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, cli, "target", "--qmp", sock)
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return machine.Target{}, fmt.Errorf("spin-machine target: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var t machine.Target
	if err := json.Unmarshal(raw, &t); err != nil {
		return machine.Target{}, fmt.Errorf("reading the record spin-machine target printed: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return machine.Target{}, fmt.Errorf("keeping the machine's record: %w", err)
	}
	return t, nil
}

// console is one QEMU whose console is its stdin and stdout.
type console struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// w is the console's end QEMU writes to, and read is closed once out holds all it wrote.
	w    *io.PipeWriter
	read chan struct{}
	mu   sync.Mutex
	out  bytes.Buffer
}

func startConsole(args []string) (*console, error) {
	g := &console{cmd: exec.Command(args[0], args[1:]...), read: make(chan struct{})}
	stdin, err := g.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("the console's stdin: %w", err)
	}
	g.stdin = stdin
	r, w := io.Pipe()
	g.w, g.cmd.Stdout, g.cmd.Stderr = w, w, w
	if err := g.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting QEMU: %w", err)
	}
	go func() {
		defer close(g.read)
		buf := make([]byte, 4096) // mutate-exempt: the read size; the console keeps every byte whatever it is
		for {
			n, err := r.Read(buf)
			g.mu.Lock()
			g.out.Write(buf[:n])
			g.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return g, nil
}

// typeLine is a line on the console. The guest's shell is not reading it when QEMU starts, so the
// first bytes are lost: blank lines go first, each costing only a prompt.
func (g *console) typeLine(line string) error {
	if _, err := io.WriteString(g.stdin, "\n\n\n\n\n\n\n\n\n\n"+line+"\n"); err != nil {
		return fmt.Errorf("typing on the console: %w", err)
	}
	return nil
}

func (g *console) said() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.out.String()
}

// await is the console printing what matches re, or an error with what it did print.
func (g *console) await(re *regexp.Regexp, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for !re.MatchString(g.said()) {
		if time.Now().After(deadline) {
			return fmt.Errorf("the console never said %s in %s:\n%s", re, timeout, tail(g.said(), 2000)) // mutate-exempt: how much of the console the error carries
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// lastTick is the highest TICK the console has printed.
func (g *console) lastTick() int {
	last := 0                                                                                // mutate-exempt: read after the console said TICK-5, so any start below 5 is the same answer
	for _, m := range regexp.MustCompile(`TICK-(\d+)`).FindAllStringSubmatch(g.said(), -1) { // mutate-exempt: any negative count is every match
		if n, _ := strconv.Atoi(m[1]); n > last {
			last = n
		}
	}
	return last
}

// kill ends QEMU, and returns once the console holds everything it said: a QEMU that refused its
// command line said why on its way out, and the failure carries it.
func (g *console) kill() {
	if g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_ = g.cmd.Wait()
		_ = g.w.Close()
		<-g.read
	}
}
