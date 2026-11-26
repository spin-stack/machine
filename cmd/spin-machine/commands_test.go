// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// monitor is a QMP socket that answers from a script and keeps what it was asked, so a test can
// hold a command to the conversation QEMU needs - which commands, in which order, with what -
// and not only to the replies it got back.
type monitor struct {
	socket string
	done   chan served
}

type served struct {
	asked []qmpRequest
	err   error
}

// greeting is what a monitor greets with: QEMU 11.2.0, the version the records here say.
const greeting = `{"QMP": {"version": {"qemu": {"major": 11, "minor": 2, "micro": 0}, "package": ""}, "capabilities": []}}`

func newMonitor(t *testing.T, replies ...string) *monitor {
	t.Helper()
	return greetedBy(t, greeting, replies...)
}

// greetedBy is newMonitor with the greeting hello, which says which QEMU it is.
func greetedBy(t *testing.T, hello string, replies ...string) *monitor {
	t.Helper()
	// A socket's path fits in 108 bytes, and t.TempDir() under a TMPDIR on disk
	// (/var/tmp/..., AGENTS.md) does not: a short directory of its own in /tmp.
	dir, err := os.MkdirTemp("/tmp", "mon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	m := &monitor{socket: filepath.Join(dir, "qmp.sock"), done: make(chan served, 1)}
	l, err := net.Listen("unix", m.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	// The goroutine sends what serve said instead of reporting it: a t.Error after the
	// test has already given up on it panics.
	go func() {
		c, err := l.Accept()
		if err != nil {
			m.done <- served{err: err}
			return
		}
		asked, err := serve(c, hello, replies)
		m.done <- served{asked, err}
	}()
	return m
}

// commands is what the command under test asked, once it has let the monitor go.
func (m *monitor) commands(t *testing.T) []qmpRequest {
	t.Helper()
	select {
	case s := <-m.done:
		if s.err != nil {
			t.Error(s.err)
		}
		return s.asked
	case <-time.After(5 * time.Second):
		t.Fatal("the monitor was never let go")
		return nil
	}
}

const ok = `{"return": {}}`

// release is a tree OpenRelease accepts, of empty files: spec never runs what it names.
func release(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"bin/qemu-system-x86_64", "bin/qemu-system-x86_64-tcg", "bin/qemu-img", "kernel/bzImage", "qemu/linuxboot_dma.bin", "qemu/qboot.bin", "image/rootfs.qcow2", "bin/swtpm", "bin/passt"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		var b []byte
		if f == "kernel/bzImage" {
			b = append(make([]byte, 0x202), "HdrS"...)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The flags are the machine: each one lands in the field of the Spec it names, and a flag not
// given is the default a person running a machine by hand expects.
func TestFlagsAreTheSpec(t *testing.T) {
	dir := release(t)
	rel, err := machine.OpenRelease(dir)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Spec("")
	if err != nil {
		t.Fatal(err)
	}
	// --kernel is a bzImage, and a file without its setup header is refused (Args) before QEMU sees it.
	bz := filepath.Join(t.TempDir(), "k")
	header := make([]byte, 0x206)
	copy(header[0x202:], "HdrS")
	if err := os.WriteFile(bz, header, 0o644); err != nil {
		t.Fatal(err)
	}

	defaults := base
	defaults.BootCPUs = 2
	defaults.Memory = machine.Memory{BaseMB: 2048}
	defaults.Console = "mon:stdio"
	defaults.Places = []machine.Place{{Serial: "root"}}
	defaults.Cmdline = machine.DefaultCmdline()
	defaults.Cmdline.Root = "/dev/vda"

	every := base
	every.QEMU, every.Kernel, every.Initrd, every.Firmware = "/q", bz, "/i", "/f"
	every.BIOS = "bios-256k.bin"
	every.CPU, every.BootCPUs, every.MaxCPUs = "Skylake-Server-v4", 3, 8
	every.Accel = "tcg"
	every.Memory = machine.Memory{BaseMB: 1024, CeilingMB: 4096}
	every.HotplugDisks, every.VsockCID = 2, 7
	every.Monitors = []machine.Monitor{{Socket: "/qmp"}}
	every.Restore = true
	every.Console = ""
	every.Places = []machine.Place{{Serial: "ser", Readonly: true}}
	every.Cmdline = machine.DefaultCmdline()
	every.Cmdline.Init, every.Cmdline.Root, every.Cmdline.RootReadonly = "/bin/sh", "/dev/vdb", true
	every.Cmdline = every.Cmdline.Profiling()
	every.Cmdline.Extra = append(every.Cmdline.Extra, "a=1", "b")
	every.Cmdline.Console = ""
	every.Cmdline.Lockdown = machine.LockdownShirudo

	emulated := defaults
	emulated.Accel, emulated.QEMU = "tcg", filepath.Join(dir, "bin/qemu-system-x86_64-tcg")

	withTPM := defaults
	kernel, err := machine.FileSHA256(defaults.Kernel)
	if err != nil {
		t.Fatal(err)
	}
	withTPM.TPM = machine.TPM{SWTPM: filepath.Join(dir, "bin/swtpm"), State: "/tpm", Kernel: kernel}

	withNet := defaults
	withNet.NICs = []machine.NIC{{Passt: filepath.Join(dir, "bin/passt"), MAC: "52:54:00:12:34:56"}}

	noDisk := defaults
	noDisk.Places = nil
	noDisk.Cmdline = machine.DefaultCmdline()

	for _, tc := range []struct {
		name  string
		flags []string
		want  machine.Spec
	}{
		{"nothing given", nil, defaults},
		{"everything given", []string{"--qemu", "/q", "--kernel", bz, "--initrd", "/i", "--firmware", "/f", "--bios", "bios-256k.bin",
			"--disk", "/d", "--disk-format", "raw", "--disk-readonly", "--disk-serial", "ser",
			"--accel", "tcg", "--memory", "1024", "--max-memory", "4096", "--cpu", "Skylake-Server-v4",
			"--cpus", "3", "--max-cpus", "8", "--hotplug-disks", "2",
			"--vsock-cid", "7", "--qmp", "/qmp", "--restore", "--console", "", "--init", "/bin/sh",
			"--root", "/dev/vdb", "--profile", "--append", "a=1 b", "--lockdown", "shirudo"}, every},
		{"tcg runs the release's TCG build", []string{"--accel", "tcg"}, emulated},
		{"no disk mounts no root", []string{"--disk", "-"}, noDisk},
		{"a TPM is its state, emulated by the release's swtpm", []string{"--tpm", "/tpm"}, withTPM},
		{"a network is a NIC on the release's passt", []string{"--net"}, withNet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var o machineFlags
			if err := parse("boot", append([]string{"--release", dir}, tc.flags...), o.register); err != nil {
				t.Fatal(err)
			}
			got, err := o.spec()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("spec\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}

	var o machineFlags
	if err := parse("boot", []string{"--release", dir, "--qmp", "/q", "--kernel", filepath.Join(dir, "bin/qemu-img")}, o.register); err != nil {
		t.Fatal(err)
	}
	s, err := o.spec()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Args(); err == nil || !strings.Contains(err.Error(), "neither a bzImage") {
		t.Errorf("a --kernel that is not a bzImage: %v", err)
	}

	// The keyrings come with the lockdown, so there is no flag that asks for integrity alone.
	if err := parse("boot", []string{"--release", dir, "--lockdown", "integrity"}, o.register); err == nil ||
		!strings.Contains(err.Error(), "none of confidentiality, kernel, shirudo") {
		t.Errorf("a --lockdown that is no machine's: %v", err)
	}
}

// What run does with the commands that build a machine: args prints the line boot would exec,
// and a release that is not whole, or a machine that cannot be, is an error before anything
// runs.
func TestRunBuildsTheMachineOrSaysWhyNot(t *testing.T) {
	dir := release(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	err = run([]string{"args", "--release", dir})
	os.Stdout = stdout
	_ = w.Close()
	printed, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(printed), filepath.Join(dir, "bin/qemu-system-x86_64")) ||
		!strings.HasSuffix(string(printed), "  -S\n") {
		t.Errorf("args printed %q, want the release's QEMU, held", printed)
	}

	// The TPM's channel is a descriptor Start makes, shown where Start hands it.
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = run([]string{"args", "--release", dir, "--tpm", "/tpm"})
	os.Stdout = stdout
	_ = w.Close()
	printed, _ = io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(printed), "-chardev \\\n  socket,id=tpm,fd=4 \\\n  -tpmdev \\\n  emulator,id=tpm0,chardev=tpm \\\n  -device \\\n  tpm-crb,tpmdev=tpm0 ") {
		t.Errorf("args --tpm printed %q, want the TPM's backend on descriptor 4 and its device", printed)
	}
	// passt's socket too, at the descriptor after the TPM's.
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = run([]string{"args", "--release", dir, "--tpm", "/tpm", "--net"})
	os.Stdout = stdout
	_ = w.Close()
	printed, _ = io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(printed), "socket,id=tpm,fd=4 ") || !strings.Contains(string(printed), "-netdev \\\n  stream,id=net0,server=off,addr.type=fd,addr.str=5 ") {
		t.Errorf("args --tpm --net printed %q, want the TPM's channel on descriptor 4 and passt's socket on 5", printed)
	}
	noPasst := release(t)
	if err := os.Remove(filepath.Join(noPasst, "bin/passt")); err != nil {
		t.Fatal(err)
	}
	noSWTPM := release(t)
	if err := os.Remove(filepath.Join(noSWTPM, "bin/swtpm")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"no command", nil, "no command given"},
		{"a network from a release without passt", []string{"args", "--release", noPasst, "--net"}, "passt, a NIC's network without root"},
		{"a profile for a release without passt", []string{"apparmor", "--release", noPasst}, "passt, a NIC's network without root"},
		{"a profile for a release that is not whole", []string{"apparmor", "--release", t.TempDir()}, "is not a whole machine"},
		{"a TPM from a release without swtpm", []string{"args", "--release", noSWTPM, "--tpm", "/tpm"}, "swtpm, the TPM's emulator"},
		{"a release that is not whole", []string{"args", "--release", t.TempDir()}, "is not a whole machine"},
		{"a machine that cannot be", []string{"args", "--release", dir, "--memory", "0"}, "memory is 0"},
		{"a flag args does not take", []string{"args", "--nope"}, "flag provided but not defined"},
		{"a command there is no more", []string{"fingerprint"}, `unknown command "fingerprint"`},
		{"restore without its socket", []string{"restore", "--from", "x"}, "are required"},
		{"target without its socket", []string{"target"}, "target: --qmp is required"},
		{"detach without its socket", []string{"detach"}, "--qmp and --disk-serial are required"},
		{"start without its socket", []string{"start"}, "--qmp is required"},
		{"save without its socket", []string{"save", "--to", "x"}, "are required"},
		{"attach without a disk", []string{"attach", "--qmp", "/q"}, "--qmp and --disk are required"},
		{"memory without a size", []string{"memory", "--qmp", "/q"}, "--qmp and --to are required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.argv); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("run(%q) returned %v, want an error containing %q", tc.argv, err, tc.want)
			}
		})
	}
}

// apparmor prints passt's profile for the release's bin/passt, attached to it alone.
func TestAppArmorPrintsTheProfileOfTheReleasesPasst(t *testing.T) {
	dir, err := filepath.EvalSymlinks(release(t))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := passtProfile(dir, &b); err != nil {
		t.Fatal(err)
	}
	if want := "profile spin-machine-passt \"" + filepath.Join(dir, "bin/passt") + "\" "; !strings.Contains(b.String(), want) {
		t.Errorf("apparmor printed no %q:\n%s", want, b.String())
	}
}
