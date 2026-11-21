// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// phases collects what a checkpoint or a resume tells.
type phases struct {
	mu   sync.Mutex
	told []Phase
}

func (p *phases) tell(ph Phase) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.told = append(p.told, ph)
}

// names is each phase's type, a run of RAMWritten as one.
func (p *phases) names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var names []string
	for _, ph := range p.told {
		n := strings.TrimPrefix(fmt.Sprintf("%T", ph), "machine.")
		if n != "RAMWritten" || names[len(names)-1] != n {
			names = append(names, n)
		}
	}
	return names
}

// saving answers a checkpoint: the guest stops once the RAM has been asked after twice, and the
// save completes at migrate-continue. It is called on the fake's one goroutine.
func saving() func(c call) string {
	asked, done := 0, false
	return func(c call) string {
		if r, ok := recordAnswers(c); ok {
			return r
		}
		switch c.Command {
		case "query-hotpluggable-cpus", "qom-list":
			return ret(`[]`)
		case "add-fd":
			return ret(`{"fdset-id": 3}`)
		case "migrate":
			return migrationEvents("setup", "active") + ok
		case "query-migrate":
			if done {
				return ret(`{"status": "completed", "downtime": 12, "total-time": 345, "ram": {"transferred": 104857600}}`)
			}
			asked++
			stopped := ""
			if asked == 2 {
				stopped = migrationEvents("pre-switchover")
			}
			return stopped + ret(`{"status": "active", "ram": {"transferred": 1000, "total": 4000}}`)
		case "migrate-continue":
			done = true
			return migrationEvents("device", "completed") + ok
		}
		return ok
	}
}

// A checkpoint tells its phases in order, what its RAM came to while the guest ran, how long the
// seal took and what it cost, and the call that lets the guest go tells the last: Hold, or
// Continue. A Continue after a Hold tells nothing more.
func TestACheckpointTellsItsPhasesInOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(m *QEMU) []func(context.Context) error
		last  []string
	}{
		{"continued, twice", func(m *QEMU) []func(context.Context) error {
			return []func(context.Context) error{m.Continue, m.Continue}
		}, []string{"GuestRunning"}},
		{"held, then continued", func(m *QEMU) []func(context.Context) error {
			return []func(context.Context) error{m.Hold, m.Continue}
		}, []string{"GuestHeld"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := (&fakeQEMU{answer: saving()}).attach(t)
			p := &phases{}
			_, cost, err := m.Checkpoint(ctx(t), image(t, "raw").File, SaveOptions{Phases: p.tell}, func(context.Context) error {
				time.Sleep(5 * time.Millisecond)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, do := range tc.after(m) {
				if err := do(ctx(t)); err != nil {
					t.Fatal(err)
				}
			}
			want := append([]string{"SaveStarted", "RAMWritten", "GuestStopped", "Sealed", "SaveCompleted"}, tc.last...)
			if got := p.names(); !reflect.DeepEqual(got, want) {
				t.Fatalf("phases %v, want %v", got, want)
			}
			for i, ph := range p.told {
				if i > 0 && ph.When().Before(p.told[i-1].When()) {
					t.Errorf("%T at %v, before the phase told before it", ph, ph.When())
				}
				switch ph := ph.(type) {
				case RAMWritten:
					if ph.Bytes != 1000 || ph.Total != 4000 {
						t.Errorf("%+v, want what query-migrate said of the RAM", ph)
					}
				case Sealed:
					if ph.Took < 5*time.Millisecond {
						t.Errorf("%+v, want what the seal took", ph)
					}
				case SaveCompleted:
					if ph.Cost != cost {
						t.Errorf("%+v, want the Cost returned, %+v", ph, cost)
					}
				}
			}
		})
	}
}

// A resume tells its phases: a full load its state loaded before the guest runs, and a fast one
// the guest running first, and its state loaded from Loaded.
func TestAResumeTellsItsPhases(t *testing.T) {
	from := fakeTarget(t)
	for _, tc := range []struct {
		name     string
		load     LoadOptions
		statuses []string
		want     []string
	}{
		{"in full", LoadOptions{}, []string{"active", "completed"}, []string{"LoadStarted", "StateLoaded", "GuestRunning"}},
		{"fast", LoadOptions{Fast: true}, []string{"active", "postcopy-active"}, []string{"LoadStarted", "GuestRunning", "StateLoaded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{}
			places := machineAnswers("inmigrate", "root")
			f.answer = migrations(func(c call) string {
				if c.Command == "query-status" && strings.Contains(strings.Join(f.commands(), " "), "migrate-incoming") {
					return ret(`{"status": "running"}`)
				}
				return places(c)
			}, tc.statuses...)
			m := f.attach(t)
			p := &phases{}
			tc.load.Phases = p.tell
			if err := m.Resume(ctx(t), image(t, "raw").File, Saved{Format: FormatMappedRAM, From: from}, from, tc.load,
				Disk{Serial: "root", Images: []Image{image(t, "qcow2")}}); err != nil {
				t.Fatal(err)
			}
			if tc.load.Fast {
				f.send(migrationEvents("completed"))
				for range 2 {
					if err := m.Loaded(ctx(t)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if got := p.names(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("phases %v, want %v", got, tc.want)
			}
		})
	}
}
