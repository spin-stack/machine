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
// then events whose digests are the banks it names, until the first entry with none.
func parseLog(b []byte) ([]event, error) {
	le := binary.LittleEndian
	if len(b) < 32 || le.Uint32(b[4:]) != 3 {
		return nil, errors.New("the log does not start with an EV_NO_ACTION")
	}
	size := int(le.Uint32(b[28:]))
	spec := b[32:]
	if len(spec) < size || size < 29 || !bytes.Equal(spec[:16], []byte("Spec ID Event03\x00")) {
		return nil, errors.New("the log's first event is not a Spec ID Event03")
	}
	n := int(le.Uint32(spec[24:]))
	if size < 29+4*n {
		return nil, errors.New("the Spec ID event is shorter than its algorithms")
	}
	sizes := map[tpm2.TPMAlgID]int{}
	order := make([]tpm2.TPMAlgID, n)
	for i := range n {
		order[i] = tpm2.TPMAlgID(le.Uint16(spec[28+4*i:]))
		sizes[order[i]] = int(le.Uint16(spec[30+4*i:]))
	}
	rest := spec[size:]
	var events []event
	for len(rest) >= 12 {
		count := int(le.Uint32(rest[8:]))
		if count == 0 {
			break
		}
		if count != n {
			return nil, fmt.Errorf("event %d has %d digests, the log's banks are %d", len(events), count, n)
		}
		e := event{pcr: le.Uint32(rest), typ: le.Uint32(rest[4:]), digests: map[tpm2.TPMAlgID][]byte{}}
		p := rest[12:]
		for range count {
			if len(p) < 2 {
				return nil, errors.New("the log ends inside an event")
			}
			alg := tpm2.TPMAlgID(le.Uint16(p))
			s, ok := sizes[alg]
			if !ok || len(p) < 2+s {
				return nil, fmt.Errorf("event %d has a digest of algorithm %#x the log does not name", len(events), alg)
			}
			e.digests[alg] = p[2 : 2+s]
			p = p[2+s:]
		}
		if len(p) < 4 || len(p) < 4+int(le.Uint32(p)) {
			return nil, errors.New("the log ends inside an event's data")
		}
		e.data = p[4 : 4+le.Uint32(p)]
		rest = p[4+len(e.data):]
		events = append(events, e)
	}
	return events, nil
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
// type, SHA-256 digest and data, then SPIN-TPM-LOG with the event count and the banks whose every
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
		for pcr, v := range want[alg] {
			got, err := readPCRBank(tpm, alg, pcr)
			if err != nil {
				return "", err
			}
			if !bytes.Equal(got, v) {
				return "", fmt.Errorf("PCR %d of bank %#x holds %x, and the log replays to %x", pcr, alg, got, v)
			}
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
