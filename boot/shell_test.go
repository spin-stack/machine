// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestShellIsRootUnderSystemd is `spin-machine-dev shell` as a person runs it, over the release:
// its console reaches a root prompt with systemd as PID 1, and the base image is unchanged after.
// The image has no autologin and root no password, so the prompt comes only from the credential
// shell puts on its own command line (#362).
//
//	SPIN_SHELL_TEST=1  it starts a machine (task boot:shell)
func TestShellIsRootUnderSystemd(t *testing.T) {
	if os.Getenv("SPIN_SHELL_TEST") == "" {
		t.Skip("set SPIN_SHELL_TEST=1: this starts a machine")
	}
	dev := filepath.Join(t.TempDir(), "spin-machine-dev")
	if out, err := exec.Command("go", "build", "-o", dev, "../cmd/spin-machine-dev").CombinedOutput(); err != nil {
		t.Fatalf("building spin-machine-dev: %v\n%s", err, out)
	}
	cmd := exec.Command(dev, "--out", releaseTree(t), "shell")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	screen := newScreen()
	cmd.Stdout, cmd.Stderr = screen, screen
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting spin-machine-dev shell: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	await := func(re *regexp.Regexp, within time.Duration) {
		t.Helper()
		deadline := time.After(within)
		for !re.MatchString(screen.String()) {
			select {
			case <-screen.news:
			case <-deadline:
				t.Fatalf("no %q on shell's console in %s:\n%s", re, within, tail([]byte(screen.String()), 3000))
			}
		}
	}
	await(regexp.MustCompile(`root@\S*:~# `), 90*time.Second)
	if _, err := io.WriteString(stdin, "echo PID1-$(cat /proc/1/comm)-UID$(id -u)\n"); err != nil {
		t.Fatal(err)
	}
	await(regexp.MustCompile(`PID1-systemd-UID0`), 30*time.Second)
	if _, err := io.WriteString(stdin, "poweroff -f\n"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil || !strings.Contains(screen.String(), "rootfs.qcow2 unchanged") {
			t.Fatalf("shell ended with %v:\n%s", err, tail([]byte(screen.String()), 3000))
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("shell did not end after poweroff -f:\n%s", tail([]byte(screen.String()), 3000))
	}
}
