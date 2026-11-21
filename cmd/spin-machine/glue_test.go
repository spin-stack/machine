// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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

	"github.com/spin-stack/machine/machine"
)

// TestMain lets this test binary stand in for the QEMU boot starts (fakeQEMU).
func TestMain(m *testing.M) {
	if status := os.Getenv("SPIN_MACHINE_FAKE_QEMU"); status != "" {
		os.Exit(fakeQEMU(status, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeQEMU is a QEMU whose machine is status: it writes its command line to
// $SPIN_MACHINE_FAKE_ARGV, answers on every monitor it is handed, and exits 7 once the machine is
// let go, 0 once it is quit. A machine held for a checkpoint exits 7 once its monitor has been
// greeted, as if a restore had run it and it had ended.
func fakeQEMU(status string, args []string) int {
	_ = os.WriteFile(os.Getenv("SPIN_MACHINE_FAKE_ARGV"), []byte(strings.Join(args, " ")), 0o600)
	exit := make(chan int, 1)
	for _, a := range args {
		m := regexp.MustCompile(`^socket,id=\w+,fd=(\d+),server=on`).FindStringSubmatch(a)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		l, err := net.FileListener(os.NewFile(uintptr(n), "monitor"))
		if err != nil {
			return 2
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					_, _ = io.WriteString(c, greeting+"\n")
					in := bufio.NewScanner(c)
					for in.Scan() {
						var req qmpRequest
						_ = json.Unmarshal(in.Bytes(), &req)
						reply := ok
						switch req.Execute {
						case "query-status":
							reply = `{"return": {"status": "` + status + `"}}`
						case "query-block":
							reply = `{"return": []}`
						}
						_, _ = io.WriteString(c, reply[:len(reply)-1]+`, "id": `+string(req.ID)+"}\n")
						switch {
						case req.Execute == "cont":
							exit <- 7
						case req.Execute == "quit":
							exit <- 0
						case req.Execute == "qmp_capabilities" && status == "inmigrate" && !slices.Contains(args, "-S"):
							exit <- 7
						}
					}
				}()
			}
		}()
	}
	return <-exit
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
	// The monitor answers qmp_capabilities once it has read it, as QEMU does. An answer sent
	// ahead of the question reached the client before it waited for one, and was dropped
	// (#428).
	done := make(chan served, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		l, err := net.Listen("unix", sock)
		if err != nil {
			done <- served{err: err}
			return
		}
		defer func() { _ = l.Close() }()
		c, err := l.Accept()
		if err != nil {
			done <- served{err: err}
			return
		}
		asked, err := serve(c, greeting, []string{ok})
		done <- served{asked, err}
	}()
	m, err := dial(sock, 5*time.Second)
	if err != nil {
		t.Fatalf("a socket made 50 ms later was not waited for: %v", err)
	}
	_ = m.Close()
	if s := <-done; s.err != nil || len(s.asked) != 1 || s.asked[0].Execute != "qmp_capabilities" {
		t.Errorf("the monitor was asked %v (%v), want qmp_capabilities alone", s.asked, s.err)
	}

	start := time.Now()
	if _, err := dial(filepath.Join(dir, "never"), 50*time.Millisecond); err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("a socket nobody makes: %v after %v, want an error once the wait is over", err, time.Since(start))
	}
}

// The process ends as the command did: QEMU's status for boot, 0 for help, 1 and the error for
// anything else.
func TestExitStatusIsTheCommands(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		said string
	}{
		{nil, 0, ""},
		{flag.ErrHelp, 0, ""},
		{fmt.Errorf("boot: %w", exitCode(7)), 7, ""},
		{errors.New("no"), 1, "spin-machine: no\n"},
	} {
		if code, said := exitStatus(tc.err); code != tc.code || said != tc.said {
			t.Errorf("exitStatus(%v) = %d, %q, want %d, %q", tc.err, code, said, tc.code, tc.said)
		}
	}
}

// A boot killed outright takes its QEMU with it: nothing is left holding the disks.
func TestAKilledBootTakesItsQEMU(t *testing.T) {
	if os.Getenv("SPIN_MACHINE_TEST_BOOT") != "" {
		dir := os.Getenv("SPIN_MACHINE_TEST_BOOT")
		// The QEMU boot starts is this binary too, and has to know it is the QEMU.
		_ = os.Setenv("SPIN_MACHINE_FAKE_QEMU", "prelaunch")
		s := machine.Spec{QEMU: os.Args[0], Kernel: filepath.Join(dir, "bzImage"), Firmware: dir, Accel: "tcg", BootCPUs: 1,
			Memory: machine.Memory{BaseMB: 64}, Monitors: []machine.Monitor{{Socket: filepath.Join(dir, "q")}}}
		_ = boot(s, machineFlags{held: true, qmp: filepath.Join(dir, "q")})
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("/tmp", "kb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	kernelIn(t, dir)
	argv := filepath.Join(dir, "argv")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAKilledBootTakesItsQEMU$")
	cmd.Env = append(os.Environ(), "SPIN_MACHINE_TEST_BOOT="+dir, "SPIN_MACHINE_FAKE_ARGV="+argv)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var qemu int
	for deadline := time.Now().Add(10 * time.Second); qemu == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the boot never started its QEMU")
		}
		if _, err := os.Stat(argv); err == nil {
			qemu = childOf(cmd.Process.Pid)
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(qemu, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(qemu, syscall.SIGKILL)
			t.Fatalf("QEMU %d outlived the boot that started it", qemu)
		}
	}
}

// childOf is pid's first child, or 0.
func childOf(pid int) int {
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	for _, t := range tasks {
		b, _ := os.ReadFile(t)
		if f := strings.Fields(string(b)); len(f) > 0 {
			n, _ := strconv.Atoi(f[0])
			return n
		}
	}
	return 0
}

// qcow2Over writes a version 3 qcow2 header at path naming backing, of the format given.
func qcow2Over(t *testing.T, path, backing, format string) {
	t.Helper()
	b := make([]byte, 1024)
	be := binary.BigEndian
	be.PutUint32(b[0:], 0x514649fb)
	be.PutUint32(b[4:], 3)
	be.PutUint64(b[8:], 512)
	be.PutUint32(b[16:], uint32(len(backing))) // #nosec G115 -- a test's name
	be.PutUint32(b[20:], 16)
	be.PutUint32(b[100:], 104)
	be.PutUint32(b[104:], 0xe2792aca)
	be.PutUint32(b[108:], uint32(len(format))) // #nosec G115 -- a test's format
	copy(b[112:], format)
	copy(b[512:], backing)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The disk a command takes when it is not told one: the release's base image under an overlay
// made with the release's qemu-img, read without a path.
func TestADiskWithNoPathIsTheBaseUnderAScratchOverlay(t *testing.T) {
	dir := release(t)
	base := filepath.Join(dir, "image/rootfs.qcow2")
	prepared := filepath.Join(t.TempDir(), "overlay")
	qcow2Over(t, prepared, base, "raw")
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
	d, err := diskFlags{serial: "root"}.open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if len(d.Images) != 2 || d.Images[0].Format != "qcow2" || d.Images[1].Format != "raw" || d.Readonly || !d.Locking {
		t.Errorf("disk %+v, want a writable, locked qcow2 overlay over the raw base", d)
	}
	if _, err := os.Stat(d.Images[0].File.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the scratch overlay is still at %s: %v", d.Images[0].File.Name(), err)
	}

	script("exit 1")
	if _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "making a scratch overlay") {
		t.Errorf("a qemu-img that failed: %v", err)
	}
	script(`for a; do last=$a; done; echo garbage > "$last"`)
	if _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "is not a qcow2 image") {
		t.Errorf("an overlay that is not one: %v", err)
	}
	t.Setenv("TMPDIR", "/nonexistent")
	if _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "a directory for the scratch overlay") {
		t.Errorf("no directory for the overlay: %v", err)
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	if _, err := (diskFlags{serial: "root"}).open(dir); err == nil || !strings.Contains(err.Error(), "base image") {
		t.Errorf("a release with no base image: %v", err)
	}
	if _, err := (diskFlags{serial: "root"}).open(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a whole machine") {
		t.Errorf("no release: %v", err)
	}
}

// A --disk's backing images are the release's base unless --disk-under names others, and a disk
// that names none needs no release.
func TestADisksBackingImagesAreTheReleasesBaseUnlessNamed(t *testing.T) {
	dir := release(t)
	base := filepath.Join(dir, "image/rootfs.qcow2")
	elsewhere := filepath.Join(t.TempDir(), "base.raw")
	if err := os.WriteFile(elsewhere, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	overBase, overElsewhere := filepath.Join(t.TempDir(), "a.qcow2"), filepath.Join(t.TempDir(), "b.qcow2")
	qcow2Over(t, overBase, base, "raw")
	qcow2Over(t, overElsewhere, elsewhere, "raw")

	var o attachFlags
	if err := parse("attach", []string{"--qmp", "/q", "--disk", overElsewhere, "--disk-under", elsewhere, "--disk-under", base}, o.register); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.disks.under, []string{elsewhere, base}) {
		t.Errorf("--disk-under twice is %v, want both", o.disks.under)
	}
	for _, tc := range []struct {
		name    string
		disk    diskFlags
		release string
		want    string
	}{
		{"over the release's base", diskFlags{path: overBase, format: "qcow2"}, dir, ""},
		{"over a base elsewhere", diskFlags{path: overElsewhere, format: "qcow2"}, dir, "names " + elsewhere},
		{"over a base elsewhere it was told of", o.disks, dir, ""},
		{"raw, with no release", diskFlags{path: elsewhere, format: "raw"}, "", ""},
		{"the release's base, writable", diskFlags{path: base, format: "qcow2"}, dir, "is one of the images a disk may only read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.disk.serial = "root"
			d, err := tc.disk.open(tc.release)
			if err == nil {
				_ = d.Close()
			}
			if (tc.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("open = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}

// boot's own refusals, before anything is started.
func TestBootRefusesBeforeStartingAnything(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "qemu")
	if err := os.WriteFile(garbage, []byte{0, 1, 2, 3}, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := machine.Spec{QEMU: garbage, Kernel: kernelIn(t, dir), Firmware: dir, Accel: "tcg", BootCPUs: 1, Memory: machine.Memory{BaseMB: 64}}
	invalid := spec
	invalid.Memory.BaseMB = 0
	absent := spec
	absent.QEMU = filepath.Join(dir, "absent")
	for _, tc := range []struct {
		name string
		spec machine.Spec
		o    machineFlags
		want string
	}{
		{"a machine that cannot be", invalid, machineFlags{}, "memory is 0"},
		{"no QEMU", absent, machineFlags{}, "no such file"},
		{"a QEMU that is not a program", spec, machineFlags{}, "exec format error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := boot(tc.spec, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("boot = %v, want %q", err, tc.want)
			}
		})
	}
}

// boot starts QEMU held and lets it go, unless it was asked for a machine held, and exits as QEMU
// did. A machine it cannot boot is ended, and boot fails where the cause is.
func TestBootStartsQEMUAndExitsAsItDid(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status string
		o            machineFlags
		want         error
		line         string
		err          string
	}{
		{"a boot", "prelaunch", machineFlags{disks: diskFlags{path: "-"}}, exitCode(7), " -S", ""},
		{"held for a checkpoint", "inmigrate", machineFlags{qmp: "/q", restore: true}, exitCode(7), " -incoming defer", ""},
		{"a boot QEMU will not run", "inmigrate", machineFlags{disks: diskFlags{path: "-"}}, nil, " -S", "booting a machine that is inmigrate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argv := filepath.Join(dir, "argv")
			t.Setenv("SPIN_MACHINE_FAKE_QEMU", tc.status)
			t.Setenv("SPIN_MACHINE_FAKE_ARGV", argv)
			socks, err := os.MkdirTemp("/tmp", "bt")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socks) })
			s := machine.Spec{QEMU: self, Kernel: kernelIn(t, dir), Firmware: dir, Accel: "tcg", BootCPUs: 1,
				Memory: machine.Memory{BaseMB: 64}, Restore: tc.o.restore}
			if tc.o.qmp != "" {
				tc.o.qmp = filepath.Join(socks, "q")
				s.Monitors = []machine.Monitor{{Socket: tc.o.qmp}}
			}
			err = boot(s, tc.o)
			switch {
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Errorf("boot returned %v, want %q", err, tc.err)
			case tc.err == "" && err != tc.want:
				t.Errorf("boot returned %v, want %v", err, tc.want)
			}
			b, err := os.ReadFile(argv)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(" "+string(b), tc.line) {
				t.Errorf("QEMU ran with %s, want %q", b, tc.line)
			}
		})
	}
}

// QEMU's end is the command's: its status, or 128 and the signal, and nothing for a clean exit.
func TestExitOfIsQEMUsEnd(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   error
	}{{"exit 0", nil}, {"exit 3", exitCode(3)}, {"kill -9 $$", exitCode(137)}} {
		cmd := exec.Command("/bin/sh", "-c", tc.script)
		_ = cmd.Run()
		if got := exitOf(machine.Exit{State: cmd.ProcessState}); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.script, got, tc.want)
		}
	}
	if got := exitCode(3).Error(); got != "exit status 3" {
		t.Errorf("exitCode(3) says %q", got)
	}
}

// The commands that take their flags through run.
func TestRunPassesFlagsOn(t *testing.T) {
	for _, argv := range [][]string{{"cpu", "--nope"}, {"start", "--nope"}} {
		if err := run(argv); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("run(%q) = %v", argv, err)
		}
	}
	dir := release(t)
	for _, tc := range []struct {
		flags []string
		held  string
	}{{nil, "socket,id=qmp0,fd=3,server=on"}, {[]string{"--held", "--qmp", "/q"}, " -S"}, {[]string{"--restore", "--qmp", "/q"}, " -incoming"}} {
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
		if !strings.Contains(b.String(), tc.held) {
			t.Errorf("args %v printed %s, want %q", tc.flags, b.String(), tc.held)
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
