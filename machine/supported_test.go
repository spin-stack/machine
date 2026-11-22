// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/machine/internal/qmpdef"
)

// supportedQEMUs is each supported release's QEMU, by its tag, and the pinned one: the QEMUs a
// machine this library drives may run, as query-qmp-schema describes them
// (qmpdef.CheckSupported).
func supportedQEMUs(t *testing.T) map[string]*qmpdef.Schema {
	t.Helper()
	paths, err := filepath.Glob("../internal/qmpdef/testdata/released/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no released QEMU's schema in internal/qmpdef/testdata/released: %v", err)
	}
	qemus := map[string]*qmpdef.Schema{}
	for _, p := range append(paths, "../internal/qmpdef/testdata/qmp-schema.json") {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s, err := qmpdef.ParseSchema(b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		if name == "qmp-schema" {
			name = "pinned"
		}
		qemus[name] = s
	}
	return qemus
}

// as answers as the QEMU s describes would: what s refuses is refused in QEMU's words, and
// query-migrate-capabilities lists each capability the machine knows that s takes. Anything else is
// answer's.
func as(s *qmpdef.Schema, answer func(c call) string) func(c call) string {
	return func(c call) string {
		args, _ := json.Marshal(c.Args)
		if err := s.Refuses(c.Command, args); err != nil {
			desc, _ := json.Marshal(err.Error())
			return `{"error": {"class": "GenericError", "desc": ` + string(desc) + `}}`
		}
		if c.Command == "query-migrate-capabilities" {
			var offered []capabilityState
			for _, c := range []migrationCapability{mappedRAM, pauseBeforeSwitchover, postcopyRAM, multifd, events, deferDiskFlush} {
				if takes(s, "migrate-set-capabilities", capabilities{Capabilities: []capabilityState{{Capability: c}}}) {
					offered = append(offered, capabilityState{Capability: c})
				}
			}
			b, _ := json.Marshal(offered)
			return ret(string(b))
		}
		return answer(c)
	}
}

// takes is whether the QEMU s describes would run command with args.
func takes(s *qmpdef.Schema, command string, args any) bool {
	b, _ := json.Marshal(args)
	return s.Refuses(command, b) == nil
}

// The machine checkpoints and reads its disks' counters on every QEMU a supported release ships,
// and asks each for what it sends only where that QEMU has it: what it sends unconditionally, each
// QEMU takes, and what one lacks, the machine asked about before sending. A machine an earlier
// release started runs that release's QEMU beside an upgraded library, which is what #435 broke.
func TestTheMachineDrivesEverySupportedQEMU(t *testing.T) {
	qemus := supportedQEMUs(t)
	for _, release := range slices.Sorted(maps.Keys(qemus)) {
		s := qemus[release]
		t.Run(release, func(t *testing.T) {
			disks := blockstatsAnswers("")
			f := &fakeQEMU{answer: as(s, migrations(func(c call) string {
				if r, ok := recordAnswers(c); ok {
					return r
				}
				switch c.Command {
				case "query-hotpluggable-cpus", "qom-list":
					return ret(`[]`)
				case "add-fd":
					return ret(`{"fdset-id": 3}`)
				case "query-blockstats":
					return disks(c)
				}
				return ok
			}, "completed"))}
			m := f.attach(t)
			if _, _, err := m.Checkpoint(ctx(t), image(t, "raw").File, SaveOptions{}, func(context.Context) error { return nil }); err != nil {
				t.Fatalf("checkpointing a machine on %s's QEMU: %v", release, err)
			}
			deferred := false
			for _, c := range f.sent("migrate-set-capabilities")[0]["capabilities"].([]any) {
				if c := c.(map[string]any); c["capability"] == string(deferDiskFlush) {
					deferred = c["state"].(bool)
				}
			}
			if offered := takes(s, "migrate-set-capabilities", capabilities{Capabilities: []capabilityState{{Capability: deferDiskFlush}}}); deferred != offered {
				t.Errorf("%s's QEMU has x-defer-disk-flush: %t, and the checkpoint asked for it: %t", release, offered, deferred)
			}
			for range 2 {
				if got, err := m.DiskUsage(ctx(t)); err != nil || len(got) != len(wantDisks) {
					t.Fatalf("reading disk counters on %s's QEMU: %+v, %v", release, got, err)
				}
			}
			sent := f.sent("query-blockstats")
			_, flat := sent[len(sent)-1]["x-flat"]
			if offered := takes(s, "query-blockstats", blockstatsArgs{Flat: true}); flat != offered {
				t.Errorf("%s's QEMU has x-flat: %t, and the counters were last asked with it: %t", release, offered, flat)
			}
		})
	}
}
