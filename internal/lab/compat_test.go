// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// compatDir is a release or a machine whose QEMU says it is version, with a bzImage.
func compatDir(t *testing.T, dir, version string) {
	t.Helper()
	write(t, filepath.Join(dir, "kernel", "bzImage"), []byte("bzImage"))
	write(t, filepath.Join(dir, "bin", "qemu-system-x86_64"), []byte("#!/bin/sh\necho 'QEMU emulator version "+version+" (v"+version+")'\n"))
}

// Compat resumes every release until this machine refuses one, says which did not go on running
// and why the refused one was, and holds CanResume to QEMU's one-way promise on the way.
func TestCompatResumesEveryReleaseUntilOneIsRefused(t *testing.T) {
	here := t.TempDir()
	compatDir(t, here, "11.1.90")
	cache := t.TempDir()
	rel := func(tag string) string {
		return filepath.Join(cache, "releases", tag, "spin-machine-"+tag+"-linux-x86_64", "usr", "share", "spin-stack")
	}
	compatDir(t, rel("v3"), "11.1.3")
	compatDir(t, rel("v2"), "11.1.3")
	compatDir(t, rel("v1"), "11.0.0")
	compatDir(t, rel("v0"), "11.0.0")
	var resumed []string
	resume := func(previous string) (string, error) {
		resumed = append(resumed, previous)
		switch previous {
		case rel("v2"):
			return "", errors.New("never went on counting")
		case rel("v1"):
			// A refusal comes with the test's own exit: it skipped, and said why.
			return "ram pc.bios: checkpoint 262144, here 65536 (same)", errors.New("exit status 0")
		}
		return "", nil
	}
	rows, err := Compat(here, []string{"v3", "v2", "v1", "v0"}, Sources{Cache: cache}, resume, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []CompatRow{
		{Release: "v3", Resumed: true},
		{Release: "v2", Why: "never went on counting"},
		{Release: "v1", Moved: true, Why: "ram pc.bios: checkpoint 262144, here 65536 (same)"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows\n%+v\nwant\n%+v", rows, want)
	}
	if !reflect.DeepEqual(resumed, []string{rel("v3"), rel("v2"), rel("v1")}) {
		t.Errorf("resumed %v: not past the refused release", resumed)
	}
	table := CompatTable(rows)
	for _, s := range []string{"| v3 | resumed |", "| v2 | **failed**: never went on counting |",
		"| v1 | not meant to: its machine is not this one (ram pc.bios: checkpoint 262144, here 65536 (same)); older releases neither |"} {
		if !strings.Contains(table, s) {
			t.Errorf("no %q in:\n%s", s, table)
		}
	}

	// A release whose QEMU is newer than here's: its checkpoints cannot resume here, which CanResume
	// says before a boot does.
	compatDir(t, rel("v4"), "12.0.0")
	if _, err := Compat(here, []string{"v4"}, Sources{Cache: cache}, resume, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "v4: a checkpoint of qemu 12.0.0 cannot resume on this machine's qemu 11.1.90") {
		t.Errorf("a release with a newer QEMU: %v", err)
	}
	if _, err := Compat(t.TempDir(), nil, Sources{Cache: cache}, resume, io.Discard); err == nil || !strings.Contains(err.Error(), "asking") {
		t.Errorf("a machine with no QEMU: %v", err)
	}
}

// A release from before v20261119.02 carries no bzImage, and no spin-machine since boots it:
// Compat stops there with a row that says so, rather than failing on what it cannot open.
func TestCompatStopsAtTheFirstReleaseWithoutABzImage(t *testing.T) {
	here := t.TempDir()
	compatDir(t, here, "11.1.90")
	cache := t.TempDir()
	rel := func(tag string) string {
		return filepath.Join(cache, "releases", tag, "spin-machine-"+tag+"-linux-x86_64", "usr", "share", "spin-stack")
	}
	compatDir(t, rel("v2"), "11.1.3")
	compatDir(t, rel("v1"), "11.1.3")
	if err := os.Rename(filepath.Join(rel("v1"), "kernel", "bzImage"), filepath.Join(rel("v1"), "kernel", "vmlinux")); err != nil {
		t.Fatal(err)
	}
	var resumed []string
	resume := func(previous string) (string, error) {
		resumed = append(resumed, previous)
		return "", nil
	}
	rows, err := Compat(here, []string{"v2", "v1", "v0"}, Sources{Cache: cache}, resume, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []CompatRow{{Release: "v2", Resumed: true}, {Release: "v1", NoBzImage: true}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows\n%+v\nwant\n%+v", rows, want)
	}
	if !reflect.DeepEqual(resumed, []string{rel("v2")}) {
		t.Errorf("resumed %v: not past the release without a bzImage", resumed)
	}
	if table := CompatTable(rows); !strings.Contains(table, "| v1 | not checked: it carries no bzImage") {
		t.Errorf("no row for v1 in:\n%s", table)
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
	compatDir(t, dir, "11.1.90")
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
}
