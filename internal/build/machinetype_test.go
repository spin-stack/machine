// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/spin-machine/machine"
)

// QEMU 11.1.2's own `-machine help`.
func help1112(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "help-11.1.2.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The pinned type stands when QEMU offers it and has not deprecated it, with a notice when a
// newer one is offered; it does not when QEMU deprecates it or no longer offers it.
func TestThePinnedTypeAgainstAQEMU(t *testing.T) {
	real := help1112(t)
	newer := "pc-q35-11.2          Standard PC (Q35 + ICH9, 2009)\n" + real
	for _, tc := range []struct {
		name, help, pinned, notice, err string
	}{
		{name: "this tree's QEMU and pin", help: real, pinned: machine.MachineType},
		{name: "a QEMU with a newer type", help: newer, pinned: "pc-q35-11.1", notice: "QEMU offers pc-q35-11.2; this machine stays on pc-q35-11.1"},
		{name: "a newer major", help: "pc-q35-12.0   Standard PC\n" + real, pinned: "pc-q35-11.1", notice: "QEMU offers pc-q35-12.0"},
		{name: "an older pin, still offered", help: real, pinned: "pc-q35-10.2", notice: "QEMU offers pc-q35-11.1; this machine stays on pc-q35-10.2"},
		{name: "a deprecated pin", help: real, pinned: "pc-q35-8.1", err: "deprecates pc-q35-8.1"},
		{name: "a pin QEMU removed", help: real, pinned: "pc-q35-2.4", err: "offers no pc-q35-2.4"},
		{name: "a pin that is no versioned q35", help: real, pinned: "q35", err: "is not a pc-q35-X.Y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notice, err := checkMachineType(tc.help, tc.pinned)
			switch {
			case tc.err != "":
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("checkMachineType: %v, want %q", err, tc.err)
				}
			case err != nil:
				t.Fatalf("checkMachineType: %v", err)
			case tc.notice == "" && notice != "":
				t.Errorf("a notice %q with nothing newer", notice)
			case !strings.Contains(notice, tc.notice):
				t.Errorf("notice %q, want %q", notice, tc.notice)
			}
		})
	}
}

// QEMU's source as an 11.2 tarball writes it: the arrays for the pinned 11.1, each whole, and
// nothing from the array beside it.
const source = `
GlobalProperty hw_compat_11_1[] = {
    { "virtio-net-pci", "x-new-feature", "off" },
};
const size_t hw_compat_11_1_len = G_N_ELEMENTS(hw_compat_11_1);

GlobalProperty hw_compat_11_0[] = {
    { "older", "x", "off" },
};
`

const pcSource = `
GlobalProperty pc_compat_11_1[] = {
    { TYPE_X86_CPU, "x-something", "false" },
};
`

func TestMachineTypeCompatIsThePinnedTypesArrays(t *testing.T) {
	got, err := machineTypeCompat(source+pcSource, "pc-q35-11.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GlobalProperty hw_compat_11_1[] = {", `"x-new-feature", "off"`, "GlobalProperty pc_compat_11_1[] = {", `"x-something", "false"`} {
		if !strings.Contains(got, want) {
			t.Errorf("compat\n%s\nwant %q", got, want)
		}
	}
	if strings.Contains(got, "older") {
		t.Errorf("compat\n%s\nholds the array of another type", got)
	}
	// QEMU writes an array with nothing to switch back off as one line (pc_compat_11_0 in 11.1.2).
	empty, err := machineTypeCompat(source+"\nGlobalProperty pc_compat_11_1[] = {};\n", "pc-q35-11.1")
	if err != nil || !strings.HasSuffix(empty, "GlobalProperty pc_compat_11_1[] = {};") {
		t.Errorf("an empty array: %q, %v", empty, err)
	}
	if _, err := machineTypeCompat(source, "pc-q35-11.1"); !errors.Is(err, errNoCompatArray) {
		t.Errorf("no pc_compat_11_1: %v", err)
	}
	if _, err := machineTypeCompat("GlobalProperty hw_compat_11_1[] = { { ", "pc-q35-11.1"); err == nil || errors.Is(err, errNoCompatArray) {
		t.Errorf("an array with no end: %v", err)
	}
	if _, err := machineTypeCompat(source, "q35"); err == nil {
		t.Error("an unversioned pin")
	}
}

// An array is its declaration to its closing brace and semicolon, and not a byte past them.
func TestAnArrayIsItsDeclarationWhole(t *testing.T) {
	got, err := array("before GlobalProperty a_1[] = { { \"x\", \"y\", \"z\" },\n};\nafter", "a_1")
	if err != nil || got != "GlobalProperty a_1[] = { { \"x\", \"y\", \"z\" },\n};" {
		t.Fatalf("array: %q, %v", got, err)
	}
}

// machineHelp asks a real binary: one that is not there is an error, not an empty answer.
func TestMachineHelpAsksTheBinary(t *testing.T) {
	if _, err := machineHelp(filepath.Join(t.TempDir(), "no-qemu")); err == nil {
		t.Error("no binary answered")
	}
}

// qemuSource is a tarball of the pinned QEMU's two machine-type files, less one whose body is "",
// and pins with its sha256 as QEMU's pin.
func qemuSource(t *testing.T, machineC, pcC string) (string, *versions.Versions) {
	t.Helper()
	v := pins(t)
	q, _ := v.Get("qemu")
	src := t.TempDir()
	for name, body := range map[string]string{"hw/core/machine.c": machineC, "hw/i386/pc.c": pcC} {
		if body == "" {
			continue
		}
		path := filepath.Join(src, "qemu-"+q.Version, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tarball := filepath.Join(t.TempDir(), "q.tar.xz")
	if out, err := exec.Command("tar", "-cJf", tarball, "-C", src, "qemu-"+q.Version).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	sum, err := FileSHA256(tarball)
	if err != nil {
		t.Fatal(err)
	}
	w := *v
	w.Entries = slices.Clone(v.Entries)
	for i := range w.Entries {
		if w.Entries[i].Name == "qemu" {
			w.Entries[i].Pin = sum
		}
	}
	return tarball, &w
}

// fakeCurl puts a curl on the PATH whose body is script, $4 the file it writes to.
func fakeCurl(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A QEMU bump says what QEMU's newer types change against the pinned one, from the source the pin
// names, and only once that source is the pin's.
func TestAQEMUBumpSaysWhatNewerTypesChange(t *testing.T) {
	tarball, v := qemuSource(t, source, pcSource)
	fakeCurl(t, `cp `+tarball+` "$4"`)
	got, err := QEMUCompat(context.Background(), v)
	if err != nil || !strings.Contains(got, `"x-new-feature", "off"`) || !strings.Contains(got, `"x-something", "false"`) {
		t.Errorf("QEMUCompat = %q, %v", got, err)
	}
	if _, err := QEMUCompat(context.Background(), pins(t)); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("a source not the pin's: %v", err)
	}
	tarball, v = qemuSource(t, "GlobalProperty hw_compat_99_0[] = {};\n", "GlobalProperty pc_compat_99_0[] = {};\n")
	fakeCurl(t, `cp `+tarball+` "$4"`)
	if got, err := QEMUCompat(context.Background(), v); err != nil || got != machine.MachineType+" is the newest type in this QEMU: nothing newer to differ from it" {
		t.Errorf("a QEMU with nothing newer: %q, %v", got, err)
	}
	tarball, v = qemuSource(t, source, "")
	fakeCurl(t, `cp `+tarball+` "$4"`)
	if _, err := QEMUCompat(context.Background(), v); err == nil || !strings.Contains(err.Error(), "unpacking") {
		t.Errorf("a source without hw/i386/pc.c: %v", err)
	}
	fakeCurl(t, "exit 22")
	if _, err := QEMUCompat(context.Background(), v); err == nil || !strings.Contains(err.Error(), "fetching") {
		t.Errorf("a source that could not be fetched: %v", err)
	}
	fakeCurl(t, `echo nothing > "$4"`)
	if _, err := QEMUCompat(context.Background(), v); err == nil {
		t.Error("a source that is not QEMU's: compared")
	}
	if _, err := QEMUCompat(context.Background(), pinsWithout(t, "qemu")); err == nil {
		t.Error("no QEMU pinned: compared")
	}
}
