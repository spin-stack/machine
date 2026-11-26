// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/simulator"
)

var line = regexp.MustCompile(`^SPIN-TPM rsa2048=([0-9a-f]{32}) pcr16=([0-9a-f]{64}) nv=([0-9a-f]{64}|none)$`)

// What tpmcheck says of a TPM, on the reference TPM, whose RSA stops at 2048 bits: before a write no index and PCR 16 at zero;
// a write extends the PCR and fills the index; a read after it says the same primary, PCR and
// index. boot:tpm holds the same of a guest's TPM across a checkpoint.
func TestTpmcheckSaysWhatTheTPMHolds(t *testing.T) {
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatalf("the reference TPM: %v", err)
	}
	t.Cleanup(func() { _ = tpm.Close() })
	said := func(write bool) []string {
		t.Helper()
		got, err := run(tpm, 2048, write)
		if err != nil {
			t.Fatal(err)
		}
		m := line.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("tpmcheck said %q", got)
		}
		return m[1:]
	}
	before := said(false)
	if before[1] != strings.Repeat("0", 64) || before[2] != "none" {
		t.Errorf("a TPM nothing wrote to reads %v", before)
	}
	written := said(true)
	if written[0] != before[0] || written[1] == before[1] || written[2] == "none" {
		t.Errorf("after a write: %v, before it %v", written, before)
	}
	if again := said(false); strings.Join(again, " ") != strings.Join(written, " ") {
		t.Errorf("a read after the write says %v, the write %v", again, written)
	}
	if rewritten := said(true); rewritten[2] == written[2] || rewritten[0] != written[0] {
		t.Errorf("a second write left the index at %v, the first %v", rewritten, written)
	}
}

// A TPM without RSA-3072, as the reference TPM is, fails the guest's primary by name.
func TestAPrimaryTheTPMCannotMakeFails(t *testing.T) {
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatalf("the reference TPM: %v", err)
	}
	t.Cleanup(func() { _ = tpm.Close() })
	if _, err := run(tpm, 3072, false); err == nil || !strings.Contains(err.Error(), "making an RSA-3072 primary") {
		t.Errorf("an RSA-3072 primary on a TPM without one: %v", err)
	}
}

// stopping is a TPM that answers its first n commands and no more, but for TPM2_FlushContext, whose
// failure tpmcheck ignores and which would otherwise leave the reference TPM's three object slots
// full of primaries.
type stopping struct {
	tpm transport.TPM
	n   int
}

func (s *stopping) Send(cmd []byte) ([]byte, error) {
	if len(cmd) >= 10 && binary.BigEndian.Uint32(cmd[6:10]) == uint32(tpm2.TPMCCFlushContext) {
		return s.tpm.Send(cmd)
	}
	if s.n == 0 {
		return nil, errors.New("the TPM stopped answering")
	}
	s.n--
	return s.tpm.Send(cmd)
}

// A TPM that stops answering at any command of a write fails tpmcheck with what it was doing, and
// says nothing of what the TPM holds: boot:tpm reads a failure as one, never as a value.
func TestATPMThatStopsAnsweringFailsTpmcheck(t *testing.T) {
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatalf("the reference TPM: %v", err)
	}
	t.Cleanup(func() { _ = tpm.Close() })
	said := map[string]bool{}
	// A write before the index is defined and one after, and a read: each a different sequence.
	for _, write := range []bool{true, true, false} {
		for n := 0; ; n++ {
			got, err := run(&stopping{tpm: tpm, n: n}, 2048, write)
			if err == nil {
				if n < 3 {
					t.Errorf("tpmcheck answered after %d commands: %q", n, got)
				}
				break
			}
			if got != "" || !strings.Contains(err.Error(), "the TPM stopped answering") {
				t.Fatalf("after %d commands: %q, %v", n, got, err)
			}
			said[strings.SplitN(err.Error(), ":", 2)[0]] = true
		}
	}
	for _, doing := range []string{"making an RSA-2048 primary", "extending PCR 16", "reading NV index 0x1500719", "defining NV index 0x1500719", "writing NV index 0x1500719", "reading PCR 16"} {
		if !said[doing] {
			t.Errorf("no failure said %q, among %v", doing, said)
		}
	}
}

// tpmcheck's exit status is what boot:tpm reads a failure by: 2 for an argument it does not take, 1
// with SPIN-TPM-FAILED and why for a TPM it cannot open or use.
func TestTpmcheckSaysAFailureByItsStatus(t *testing.T) {
	var out, errs strings.Builder
	if got := check([]string{"seal"}, &out, &errs, nil, 2048); got != 2 || !strings.Contains(errs.String(), "usage") {
		t.Errorf("tpmcheck seal = %d, %q", got, errs.String())
	}
	none := func() (transport.TPMCloser, error) { return nil, errors.New("no such device") }
	out.Reset()
	if got := check([]string{"read"}, &out, &errs, none, 2048); got != 1 || out.String() != "SPIN-TPM-FAILED opening /dev/tpmrm0: no such device\n" {
		t.Errorf("tpmcheck read with no TPM = %d, %q", got, out.String())
	}
	// A host has no firmware log of its own to read, so log fails, naming the file.
	out.Reset()
	if got := check([]string{"log"}, &out, &errs, simulator.OpenSimulator, 2048); got != 1 || !strings.Contains(out.String(), "SPIN-TPM-FAILED open "+eventLog) {
		t.Errorf("tpmcheck log with no log = %d, %q", got, out.String())
	}
	out.Reset()
	if got := check([]string{"write"}, &out, &errs, simulator.OpenSimulator, 3072); got != 1 || !strings.HasPrefix(out.String(), "SPIN-TPM-FAILED making an RSA-3072 primary") {
		t.Errorf("tpmcheck write on a TPM without RSA-3072 = %d, %q", got, out.String())
	}
	// A write is a write and a read a read: the index the write filled is the one read back, from
	// the one TPM both open, as a guest's two runs do.
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatalf("the reference TPM: %v", err)
	}
	t.Cleanup(func() { _ = tpm.Close() })
	same := func() (transport.TPMCloser, error) { return kept{tpm}, nil }
	var lines []string
	for _, arg := range []string{"write", "read"} {
		out.Reset()
		if got := check([]string{arg}, &out, &errs, same, 2048); got != 0 || line.FindStringSubmatch(strings.TrimSpace(out.String())) == nil {
			t.Fatalf("tpmcheck %s = %d, %q", arg, got, out.String())
		}
		lines = append(lines, out.String())
	}
	if lines[0] != lines[1] || strings.Contains(lines[0], "nv=none") {
		t.Errorf("tpmcheck write said %q and read %q", lines[0], lines[1])
	}
}

// The primary is the size asked: 1024 bits on the reference TPM is a 128-byte modulus.
func TestThePrimaryIsTheSizeAsked(t *testing.T) {
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatalf("the reference TPM: %v", err)
	}
	t.Cleanup(func() { _ = tpm.Close() })
	n, err := primary(tpm, 1024)
	if err != nil || len(n) != 128 {
		t.Errorf("an RSA-1024 primary: a %d-byte modulus, %v", len(n), err)
	}
}

// kept is a TPM whose Close leaves it open, as a guest's TPM outlives tpmcheck's runs.
type kept struct{ transport.TPM }

func (kept) Close() error { return nil }
