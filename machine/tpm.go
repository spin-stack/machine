// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// TPM is a machine's TPM 2.0: swtpm, which Start runs beside QEMU, behind QEMU's tpm-emulator
// backend and the tpm-crb device (docs/machine.md, "A TPM").
//
// The zero value is a machine with none. shirudo keeps its measurement register in an NV index of
// it (spin-stack/machine#719), so a workspace that runs shirudo is a machine with one.
type TPM struct {
	// SWTPM is the swtpm binary (Release.SWTPM).
	SWTPM string

	// State is the directory swtpm keeps the TPM's state in. It is the workspace's, like a disk:
	// the caller makes it, keeps it and hands the same one to every machine of that workspace,
	// and it outlives each of them. A TPM's keys are made from seeds in it, and an NV index
	// written once is in it from then on. An empty directory is a TPM made at its first start,
	// with seeds of its own and no endorsement key: provisioning one is the caller's, before the
	// first start (swtpm_setup, or the TPM2 commands themselves). swtpm locks it, so two machines
	// cannot run one TPM. A resume overwrites it with the TPM of the checkpoint, which the
	// migration stream carries with the guest's memory: the TPM the guest saw is the one it
	// resumes with. Setting it is what gives the machine a TPM.
	State string

	// FD is QEMU's end of swtpm's control channel, a connected socket. Start makes the pair; a
	// caller of Args alone names the descriptor the command line it prints would be handed.
	FD int

	// Device is the experiment of #719's: tpm-crb or tpm-tis, measured against each other.
	Device string
}

// TPMDevice is the TPM's interface to the guest. tpm-crb is a command buffer in memory the guest
// writes a whole command to, where tpm-tis is a FIFO it writes a byte at a time, an MMIO exit
// each; docs/machine.md, "A TPM", has what each cost a boot.
const TPMDevice = "tpm-crb"

func (t TPM) on() bool { return t.State != "" }

func (t TPM) validate() error {
	switch {
	case !t.on() && (t.SWTPM != "" || t.FD != 0):
		return errors.New("a TPM with no state directory: State is what gives the machine one")
	case !t.on():
		return nil
	case t.SWTPM == "":
		return errors.New("a TPM with no swtpm binary to emulate it")
	case t.FD < 3:
		return fmt.Errorf("the TPM's control channel is descriptor %d, which is stdin, stdout or stderr (Start makes it)", t.FD)
	}
	return nil
}

// appendTPM is the TPM's backend on its control channel, and its device.
func (s Spec) appendTPM(args []string) []string {
	if !s.TPM.on() {
		return args
	}
	return append(args,
		"-chardev", fmt.Sprintf("socket,id=%s,fd=%d", tpmChardev, s.TPM.FD),
		"-tpmdev", fmt.Sprintf("emulator,id=%s,chardev=%s", tpmBackend, tpmChardev),
		"-device", fmt.Sprintf("%s,tpmdev=%s", cmp.Or(s.TPM.Device, TPMDevice), tpmBackend))
}

const (
	tpmChardev = "tpm"
	tpmBackend = "tpm0"
)

// swtpmWait is how long a machine's end waits for swtpm to follow QEMU out, once QEMU's end of the
// control channel is closed, before it kills it.
const swtpmWait = 5 * time.Second

// swtpm is the TPM's emulator as Start runs it: one process for one machine, ended by the end of
// QEMU's control channel.
type swtpm struct {
	cmd    *exec.Cmd
	stderr *tail
	exited chan struct{}
}

// startSWTPM starts swtpm on t.State, its control channel one end of a socket pair whose other
// end it returns, for QEMU. shape is StartOptions.TPMCmd.
//
// A pair and not a socket at a path: nothing is bound, so no 108-byte sun_path to fit, no socket
// file to find or remove, and nothing for another process to connect to; and swtpm takes the
// connected end (clientfd=), so there is no wait for it to listen. QEMU asks swtpm for the data
// channel over it, a second pair swtpm makes and passes back.
func startSWTPM(t TPM, shape func(*exec.Cmd)) (*swtpm, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("making the TPM's control channel: %w", err)
	}
	qemu, own := os.NewFile(uintptr(fds[0]), "tpm-qemu"), os.NewFile(uintptr(fds[1]), "tpm-swtpm")
	defer func() { _ = own.Close() }()
	if err := os.MkdirAll(t.State, 0o700); err != nil {
		_ = qemu.Close()
		return nil, nil, fmt.Errorf("making the TPM's state directory: %w", err)
	}
	w := &swtpm{stderr: &tail{}, exited: make(chan struct{})}
	// #nosec G204 -- the caller's swtpm and its state directory
	w.cmd = exec.Command(t.SWTPM, "socket", "--tpm2",
		"--tpmstate", "dir="+t.State+",mode=0600",
		"--ctrl", "type=unixio,clientfd=3")
	w.cmd.ExtraFiles = []*os.File{own}
	w.cmd.Stderr = w.stderr
	if shape != nil {
		shape(w.cmd)
	}
	if err := w.cmd.Start(); err != nil {
		_ = qemu.Close()
		return nil, nil, fmt.Errorf("starting %s: %w", t.SWTPM, err)
	}
	go func() {
		_ = w.cmd.Wait()
		close(w.exited)
	}()
	return w, qemu, nil
}

// end waits for swtpm to exit, as it does once QEMU's end of the control channel is closed, and
// kills it after swtpmWait. What it said is returned when it ended otherwise than with 0.
func (w *swtpm) end() string {
	select {
	case <-w.exited:
	case <-time.After(swtpmWait):
		_ = w.cmd.Process.Kill()
		<-w.exited
	}
	if w.cmd.ProcessState.Success() {
		return ""
	}
	return fmt.Sprintf("swtpm %s: %s", w.cmd.ProcessState, w.stderr.String())
}
