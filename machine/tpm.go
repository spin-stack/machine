// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
}

// TPMDevice is the TPM's interface to the guest, the one QEMU is built with (qemu/devices.mak, which
// has the measurement it was chosen by): a command buffer in memory the guest writes a whole
// command to, where tpm-tis is a FIFO with an exit a byte. A TPM costs the cold first login +36 ms
// with it, and a restored one +6 ms (labs 45748711725, 45748711747, 2026-11-26), which is why it is
// a machine's to ask for and not every machine's.
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
		"-device", fmt.Sprintf("%s,tpmdev=%s", TPMDevice, tpmBackend))
}

const (
	tpmChardev = "tpm"
	tpmBackend = "tpm0"
)

// startSWTPM starts swtpm on t.State, its control channel one end of a socket pair whose other
// end it returns, for QEMU. shape is StartOptions.TPMCmd.
//
// swtpm takes the connected end (clientfd=), so there is no wait for it to listen. QEMU asks swtpm
// for the data channel over it, a second pair swtpm makes and passes back.
func startSWTPM(t TPM, shape func(*exec.Cmd)) (*beside, *os.File, error) {
	if err := os.MkdirAll(t.State, 0o700); err != nil {
		return nil, nil, fmt.Errorf("making the TPM's state directory: %w", err)
	}
	// #nosec G204 -- the caller's swtpm and its state directory
	cmd := exec.Command(t.SWTPM, "socket", "--tpm2",
		"--tpmstate", "dir="+t.State+",mode=0600",
		"--ctrl", "type=unixio,clientfd=3")
	return startBeside("swtpm", "the TPM's control channel", cmd, shape)
}
