// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// save writes beside the state the machine it was taken on and what was added to it, and restore
// reads it: into that machine the state loads, into another it is refused before a byte of it is
// read, each difference on a line of its own, and a state with nothing beside it is no checkpoint.
// The machine's own record is asked of it, or with --target taken from the file as given, and the
// machine is not asked for it.
func TestSaveAndRestoreKeepTheMachineBesideTheState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "vm.state")
	// The migration's end, as QEMU sends it before its answer to migrate or migrate-incoming.
	completed := `{"event": "MIGRATION", "data": {"status": "completed"}, "timestamp": {"seconds": 1, "microseconds": 2}}` + "\n" + ok

	saving := newMonitor(t, slices.Concat([]string{ok, `{"return": []}`, `{"return": []}`, ok, ok, `{"return": {"fdset-id": 1}}`, completed},
		readRecord, []string{ok, ok})...)
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
	want := slices.Concat([]string{"qmp_capabilities", "query-hotpluggable-cpus", "qom-list", "migrate-set-capabilities", "migrate-set-parameters", "add-fd", "migrate"},
		readRecordAsked, []string{"remove-fd", "quit"})
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

	// The machine held for the checkpoint, the QEMU it greets as, and the record restore is given
	// of it, if any.
	restoringOn := func(hello, target string, replies ...string) (*monitor, error) {
		m := greetedBy(t, hello, append([]string{ok}, replies...)...)
		return m, restore(restoreFlags{qmp: m.socket, from: state, target: target, disks: diskFlags{path: "-"}, timeout: time.Minute})
	}
	restoring := func(target string, replies ...string) (*monitor, error) {
		return restoringOn(greeting, target, replies...)
	}
	askedOf := func(m *monitor) (out []string) {
		for _, r := range m.commands(t)[1:] {
			out = append(out, r.Execute)
		}
		return out
	}
	load := []string{`{"return": {"status": "inmigrate"}}`, `{"return": []}`, ok}
	if machine.DefaultChannels > 1 { // migrate-set-parameters: a mapped-ram state loads by the default's channels
		load = append(load, ok)
	}
	load = append(load, `{"return": {"fdset-id": 1}}`, completed, ok, `{"return": {"status": "running"}}`)
	m, err := restoring("", slices.Concat(readRecord, load)...)
	if err != nil {
		t.Errorf("restore into the machine it was taken on: %v", err)
	}
	if got := askedOf(m); !slices.Equal(got[:len(readRecordAsked)], readRecordAsked) || !slices.Contains(got, "migrate-incoming") {
		t.Errorf("restore into the machine it was taken on asked %v, want what it is and then the load", got)
	}
	// An older QEMU, whose conversation ends at what it is: a restore that went on to load the
	// state would keep the monitor past it, which the monitor reports.
	older := strings.Replace(greeting, `"minor": 2`, `"minor": 1`, 1)
	refusal := "restore: the checkpoint's machine is not this one:\n  vmm: checkpoint qemu 11.2.0, here qemu 11.1.0 (as new)"
	m, err = restoringOn(older, "", readRecord...)
	if err == nil || err.Error() != refusal {
		t.Errorf("restore into an older QEMU: %v, want %q", err, refusal)
	}
	m.commands(t)

	// --target: the record target printed, as given, and only the load asked of the machine.
	var printed bytes.Buffer
	reading := newMonitor(t, append([]string{ok}, readRecord...)...)
	if err := targetReport(reading.socket, &printed); err != nil {
		t.Fatal(err)
	}
	reading.commands(t)
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, printed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err = restoring(target, load...)
	if err != nil {
		t.Errorf("restore --target of the machine it was taken on: %v", err)
	}
	if got := askedOf(m); len(got) == 0 || got[0] != "query-status" || !slices.Contains(got, "migrate-incoming") {
		t.Errorf("restore --target asked %v, want the load and nothing of what the machine is", got)
	}
	// --target of an older QEMU: refused before the machine is asked anything.
	from := saved.From
	from.VMM = "qemu 11.1.0"
	if b, err := json.Marshal(from); err != nil || os.WriteFile(target, b, 0o600) != nil {
		t.Fatalf("writing the machine's record: %v", err)
	}
	m, err = restoring(target)
	if err == nil || err.Error() != refusal {
		t.Errorf("restore --target of an older QEMU: %v, want %q", err, refusal)
	}
	if got := askedOf(m); len(got) != 0 {
		t.Errorf("restore --target of an older QEMU asked %v, want nothing", got)
	}
	// A record that is not one is refused before the machine is dialled.
	if err := os.WriteFile(target, []byte(`{"machine": {"exact": [{"name": "a"}, {"name": "a"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restore(restoreFlags{qmp: "/nowhere", from: state, target: target}); err == nil || !strings.Contains(err.Error(), "reading "+target) {
		t.Errorf("restore --target of a record that is not one: %v", err)
	}
	if err := restore(restoreFlags{qmp: "/nowhere", from: state, target: "/nonexistent/target.json"}); err == nil || !strings.Contains(err.Error(), "reading the machine's record") {
		t.Errorf("restore --target of no file: %v", err)
	}

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
