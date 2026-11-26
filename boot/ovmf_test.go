// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/lab"
	"github.com/spin-stack/machine/machine"
)

// Experiment (exp/ovmf-uki-arm, never merged): what a generic UEFI measured boot, OVMF with a
// TPM booting a UKI through systemd-stub, costs against qboot, interleaved on one host.
//
// The release kernel cannot be in a UKI: it is a vmlinux entered through PVH, with no bzImage and
// no CONFIG_EFI. So the UKI arms boot Ubuntu's generic kernel, and a qboot arm boots the same
// bzImage as their control: the OVMF arms less that control is OVMF + sd-stub + the firmware's
// measurements, the kernel held constant.
//
//	SPIN_OVMF_ARM=<dir>  the extracted packages (lab.yml on this branch): ovmf-generic,
//	                     systemd-boot-efi, systemd-ukify, python3-pefile, linux-image-*
//	REPS=<n>             boots per arm (default 20)
func TestOVMFAgainstQboot(t *testing.T) {
	pkgs := os.Getenv("SPIN_OVMF_ARM")
	if pkgs == "" {
		t.Skip("set SPIN_OVMF_ARM to the extracted packages")
	}
	out := releaseDir(t)
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := rel.Spec("kvm")
	if err != nil {
		t.Fatal(err)
	}
	swtpm, err := rel.SWTPM()
	if err != nil {
		if swtpm, err = exec.LookPath("swtpm"); err != nil {
			t.Fatal("no swtpm in the release or on the PATH")
		}
	}
	reps := envInt(t, "REPS", 20)
	dir := t.TempDir()
	socks, err := os.MkdirTemp("/tmp", "ov")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socks) })

	glob := func(pattern string) string {
		m, _ := filepath.Glob(filepath.Join(pkgs, pattern))
		if len(m) != 1 {
			t.Fatalf("%s matches %v in %s", pattern, m, pkgs)
		}
		return m[0]
	}
	code := glob("ovmf-generic/usr/share/OVMF/OVMF_CODE_4M.fd")
	vars := glob("ovmf-generic/usr/share/OVMF/OVMF_VARS_4M.fd")
	stub := glob("systemd-boot-efi/usr/lib/systemd/boot/efi/linuxx64.efi.stub")
	ukify := glob("systemd-ukify/usr/lib/systemd/ukify")
	pefile := glob("python3-pefile/usr/lib/python3/dist-packages")
	ubuntu := glob("linux-image-*/boot/vmlinuz-*")
	if v, err := os.ReadFile(filepath.Join(pkgs, "SHA256SUMS")); err == nil {
		t.Logf("packages:\n%s", v)
	}

	probe := probeInitrd(t)
	padded := filepath.Join(dir, "padded.cpio")
	writePadded(t, probe, padded, 60<<20)
	uki := func(name, initrd string) string {
		path := filepath.Join(dir, name)
		c := exec.Command("python3", ukify, "build", "--linux", ubuntu, "--initrd", initrd,
			"--cmdline", cmdline, "--stub", stub, "--output", path)
		c.Env = append(os.Environ(), "PYTHONPATH="+pefile)
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("ukify: %v\n%s", err, b)
		}
		return path
	}
	for _, f := range []string{probe, padded, ubuntu} {
		t.Logf("%s: %d bytes", filepath.Base(f), size(t, f))
	}

	arms := []*ovmfArm{
		{name: "qboot", kernel: spec.Kernel, initrd: probe},
		{name: "qboot+tpm", kernel: spec.Kernel, initrd: probe, tpm: true},
		{name: "qboot+tpm ubuntu", kernel: ubuntu, initrd: probe, tpm: true},
		{name: "ovmf+tpm uki", ovmf: true, kernel: uki("uki.efi", probe), tpm: true},
		{name: "ovmf uki (no tpm)", ovmf: true, kernel: uki("uki.efi", probe)},
		{name: "ovmf-trim+tpm uki", ovmf: true, trim: true, kernel: uki("uki.efi", probe), tpm: true},
		{name: "qboot+tpm ubuntu 60M", kernel: ubuntu, initrd: padded, tpm: true},
		{name: "ovmf+tpm uki 60M", ovmf: true, kernel: uki("uki-60m.efi", padded), tpm: true},
	}
	for _, a := range arms {
		t.Logf("%s: kernel %s, %d bytes", a.name, filepath.Base(a.kernel), size(t, a.kernel))
	}
	b := &ovmfBench{qemu: filepath.Join(out, "bin", "qemu-system-x86_64"), firmware: spec.Firmware,
		qboot: filepath.Join(out, "qemu", "qboot.bin"), code: code, swtpm: swtpm, dir: dir, socks: socks}
	// A varstore per OVMF arm, kept across its boots as a machine's would be, after one boot
	// unmeasured: OVMF's first boot on an empty store writes its boot options.
	for i, a := range arms {
		if a.ovmf {
			a.vars = filepath.Join(dir, fmt.Sprintf("vars-%d.fd", i))
			copyFile(t, vars, a.vars)
		}
		s := b.mustBoot(t, a)
		t.Logf("%s, warm-up: %.1f ms, pre-kernel %.1f ms, %s", a.name, s.ready, s.pre, s.tpm)
		if a.ovmf && a.tpm && !measured(s.tpm) {
			t.Fatalf("%s does not measure what it claims: %s", a.name, s.tpm)
		}
	}

	ready := make([][]float64, len(arms))
	pre := make([][]float64, len(arms))
	for r := range reps {
		// Each round starts at the next arm, so no arm is always first after the one before.
		for k := range arms {
			i := (r + k) % len(arms)
			s := b.mustBoot(t, arms[i])
			ready[i] = append(ready[i], s.ready)
			pre[i] = append(pre[i], s.pre)
		}
	}

	var tab strings.Builder
	fmt.Fprintf(&tab, "%d boots each, interleaved; shape: q35, 2 vCPU, 2048 MiB, no disks/NICs; Secure Boot off\n", reps)
	fmt.Fprintf(&tab, "%-22s %9s %9s %26s %26s %26s\n", "arm", "ready p50", "pre p50",
		"Δready vs qboot", "Δready vs qboot+tpm ubuntu", "Δpre vs qboot+tpm ubuntu")
	for i, a := range arms {
		d0, _ := lab.DeltaP50(ready[0], ready[i])
		d2, _ := lab.DeltaP50(ready[2], ready[i])
		p2, _ := lab.DeltaP50(pre[2], pre[i])
		fmt.Fprintf(&tab, "%-22s %9.1f %9.1f %26s %26s %26s\n", a.name, pct(ready[i], 50), pct(pre[i], 50), d0, d2, p2)
	}
	d60, _ := lab.DeltaP50(ready[6], ready[7])
	fmt.Fprintf(&tab, "ovmf+tpm uki 60M vs qboot+tpm ubuntu 60M: %s\n", d60)
	for _, a := range arms {
		slow := 0
		for _, ms := range ready[slices.Index(arms, a)] {
			if ms > 5000 {
				slow++
			}
		}
		fmt.Fprintf(&tab, "%s: max %.0f ms, %d boots over 5 s, %d never reached SPIN-READY in %s and were retried\n",
			a.name, slices.Max(ready[slices.Index(arms, a)]), slow, a.hangs, hang)
	}
	t.Logf("SPIN-OVMF-RESULT\n%s", tab.String())
	for i, a := range arms {
		t.Logf("samples %s ready=%v pre=%v", a.name, ready[i], pre[i])
	}
}

const cmdline = "console=ttyS0 quiet loglevel=3 pci=lastbus=0 no_timer_check tsc=reliable TERM=dumb rdinit=/init"

type ovmfArm struct {
	name                 string
	ovmf, trim, tpm      bool
	kernel, initrd, vars string
	hangs                int
}

// hang is how long a boot may take before it counts as hung. On a loaded laptop most OVMF boots
// with a TPM took 12-30 s to SPIN-READY and none without one (2026-11-26), so a slow boot is a
// sample like any other and only one past this is a failure.
const hang = 120 * time.Second

// mustBoot boots a until one boot reaches SPIN-READY, counting the ones that hung.
func (b *ovmfBench) mustBoot(t *testing.T, a *ovmfArm) ovmfSample {
	t.Helper()
	var err error
	for range 5 {
		var s ovmfSample
		if s, err = b.boot(a); err == nil {
			return s
		}
		a.hangs++
		t.Logf("%s: %v", a.name, err)
	}
	t.Fatalf("%s: five boots in a row did not reach SPIN-READY: %v", a.name, err)
	return ovmfSample{}
}

type ovmfBench struct {
	qemu, firmware, qboot, code, swtpm, dir, socks string
	n                                              int
}

type ovmfSample struct {
	// ready is QEMU's exec to SPIN-READY; pre is ready less the kernel's own clock at its first
	// "Freeing unused kernel image": the time before the kernel's clock started, firmware and
	// decompression together.
	ready, pre float64
	tpm        string
}

var tpmLine = regexp.MustCompile(`SPIN-TPM [^\r\n]*eventlog=\S+\r?\n`)

var freeing = regexp.MustCompile(`SPIN-KMSG (\d+) Freeing unused kernel image`)

func measured(line string) bool {
	return strings.Contains(line, "eventlog=") && !strings.Contains(line, "eventlog=absent") &&
		!strings.Contains(line, "pcr11=none") && !strings.Contains(line, "pcr11="+strings.Repeat("0", 64))
}

func (b *ovmfBench) boot(a *ovmfArm) (ovmfSample, error) {
	b.n++
	args := []string{
		"-L", b.firmware,
		"-machine", "q35,sata=off,smbus=off",
		"-accel", "kvm", "-cpu", "host",
		"-m", "2048", "-smp", "2",
		"-nodefaults", "-display", "none", "-serial", "stdio", "-monitor", "none",
		"-kernel", a.kernel,
	}
	if a.ovmf {
		args = append(args,
			"-drive", "if=pflash,format=raw,unit=0,readonly=on,file="+b.code,
			"-drive", "if=pflash,format=raw,unit=1,file="+a.vars)
		if a.trim {
			for _, k := range []string{"IPv4Support", "IPv6Support", "IPv4PXESupport", "IPv6PXESupport",
				"VirtioNetSupport", "EFIShellSupport", "FirmwareSetupSupport", "UsbStorageSupport"} {
				args = append(args, "-fw_cfg", "name=opt/org.tianocore/"+k+",string=no")
			}
		}
	} else {
		args = append(args, "-bios", b.qboot, "-initrd", a.initrd, "-append", cmdline)
	}

	r, w, err := os.Pipe()
	if err != nil {
		return ovmfSample{}, err
	}
	defer func() { _ = r.Close() }()
	qemu := exec.Command(b.qemu, args...)
	qemu.Stdout, qemu.Stderr = w, w
	qemu.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var tpm *exec.Cmd
	if a.tpm {
		// As machine.startSWTPM: swtpm on one end of a socket pair, QEMU's chardev on the other.
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			return ovmfSample{}, err
		}
		mine, theirs := os.NewFile(uintptr(fds[0]), "swtpm"), os.NewFile(uintptr(fds[1]), "qemu-tpm")
		defer func() { _ = mine.Close(); _ = theirs.Close() }()
		state := filepath.Join(b.dir, fmt.Sprintf("tpm-%d", b.n))
		if err := os.MkdirAll(state, 0o700); err != nil {
			return ovmfSample{}, err
		}
		tpm = exec.Command(b.swtpm, "socket", "--tpm2", "--tpmstate", "dir="+state+",mode=0600",
			"--ctrl", "type=unixio,clientfd=3")
		tpm.ExtraFiles = []*os.File{mine}
		qemu.ExtraFiles = []*os.File{theirs}
		qemu.Args = append(qemu.Args, "-chardev", "socket,id=tpm,fd=3",
			"-tpmdev", "emulator,id=tpm0,chardev=tpm", "-device", "tpm-crb,tpmdev=tpm0")
	}

	t0 := time.Now()
	if tpm != nil {
		if err := tpm.Start(); err != nil {
			return ovmfSample{}, fmt.Errorf("starting swtpm: %w", err)
		}
		defer func() { _ = tpm.Process.Kill(); _ = tpm.Wait() }()
	}
	if err := qemu.Start(); err != nil {
		return ovmfSample{}, fmt.Errorf("starting QEMU: %w", err)
	}
	_ = w.Close()
	v := &vm{cmd: qemu, stdout: r, t0: t0}
	defer func() {
		if pgid, err := syscall.Getpgid(qemu.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = qemu.Wait()
	}()
	ms, err := v.wait("SPIN-READY", hang)
	if err != nil {
		return ovmfSample{}, fmt.Errorf("%w\nQEMU: %q", err, qemu.Args)
	}
	s := ovmfSample{ready: ms}
	deadline := time.Now().Add(10 * time.Second)
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		if m := tpmLine.Find(v.out); m != nil {
			s.tpm = strings.TrimSpace(string(m))
			break
		}
		_ = v.stdout.SetReadDeadline(deadline)
		n, err := v.stdout.Read(buf)
		v.out = append(v.out, buf[:n]...)
		if err != nil {
			break
		}
	}
	if m := freeing.FindSubmatch(v.out); m != nil {
		us, _ := strconv.ParseFloat(string(m[1]), 64)
		s.pre = ms - us/1000
	}
	return s, nil
}

// writePadded is the probe's initrd and a second archive holding n random bytes, as a real
// initrd's size is (spin's is ~62 MB): random, so nothing on the way compresses it.
func writePadded(t *testing.T, probe, path string, n int) {
	t.Helper()
	head, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, n)
	rng := rand.NewChaCha8([32]byte{1})
	_, _ = rng.Read(data)
	pad := newcArchive([]cpioEntry{{name: "pad", mode: 0o100644, data: data}})
	if err := os.WriteFile(path, append(head, pad...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
