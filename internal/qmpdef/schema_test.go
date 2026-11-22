// SPDX-License-Identifier: Apache-2.0

package qmpdef_test

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// What spin-machine-dev verify qemu links, and so every definition it holds to the binary it
	// verified: its own query-qmp-schema, qmp's and the machine's.
	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/internal/qmpdef"
)

var record = flag.Bool("record", false, "record testdata/qmp-schema.json from the TCG QEMU in _output (SPIN_MACHINE_OUTPUT) first")

// schemaPath is query-qmp-schema's answer from the pinned QEMU's TCG binary: 11.2.0-rc0, recorded
// on 2026-11-21 with `go test ./internal/qmpdef -run TestQEMUKnowsEveryCommand -record` after
// `spin-machine-dev fetch qemu`. It is recorded again at a bump, and whenever qemu/patches
// changes what QEMU answers, so that CI, which has no QEMU when its tests run, holds the module's
// definitions to the QEMU it ships.
//
// releasedPath holds each supported release's, from the qemu-system-x86_64 in its tarball:
// `spin-machine-dev releases schema TAG` records one, and report.yml the newest release's. How one
// ages out is CheckSupported's to say.
const (
	schemaPath   = "testdata/qmp-schema.json"
	releasedPath = "testdata/released"
)

// Every command and event the module defines is one the pinned QEMU has, as the module defines
// it. `spin-machine-dev verify qemu` asks the binary itself, and so does this test when a QEMU is
// in _output.
func TestQEMUKnowsEveryCommand(t *testing.T) {
	if *record {
		schema, err := build.QMPSchema(bounded(t), tcg(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(schemaPath, append(schema, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := qmpdef.Check(recorded); err != nil {
		t.Errorf("against %s:\n%v", schemaPath, err)
	}

	qemu := tcg(t)
	if _, err := os.Stat(qemu); err != nil {
		t.Skipf("no QEMU at %s to ask as well: %v", qemu, err)
	}
	live, err := build.QMPSchema(bounded(t), qemu)
	if err != nil {
		t.Fatal(err)
	}
	if err := qmpdef.Check(live); err != nil {
		t.Errorf("against %s:\n%v", qemu, err)
	}
}

// Every command, argument and capability the module sends unconditionally is one every supported
// release's QEMU has, and what it sends only after asking is still missing from one of them: the
// library drives each QEMU a machine a release started runs, which compat proves on KVM by
// resuming each release's checkpoint.
func TestEverySupportedQEMUTakesWhatTheModuleSends(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(releasedPath, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	released := map[string]json.RawMessage{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		released[strings.TrimSuffix(filepath.Base(p), ".json")] = b
	}
	if err := qmpdef.CheckSupported(released); err != nil {
		t.Errorf("against %s:\n%v", releasedPath, err)
	}
}

func tcg(t *testing.T) string {
	t.Helper()
	out, err := filepath.Abs(cmp.Or(os.Getenv("SPIN_MACHINE_OUTPUT"), "../../_output"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(out, "bin", "qemu-system-x86_64-tcg")
}

func bounded(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}
