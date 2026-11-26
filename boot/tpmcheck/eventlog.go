// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha1" // #nosec G505 -- a PCR bank's algorithm, replayed, not chosen
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// event is one entry of a TCG2 crypto-agile event log, its digests by bank.
type event struct {
	pcr, typ uint32
	digests  map[tpm2.TPMAlgID][]byte
	data     []byte
}

// banks are the algorithms tpmcheck replays, and the hashes a bank's PCR is extended with.
var banks = map[tpm2.TPMAlgID]func() hash.Hash{
	tpm2.TPMAlgSHA1:   sha1.New,
	tpm2.TPMAlgSHA256: sha256.New,
	tpm2.TPMAlgSHA384: sha512.New384,
	tpm2.TPMAlgSHA512: sha512.New,
}

// parseLog reads a TCG2 crypto-agile log as Linux exports it: a Spec ID event in the TPM 1.2 format,
// then events whose digests are the banks it names, until the log ends or an entry has none.
func parseLog(b []byte) ([]event, error) {
	le := binary.LittleEndian
	r := bytes.NewReader(b)
	var first struct {
		PCR, Type uint32
		Digest    [20]byte
		Size      uint32
	}
	if err := binary.Read(r, le, &first); err != nil || first.Type != 3 {
		return nil, errors.New("the log does not start with an EV_NO_ACTION")
	}
	var spec struct {
		Signature [16]byte
		_         [8]byte
		Banks     uint32
	}
	if err := binary.Read(r, le, &spec); err != nil || string(spec.Signature[:]) != "Spec ID Event03\x00" {
		return nil, errors.New("the log's first event is not a Spec ID Event03")
	}
	sizes := map[tpm2.TPMAlgID]uint16{}
	for range spec.Banks {
		var bank struct{ Alg, Size uint16 }
		if err := binary.Read(r, le, &bank); err != nil {
			return nil, fmt.Errorf("the Spec ID event's banks: %w", err)
		}
		sizes[tpm2.TPMAlgID(bank.Alg)] = bank.Size
	}
	// What is left of the Spec ID event: its vendor information.
	if _, err := r.Seek(int64(32+first.Size), io.SeekStart); err != nil {
		return nil, err
	}
	var events []event
	for r.Len() > 0 {
		e, err := readEvent(r, sizes, int(spec.Banks))
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", len(events), err)
		}
		if e.digests == nil {
			break
		}
		events = append(events, e)
	}
	return events, nil
}

// readEvent is the next event of r, whose digests are of banks of sizes, or an event without
// digests for the zeroes past the log's last event.
func readEvent(r io.Reader, sizes map[tpm2.TPMAlgID]uint16, banks int) (event, error) {
	le := binary.LittleEndian
	var head struct{ PCR, Type, Count uint32 }
	if err := binary.Read(r, le, &head); err != nil {
		return event{}, fmt.Errorf("the log ends inside it: %w", err)
	}
	if head.Count == 0 {
		return event{}, nil
	}
	if int(head.Count) != banks {
		return event{}, fmt.Errorf("%d digests, and the log's banks are %d", head.Count, banks)
	}
	e := event{pcr: head.PCR, typ: head.Type, digests: map[tpm2.TPMAlgID][]byte{}}
	for range head.Count {
		var alg tpm2.TPMAlgID
		if err := binary.Read(r, le, &alg); err != nil {
			return event{}, fmt.Errorf("the log ends inside it: %w", err)
		}
		size, ok := sizes[alg]
		if !ok {
			return event{}, fmt.Errorf("a digest of algorithm %#x, which the log does not name", alg)
		}
		e.digests[alg] = make([]byte, size)
		if _, err := io.ReadFull(r, e.digests[alg]); err != nil {
			return event{}, fmt.Errorf("the log ends inside it: %w", err)
		}
	}
	var size uint32
	if err := binary.Read(r, le, &size); err != nil {
		return event{}, fmt.Errorf("the log ends inside it: %w", err)
	}
	e.data = make([]byte, size)
	if _, err := io.ReadFull(r, e.data); err != nil {
		return event{}, fmt.Errorf("the log ends inside it: %w", err)
	}
	return e, nil
}

// replay is each PCR's value in each bank after the events are extended into PCRs at zero.
func replay(events []event) (map[tpm2.TPMAlgID]map[uint32][]byte, error) {
	pcrs := map[tpm2.TPMAlgID]map[uint32][]byte{}
	for _, e := range events {
		for alg, d := range e.digests {
			newHash, ok := banks[alg]
			if !ok {
				return nil, fmt.Errorf("a bank of algorithm %#x, which tpmcheck does not replay", alg)
			}
			if pcrs[alg] == nil {
				pcrs[alg] = map[uint32][]byte{}
			}
			h := newHash()
			v, ok := pcrs[alg][e.pcr]
			if !ok {
				v = make([]byte, h.Size())
			}
			h.Write(v)
			h.Write(d)
			pcrs[alg][e.pcr] = h.Sum(nil)
		}
	}
	return pcrs, nil
}

// checkLog is what tpmcheck log prints of the log b and the TPM: a line for each event, its PCR,
// type, SHA-256 digest and data, a line for each PCR the log names in each bank with the value the
// TPM holds, then SPIN-TPM-LOG with the event count and the banks whose every
// PCR the log names holds what the log replays to. A PCR that does not is an error.
func checkLog(tpm transport.TPM, b []byte) (string, error) {
	events, err := parseLog(b)
	if err != nil {
		return "", err
	}
	want, err := replay(events)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, e := range events {
		fmt.Fprintf(&out, "SPIN-TPM-EVENT pcr=%d type=%#x sha256=%x data=%x\n", e.pcr, e.typ, e.digests[tpm2.TPMAlgSHA256], e.data)
	}
	var replayed []string
	for _, alg := range []tpm2.TPMAlgID{tpm2.TPMAlgSHA1, tpm2.TPMAlgSHA256, tpm2.TPMAlgSHA384, tpm2.TPMAlgSHA512} {
		for _, pcr := range slices.Sorted(maps.Keys(want[alg])) {
			got, err := readPCRBank(tpm, alg, pcr)
			if err != nil {
				return "", err
			}
			if !bytes.Equal(got, want[alg][pcr]) {
				return "", fmt.Errorf("PCR %d of bank %#x holds %x, and the log replays to %x", pcr, alg, got, want[alg][pcr])
			}
			fmt.Fprintf(&out, "SPIN-TPM-PCR bank=%#x pcr=%d value=%x\n", alg, pcr, got)
		}
		if want[alg] != nil {
			replayed = append(replayed, fmt.Sprintf("%#x", alg))
		}
	}
	fmt.Fprintf(&out, "SPIN-TPM-LOG events=%d replayed=%s", len(events), strings.Join(replayed, ","))
	return out.String(), nil
}

func readPCRBank(tpm transport.TPM, alg tpm2.TPMAlgID, pcr uint32) ([]byte, error) {
	rsp, err := tpm2.PCRRead{PCRSelectionIn: tpm2.TPMLPCRSelection{PCRSelections: []tpm2.TPMSPCRSelection{
		{Hash: alg, PCRSelect: tpm2.PCClientCompatible.PCRs(uint(pcr))},
	}}}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("reading PCR %d of bank %#x: %w", pcr, alg, err)
	}
	if len(rsp.PCRValues.Digests) != 1 {
		return nil, fmt.Errorf("reading PCR %d of bank %#x: %d digests", pcr, alg, len(rsp.PCRValues.Digests))
	}
	return rsp.PCRValues.Digests[0].Buffer, nil
}
