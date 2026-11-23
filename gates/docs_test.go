// SPDX-License-Identifier: Apache-2.0

package gates_test

import (
	"path/filepath"
	"testing"

	"github.com/spin-stack/machine/internal/docsrec"
)

// Every log entry and experiment record parses, with its required fields, a decision and section
// that exist, valid dates, and the issues, pull requests and runs its text cites (AGENTS.md,
// "Experiments"). The pages they make are rendered, never committed (#709), so this is where a
// broken file is found, not on the site after a merge.
func TestEveryLogEntryAndExperimentRecordParses(t *testing.T) {
	l, err := docsrec.ReadLog(filepath.Join(root, docsrec.LogDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) < 200 {
		t.Fatalf("the log read %d entries: the check reads almost nothing", len(l.Entries))
	}
	records, err := docsrec.ReadRecords(filepath.Join(root, docsrec.ExperimentsDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 150 {
		t.Fatalf("the experiments read %d records: the check reads almost nothing", len(records))
	}
}
