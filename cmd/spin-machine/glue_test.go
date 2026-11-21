// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// TestMain lets this test binary stand in for the helper boot starts beside QEMU, which is this
// command run again as bindCommand: started that way, it has nothing to bind and exits.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == bindCommand {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// noSocket is a socket path no command can reach, refused at once rather than waited for: a
// directory nobody may search.
func noSocket(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "closed")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return filepath.Join(dir, "q")
}

func rawFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "disk.raw")
	if err := os.WriteFile(p, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// checkpointFile is a state with a record of a mapped-ram checkpoint beside it, as save leaves one.
func checkpointFile(t *testing.T) string {
	t.Helper()
	p := rawFile(t)
	if err := os.WriteFile(p+".json", []byte(`{"format": "qemu-mapped-ram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// checkpointOn is a state whose record says it was taken on a machine, and that machine's record
// as target prints one.
func checkpointOn(t *testing.T) (state, target string) {
	t.Helper()
	const record = `{"vmm": "qemu 11.2.0", "formats": ["qemu-mapped-ram", "qemu-stream"], "cpu": {"vendor": "GenuineIntel", "model": "host"}, "machine": {"exact": [{"name": "accel", "value": "kvm"}]}}`
	state = rawFile(t)
	target = state + ".target"
	if os.WriteFile(state+".json", []byte(`{"format": "qemu-mapped-ram", "from": `+record+`}`), 0o600) != nil ||
		os.WriteFile(target, []byte(record), 0o600) != nil {
		t.Fatal("writing the checkpoint's records")
	}
	return state, target
}

// ev is a reply with the event QEMU sends when a device has gone ahead of it.
func ev(device, reply string) string {
	return `{"event": "DEVICE_DELETED", "data": {"device": "` + device + `"}}` + "\n" + reply
}

// What QEMU answers about its CPU: its vCPUs, the first one's properties, and its model's flags.
const (
	cpuFast      = `{"return": [{"qom-path": "/machine/unattached/device[0]"}]}`
	cpuVCPU      = `{"return": [{"properties": [{"name": "type", "type": "string", "value": "host-x86_64-cpu"}, {"name": "vendor", "type": "string", "value": "GenuineIntel"}, {"name": "sse2", "type": "bool", "value": true}]}]}`
	cpuExpansion = `{"return": {"model": {"name": "base", "props": {"vendor": "GenuineIntel", "sse2": true}}}}`
)

// readRecord is what QEMU answers a Target with, in the order it is asked: its CPU,
// its accelerator, its machine and where its devices are (it has none) and its RAM blocks.
var readRecord = []string{
	cpuFast, cpuVCPU, cpuExpansion,
	`{"return": {"enabled": "kvm", "present": ["kvm", "tcg"]}}`,
	`{"return": [{"properties": [{"name": "type", "type": "string", "value": "pc-q35-11.1-machine"}, ` +
		`{"name": "smp", "type": "SMPConfiguration", "value": {"cpus": 1}}, {"name": "memory", "type": "x", "value": {"size": 1}}, ` +
		`{"name": "hpet", "type": "bool", "value": false}, {"name": "acpi", "type": "OnOffAuto", "value": "on"}, ` +
		`{"name": "sata", "type": "bool", "value": false}, {"name": "smbus", "type": "bool", "value": false}]}, ` +
		`{"properties": []}, {"properties": []}, {"properties": []}, {"properties": []}]}`,
	`{"return": []}`,
	`{"return": {"human-readable-text": "Block Name PSize Offset Used Total HVA RO\npc.bios 4 KiB 0x0 0x10000 0x10000 0x0 ro"}}`,
}

// readRecordAsked is what a Target asks.
var readRecordAsked = []string{"query-cpus-fast", "qom-list-get", "query-cpu-model-expansion",
	"query-accelerators", "qom-list-get", "qom-list-get", "x-query-ramblock"}

// Each command that acts on a running machine, against a monitor that answers as QEMU does:
// what it asks, and what it makes of a refusal.
func TestCommandsOverTheMonitor(t *testing.T) {
	no := `{"error": {"class": "GenericError", "desc": "no"}}`
	// virtio-mem's block and the 1 GiB it may plug, as qom-list-get answers them.
	growth := `{"return": [{"properties": [{"name": "block-size", "type": "size", "value": 2097152}]}, {"properties": [{"name": "size", "type": "uint64", "value": 1073741824}]}]}`
	var out bytes.Buffer
	for _, tc := range []struct {
		name    string
		replies []string
		do      func(t *testing.T, sock string) error
		asked   []string // after qmp_capabilities
		wantErr string
		printed string
	}{{
		name:    "attach a raw disk",
		replies: []string{ok, `{"return": []}`, `{"return": {"fdset-id": 1}}`, ok, ok, ok, ok},
		do: func(t *testing.T, sock string) error {
			return attach(attachFlags{qmp: sock, disks: diskFlags{path: rawFile(t), format: "raw", serial: "ext"}})
		},
		asked: []string{"qom-list", "add-fd", "add-fd", "blockdev-add", "blockdev-add", "device_add"},
	}, {
		name:    "attach a disk QEMU refuses",
		replies: []string{ok, `{"return": []}`, `{"return": {"fdset-id": 1}}`, ok, ok, ok, no},
		do: func(t *testing.T, sock string) error {
			return attach(attachFlags{qmp: sock, disks: diskFlags{path: rawFile(t), format: "raw", serial: "ext"}})
		},
		wantErr: "attaching it at target 0",
	}, {
		name:    "detach",
		replies: []string{ok, `{"return": [{"name": "hd0", "type": "child<scsi-hd>"}]}`, `{"return": "ext"}`, ev("hd0", ok), `{"return": []}`},
		do: func(_ *testing.T, sock string) error {
			return detach(detachFlags{qmp: sock, serial: "ext", timeout: time.Minute})
		},
		asked: []string{"qom-list", "qom-get", "device_del", "query-named-block-nodes"},
	}, {
		name:    "detach a disk the machine does not have",
		replies: []string{ok, `{"return": []}`},
		do: func(_ *testing.T, sock string) error {
			return detach(detachFlags{qmp: sock, serial: "ext", timeout: time.Minute})
		},
		wantErr: `no disk "ext"`,
	}, {
		name: "memory reached",
		replies: []string{ok, `{"return": {"base-memory": 536870912}}`, growth, ok,
			`{"return": {"base-memory": 536870912, "plugged-memory": 536870912}}`, growth},
		do: func(_ *testing.T, sock string) error {
			return memory(memoryFlags{qmp: sock, totalMB: 1024, timeout: time.Minute}, &out)
		},
		asked:   []string{"query-memory-size-summary", "qom-list-get", "qom-set", "query-memory-size-summary", "qom-list-get"},
		printed: "1024 MiB in",
	}, {
		name:    "memory above the ceiling",
		replies: []string{ok, `{"return": {"base-memory": 536870912}}`, growth},
		do: func(_ *testing.T, sock string) error {
			return memory(memoryFlags{qmp: sock, totalMB: 4096, timeout: time.Minute}, &out)
		},
		wantErr: "512 MiB to 1536 MiB",
	}, {
		name:    "memory that cannot be read back",
		replies: []string{ok, `{"return": {"base-memory": 536870912}}`, growth, ok, no},
		do: func(_ *testing.T, sock string) error {
			return memory(memoryFlags{qmp: sock, totalMB: 1024, timeout: time.Minute}, &out)
		},
		wantErr: "reading the machine's memory",
		printed: "had not reached 1024 MiB",
	}, {
		name: "cpu",
		replies: []string{ok, cpuFast, cpuVCPU, cpuExpansion,
			`{"return": [{"name": "Skylake-Client-v4", "unavailable-features": [], "migration-safe": true}]}`,
			`{"return": {"model": {"name": "base", "props": {"avx2": true}}}}`},
		do:      func(_ *testing.T, sock string) error { return cpuReport(sock, &out) },
		asked:   []string{"query-cpus-fast", "qom-list-get", "query-cpu-model-expansion", "query-cpu-definitions", "query-cpu-model-expansion"},
		printed: `"name": "Skylake-Client-v4"`,
	}, {
		name:    "cpu, its vCPUs unreadable",
		replies: []string{ok, no},
		do:      func(_ *testing.T, sock string) error { return cpuReport(sock, &out) },
		wantErr: "listing the machine's vCPUs",
	}, {
		name:    "cpu, its models unreadable",
		replies: []string{ok, cpuFast, cpuVCPU, cpuExpansion, no},
		do:      func(_ *testing.T, sock string) error { return cpuReport(sock, &out) },
		wantErr: "listing the CPU models",
	}, {
		name:    "start",
		replies: []string{ok, `{"return": {"status": "prelaunch"}}`, `{"return": []}`, ok},
		do: func(_ *testing.T, sock string) error {
			return start(startFlags{qmp: sock, disks: diskFlags{path: "-"}, timeout: time.Minute})
		},
		asked: []string{"query-status", "query-block", "cont"},
	}, {
		name:    "the boot's helper",
		replies: []string{ok, `{"return": {"status": "prelaunch"}}`, `{"return": []}`, ok},
		do: func(_ *testing.T, sock string) error {
			return bindAndBoot(machineFlags{qmp: sock, disks: diskFlags{path: "-"}})
		},
		asked: []string{"query-status", "query-block", "cont"},
	}, {
		// A machine the helper cannot boot is ended: boot then fails where the cause is.
		name:    "the boot's helper, refused",
		replies: []string{ok, `{"return": {"status": "inmigrate"}}`, ok},
		do: func(_ *testing.T, sock string) error {
			return bindAndBoot(machineFlags{qmp: sock, disks: diskFlags{path: "-"}})
		},
		asked:   []string{"query-status", "quit"},
		wantErr: "booting a machine that is inmigrate",
	}, {
		name:    "start with a disk not there",
		replies: []string{ok},
		do: func(_ *testing.T, sock string) error {
			return start(startFlags{qmp: sock, disks: diskFlags{path: "/nonexistent/d.raw", format: "raw", serial: "root"}, timeout: time.Minute})
		},
		wantErr: "opening /nonexistent/d.raw",
	}, {
		name:    "restore with a disk not there",
		replies: []string{ok},
		do: func(t *testing.T, sock string) error {
			return restore(restoreFlags{qmp: sock, from: checkpointFile(t), disks: diskFlags{path: "/nonexistent/d.raw", format: "raw", serial: "root"}, timeout: time.Minute})
		},
		wantErr: "opening /nonexistent/d.raw",
	}, {
		name:    "restore into a machine held for a boot",
		replies: []string{ok, `{"return": {"status": "prelaunch"}}`},
		do: func(t *testing.T, sock string) error {
			state, target := checkpointOn(t)
			return restore(restoreFlags{qmp: sock, from: state, target: target, disks: diskFlags{path: "-"}, timeout: time.Minute})
		},
		asked:   []string{"query-status"},
		wantErr: "only one started with Spec.Restore",
	}, {
		name:    "target",
		replies: append([]string{ok}, readRecord...),
		do:      func(_ *testing.T, sock string) error { return targetReport(sock, &out) },
		asked:   readRecordAsked,
		printed: `"vmm": "qemu 11.2.0"`,
	}, {
		name:    "target, its vCPUs unreadable",
		replies: []string{ok, no},
		do:      func(_ *testing.T, sock string) error { return targetReport(sock, &out) },
		wantErr: "listing the machine's vCPUs",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			out.Reset()
			m := newMonitor(t, tc.replies...)
			err := tc.do(t, m.socket)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("returned %v, want an error saying %q", err, tc.wantErr)
			}
			if !strings.Contains(out.String(), tc.printed) {
				t.Errorf("printed %q, want %q", out.String(), tc.printed)
			}
			if tc.asked == nil {
				return
			}
			var asked []string
			for _, r := range m.commands(t)[1:] {
				asked = append(asked, r.Execute)
			}
			if !slices.Equal(asked, tc.asked) {
				t.Errorf("asked %v, want %v", asked, tc.asked)
			}
		})
	}
}

// Every command that dials a machine says so when it cannot reach it, and a state that is not
// there is refused before the machine is.
func TestCommandsThatCannotReachTheMachine(t *testing.T) {
	sock := noSocket(t)
	for name, do := range map[string]func() error{
		"attach": func() error {
			return attach(attachFlags{qmp: sock, disks: diskFlags{path: rawFile(t), format: "raw", serial: "ext"}})
		},
		"detach":        func() error { return detach(detachFlags{qmp: sock, serial: "ext"}) },
		"memory":        func() error { return memory(memoryFlags{qmp: sock, totalMB: 1024}, &bytes.Buffer{}) },
		"cpu":           func() error { return cpuReport(sock, &bytes.Buffer{}) },
		"start":         func() error { return start(startFlags{qmp: sock}) },
		"the helper":    func() error { return bindAndBoot(machineFlags{qmp: sock}) },
		"save":          func() error { return save(saveFlags{qmp: sock, to: filepath.Join(t.TempDir(), "s")}) },
		"restore":       func() error { return restore(restoreFlags{qmp: sock, from: checkpointFile(t)}) },
		"no state":      func() error { return restore(restoreFlags{qmp: sock, from: "/nonexistent/state"}) },
		"no cpu qmp":    func() error { return cpuReport("", &bytes.Buffer{}) },
		"target":        func() error { return targetReport(sock, &bytes.Buffer{}) },
		"no target qmp": func() error { return targetReport("", &bytes.Buffer{}) },
		"one MiB":       func() error { return memory(memoryFlags{qmp: sock, totalMB: 1}, &bytes.Buffer{}) },
		"a disk not there": func() error {
			return attach(attachFlags{qmp: sock, disks: diskFlags{path: "/nonexistent/disk.raw", format: "raw", serial: "ext"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			err := do()
			if err == nil {
				t.Fatal("succeeded")
			}
			if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "no such file") &&
				!strings.Contains(err.Error(), "is required") || (strings.HasPrefix(name, "one ") && !strings.Contains(err.Error(), "permission denied")) {
				t.Errorf("returned %v, want it to say what could not be reached", err)
			}
			if time.Since(start) > 2*time.Second {
				t.Errorf("took %s to give up on a socket it can never reach", time.Since(start))
			}
		})
	}
}

// dial waits for a socket QEMU has yet to make, and not for one it can never reach.
func TestDialWaitsOnlyForASocketThatMayStillCome(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "dial")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "q")
	go func() {
		time.Sleep(50 * time.Millisecond)
		l, err := net.Listen("unix", sock)
		if err != nil {
			return
		}
		c, err := l.Accept()
		if err == nil {
			_, _ = c.Write([]byte(`{"QMP": {}}` + "\n" + `{"return": {}, "id": 1}` + "\n"))
			time.Sleep(100 * time.Millisecond)
			_ = c.Close()
		}
		_ = l.Close()
	}()
	m, err := dial(sock, 5*time.Second)
	if err != nil {
		t.Fatalf("a socket made 50 ms later was not waited for: %v", err)
	}
	_ = m.Close()
}

// The disk a command takes when it is not told one: the release's base image under an overlay
// made with the release's qemu-img, read without a path.
func TestADiskWithNoPathIsTheBaseUnderAScratchOverlay(t *testing.T) {
	dir := release(t)
	base := filepath.Join(dir, "image/rootfs.qcow2")
	prepared := filepath.Join(t.TempDir(), "overlay")
	qcow2(t, prepared, 3, base, "raw")
	script := func(body string) {
		t.Helper()
		p := filepath.Join(dir, "bin/qemu-img")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	script(`for a; do last=$a; done; cp ` + prepared + ` "$last"`)
	d, closeAll, err := diskFlags{serial: "root"}.open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if len(d.Images) != 2 || d.Images[0].Format != "qcow2" || d.Images[1].Format != "raw" || d.Readonly || !d.Locking {
		t.Errorf("disk %+v, want a writable, locked qcow2 overlay over the raw base", d)
	}
	if _, err := os.Stat(d.Images[0].File.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the scratch overlay is still at %s: %v", d.Images[0].File.Name(), err)
	}

	script("exit 1")
	if _, _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "making a scratch overlay") {
		t.Errorf("a qemu-img that failed: %v", err)
	}
	script(`for a; do last=$a; done; echo garbage > "$last"`)
	if _, _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "is not a qcow2 image") {
		t.Errorf("an overlay that is not one: %v", err)
	}
	t.Setenv("TMPDIR", "/nonexistent")
	if _, _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "a directory for the scratch overlay") {
		t.Errorf("no directory for the overlay: %v", err)
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "base image") {
		t.Errorf("a release with no base image: %v", err)
	}
	if _, _, err := (diskFlags{serial: "root"}).open(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a whole machine") {
		t.Errorf("no release: %v", err)
	}
}

// A disk's top is opened for writing unless it is read-only, and nothing under it ever is.
func TestADiskIsOpenedForWhatItWillBe(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.raw")
	if err := os.WriteFile(base, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(dir, "top.qcow2")
	qcow2(t, top, 3, base, "raw")
	for _, readonly := range []bool{false, true} {
		d, closeAll, err := diskFlags{path: top, format: "qcow2", serial: "root", readonly: readonly}.open(release(t))
		if err != nil {
			t.Fatal(err)
		}
		writable := func(f *os.File) bool {
			flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFL, 0)
			if errno != 0 {
				t.Fatal(errno)
			}
			return flags&syscall.O_ACCMODE == syscall.O_RDWR
		}
		if writable(d.Images[0].File) == readonly || writable(d.Images[1].File) {
			t.Errorf("read-only %t: the top writable %t, the base %t", readonly, writable(d.Images[0].File), writable(d.Images[1].File))
		}
		closeAll()
	}
}

// boot's own refusals, before anything is started.
func TestBootRefusesBeforeStartingAnything(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "qemu")
	if err := os.WriteFile(garbage, []byte{0, 1, 2, 3}, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := machine.Spec{QEMU: garbage, Kernel: kernelIn(t, dir), Firmware: dir, BootCPUs: 1, Memory: machine.Memory{SizeMB: 64},
		Monitors: []machine.Monitor{{Socket: "/q"}}}
	invalid := spec
	invalid.Memory.SizeMB = 0
	absent := spec
	absent.QEMU = filepath.Join(dir, "absent")
	for _, tc := range []struct {
		name string
		spec machine.Spec
		o    machineFlags
		want string
	}{
		{"a machine that cannot be", invalid, machineFlags{}, "memory is 0"},
		{"no QEMU", absent, machineFlags{held: true, qmp: "/q"}, "finding QEMU"},
		{"a QEMU that is not a program", spec, machineFlags{held: true, qmp: "/q"}, "executing"},
		{"a QEMU that is not a program, with its helper", spec, machineFlags{}, "executing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := boot(tc.spec, tc.o, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("boot = %v, want %q", err, tc.want)
			}
		})
	}
}

// What boot would run: held for a boot, with the helper and the monitor it boots the machine
// through, or held for a checkpoint with neither.
func TestBootCommandIsHeld(t *testing.T) {
	dir := t.TempDir()
	qemu := filepath.Join(dir, "qemu")
	if err := os.WriteFile(qemu, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	s := machine.Spec{QEMU: qemu, Kernel: kernelIn(t, dir), Firmware: dir, BootCPUs: 1, Memory: machine.Memory{SizeMB: 64},
		Monitors: []machine.Monitor{{Socket: "/run/q"}}}
	for _, tc := range []struct {
		name         string
		o            machineFlags
		restore      bool
		helper, held string
	}{
		{"a boot", machineFlags{qmp: "/run/q"}, false, "unix:/tmp/sm", " -S"},
		{"held for start", machineFlags{qmp: "/run/q", held: true}, false, "", " -S"},
		{"held for a checkpoint", machineFlags{qmp: "/run/q", restore: true}, true, "", " -incoming defer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := s
			spec.Restore = tc.restore
			_, args, helper, err := bootCommand(spec, tc.o, []string{"--memory", "64", "--qmp", "/run/q"})
			if err != nil {
				t.Fatal(err)
			}
			line := " " + strings.Join(args, " ")
			if !strings.Contains(line, tc.held) {
				t.Errorf("exec'd %s, want %q", line, tc.held)
			}
			if (helper != nil) != (tc.helper != "") || strings.Contains(line, "unix:/tmp/sm") != (tc.helper != "") {
				t.Fatalf("helper %v, line %s, want a helper and its monitor: %t", helper, line, tc.helper != "")
			}
			if helper == nil {
				return
			}
			want := []string{bindCommand, "--qmp"}
			if got := helper.Args[1:3]; !slices.Equal(got, want) || !strings.Contains(line, "unix:"+helper.Args[3]) ||
				!slices.Equal(helper.Args[4:], []string{"--memory", "64"}) {
				t.Errorf("helper %v, want %v, the monitor on the line and the boot's flags without its --qmp", helper.Args, want)
			}
		})
	}
}

// What QEMU is exec'd with: held for a boot, with the monitor the helper boots it through, or
// held for a checkpoint with none. Run as a child, because a process that execs is not there
// afterwards to say what it ran.
func TestBootExecsQEMUHeld(t *testing.T) {
	if argv := os.Getenv("SPIN_MACHINE_TEST_ARGV"); argv != "" {
		qemu := os.Getenv("SPIN_MACHINE_TEST_QEMU")
		s := machine.Spec{QEMU: qemu, Kernel: filepath.Join(filepath.Dir(qemu), "bzImage"), Firmware: filepath.Dir(qemu), BootCPUs: 1, Memory: machine.Memory{SizeMB: 64},
			Monitors: []machine.Monitor{{Socket: "/run/q"}}}
		o := machineFlags{qmp: "/run/q"}
		if os.Getenv("SPIN_MACHINE_TEST_RESTORE") != "" {
			o.restore, s.Restore = true, true
		}
		_ = boot(s, o, []string{"--qmp", "/run/q"})
		os.Exit(100)
	}
	for _, restore := range []bool{false, true} {
		dir := t.TempDir()
		kernelIn(t, dir)
		qemu, argv := filepath.Join(dir, "qemu"), filepath.Join(dir, "argv")
		if err := os.WriteFile(qemu, []byte("#!/bin/sh\necho \"$@\" > "+argv+"\nexit 7\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestBootExecsQEMUHeld$")
		cmd.Env = append(os.Environ(), "SPIN_MACHINE_TEST_ARGV=1", "SPIN_MACHINE_TEST_QEMU="+qemu)
		if restore {
			cmd.Env = append(cmd.Env, "SPIN_MACHINE_TEST_RESTORE=1")
		}
		var exit *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("restore %t: boot ended with %v, want QEMU's exit status 7", restore, err)
		}
		b, err := os.ReadFile(argv)
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		helper := strings.Contains(got, "unix:/tmp/sm")
		switch {
		case !restore && (!helper || !strings.Contains(got, " -S")):
			t.Errorf("a boot exec'd %s, want -S and the helper's monitor", got)
		case restore && (helper || !strings.Contains(got, "-incoming defer")):
			t.Errorf("a restore exec'd %s, want -incoming defer and no helper", got)
		}
	}
}

// The commands that take their flags through run.
func TestRunPassesFlagsOn(t *testing.T) {
	for _, argv := range [][]string{{bindCommand, "--nope"}, {"cpu", "--nope"}, {"start", "--nope"}} {
		if err := run(argv); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("run(%q) = %v", argv, err)
		}
	}
	dir := release(t)
	for _, tc := range []struct {
		flags  []string
		helper bool
	}{{nil, true}, {[]string{"--held", "--qmp", "/q"}, false}, {[]string{"--restore", "--qmp", "/q"}, false}} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout := os.Stdout
		os.Stdout = w
		err = run(append([]string{"args", "--release", dir}, tc.flags...))
		os.Stdout = stdout
		_ = w.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "/tmp/sm/m") != tc.helper {
			t.Errorf("args %v printed the helper's monitor: %t, want %t", tc.flags, !tc.helper, tc.helper)
		}
	}
}

// kernelIn writes dir/bzImage, a file with a bzImage's setup header magic, which Args asks of a
// kernel, and returns its path.
func kernelIn(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "bzImage")
	if err := os.WriteFile(path, append(make([]byte, 0x202), "HdrS"...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
