// SPDX-License-Identifier: Apache-2.0

// tpmcheck is what boot:tpm (boot/tpm_test.go) runs inside a guest to use its TPM as shirudo does:
// through /dev/tpmrm0, an RSA-3072 primary in the owner hierarchy, a PCR extended and an NV index
// written. It prints one line, SPIN-TPM followed by what a restored or rebooted guest has to read
// back the same: the primary's modulus (made again from the seed in the TPM's state), PCR 16 and
// the index. The image has no tpm2-tools, and a static Go binary needs nothing from it.
//
//	tpmcheck write   extend PCR 16, write random bytes to the index (defined first if missing), print
//	tpmcheck read    print
//
// Diagnostic input, never part of a release.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
)

// index is the NV index written, in the owner range; pcr is the debug PCR, which nothing else in
// the guest extends.
const (
	index tpm2.TPMHandle = 0x01500719
	pcr                  = 16
	size                 = 32
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "write" && os.Args[1] != "read") {
		fmt.Fprintln(os.Stderr, "usage: tpmcheck write|read")
		os.Exit(2)
	}
	if err := run(os.Args[1] == "write"); err != nil {
		fmt.Printf("SPIN-TPM-FAILED %v\n", err)
		os.Exit(1)
	}
}

func run(write bool) error {
	tpm, err := linuxtpm.Open("/dev/tpmrm0")
	if err != nil {
		return fmt.Errorf("opening /dev/tpmrm0: %w", err)
	}
	defer func() { _ = tpm.Close() }()
	modulus, err := primary(tpm)
	if err != nil {
		return err
	}
	if write {
		if err := extend(tpm); err != nil {
			return err
		}
		if err := writeIndex(tpm); err != nil {
			return err
		}
	}
	value, err := readPCR(tpm)
	if err != nil {
		return err
	}
	nv, err := readIndex(tpm)
	if err != nil {
		return err
	}
	fmt.Printf("SPIN-TPM rsa3072=%x pcr16=%x nv=%s\n", modulus[:16], value, nv)
	return nil
}

// primary makes the TCG's SRK template at 3072 bits under the owner hierarchy, as shirudo's init
// makes its primaries, and returns its modulus: the same seed makes the same key.
func primary(tpm transport.TPM) ([]byte, error) {
	template := tpm2.RSASRKTemplate
	parms, err := template.Parameters.RSADetail()
	if err != nil {
		return nil, err
	}
	parms.KeyBits = 3072
	template.Parameters = tpm2.NewTPMUPublicParms(tpm2.TPMAlgRSA, parms)
	rsp, err := tpm2.CreatePrimary{PrimaryHandle: tpm2.TPMRHOwner, InPublic: tpm2.New2B(template)}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("making an RSA-3072 primary: %w", err)
	}
	defer func() { _, _ = tpm2.FlushContext{FlushHandle: rsp.ObjectHandle}.Execute(tpm) }()
	pub, err := rsp.OutPublic.Contents()
	if err != nil {
		return nil, err
	}
	n, err := pub.Unique.RSA()
	if err != nil {
		return nil, err
	}
	if len(n.Buffer) != 3072/8 {
		return nil, fmt.Errorf("the primary's modulus is %d bytes, not 384", len(n.Buffer))
	}
	return n.Buffer, nil
}

func extend(tpm transport.TPM) error {
	d := sha256.Sum256([]byte("spin-stack/machine#719"))
	_, err := tpm2.PCRExtend{
		PCRHandle: tpm2.AuthHandle{Handle: tpm2.TPMHandle(pcr), Auth: tpm2.PasswordAuth(nil)},
		Digests:   tpm2.TPMLDigestValues{Digests: []tpm2.TPMTHA{{HashAlg: tpm2.TPMAlgSHA256, Digest: d[:]}}},
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("extending PCR %d: %w", pcr, err)
	}
	return nil
}

func readPCR(tpm transport.TPM) ([]byte, error) {
	rsp, err := tpm2.PCRRead{PCRSelectionIn: tpm2.TPMLPCRSelection{PCRSelections: []tpm2.TPMSPCRSelection{
		{Hash: tpm2.TPMAlgSHA256, PCRSelect: tpm2.PCClientCompatible.PCRs(pcr)},
	}}}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("reading PCR %d: %w", pcr, err)
	}
	if len(rsp.PCRValues.Digests) != 1 {
		return nil, fmt.Errorf("reading PCR %d: %d digests", pcr, len(rsp.PCRValues.Digests))
	}
	return rsp.PCRValues.Digests[0].Buffer, nil
}

// named is the index as a handle with its name, which a command on it needs, or ok false when it is
// not defined.
func named(tpm transport.TPM) (tpm2.NamedHandle, bool, error) {
	rsp, err := tpm2.NVReadPublic{NVIndex: index}.Execute(tpm)
	if errors.Is(err, tpm2.TPMRCHandle) {
		return tpm2.NamedHandle{}, false, nil
	}
	if err != nil {
		return tpm2.NamedHandle{}, false, fmt.Errorf("reading NV index %#x: %w", index, err)
	}
	return tpm2.NamedHandle{Handle: index, Name: rsp.NVName}, true, nil
}

func writeIndex(tpm transport.TPM) error {
	h, ok, err := named(tpm)
	if err != nil {
		return err
	}
	if !ok {
		_, err := tpm2.NVDefineSpace{
			AuthHandle: tpm2.TPMRHOwner,
			PublicInfo: tpm2.New2B(tpm2.TPMSNVPublic{
				NVIndex: index, NameAlg: tpm2.TPMAlgSHA256, DataSize: size,
				Attributes: tpm2.TPMANV{OwnerWrite: true, OwnerRead: true, NT: tpm2.TPMNTOrdinary, NoDA: true},
			}),
		}.Execute(tpm)
		if err != nil {
			return fmt.Errorf("defining NV index %#x: %w", index, err)
		}
		if h, _, err = named(tpm); err != nil {
			return err
		}
	}
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return err
	}
	_, err = tpm2.NVWrite{AuthHandle: tpm2.TPMRHOwner, NVIndex: h, Data: tpm2.TPM2BMaxNVBuffer{Buffer: data}}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("writing NV index %#x: %w", index, err)
	}
	return nil
}

func readIndex(tpm transport.TPM) (string, error) {
	h, ok, err := named(tpm)
	if err != nil || !ok {
		return "none", err
	}
	rsp, err := tpm2.NVRead{AuthHandle: tpm2.TPMRHOwner, NVIndex: h, Size: size}.Execute(tpm)
	if err != nil {
		return "", fmt.Errorf("reading NV index %#x: %w", index, err)
	}
	return fmt.Sprintf("%x", rsp.Data.Buffer), nil
}
