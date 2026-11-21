// SPDX-License-Identifier: Apache-2.0

package boot_test

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spin-stack/spin-machine/machine"
)

// A checkpoint the release before saved resumes on this tree's machine when the two are one
// machine, and this holds CanResume's answer to QEMU's. The guest is booted at spin's shape on
// the previous release's QEMU, kernel and firmware, over an overlay of its base image, with a
// counter printing on its console; this tree's spin-machine saves it, which records the machine
// as that QEMU reports it. A machine of this tree's QEMU, held for a checkpoint over the same
// overlay, says whether it resumes it (machine.CanResume); if so it is restored, and the counter
// has to go on from where it was and the disk has to answer.
//
// When this machine refuses the checkpoint, the machine moved and no checkpoint of the previous
// release is meant to resume: the test says so with every difference, holds nothing, and writes
// the differences to SPIN_COMPAT_REFUSED for lab compat's row.
//
//	SPIN_COMPAT_PREVIOUS=<dir>   the previous release's usr/share/spin-stack (task boot:compat)
//	SPIN_COMPAT_REFUSED=<file>   where a refusal is said (spin-machine-dev lab compat)
func TestACheckpointOfTheReleaseBeforeResumesHere(t *testing.T) {
	previous := os.Getenv("SPIN_COMPAT_PREVIOUS")
	if previous == "" {
		t.Skip("set SPIN_COMPAT_PREVIOUS to a release's usr/share/spin-stack: task boot:compat fetches the newest")
	}
	out := releaseDir(t)
	cli := filepath.Join(out, "bin", "spin-machine")
	dir := t.TempDir()
	socks, err := os.MkdirTemp("/tmp", "compat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socks) })
	overlay := filepath.Join(dir, "root.qcow2")
	if b, err := exec.Command(filepath.Join(out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", //nolint:gosec // this tree's qemu-img
		"-b", filepath.Join(previous, "image", "rootfs.qcow2"), "-F", "qcow2", overlay).CombinedOutput(); err != nil {
		t.Fatalf("an overlay of the previous base image: %v: %s", err, b)
	}
	state := filepath.Join(dir, "state")
	cid := 3500000 + os.Getpid()%100000*10
	machineOf := func(release, sock string, n int, more ...string) *guest {
		args, start := machineArgs(t, cli, release, slices.Concat(spinsShape, []string{"--qmp", sock, "--vsock-cid", fmt.Sprint(cid + n),
			"--disk", overlay, "--disk-format", "qcow2", "--init", "/bin/sh"}, more))
		g := startGuest(t, append(args, spinsNIC(fmt.Sprintf("52:54:00:00:01:%02x", n))...))
		if !slices.Contains(more, "--restore") {
			start(t)
		}
		return g
	}

	aSock := filepath.Join(socks, "a")
	a := machineOf(previous, aSock, 1)
	a.type_(t, "mount -t proc proc /proc; ( i=0; while :; do i=$((i+1)); echo TICK-$i; sleep 0.2; done ) &")
	a.await(t, regexp.MustCompile(`TICK-5\b`), 60*time.Second)
	// #nosec G204 -- this tree's spin-machine
	if o, err := exec.Command(cli, "save", "--qmp", aSock, "--to", state).CombinedOutput(); err != nil {
		t.Fatalf("saving: %v: %s\n%s", err, o, a.said())
	}
	last := a.lastTick()
	a.kill()

	bSock := filepath.Join(socks, "b")
	b := machineOf(out, bSock, 2, "--restore")
	var saved machine.Saved
	if raw, err := os.ReadFile(state + ".json"); err != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatalf("what save wrote beside the state: %v", err)
	}
	m := attachTo(t, bSock, b)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	here, err := m.Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	var refusal *machine.Refusal
	if errors.As(machine.CanResume(saved, here), &refusal) {
		var ds []string
		for _, d := range refusal.Differences {
			ds = append(ds, d.String())
		}
		if f := os.Getenv("SPIN_COMPAT_REFUSED"); f != "" {
			if err := os.WriteFile(f, []byte(strings.Join(ds, "; ")), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Skipf("the machine of %s is not this one:\n  %s", previous, strings.Join(ds, "\n  "))
	}
	// #nosec G204 -- this tree's spin-machine
	if o, err := exec.Command(cli, "restore", "--release", out, "--qmp", bSock, "--from", state,
		"--disk", overlay, "--disk-format", "qcow2").CombinedOutput(); err != nil {
		t.Fatalf("restoring: %v: %s\n%s", err, o, b.said())
	}
	b.await(t, regexp.MustCompile(fmt.Sprintf(`TICK-%d\b`, last+3)), 30*time.Second)
	// Read through the disk the guest had before it was saved: a file it never opened, so
	// the read reaches the restored virtio-blk and the overlay behind it.
	b.type_(t, "echo READ-$(wc -c < /etc/os-release)")
	b.await(t, regexp.MustCompile(`READ-[1-9][0-9]*`), 30*time.Second)
	rel, err := machine.OpenRelease(previous)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a checkpoint of %s, saved at TICK-%d, went on counting on this tree's machine", rel.Version(), last)
}

// guest is one QEMU whose console is its stdin and stdout.
type guest struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	out   bytes.Buffer
}

func startGuest(t *testing.T, args []string) *guest {
	t.Helper()
	g := &guest{cmd: exec.Command(args[0], args[1:]...)} //nolint:gosec // the machine's own command line
	stdin, err := g.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	g.stdin = stdin
	r, w := io.Pipe()
	g.cmd.Stdout, g.cmd.Stderr = w, w
	if err := g.cmd.Start(); err != nil {
		t.Fatalf("starting QEMU: %v", err)
	}
	go func() {
		buf := make([]byte, 4096)
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
	t.Cleanup(g.kill)
	return g
}

// type_ is a line on the console. The guest's shell is not reading it when QEMU starts, so the
// first bytes are lost: blank lines go first, each costing only a prompt.
func (g *guest) type_(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(g.stdin, "\n\n\n\n\n\n\n\n\n\n"+line+"\n"); err != nil {
		t.Fatalf("typing on the console: %v", err)
	}
}

func (g *guest) said() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.out.String()
}

// await is the console printing what matches re, or the test failing with what it did print.
func (g *guest) await(t *testing.T, re *regexp.Regexp, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !re.MatchString(g.said()) {
		if time.Now().After(deadline) {
			t.Fatalf("the console never said %s in %s:\n%s", re, timeout, tail([]byte(g.said()), 2000))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lastTick is the highest TICK the console has printed.
func (g *guest) lastTick() int {
	last := 0
	for _, m := range regexp.MustCompile(`TICK-(\d+)`).FindAllStringSubmatch(g.said(), -1) {
		if n, _ := strconv.Atoi(m[1]); n > last {
			last = n
		}
	}
	return last
}

func (g *guest) kill() {
	if g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_ = g.cmd.Wait()
	}
}
