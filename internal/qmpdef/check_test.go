// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// recorded is the schema TestQEMUKnowsEveryCommand records from the pinned QEMU: every case here
// is held to what a real QEMU says, numbered type names and all.
func recorded(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/qmp-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Shapes the machine sends and reads, written as it would write them.
type (
	runState string
	status   struct {
		Running bool     `json:"running"`
		Status  runState `json:"status"`
	}
	addFD struct {
		FDSet  *int   `json:"fdset-id,omitempty"`
		Opaque string `json:"opaque,omitempty"`
	}
	fdInfo struct {
		FDSet int `json:"fdset-id"`
		FD    int `json:"fd"`
	}
	deviceDel struct {
		ID string `json:"id"`
	}
	capability struct {
		Capability string `json:"capability"`
		State      bool   `json:"state"`
	}
	capabilities struct {
		Capabilities []capability `json:"capabilities"`
	}
	qomSet struct {
		Path     string `json:"path"`
		Property string `json:"property"`
		Value    int    `json:"value"`
	}
	ramblock struct {
		Text string `json:"human-readable-text"`
	}
	shutdown struct {
		Guest  bool   `json:"guest"`
		Reason string `json:"reason"`
	}
	// migration's field is untagged: encoding/json reads "ram" into Ram all the same.
	migration struct {
		Ram struct {
			MBps float64 `json:"mbps"`
		}
	}
	image struct {
		Filename string `json:"filename"`
		Backing  *image `json:"backing-image"`
	}
	blockNode struct {
		Name  string `json:"node-name"`
		Image image  `json:"image"`
	}
	named struct {
		Flat bool `json:"flat"`
	}
	// driveNode is blockdev-add's union: its base's members, and its variants', one of them in a
	// struct of its own embedded. Its child is an alternate: a node's options, or a node's name.
	driveNode struct {
		Driver string `json:"driver"`
		Name   string `json:"node-name,omitempty"`
		Child  string `json:"file,omitempty"`
		file
	}
	file struct {
		Filename string `json:"filename,omitempty"`
		Ignored  string `json:"-"`
		unread   string
	}
	chardevKind string
	chardevAdd  struct {
		ID      string `json:"id"`
		Backend struct {
			Type chardevKind `json:"type"`
		} `json:"backend"`
	}
)

func TestCheckFindsNothingWrongWithWhatQEMUHas(t *testing.T) {
	isolated(t)
	Define[struct{}, status]("query-status")
	Define[addFD, fdInfo]("add-fd")
	Define[struct{ deviceDel }, struct{}]("device_del") // id, from the embedded struct
	Define[capabilities, struct{}]("migrate-set-capabilities")
	Define[qomSet, struct{}]("qom-set")
	Define[struct{}, migration]("query-migrate")
	Define[named, []blockNode]("query-named-block-nodes")
	Define[driveNode, struct{}]("blockdev-add")
	Define[struct{}, ramblock]("x-query-ramblock", Unstable)
	DefineEvent[shutdown]("SHUTDOWN")
	Enum[runState]("prelaunch", "inmigrate", "running", "paused", "postmigrate")
	if err := Check(recorded(t)); err != nil {
		t.Errorf("Check of definitions QEMU agrees with:\n%v", err)
	}
}

// What a type puts on the wire of its own accord is taken as it is: an interface, a map, and a
// type that encodes or decodes itself.
func TestCheckTakesWhatEncodesItselfAsItIs(t *testing.T) {
	type anyID struct {
		ID any `json:"id"`
	}
	isolated(t)
	Define[anyID, struct{}]("device_del")
	Define[encodesItself, decodesItself]("query-status")
	Define[struct{}, map[string]bool]("query-name")
	if err := Check(recorded(t)); err != nil {
		t.Errorf("Check of types that encode themselves:\n%v", err)
	}
}

type (
	encodesItself struct{ Nonsense int }
	decodesItself struct{ Nonsense int }
)

func (encodesItself) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

func (*decodesItself) UnmarshalJSON([]byte) error { return nil }

// Each definition here is wrong in one way, and Check names it and nothing else: what a
// definition the module carries would be told on a QEMU bump that broke it.
func TestCheckNamesEachDisagreement(t *testing.T) {
	type (
		wrongMember struct {
			ID     string `json:"id"`
			Device string `json:"device"`
		}
		wrongKind struct {
			ID int `json:"id"`
		}
		maybeID struct {
			ID string `json:"id,omitzero"`
		}
		answerMember struct {
			Running bool `json:"running"`
			Since   int  `json:"since"`
		}
		badEvent struct {
			Reason int `json:"reason"`
		}
		notAnArray struct {
			Capabilities string `json:"capabilities"`
		}
		notAnObject struct {
			Capabilities []string `json:"capabilities"`
		}
		textFD struct {
			FDSet string `json:"fdset-id"`
		}
		textMBps struct {
			RAM struct {
				MBps string `json:"mbps"`
			} `json:"ram"`
		}
		noVariant struct {
			Driver   string `json:"driver"`
			Bogus    string `json:"bogus"`
			Filename string `json:"filename"`
		}
		badAlternate struct {
			Driver string `json:"driver"`
			File   bool   `json:"file"`
		}
		unstableMember struct {
			Driver string `json:"driver"`
			Check  bool   `json:"x-check-cache-dropped"`
		}
		textStatus struct {
			Status int `json:"status"`
		}
	)
	for _, tc := range []struct {
		name   string
		define func()
		want   string
	}{
		{"an argument QEMU does not take", func() { Define[wrongMember, struct{}]("device_del") },
			`device_del arguments: QEMU has no member "device"`},
		{"a required argument not sent", func() { Define[struct{}, struct{}]("device_del") },
			`device_del arguments: QEMU requires "id"`},
		{"a required argument sent only sometimes", func() { Define[maybeID, struct{}]("device_del") },
			`device_del arguments: QEMU requires "id"`},
		{"an argument of the wrong kind", func() { Define[wrongKind, struct{}]("device_del") },
			`device_del arguments.id: QEMU's is a JSON string`},
		{"an int read as text", func() { Define[struct{}, textFD]("add-fd") },
			`add-fd answer.fdset-id: QEMU's is a JSON int`},
		{"a number read as text", func() { Define[struct{}, textMBps]("query-migrate") },
			`query-migrate answer.ram.mbps: QEMU's is a JSON number`},
		{"an answer member QEMU does not send", func() { Define[struct{}, answerMember]("query-status") },
			`query-status answer: QEMU has no member "since"`},
		{"an array sent as text", func() { Define[notAnArray, struct{}]("migrate-set-capabilities") },
			`migrate-set-capabilities arguments.capabilities: QEMU's is an array`},
		{"an object sent as text", func() { Define[notAnObject, struct{}]("migrate-set-capabilities") },
			`migrate-set-capabilities arguments.capabilities[]: QEMU's is an object`},
		{"an enum read as a number", func() { Define[struct{}, textStatus]("query-status") },
			`query-status answer.status: QEMU's is an enum`},
		{"a member in no variant of a union", func() { Define[noVariant, struct{}]("blockdev-add") },
			`blockdev-add arguments: QEMU has no member "bogus"`},
		{"none of an alternate's alternatives", func() { Define[badAlternate, struct{}]("blockdev-add") },
			`blockdev-add arguments.file: a Go bool is none of QEMU's alternatives`},
		{"a command QEMU does not have", func() { Define[struct{}, struct{}]("query-nothing") },
			`query-nothing: QEMU has no command`},
		{"an event QEMU does not have", func() { DefineEvent[struct{}]("NOTHING") },
			`NOTHING: QEMU has no event`},
		{"a command defined as an event", func() { DefineEvent[struct{}]("query-status") },
			`query-status: QEMU has no event`},
		{"event data of the wrong kind", func() { DefineEvent[badEvent]("SHUTDOWN") },
			`SHUTDOWN data.reason: QEMU's is an enum`},
		{"a deprecated command", func() { Define[struct{}, struct{}]("query-kvm") },
			`query-kvm is deprecated`},
		{"an unstable command not said so", func() { Define[struct{}, ramblock]("x-query-ramblock") },
			`x-query-ramblock is unstable`},
		{"an unstable member not said so", func() { Define[unstableMember, struct{}]("blockdev-add") },
			`blockdev-add arguments.x-check-cache-dropped is unstable`},
		{"a stable command said unstable", func() { Define[struct{}, status]("query-status", Unstable) },
			`query-status is defined Unstable`},
		{"an enum value QEMU does not have", func() {
			Define[struct{}, status]("query-status")
			Enum[runState]("running", "held")
		}, `query-status answer.status: QEMU's enum has no "held"`},
		{"a deprecated enum value", func() {
			Define[chardevAdd, struct{}]("chardev-add")
			Enum[chardevKind]("socket", "memory")
		}, `chardev-add arguments.backend.type "memory" is deprecated`},
		{"an Enum nothing reaches", func() { Enum[runState]("running") },
			`Enum qmpdef.runState is reached by no definition`},
		{"a name defined twice", func() {
			Define[deviceDel, struct{}]("device_del")
			Define[deviceDel, struct{}]("device_del")
		}, `device_del is defined 2 times`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolated(t)
			tc.define()
			err := Check(recorded(t))
			if err == nil {
				t.Fatalf("Check passed; want %q", tc.want)
			}
			if lines := strings.Split(err.Error(), "\n"); len(lines) != 1 || !strings.Contains(lines[0], tc.want) {
				t.Errorf("Check said:\n%v\nwant one line saying %q", err, tc.want)
			}
		})
	}
}

// An error found before an alternate fits stays found.
func TestAnAlternateKeepsWhatWasFoundBeforeIt(t *testing.T) {
	type node struct {
		Driver string `json:"driver"`
		Bogus  string `json:"bogus"`
		File   string `json:"file"`
	}
	isolated(t)
	Define[node, struct{}]("blockdev-add")
	if err := Check(recorded(t)); err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Errorf("Check = %v, want the member before the alternate named", err)
	}
}

func TestCheckRefusesWhatIsNotASchema(t *testing.T) {
	if err := Check(json.RawMessage(`{"QMP": {}}`)); err == nil {
		t.Error("Check read a greeting as a schema")
	}
}
