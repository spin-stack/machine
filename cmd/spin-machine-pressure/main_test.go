// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/internal/pressure"
)

// A host that is not there is dialled again later and later, up to lastRedial.
func TestAHostThatIsNotThereIsDialledLessOften(t *testing.T) {
	// A host that refuses at once: a real dial's own time, seconds where nothing answers vsock,
	// would read as part of each wait.
	s := sender{fd: -1, connect: func() (int, error) { return -1, unix.ECONNREFUSED }}
	var waits []time.Duration
	for range 7 {
		before := time.Now()
		s.dial(0)
		waits = append(waits, s.next.Sub(before).Round(time.Second))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i := range want {
		if waits[i] != want[i]*time.Second {
			t.Fatalf("the waits between dials are %v", waits)
		}
	}
}

// What the sender sends is one whole line each; a host that is gone is dropped, and dialled again
// a second later.
func TestTheSenderSendsWholeLinesAndDropsAHostThatIsGone(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := os.NewFile(uintptr(fds[1]), "host")
	defer func() { _ = host.Close() }()
	s := sender{fd: fds[0], redial: 8 * time.Second}
	s.send(pressure.OOM{Kills: 2})
	b := make([]byte, 64)
	n, _ := host.Read(b)
	if string(b[:n]) != "oom 2\n" {
		t.Errorf("the host read %q", b[:n])
	}
	_ = host.Close()
	before := time.Now()
	s.send(pressure.OOM{Kills: 3})
	if s.fd != -1 || s.redial != firstRedial || s.next.Sub(before).Round(time.Second) != firstRedial {
		t.Errorf("a host gone left %+v", s)
	}
	s.send(pressure.OOM{Kills: 4}) // nothing to send it to, and nothing breaks
}

// A host that answers is sent the hello first, with the kills so far, and is dialled no more.
func TestAHostThatAnswersIsSentTheHello(t *testing.T) {
	var host *os.File
	s := sender{fd: -1, redial: 8 * time.Second, connect: func() (int, error) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		host = os.NewFile(uintptr(fds[1]), "host")
		return fds[0], err
	}}
	s.dial(3)
	defer func() { _ = host.Close() }()
	b := make([]byte, 64)
	n, _ := host.Read(b)
	if string(b[:n]) != "spin-machine-pressure 1 oom=3\n" || s.fd < 0 || s.redial != 0 {
		t.Errorf("the host read %q, the sender is %+v", b[:n], s)
	}
	s.send(pressure.Stall{Resource: pressure.IO})
	n, _ = host.Read(b)
	if string(b[:n]) != "stall io 0 0 0\n" || s.fd < 0 {
		t.Errorf("the host read %q, the sender is %+v", b[:n], s)
	}
}

func TestMemoryEventsAreTheTopCgroups(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"system.slice", "user.slice", "init.scope/x"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"system.slice/memory.events", "user.slice/memory.events", "init.scope/x/memory.events"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("oom_kill 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := memoryEvents(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimPrefix(f.Name(), root+"/"))
		_ = f.Close()
	}
	if strings.Join(names, " ") != "system.slice/memory.events user.slice/memory.events" {
		t.Errorf("watched %v", names)
	}
	if _, err := memoryEvents(t.TempDir()); err == nil || !strings.Contains(err.Error(), "has memory.events") {
		t.Errorf("a hierarchy with none: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, "user.slice/memory.events"), 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := memoryEvents(root); err == nil {
			t.Error("a memory.events that cannot be opened was watched")
		}
	}
}

func TestOOMKillsAreVmstatsCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vmstat")
	for text, want := range map[string]string{
		"pgfault 9\noom_kill 3\nfoo 1\n": "",
		"pgfault 9\n":                    "has no oom_kill",
		"oom_kill x\n":                   "is not a count",
		"oom_kill a\n":                   "is not a count",
	} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		n, err := oomKills(path)
		switch {
		case want == "" && (err != nil || n != 3):
			t.Errorf("%q: %d, %v", text, n, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: %v, not %q", text, err, want)
		}
	}
	if _, err := oomKills(filepath.Join(dir, "none")); err == nil {
		t.Error("no vmstat read")
	}
}

// TestTheSenderReportsTheHostsOwnPressure is the sender's lane: run as root on the host itself,
// whose /proc/pressure, cgroups and vmstat are a kernel's like a guest's, with the host's
// connection a socket pair. The host's CPU is overloaded for a moment, and a cgroup of 32 MiB is
// made to kill: the sender must say both, after its hello. task boot:pressure runs it.
func TestTheSenderReportsTheHostsOwnPressure(t *testing.T) {
	if os.Getenv("SPIN_PRESSURE_TEST") != "1" {
		t.Skip("set SPIN_PRESSURE_TEST=1, as root, to put this host under pressure: task boot:pressure")
	}
	if os.Geteuid() != 0 {
		t.Fatal("a trigger with a window under 2 s is root's: task boot:pressure runs this under sudo")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := os.NewFile(uintptr(fds[1]), "host")
	defer func() { _ = host.Close() }()
	connected := false
	s := &sender{fd: -1, connect: func() (int, error) {
		if connected {
			return -1, unix.ECONNREFUSED
		}
		connected = true
		return fds[0], nil
	}}
	failed := make(chan error, 1)
	go func() { failed <- run(s) }()
	lines := make(chan string, 64)
	go func() {
		in := bufio.NewScanner(host)
		for in.Scan() {
			lines <- in.Text()
		}
		close(lines)
	}()
	next := func(prefix string) string {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the sender's connection ended before a %q line", prefix)
				}
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case err := <-failed:
				t.Fatalf("the sender failed: %v", err)
			case <-deadline:
				t.Fatalf("no %q line in 20 s", prefix)
			}
		}
	}
	t.Log(next("spin-machine-pressure 1 oom="))

	stop := make(chan struct{})
	for range 4 * runtime.NumCPU() {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}
	t.Log(next("stall cpu 150000 1000000 "))
	close(stop)

	parent := ""
	for _, d := range []string{"/sys/fs/cgroup/system.slice", "/sys/fs/cgroup/user.slice"} {
		if _, err := os.Stat(filepath.Join(d, "memory.events")); err == nil {
			parent = d
			break
		}
	}
	if parent == "" {
		t.Fatal("no top-level cgroup with memory.events to make a cgroup under")
	}
	cg := filepath.Join(parent, fmt.Sprintf("spin-pressure-sender-%d", os.Getpid()))
	if err := os.Mkdir(cg, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(cg) }()
	if err := os.WriteFile(filepath.Join(cg, "memory.max"), []byte("32M"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cg, "memory.swap.max"), []byte("0"), 0o644)
	dir, err := os.Open(cg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	hog := exec.Command("sh", "-c", "head -c 256M /dev/zero | tail")
	hog.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
	_ = hog.Run()
	t.Log(next("oom "))
}
