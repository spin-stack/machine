// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/spin-machine/machine"
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

func newMonitor(t *testing.T, replies ...string) *monitor {
	t.Helper()
	m := &monitor{socket: filepath.Join(t.TempDir(), "qmp.sock"), done: make(chan served, 1)}
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
		asked, err := serve(c, replies)
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
	for _, f := range []string{"bin/qemu-system-x86_64", "bin/qemu-system-x86_64-tcg", "bin/qemu-img", "kernel/bzImage", "qemu/linuxboot_dma.bin", "qemu/qboot.bin", "image/rootfs.qcow2"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
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
	base := rel.Spec()

	defaults := base
	defaults.BootCPUs = 2
	defaults.Memory = machine.Memory{SizeMB: 2048}
	defaults.Serial = "mon:stdio"
	defaults.Disks = []machine.DiskSlot{{Serial: "root"}}
	defaults.Cmdline = machine.DefaultCmdline()
	defaults.Cmdline.Root = "/dev/vda"

	every := base
	every.QEMU, every.Kernel, every.Initrd, every.Firmware = "/q", "/k", "/i", "/f"
	every.BIOS = "bios-256k.bin"
	every.CPU, every.BootCPUs, every.MaxCPUs = "Skylake-Server-v4", 3, 8
	every.Accel = "tcg"
	every.Memory = machine.Memory{SizeMB: 1024, MaxMB: 4096}
	every.HotplugDisks, every.VsockCID = 2, 7
	every.Monitors = []machine.Monitor{{Socket: "/qmp"}}
	every.Restore = true
	every.Serial = ""
	every.Disks = []machine.DiskSlot{{Serial: "ser", Readonly: true}}
	every.Cmdline = machine.DefaultCmdline()
	every.Cmdline.Init, every.Cmdline.Root, every.Cmdline.RootReadonly = "/bin/sh", "/dev/vdb", true
	every.Cmdline = every.Cmdline.Profiling()
	every.Cmdline.Extra = append(every.Cmdline.Extra, "a=1", "b")
	every.Cmdline.Console = ""

	emulated := defaults
	emulated.Accel, emulated.QEMU = "tcg", filepath.Join(dir, "bin/qemu-system-x86_64-tcg")

	noDisk := defaults
	noDisk.Disks = nil
	noDisk.Cmdline = machine.DefaultCmdline()

	for _, tc := range []struct {
		name  string
		flags []string
		want  machine.Spec
	}{
		{"nothing given", nil, defaults},
		{"everything given", []string{"--qemu", "/q", "--kernel", "/k", "--initrd", "/i", "--firmware", "/f", "--bios", "bios-256k.bin",
			"--disk", "/d", "--disk-format", "raw", "--disk-readonly", "--disk-serial", "ser",
			"--disk-direct-over-backing", "--accel", "tcg", "--memory", "1024", "--max-memory", "4096", "--cpu", "Skylake-Server-v4",
			"--cpus", "3", "--max-cpus", "8", "--hotplug-disks", "2",
			"--vsock-cid", "7", "--qmp", "/qmp", "--restore", "--console", "", "--init", "/bin/sh",
			"--root", "/dev/vdb", "--profile", "--append", "a=1 b"}, every},
		{"tcg runs the release's TCG build", []string{"--accel", "tcg"}, emulated},
		{"no disk mounts no root", []string{"--disk", "-"}, noDisk},
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

	// fingerprint prints the number together with the shape that went into it.
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = run([]string{"fingerprint", "--release", dir})
	os.Stdout = stdout
	_ = w.Close()
	printed, _ = io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var fp struct {
		Fingerprint string
		Shape       struct{ Machine string }
	}
	if err := json.Unmarshal(printed, &fp); err != nil || len(fp.Fingerprint) != 64 || fp.Shape.Machine == "" {
		t.Errorf("fingerprint printed %q (%v), want a sha256 and the shape it hashed", printed, err)
	}

	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"no command", nil, "no command given"},
		{"a release that is not whole", []string{"args", "--release", t.TempDir()}, "is not a whole machine"},
		{"a machine that cannot be", []string{"args", "--release", dir, "--memory", "0"}, "memory is 0"},
		{"a flag boot does not take", []string{"fingerprint", "--nope"}, "flag provided but not defined"},
		{"restore without its socket", []string{"restore", "--from", "x"}, "are required"},
		{"detach without its socket", []string{"detach"}, "--qmp and --disk-serial are required"},
		{"start without its socket", []string{"start"}, "--qmp is required"},
		{"save without its socket", []string{"save", "--to", "x"}, "are required"},
		{"attach without a disk", []string{"attach", "--qmp", "/q"}, "--qmp and --disk are required"},
		{"memory without a size", []string{"memory", "--qmp", "/q"}, "--qmp and --to are required"},
		{"cpus without a count", []string{"cpus", "--qmp", "/q"}, "--qmp and --to are required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.argv); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("run(%q) returned %v, want an error containing %q", tc.argv, err, tc.want)
			}
		})
	}
}
