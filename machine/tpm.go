// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

	// Kernel and Initrd are the SHA-256 digests of Spec.Kernel and Spec.Initrd, which the firmware
	// extends into the TPM's PCRs before the kernel runs (Spec.TPMEvents). The caller hashes them
	// (FileSHA256), once a file and not once a machine: hashing a 60 MB initrd at a boot would be
	// on the first login's clock, and qboot cannot see the vmlinux QEMU loads for PVH at all. A
	// machine started to restore (Spec.Restore) needs neither: its firmware never runs.
	Kernel, Initrd [sha256.Size]byte
}

// TPMDevice is the TPM's interface to the guest, the one QEMU is built with (qemu/devices.mak, which
// has the measurement it was chosen by): a command buffer in memory the guest writes a whole
// command to, where tpm-tis is a FIFO with an exit a byte. A TPM costs the cold first login ~4 ms
// with it since kernel 0032 and swtpm's patch (+31.7 before both), and a restored one ~2 ms
// (docs/boot-time.md, "A machine with a TPM", 2026-11-26), which is why it is a machine's to ask
// for and not every machine's.
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

// FileSHA256 is the SHA-256 digest of the file at path: TPM.Kernel or TPM.Initrd.
func FileSHA256(path string) ([sha256.Size]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- the caller's kernel or initrd
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("hashing %s: %w", path, err)
	}
	return [sha256.Size]byte(h.Sum(nil)), nil
}

// TPMEvent is one measurement a machine's firmware makes before the kernel runs: a PCR extended
// with the SHA-256 digest of what was measured, and the event the TCG event log records for it,
// which the guest reads at /sys/kernel/security/tpm0/binary_bios_measurements.
type TPMEvent struct {
	PCR    uint32
	Type   uint32
	Digest [sha256.Size]byte
	Data   []byte
}

// The TCG PC Client event types the firmware logs.
const (
	EvSeparator = 0x4
	EvEventTag  = 0x6
	EvIPL       = 0xd
)

// TPMEvents are what the firmware of a machine with a TPM measures as it boots, in order, and nil
// for a machine without one or started to restore. They follow where a UEFI boot puts the same things (the TCG PC Client firmware
// profile, the UAPI group's Linux TPM PCR registry): PCRs 0 to 7 end the firmware's phase with a
// separator; the kernel is the code the firmware boots, in PCR 4; and the command line and the
// initrd are in PCR 9, as tagged events of the tags and descriptions Linux's EFI stub uses, which
// is what shirudo binds its register under. The command line is the one QEMU hands the kernel, its
// bytes without a NUL; the stub measures UEFI's UTF-16 LoadOptions instead, which a PVH boot has none of.
func (s Spec) TPMEvents() []TPMEvent {
	if !s.TPM.on() || s.Restore {
		return nil
	}
	separator := []byte{0, 0, 0, 0}
	var events []TPMEvent
	for pcr := range uint32(8) {
		events = append(events, TPMEvent{PCR: pcr, Type: EvSeparator, Digest: sha256.Sum256(separator), Data: separator})
	}
	events = append(events, TPMEvent{PCR: 4, Type: EvIPL, Digest: s.TPM.Kernel, Data: []byte("Linux kernel")})
	events = append(events, tagged(sha256.Sum256([]byte(s.cmdline().String())), 0x8f3b22ed, "LOADED_IMAGE::LoadOptions"))
	if s.Initrd != "" {
		events = append(events, tagged(s.TPM.Initrd, 0x8f3b22ec, "Linux initrd"))
	}
	return events
}

// tagged is an EV_EVENT_TAG in PCR 9: a TCG_PCClientTaggedEvent of the tag and its description
// with the NUL that ends it.
func tagged(digest [sha256.Size]byte, tag uint32, description string) TPMEvent {
	data := binary.LittleEndian.AppendUint32(nil, tag)
	data = binary.LittleEndian.AppendUint32(data, uint32(len(description)+1)) // #nosec G115 -- a constant's length
	data = append(append(data, description...), 0)
	return TPMEvent{PCR: 9, Type: EvEventTag, Digest: digest, Data: data}
}

// tpmEventsFile is the fw_cfg file qboot measures from (qemu/qboot/tpm.patch), and tpmEventsArg
// its contents: each event as four hex tokens, its PCR, type, digest and data, all separated by
// spaces. Text, because -fw_cfg string= takes no NUL.
const tpmEventsFile = "opt/org.spin-stack/tpm-events"

func tpmEventsArg(events []TPMEvent) string {
	var tokens []string
	for _, e := range events {
		tokens = append(tokens, fmt.Sprintf("%02x", e.PCR), fmt.Sprintf("%08x", e.Type), fmt.Sprintf("%x", e.Digest), fmt.Sprintf("%x", e.Data))
	}
	return "name=" + tpmEventsFile + ",string=" + strings.Join(tokens, " ")
}

// appendTPM is the TPM's backend on its control channel, and its device.
func (s Spec) appendTPM(args []string) []string {
	if !s.TPM.on() {
		return args
	}
	args = append(args,
		"-chardev", fmt.Sprintf("socket,id=%s,fd=%d", tpmChardev, s.TPM.FD),
		"-tpmdev", fmt.Sprintf("emulator,id=%s,chardev=%s", tpmBackend, tpmChardev),
		"-device", fmt.Sprintf("%s,tpmdev=%s", TPMDevice, tpmBackend))
	if events := s.TPMEvents(); events != nil {
		args = append(args, "-fw_cfg", tpmEventsArg(events))
	}
	return args
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
