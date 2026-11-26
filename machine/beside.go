// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// besideWait is how long a machine's end waits for a process beside QEMU to follow QEMU out, once
// QEMU's end of their socket is closed, before it kills it.
const besideWait = 5 * time.Second

// beside is a process Start runs beside QEMU for one machine, on one end of a socket pair whose
// other end is QEMU's, and which QEMU's end closing ends: swtpm for a TPM, passt for a NIC.
type beside struct {
	name   string
	cmd    *exec.Cmd
	stderr *tail
	exited chan struct{}
}

// startBeside starts cmd with one end of a new socket pair as its descriptor 3, and returns the
// other end, for QEMU. channel names the pair in an error, shape is the caller's (StartOptions.TPMCmd).
//
// A pair and not a socket at a path: nothing is bound, so no 108-byte sun_path to fit, no socket
// file to find or remove, nothing for another process to connect to, and no wait for the process
// to listen.
func startBeside(name, channel string, cmd *exec.Cmd, shape func(*exec.Cmd)) (*beside, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0) // mutate-exempt: protocol 1 is PF_UNIX, which AF_UNIX takes as it takes 0 (unix_create)
	if err != nil {
		return nil, nil, fmt.Errorf("making %s: %w", channel, err)
	}
	qemu, own := os.NewFile(uintptr(fds[0]), name+"-qemu"), os.NewFile(uintptr(fds[1]), name)
	defer func() { _ = own.Close() }()
	b := &beside{name: name, cmd: cmd, stderr: &tail{}, exited: make(chan struct{})}
	cmd.ExtraFiles = []*os.File{own}
	cmd.Stderr = b.stderr
	if shape != nil {
		shape(cmd)
	}
	if err := cmd.Start(); err != nil {
		_ = qemu.Close()
		return nil, nil, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	go func() {
		_ = cmd.Wait()
		close(b.exited)
	}()
	return b, qemu, nil
}

// end waits for the process to exit, as it does once QEMU's end of its socket is closed, and kills
// it after besideWait. What it said is returned when it ended otherwise than with 0.
func (b *beside) end() string {
	select {
	case <-b.exited:
	case <-time.After(besideWait):
		_ = b.cmd.Process.Kill()
		<-b.exited
	}
	if b.cmd.ProcessState.Success() {
		return ""
	}
	return fmt.Sprintf("%s %s: %s", b.name, b.cmd.ProcessState, b.stderr.String())
}
