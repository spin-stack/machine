// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/simulator"
)

// logged is an event as the firmware logs it: its SHA-256 digest, which a bank of another size
// takes cut or zero-padded (qemu/qboot/tpm.patch).
type logged struct {
	pcr, typ uint32
	sha256   [32]byte
	data     string
}

var sizes = map[tpm2.TPMAlgID]int{tpm2.TPMAlgSM3256: 32, tpm2.TPMAlgSHA1: 20, tpm2.TPMAlgSHA256: 32, tpm2.TPMAlgSHA384: 48, tpm2.TPMAlgSHA512: 64}

func digestFor(alg tpm2.TPMAlgID, d [32]byte) []byte {
	out := make([]byte, sizes[alg])
	copy(out, d[:])
	return out
}

// firmwareLog is the log qboot writes of events over banks: a Spec ID Event03, then each event with
// a digest for every bank.
func firmwareLog(banks []tpm2.TPMAlgID, events []logged) []byte {
	return logOf(banks, banks, events)
}

// logOf is a log whose Spec ID event names banks and whose events have digests of digested.
func logOf(banks, digested []tpm2.TPMAlgID, events []logged) []byte {
	le := binary.LittleEndian
	spec := append([]byte("Spec ID Event03\x00"), 0, 0, 0, 0, 0, 2, 2, 2)
	spec = le.AppendUint32(spec, uint32(len(banks)))
	for _, alg := range banks {
		spec = le.AppendUint16(spec, uint16(alg))
		spec = le.AppendUint16(spec, uint16(sizes[alg]))
	}
	spec = append(spec, 0)
	b := le.AppendUint32(nil, 0)
	b = le.AppendUint32(b, 3)
	b = append(b, make([]byte, 20)...)
	b = le.AppendUint32(b, uint32(len(spec)))
	b = append(b, spec...)
	for _, e := range events {
		b = le.AppendUint32(b, e.pcr)
		b = le.AppendUint32(b, e.typ)
		b = le.AppendUint32(b, uint32(len(digested)))
		for _, alg := range digested {
			b = le.AppendUint16(b, uint16(alg))
			b = append(b, digestFor(alg, e.sha256)...)
		}
		b = le.AppendUint32(b, uint32(len(e.data)))
		b = append(b, e.data...)
	}
	// The area QEMU hands the firmware is zeroes past the last event.
	return append(b, make([]byte, 64)...)
}

// firmwareExtend does to tpm what the firmware does for e.
func firmwareExtend(t *testing.T, tpm transport.TPM, banks []tpm2.TPMAlgID, e logged) {
	t.Helper()
	var digests []tpm2.TPMTHA
	for _, alg := range banks {
		digests = append(digests, tpm2.TPMTHA{HashAlg: alg, Digest: digestFor(alg, e.sha256)})
	}
	if _, err := (tpm2.PCRExtend{
		PCRHandle: tpm2.AuthHandle{Handle: tpm2.TPMHandle(e.pcr), Auth: tpm2.PasswordAuth(nil)},
		Digests:   tpm2.TPMLDigestValues{Digests: digests},
	}).Execute(tpm); err != nil {
		t.Fatal(err)
	}
}

// The reference TPM's banks with a PCR selected.
func activeBanks(t *testing.T, tpm transport.TPM) []tpm2.TPMAlgID {
	t.Helper()
	rsp, err := tpm2.GetCapability{Capability: tpm2.TPMCapPCRs, PropertyCount: 1}.Execute(tpm)
	if err != nil {
		t.Fatal(err)
	}
	pcrs, err := rsp.CapabilityData.Data.AssignedPCR()
	if err != nil {
		t.Fatal(err)
	}
	var banks []tpm2.TPMAlgID
	for _, s := range pcrs.PCRSelections {
		if _, ok := sizes[s.Hash]; ok && strings.Trim(string(s.PCRSelect), "\x00") != "" {
			banks = append(banks, s.Hash)
		}
	}
	return banks
}

// tpmcheck log prints each event of a log the TPM's PCRs replay to, and names the banks it replayed;
// a PCR the log does not account for, or a log that is not a TCG2 one, fails it.
func TestTpmcheckLogHoldsTheLogToThePCRs(t *testing.T) {
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatalf("the reference TPM: %v", err)
	}
	t.Cleanup(func() { _ = tpm.Close() })
	banks := activeBanks(t, tpm)
	events := []logged{
		{4, 0xd, sha256.Sum256([]byte("vmlinux")), "Linux kernel"},
		{9, 0x6, sha256.Sum256([]byte("console=ttyS0")), "\xed\x22\x3b\x8f\x1a\x00\x00\x00LOADED_IMAGE::LoadOptions\x00"},
		{9, 0x6, sha256.Sum256([]byte("initrd")), "\xec\x22\x3b\x8f\x0d\x00\x00\x00Linux initrd\x00"},
	}
	for _, e := range events {
		firmwareExtend(t, tpm, banks, e)
	}
	log := firmwareLog(banks, events)
	got, err := checkLog(tpm, log)
	if err != nil {
		t.Fatalf("a log the PCRs replay to: %v", err)
	}
	var names []string
	for _, alg := range banks {
		names = append(names, fmt.Sprintf("%#x", alg))
	}
	want := fmt.Sprintf("SPIN-TPM-EVENT pcr=4 type=0xd sha256=%x data=%x\n", events[0].sha256, events[0].data) +
		fmt.Sprintf("SPIN-TPM-EVENT pcr=9 type=0x6 sha256=%x data=%x\n", events[1].sha256, events[1].data) +
		fmt.Sprintf("SPIN-TPM-EVENT pcr=9 type=0x6 sha256=%x data=%x\n", events[2].sha256, events[2].data) +
		"SPIN-TPM-LOG events=3 replayed=" + strings.Join(names, ",")
	if got != want {
		t.Errorf("tpmcheck log said\n%s\nwant\n%s", got, want)
	}

	// The log less its last event: PCR 9 holds an extend it does not account for.
	short := firmwareLog(banks, events[:2])
	if _, err := checkLog(tpm, short); err == nil || !strings.Contains(err.Error(), "PCR 9") {
		t.Errorf("a log one event short: %v", err)
	}
	for _, tc := range []struct {
		name string
		log  []byte
		says string
	}{
		{"no log", nil, "EV_NO_ACTION"},
		{"another first event", append([]byte{0, 0, 0, 0, 3, 0, 0, 0}, make([]byte, 60)...), "Spec ID Event03"},
		{"a first event of another type", append([]byte{0, 0, 0, 0, 4}, log[5:]...), "EV_NO_ACTION"},
		{"a byte past the last event", append(log[:len(log)-64:len(log)-64], 1), "event 3: the log ends inside it"},
		{"cut inside an event's data", log[:len(log)-64-10], "event 2: the log ends inside it"},
		{"cut inside a digest", log[:len(log)-64-len(events[2].data)-4-10], "event 2: the log ends inside it"},
		{"cut after an event's digest count", log[:len(log)-64-len(events[2].data)-4-len(banks)*2-10], "event 2: the log ends inside it"},
		{"an event of fewer banks", logOf(banks, banks[:1], events), "event 0: 1 digests"},
		{"a digest of a bank the log does not name", logOf(banks, append([]tpm2.TPMAlgID{tpm2.TPMAlgSM3256}, banks[1:]...), events), "algorithm 0x12"},
	} {
		if _, err := checkLog(tpm, tc.log); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.says)
		}
	}
}

// Every bank replays with its own hash: SHA-384's PCR is SHA-384 of the PCR and the digest.
func TestReplayExtendsEachBankWithItsHash(t *testing.T) {
	d := []byte{1, 2, 3}
	got, err := replay([]event{{pcr: 9, digests: map[tpm2.TPMAlgID][]byte{tpm2.TPMAlgSHA384: d, tpm2.TPMAlgSHA1: d}}})
	if err != nil {
		t.Fatal(err)
	}
	h := banks[tpm2.TPMAlgSHA384]()
	h.Write(make([]byte, 48))
	h.Write(d)
	if fmt.Sprintf("%x", got[tpm2.TPMAlgSHA384][9]) != fmt.Sprintf("%x", h.Sum(nil)) || len(got[tpm2.TPMAlgSHA1][9]) != 20 {
		t.Errorf("replayed %x", got)
	}
	if _, err := replay([]event{{digests: map[tpm2.TPMAlgID][]byte{tpm2.TPMAlgSM3256: d}}}); err == nil {
		t.Error("a bank tpmcheck has no hash for replayed")
	}
}
