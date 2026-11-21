// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/spin-machine/machine"
)

// save writes beside the state the machine it was taken on and what was added to it, and restore
// reads it: into that machine the state loads, into another it is refused before a byte of it is
// read, each difference on a line of its own, and a state with nothing beside it is no checkpoint.
func TestSaveAndRestoreKeepTheMachineBesideTheState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "vm.state")
	completed := `{"return": {"status": "completed"}}`

	saving := newMonitor(t, slices.Concat([]string{ok, `{"return": []}`, `{"return": []}`}, readRecord,
		[]string{ok, ok, `{"return": {"fdset-id": 1}}`, ok, completed, ok, ok})...)
	if err := save(saveFlags{qmp: saving.socket, to: state, channels: 4, timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, r := range saving.commands(t) {
		asked = append(asked, r.Execute)
		if r.Execute == "migrate-set-parameters" && r.Arguments["multifd-channels"] != float64(4) {
			t.Errorf("save --channels 4 set %v", r.Arguments)
		}
	}
	want := slices.Concat([]string{"qmp_capabilities", "query-hotpluggable-cpus", "qom-list"}, readRecordAsked,
		[]string{"migrate-set-capabilities", "migrate-set-parameters", "add-fd", "migrate", "query-migrate", "remove-fd", "quit"})
	if !slices.Equal(asked, want) {
		t.Errorf("save asked %v, want %v", asked, want)
	}
	b, err := os.ReadFile(savedPath(state))
	if err != nil {
		t.Fatal(err)
	}
	var saved machine.Saved
	if err := json.Unmarshal(b, &saved); err != nil || saved.Format != machine.FormatMappedRAM || saved.From.VMM != "qemu 11.2.0" {
		t.Fatalf("save recorded %s (%v), want the mapped-ram format and the machine it was taken on", b, err)
	}

	// The machine held for the checkpoint, and what it says it is.
	restoring := func(replies ...string) (*monitor, error) {
		m := newMonitor(t, slices.Concat([]string{ok, `{"return": {"status": "inmigrate"}}`}, replies)...)
		return m, restore(restoreFlags{qmp: m.socket, from: state, disks: diskFlags{path: "-"}, timeout: time.Minute})
	}
	load := []string{`{"return": []}`, ok}
	if machine.DefaultChannels > 1 { // migrate-set-parameters: a mapped-ram state loads by the default's channels
		load = append(load, ok)
	}
	load = append(load, `{"return": {"fdset-id": 1}}`, ok, completed, ok, `{"return": {"status": "running"}}`)
	m, err := restoring(slices.Concat(readRecord, load)...)
	if err != nil {
		t.Errorf("restore into the machine it was taken on: %v", err)
	}
	if !slices.ContainsFunc(m.commands(t), func(r qmpRequest) bool { return r.Execute == "migrate-incoming" }) {
		t.Error("restore into the machine it was taken on did not load the state")
	}
	// An older QEMU, whose conversation ends at what it is: a restore that went on to load the
	// state would keep the monitor past it, which the monitor reports.
	older := slices.Clone(readRecord)
	older[0] = `{"return": {"qemu": {"major": 11, "minor": 1, "micro": 0}}}`
	m, err = restoring(older...)
	if want := "restore: the checkpoint's machine is not this one:\n  vmm: checkpoint qemu 11.2.0, here qemu 11.1.0 (as new)"; err == nil || err.Error() != want {
		t.Errorf("restore into an older QEMU: %v, want %q", err, want)
	}
	m.commands(t)

	if err := os.Remove(savedPath(state)); err != nil {
		t.Fatal(err)
	}
	if err := restore(restoreFlags{qmp: "/nowhere", from: state}); err == nil || !strings.Contains(err.Error(), "what save wrote beside the state") {
		t.Errorf("restore of a state with nothing beside it: %v", err)
	}
}

// save and restore name no count of channels unless told one, so that the machine's default moves
// the guest's memory - and a stream state, which one thread reads, still loads - and pass on a
// count they are told, one included.
func TestSaveAndRestoreLeaveTheChannelsToTheMachineUnlessGivenThem(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{{nil, 0}, {[]string{"--channels", "1"}, 1}, {[]string{"--channels", "4"}, 4}} {
		var s saveFlags
		var r restoreFlags
		for _, f := range []interface{ register(*flag.FlagSet) }{&s, &r} {
			fs := flag.NewFlagSet("x", flag.ContinueOnError)
			f.register(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
		}
		if s.channels != tc.want || r.channels != tc.want {
			t.Errorf("%v: save --channels %d, restore --channels %d, want %d", tc.args, s.channels, r.channels, tc.want)
		}
	}
}

// The helper boot starts takes the machine's flags and its own monitor in place of the boot's.
func TestTheBootsHelperHasItsOwnMonitor(t *testing.T) {
	got := withoutQMP([]string{"--memory", "1024", "--qmp", "/a", "--cpus", "2", "--qmp=/b", "-qmp", "/c", "-qmp=/d", "--disk", "x"})
	if want := []string{"--memory", "1024", "--cpus", "2", "--disk", "x"}; !slices.Equal(got, want) {
		t.Errorf("withoutQMP = %v, want %v", got, want)
	}
}
