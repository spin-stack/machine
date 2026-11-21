// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/lab"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// Whether the machine's firmware runs it, against a diagnostic initrd: qboot boots, publishes
// vmgenid's table and survives a restore with the guest reseeding, and SeaBIOS, the fallback,
// still boots. What qboot saved over SeaBIOS is recorded at machine.DefaultBIOS; where the
// time before the kernel goes now is `task boot:firmware-stages`.
//
// Stock qboot cannot run this machine. Its ACPI linker-loader implements ALLOCATE, ADD_POINTER and
// ADD_CHECKSUM and stops on anything else, and vmgenid needs WRITE_POINTER to tell QEMU
// where the GUID lives. The symptom is a vCPU halted inside the firmware with not one byte
// on the console, not even earlyprintk. The qboot built beside QEMU carries the command
// (qemu/qboot/write-pointer.patch), and this asserts it: the table published and a restored
// guest reseeded.
//
// The initrd is diagnostic input, not a release artefact, because what runs as PID 1 is not
// this repository's business: SPIN_PROBE_INITRD's, or boot/probeinit built into one
// (probe_initrd_test.go). Its /init must mount proc, print any dmesg line matching vmgenid,
// print SPIN-READY, stay alive, and keep printing new dmesg output so a reseed after restore is
// visible.
//
//	SPIN_FIRMWARE_PROBE=1        run at all
//	SPIN_PROBE_INITRD=<cpio>     a diagnostic initrd instead of probeinit's
//
// Both firmwares are in the tree's qemu/: bios-256k.bin, the SeaBIOS QEMU ships, and qboot.bin,
// built beside it with qemu/qboot/'s patches. SPIN_QBOOT names another qboot to try.
// A relative path there resolves against this package's directory and not the repository
// root, so `_output/...` on the command line looks right and points at boot/_output.
func TestFirmwareRuns(t *testing.T) {
	if os.Getenv("SPIN_FIRMWARE_PROBE") == "" {
		t.Skip("set SPIN_FIRMWARE_PROBE=1: this boots dozens of VMs")
	}
	out := releaseDir(t)
	// The kernel the machine boots: SeaBIOS enters the bzImage through linuxboot_dma.bin, and
	// qboot reads it out of fw_cfg itself.
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}

	bios := map[string]string{
		"seabios": filepath.Join(out, "qemu", "bios-256k.bin"),
		"qboot":   firmwareFile(t, "SPIN_QBOOT", filepath.Join(out, "qemu", "qboot.bin")),
	}
	p := &probe{
		out:      out,
		firmware: spec.Firmware,
		kernel:   spec.Kernel,
		initrd:   probeInitrd(t),
		bios:     bios,
		dir:      t.TempDir(),
	}
	if p.socks, err = os.MkdirTemp("/tmp", "fw"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(p.socks) })

	// qboot boots the machine at all, with vmgenid off: a firmware that cannot do this is not
	// slow, it is broken, and separating the two is why there are boots before any timing.
	if ms := p.mustReach(t, "qboot", noVMGenID, "SPIN-READY"); ms > 0 {
		t.Logf("qboot, no vmgenid: %.1f ms to SPIN-READY", ms)
	}

	p.mustPublishVMGenID(t)

	// SeaBIOS is kept as the fallback a machine can be told to boot (--bios bios-256k.bin),
	// and a fallback that no longer boots the machine is not one.
	if ms := p.mustReach(t, "seabios", withVMGenID, "SPIN-READY"); ms > 0 {
		t.Logf("seabios, vmgenid on: %.1f ms to SPIN-READY", ms)
	}
}

const (
	withVMGenID = true
	noVMGenID   = false

	cold     = false
	incoming = true
)

type probe struct {
	out, firmware, kernel, initrd, dir string
	// socks holds the monitors' sockets, apart from dir: a socket's path fits in 108 bytes.
	socks string
	bios  map[string]string
	n     int
}

// vm is one QEMU.
type vm struct {
	cmd    *exec.Cmd
	stdout *os.File
	out    []byte
	t0     time.Time
	qmp    string
}

// start launches QEMU, held for a checkpoint to be resumed into when restore is set.
func (p *probe) start(t *testing.T, variant string, gen, restore bool) *vm {
	t.Helper()
	// Numbered: a probe starts dozens, and a reused path is a QMP connection to the previous
	// one's corpse.
	p.n++
	sock := filepath.Join(p.socks, fmt.Sprintf("qmp-%d.sock", p.n))
	// Bound here and handed over, as machine.Start does, so the socket is there before QEMU
	// runs and machine.Dial needs no wait for it.
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	monitor, err := l.File()
	_ = l.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = monitor.Close() }()

	// The machine line is the probe's own, not machine.Spec's: this compares firmware, so
	// the disks, NICs and vsock a real machine carries are absent on purpose — every device
	// is work inside the interval, and work that is identical in both variants only adds
	// variance. It is also why these numbers are not a machine's boot time. So machine.Start,
	// which starts a Spec, does not start it.
	args := []string{
		"-L", p.firmware,
		"-machine", "q35,sata=off,smbus=off",
		"-accel", "kvm", "-cpu", "host",
		"-m", "2048", "-smp", "2",
		"-nodefaults", "-display", "none", "-serial", "stdio", "-monitor", "none",
		"-chardev", "socket,id=qmp,fd=3,server=on,wait=off", "-mon", "chardev=qmp,mode=control",
		"-bios", p.bios[variant],
		"-kernel", p.kernel,
		"-initrd", p.initrd,
		"-append", "console=ttyS0 quiet loglevel=3 pci=lastbus=0 no_timer_check " +
			"tsc=reliable TERM=dumb rdinit=/init",
	}
	if gen {
		args = append(args, "-device", "vmgenid,guid=auto")
	}
	if restore {
		args = append(args, "-incoming", "defer")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("console pipe: %v", err)
	}
	cmd := exec.Command(filepath.Join(p.out, "bin", "qemu-system-x86_64"), args...)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.ExtraFiles = []*os.File{monitor}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// t0 before Start, so exec'ing QEMU and the firmware are both inside the interval. They
	// are the two things this test exists to compare.
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("launching QEMU: %v", err)
	}
	_ = w.Close()
	return &vm{cmd: cmd, stdout: r, t0: t0, qmp: sock}
}

// wait reads the console until marker appears, and returns when it did. A deadline on the
// descriptor rather than a goroutine and a channel: there is one reader, and it is this one.
func (v *vm) wait(marker string, timeout time.Duration) (float64, error) {
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 65536)
	for {
		if err := v.stdout.SetReadDeadline(deadline); err != nil {
			return 0, fmt.Errorf("setting the console deadline: %w", err)
		}
		n, err := v.stdout.Read(buf)
		v.out = append(v.out, buf[:n]...)
		if strings.Contains(string(v.out), marker) {
			return float64(time.Since(v.t0).Microseconds()) / 1000, nil
		}
		if err != nil {
			return 0, fmt.Errorf("never saw %q in %s; console was:\n%s",
				marker, timeout, tail(v.out, 800))
		}
	}
}

func (v *vm) close() {
	if pgid, err := syscall.Getpgid(v.cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = v.cmd.Wait()
	_ = v.stdout.Close()
}

func (p *probe) reach(t *testing.T, variant string, gen bool, marker string, timeout time.Duration) (float64, error) {
	t.Helper()
	v := p.start(t, variant, gen, cold)
	defer v.close()
	return v.wait(marker, timeout)
}

func (p *probe) mustReach(t *testing.T, variant string, gen bool, marker string) float64 {
	t.Helper()
	ms, err := p.reach(t, variant, gen, marker, 8*time.Second)
	if err != nil {
		t.Fatalf("%s firmware (vmgenid=%v): %v", variant, gen, err)
	}
	return ms
}

// mustPublishVMGenID boots qboot, checkpoints the machine, resumes it into a new QEMU with a
// fresh GUID, and requires the guest to say it noticed. The checkpoint and the resume are
// machine.QEMU's, the ones spin takes and resumes workspaces with.
//
// The reseed is the whole reason the device is on the machine's command line: two guests
// resumed from one checkpoint share its memory, and that memory contains the state of the
// random pool. A firmware that boots and quietly fails to publish the table
// produces two VMs generating the same "random" bytes, and nothing on either of them
// reports a fault.
func (p *probe) mustPublishVMGenID(t *testing.T) {
	t.Helper()
	v := p.start(t, "qboot", withVMGenID, cold)
	ms, err := v.wait("SPIN-READY", 8*time.Second)
	if err != nil {
		v.close()
		t.Fatalf("qboot: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(v.out)), "vmgenid") {
		v.close()
		t.Fatalf("qboot booted in %.1f ms but the guest saw no VMGENID table; "+
			"console was:\n%s", ms, tail(v.out, 800))
	}
	t.Logf("qboot, vmgenid on: %.1f ms to SPIN-READY, table present", ms)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	state, err := os.Create(filepath.Join(p.dir, "state"))
	if err != nil {
		v.close()
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	src, err := machine.Dial(ctx, v.qmp, qmp.Observer{})
	if err != nil {
		v.close()
		t.Fatal(err)
	}
	saved, _, err := src.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
	_ = src.Close()
	v.close()
	if err != nil {
		t.Fatalf("checkpointing qboot's machine: %v\nconsole was:\n%s", err, tail(v.out, 800))
	}

	r := p.start(t, "qboot", withVMGenID, incoming)
	defer r.close()
	dst, err := machine.Dial(ctx, r.qmp, qmp.Observer{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	here, err := dst.Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Resume(ctx, state, saved, here, machine.LoadOptions{}); err != nil {
		t.Fatalf("resuming qboot's machine: %v", err)
	}
	if _, err := r.wait("crng reseeded due to virtual machine fork", 20*time.Second); err != nil {
		t.Fatalf("the restored guest never reseeded, so the GUID did not reach it "+
			"— every VM resumed from one checkpoint would share its random pool: %v", err)
	}
	t.Log("restored guest reseeded: the WRITE_POINTER path carries the GUID")
}

// firmwareFile takes the override if there is one and the built firmware otherwise, and
// skips rather than failing when neither is there: this compares firmware no release carries,
// so a checkout without it is not a broken checkout.
func firmwareFile(t *testing.T, name, dflt string) string {
	t.Helper()
	if v := os.Getenv(name); v != "" {
		return envFile(t, name)
	}
	if _, err := os.Stat(dflt); err != nil {
		t.Skipf("no %s — run: spin-machine-dev build qemu (or set %s)", dflt, name)
	}
	return dflt
}

// envFile fails rather than skipping: a path given explicitly and not there is a mistake in
// the invocation, and skipping would hide it.
func envFile(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	abs, err := filepath.Abs(v)
	if err != nil {
		t.Fatalf("resolving %s=%q: %v", name, v, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("%s=%s: %v\n(a relative path here resolves against %s, not the repository root)",
			name, abs, err, mustCwd(t))
	}
	return abs
}

func mustCwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return d
}

// pct is lab.Percentile in the shape a format argument needs: one value, p in percent.
func pct(v []float64, p int) float64 {
	x, _ := lab.Percentile(v, float64(p)/100)
	return x
}
