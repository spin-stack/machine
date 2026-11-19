// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// compatDir is a release or a machine whose spin-machine says its fingerprint is fp and whose
// QEMU says it is version.
func compatDir(t *testing.T, dir, fp, version string) {
	t.Helper()
	write(t, filepath.Join(dir, "fp"), []byte(fp))
	write(t, filepath.Join(dir, "bin", "qemu-system-x86_64"), []byte("#!/bin/sh\necho 'QEMU emulator version "+version+" (v"+version+")'\n"))
}

// Compat resumes every release while the fingerprint is here's, says which did not, stops at the
// first whose contract moved, and holds CanResume to QEMU's one-way promise on the way.
func TestCompatResumesEveryReleaseSinceTheContractMoved(t *testing.T) {
	here := t.TempDir()
	compatDir(t, here, "same", "11.1.90")
	// here's spin-machine fingerprints any release by the fp file in it.
	write(t, filepath.Join(here, "bin", "spin-machine"), []byte("#!/bin/sh\nprintf '{\"Fingerprint\": \"%s\"}' \"$(cat \"$3/fp\")\"\n"))
	cache := t.TempDir()
	rel := func(tag string) string {
		return filepath.Join(cache, "releases", tag, "spin-machine-"+tag+"-linux-x86_64", "usr", "share", "spin-stack")
	}
	compatDir(t, rel("v3"), "same", "11.1.3")
	compatDir(t, rel("v2"), "same", "11.1.3")
	compatDir(t, rel("v1"), "other-fingerprint", "11.0.0")
	var resumed []string
	resume := func(previous string) error {
		resumed = append(resumed, previous)
		if previous == rel("v2") {
			return errors.New("never went on counting")
		}
		return nil
	}
	rows, err := Compat(here, []string{"v3", "v2", "v1", "v0"}, Sources{Cache: cache}, resume, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []CompatRow{
		{Release: "v3", Resumed: true},
		{Release: "v2", Why: "never went on counting"},
		{Release: "v1", Moved: true, Why: "fingerprint other-finger, here same"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows\n%+v\nwant\n%+v", rows, want)
	}
	if !reflect.DeepEqual(resumed, []string{rel("v3"), rel("v2")}) {
		t.Errorf("resumed %v: not past the contract's move", resumed)
	}
	table := CompatTable(rows)
	for _, s := range []string{"| v3 | resumed |", "| v2 | **failed**: never went on counting |", "| v1 | not meant to: the contract moved"} {
		if !strings.Contains(table, s) {
			t.Errorf("no %q in:\n%s", s, table)
		}
	}

	// A release whose QEMU is newer than here's: its checkpoints cannot resume here, which CanResume
	// says before a boot does.
	compatDir(t, rel("v4"), "same", "12.0.0")
	if _, err := Compat(here, []string{"v4"}, Sources{Cache: cache}, resume, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "v4: a checkpoint of qemu 12.0.0 cannot resume on this machine's qemu 11.1.90") {
		t.Errorf("a release with a newer QEMU: %v", err)
	}
	if _, err := Compat(t.TempDir(), nil, Sources{Cache: cache}, resume, io.Discard); err == nil || !strings.Contains(err.Error(), "fingerprinting") {
		t.Errorf("a machine with no spin-machine: %v", err)
	}
}

func TestCanResumeKeepsQEMUsOneWayPromise(t *testing.T) {
	for _, tc := range []struct {
		here, release, want string
	}{
		{"qemu 11.1.90", "qemu 11.1.3", ""},
		{"qemu 11.1.3", "qemu 11.1.3", ""},
		{"qemu 11.1.3", "qemu 11.1.90", "a checkpoint of qemu 11.1.90 cannot resume on this machine's qemu 11.1.3"},
	} {
		err := oneWay(tc.here, tc.release)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("oneWay(%s, %s) = %v, want %q", tc.here, tc.release, err, tc.want)
		}
	}
}

func TestQEMUsVersionIsWhatACheckpointRecords(t *testing.T) {
	dir := t.TempDir()
	compatDir(t, dir, "x", "11.1.90")
	if v, err := qemuVersion(filepath.Join(dir, "bin", "qemu-system-x86_64")); err != nil || v != "qemu 11.1.90" {
		t.Errorf("qemuVersion = %q, %v", v, err)
	}
	write(t, filepath.Join(dir, "bin", "liar"), []byte("#!/bin/sh\necho 'not QEMU'\n"))
	if _, err := qemuVersion(filepath.Join(dir, "bin", "liar")); err == nil || !strings.Contains(err.Error(), "not its version") {
		t.Errorf("a binary that is not QEMU: %v", err)
	}
	if _, err := qemuVersion(filepath.Join(dir, "none")); err == nil {
		t.Error("a QEMU that is not there")
	}
	write(t, filepath.Join(dir, "bin", "fp-empty"), []byte("#!/bin/sh\necho '{}'\n"))
	if _, err := fingerprint(filepath.Join(dir, "bin", "fp-empty"), dir); err == nil || !strings.Contains(err.Error(), "no fingerprint") {
		t.Errorf("an empty fingerprint: %v", err)
	}
}
