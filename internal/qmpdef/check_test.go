// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"encoding/json"
	"os"
	"slices"
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

// node is a sealed union of block nodes as package qmp writes one: each variant encodes itself, to
// write its driver, and blockdev-reopen sends a list of them.
type (
	node   interface{ isNode() }
	reopen struct {
		Options []node `json:"options"`
	}
	fileNode struct {
		Filename string `json:"filename"`
	}
	nullNode struct {
		Size int `json:"size,omitempty"`
	}
	noFile struct {
		Name string `json:"node-name"`
	}
	sizedFile struct {
		Filename string `json:"filename"`
		Size     int    `json:"size"`
	}
	nowhere struct{}
)

func (fileNode) isNode()  {}
func (nullNode) isNode()  {}
func (noFile) isNode()    {}
func (sizedFile) isNode() {}
func (nowhere) isNode()   {}

// Check reads a variant's fields by reflection, and only its driver from what it encodes.
func (fileNode) MarshalJSON() ([]byte, error)  { return []byte(`{"driver": "file"}`), nil }
func (nullNode) MarshalJSON() ([]byte, error)  { return []byte(`{"driver": "null-co"}`), nil }
func (noFile) MarshalJSON() ([]byte, error)    { return []byte(`{"driver": "file"}`), nil }
func (sizedFile) MarshalJSON() ([]byte, error) { return []byte(`{"driver": "file"}`), nil }
func (nowhere) MarshalJSON() ([]byte, error)   { return []byte(`{"driver": "nowhere"}`), nil }

// A Union's variants are each held to QEMU's union through the variant their driver names: its
// base and that variant's members, which they may send, and what both require, which they must.
func TestCheckHoldsEachVariantToItsOwn(t *testing.T) {
	isolated(t)
	Define[reopen, struct{}]("blockdev-reopen")
	Union[node](fileNode{}, nullNode{})
	if err := Check(recorded(t)); err != nil {
		t.Errorf("Check of variants QEMU agrees with:\n%v", err)
	}
	for _, tc := range []struct {
		name    string
		variant node
		want    string
	}{
		{"a member only another variant has", sizedFile{}, `blockdev-reopen arguments.options[] qmpdef.sizedFile: QEMU has no member "size"`},
		{"a member its own variant requires", noFile{}, `blockdev-reopen arguments.options[] qmpdef.noFile: QEMU requires "filename"`},
		{"a variant QEMU's union does not have", nowhere{}, `blockdev-reopen arguments.options[] qmpdef.nowhere: QEMU's union has no driver "nowhere"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolated(t)
			Define[reopen, struct{}]("blockdev-reopen")
			Union(tc.variant)
			err := Check(recorded(t))
			if err == nil || strings.Count(err.Error(), "\n") > 0 || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Check said:\n%v\nwant one line saying %q", err, tc.want)
			}
		})
	}
}

// A Union no definition reaches holds nothing to QEMU, which is said rather than passed over.
func TestAUnionNothingReachesIsNamed(t *testing.T) {
	isolated(t)
	Union[node](fileNode{})
	if err := Check(recorded(t)); err == nil || !strings.Contains(err.Error(), "Union qmpdef.node is reached by no definition") {
		t.Errorf("Check = %v, want the Union named", err)
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

// without is schema with no member and no enum value named any of names: a QEMU without the
// patches that add them, as a release before them ships.
func without(t *testing.T, schema json.RawMessage, names ...string) json.RawMessage {
	t.Helper()
	var entries []map[string]any
	if err := json.Unmarshal(schema, &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		members, ok := e["members"].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, m := range members {
			// An alternate's members are named by type alone.
			if name, _ := m.(map[string]any)["name"].(string); !slices.Contains(names, name) {
				kept = append(kept, m)
			}
		}
		e["members"] = kept
	}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type (
	capabilityName string
	setCapability  struct {
		Capabilities []struct {
			Capability capabilityName `json:"capability"`
			State      bool           `json:"state"`
		} `json:"capabilities"`
	}
	flatStats struct {
		Flat bool `json:"x-flat,omitempty" qmpdef:"probed"`
	}
)

// defineProbes defines what the machine asks QEMU for before it sends: x-defer-disk-flush, a
// probed capability, and x-flat, a probed member. Both are unstable in the pinned QEMU, and in a
// release without them nothing the definitions send is.
func defineProbes() {
	Define[setCapability, struct{}]("migrate-set-capabilities", Unstable)
	Define[flatStats, []struct{}]("query-blockstats", Unstable)
	Enum[capabilityName]("events")
	Probed[capabilityName]("x-defer-disk-flush")
}

// A release whose QEMU lacks what the module asks for before it sends is one the module drives,
// though Check, which holds the pinned QEMU, refuses it.
func TestCheckSupportedPassesAReleaseWithoutWhatIsAskedFirst(t *testing.T) {
	isolated(t)
	defineProbes()
	old := without(t, recorded(t), "x-flat", "x-defer-disk-flush")
	if err := CheckSupported(map[string]json.RawMessage{"v1": old, "v2": recorded(t)}); err != nil {
		t.Errorf("CheckSupported of a release without the probed:\n%v", err)
	}
	err := Check(old)
	for _, want := range []string{
		`migrate-set-capabilities arguments.capabilities[].capability: QEMU's enum has no "x-defer-disk-flush"`,
		`query-blockstats arguments: QEMU has no member "x-flat"`,
		"migrate-set-capabilities is defined Unstable, and QEMU marks nothing it sends or reads unstable",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check of the pinned QEMU without the probed = %v, want %q", err, want)
		}
	}
}

// What the module sends every time is held to every release, and each disagreement names the
// release it is with.
func TestCheckSupportedNamesTheReleaseOfEachDisagreement(t *testing.T) {
	type flat struct {
		Flat bool `json:"x-flat,omitempty"`
	}
	isolated(t)
	Define[flat, []struct{}]("query-blockstats", Unstable)
	Define[setCapability, struct{}]("migrate-set-capabilities", Unstable)
	Enum[capabilityName]("x-defer-disk-flush")
	err := CheckSupported(map[string]json.RawMessage{
		"v1": without(t, recorded(t), "x-flat", "x-defer-disk-flush"),
		"v2": without(t, recorded(t), "x-flat"),
		"v3": recorded(t),
		"v4": json.RawMessage(`{"QMP": {}}`),
	})
	want := strings.Join([]string{
		`v1: migrate-set-capabilities arguments.capabilities[].capability: QEMU's enum has no "x-defer-disk-flush", which Enum qmpdef.capabilityName says it has`,
		`v1: query-blockstats arguments: QEMU has no member "x-flat"`,
		`v2: query-blockstats arguments: QEMU has no member "x-flat"`,
		`v4: reading query-qmp-schema's answer: json: cannot unmarshal object into Go value of type []qmpdef.entry`,
	}, "\n")
	if err == nil || err.Error() != want {
		t.Errorf("CheckSupported said:\n%v\nwant:\n%s", err, want)
	}
}

// A probed value or member every supported release has needs no ask: the window aged out of the
// last release without it, and the ask and its fallback go.
func TestCheckSupportedRefusesAnAskEveryReleaseAnswers(t *testing.T) {
	isolated(t)
	defineProbes()
	err := CheckSupported(map[string]json.RawMessage{"v1": recorded(t), "v2": recorded(t)})
	want := strings.Join([]string{
		`qmpdef.capabilityName "x-defer-disk-flush" is probed, and every supported release's QEMU has it: the ask and its fallback can go`,
		`query-blockstats arguments.x-flat is probed, and every supported release's QEMU has it: the ask and its fallback can go`,
	}, "\n")
	if err == nil || err.Error() != want {
		t.Errorf("CheckSupported said:\n%v\nwant:\n%s", err, want)
	}
}

// No release to hold the definitions to is not a pass.
func TestCheckSupportedNeedsARelease(t *testing.T) {
	if err := CheckSupported(nil); err == nil {
		t.Error("CheckSupported of no release passed")
	}
}

// A probed member sent every time is sent without asking, and Check says so.
func TestAProbedMemberIsNotSentEveryTime(t *testing.T) {
	type alwaysFlat struct {
		Flat bool `json:"x-flat" qmpdef:"probed"`
	}
	isolated(t)
	Define[alwaysFlat, []struct{}]("query-blockstats", Unstable)
	want := `query-blockstats arguments.x-flat is probed, and a qmpdef.alwaysFlat sends it every time: a probed member is omitempty or omitzero`
	if err := Check(recorded(t)); err == nil || err.Error() != want {
		t.Errorf("Check = %v, want %q", err, want)
	}
}
