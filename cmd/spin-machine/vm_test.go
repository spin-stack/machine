// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/spin-stack/spin-machine/machine"
)

// save writes what restore needs besides the state beside it, and restore reads it: a state with
// none is one written before the format was recorded, in the stream format, and loads as one.
func TestSaveAndRestoreKeepTheFormatBesideTheState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "vm.state")
	completed := `{"return": {"status": "completed"}}`

	saving := newMonitor(t, ok, `{"return": []}`, `{"return": []}`,
		`{"return": {"name": "spin-machine:f00d"}}`, `{"return": []}`, `{"return": {"qemu": {"major": 11, "minor": 2, "micro": 0}}}`,
		`{"return": [{"qom-path": "/machine/unattached/device[0]"}]}`, `{"return": "host-x86_64-cpu"}`,
		`{"return": {"model": {"name": "host", "props": {"vendor": "GenuineIntel", "sse2": true}}}}`,
		ok, ok, `{"return": {"fdset-id": 1}}`, ok, ok, completed, ok)
	if err := save(saveFlags{qmp: saving.socket, to: state, timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, r := range saving.commands(t) {
		asked = append(asked, r.Execute)
	}
	if want := []string{"qmp_capabilities", "query-hotpluggable-cpus", "qom-list",
		"query-name", "query-memdev", "query-version", "query-cpus-fast", "qom-get", "query-cpu-model-expansion", "migrate-set-capabilities",
		"migrate-set-parameters", "add-fd", "migrate", "remove-fd", "query-migrate", "quit"}; !slices.Equal(asked, want) {
		t.Errorf("save asked %v, want %v", asked, want)
	}
	b, err := os.ReadFile(savedPath(state))
	if err != nil {
		t.Fatal(err)
	}
	var saved machine.Saved
	if err := json.Unmarshal(b, &saved); err != nil || saved.Format != machine.FormatMappedRAM {
		t.Fatalf("save recorded %s (%v), want the mapped-ram format", b, err)
	}

	mappedRAM := func(record bool) any {
		if !record {
			if err := os.Remove(savedPath(state)); err != nil {
				t.Fatal(err)
			}
		}
		// The machine says what it resumes (Target) whether or not the state has a record.
		replies := []string{ok, `{"return": {"status": "inmigrate"}}`,
			`{"return": {"name": "spin-machine:f00d"}}`, `{"return": []}`, `{"return": {"qemu": {"major": 11, "minor": 2, "micro": 0}}}`,
			`{"return": [{"qom-path": "/machine/unattached/device[0]"}]}`, `{"return": "host-x86_64-cpu"}`,
			`{"return": {"model": {"name": "host", "props": {"vendor": "GenuineIntel", "sse2": true}}}}`}
		replies = append(replies, `{"return": []}`, ok, `{"return": {"fdset-id": 1}}`, ok, ok, completed, `{"return": {"status": "running"}}`)
		restoring := newMonitor(t, replies...)
		if err := restore(restoreFlags{qmp: restoring.socket, from: state, disks: diskFlags{path: "-"}, timeout: time.Minute}); err != nil {
			t.Fatal(err)
		}
		for _, r := range restoring.commands(t) {
			if r.Execute != "migrate-set-capabilities" {
				continue
			}
			for _, c := range r.Arguments["capabilities"].([]any) {
				if c := c.(map[string]any); c["capability"] == "mapped-ram" {
					return c["state"]
				}
			}
		}
		return "no mapped-ram capability"
	}
	if got := mappedRAM(true); got != true {
		t.Errorf("a recorded mapped-ram state loaded with mapped-ram %v", got)
	}
	if got := mappedRAM(false); got != false {
		t.Errorf("a state with no record loaded with mapped-ram %v, want the stream format", got)
	}
}

// The helper boot starts takes the machine's flags and its own monitor in place of the boot's.
func TestTheBootsHelperHasItsOwnMonitor(t *testing.T) {
	got := withoutQMP([]string{"--memory", "1024", "--qmp", "/a", "--cpus", "2", "--qmp=/b", "-qmp", "/c", "-qmp=/d", "--disk", "x"})
	if want := []string{"--memory", "1024", "--cpus", "2", "--disk", "x"}; !slices.Equal(got, want) {
		t.Errorf("withoutQMP = %v, want %v", got, want)
	}
}
