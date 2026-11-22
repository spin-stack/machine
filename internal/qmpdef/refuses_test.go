// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"encoding/json"
	"testing"
)

// The pinned QEMU refuses what it would refuse, in its own words, and takes the rest.
func TestRefusesSaysWhatQEMUWouldAnswer(t *testing.T) {
	s, err := ParseSchema(recorded(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command, args, want string
	}{
		{"query-nothing", `{}`, "The command query-nothing has not been found"},
		{"SHUTDOWN", `{}`, "The command SHUTDOWN has not been found"},
		{"query-status", ``, ""},
		{"query-status", `null`, ""},
		{"query-status", `{"verbose": true}`, "Parameter 'verbose' is unexpected"},
		{"query-blockstats", `{"x-flat": true}`, ""},
		{"query-blockstats", `{"x-flat": "yes"}`, "Invalid parameter type for 'x-flat', expected: boolean"},
		{"device_del", `{}`, "Parameter 'id' is missing"},
		{"device_del", `{"id": 3}`, "Invalid parameter type for 'id', expected: string"},
		{"migrate-set-capabilities", `{"capabilities": [{"capability": "x-defer-disk-flush", "state": true}]}`, ""},
		{"migrate-set-capabilities", `{"capabilities": [{"capability": "x-nothing", "state": true}]}`, "Parameter 'capability' does not accept value 'x-nothing'"},
		{"migrate-set-capabilities", `{"capabilities": [{"capability": 1, "state": true}]}`, "Invalid parameter type for 'capabilities[0].capability', expected: string"},
		{"migrate-set-capabilities", `{"capabilities": [{"capability": "events", "state": true}, {"capability": "events"}]}`, "Parameter 'capabilities[1].state' is missing"},
		{"migrate-set-capabilities", `{"capabilities": "events"}`, "Invalid parameter type for 'capabilities', expected: array"},
		{"migrate-set-capabilities", `{"capabilities": ["events"]}`, "Invalid parameter type for 'capabilities[0]', expected: object"},
		{"migrate-set-parameters", `{"max-bandwidth": 1}`, ""},
		{"migrate-set-parameters", `{"max-bandwidth": 1.5}`, "Invalid parameter type for 'max-bandwidth', expected: int"},
		{"migrate-set-parameters", `{"max-bandwidth": "1"}`, "Invalid parameter type for 'max-bandwidth', expected: int"},
		{"migrate-set-parameters", `{"tls-creds": null}`, ""},
		{"migrate-set-parameters", `{"tls-creds": 3}`, "Invalid parameter type for 'tls-creds', expected: one of its alternatives"},
		{"qom-set", `{"path": "/machine", "property": "x", "value": [1, "two"]}`, ""},
		// blockdev-add's union: the base's members, and those of the variant its driver names.
		{"blockdev-add", `{"driver": "file", "node-name": "n", "filename": "/f"}`, ""},
		{"blockdev-add", `{"driver": "file", "node-name": "n"}`, "Parameter 'filename' is missing"},
		{"blockdev-add", `{"driver": "file", "filename": "/f", "backing": "b"}`, "Parameter 'backing' is unexpected"},
		{"blockdev-add", `{"driver": "nowhere"}`, "Parameter 'driver' does not accept value 'nowhere'"},
		{"blockdev-add", `{"driver": "qcow2", "file": "n"}`, ""},
		{"blockdev-add", `{"driver": "qcow2", "file": {"driver": "file", "filename": "/f"}}`, ""},
		{"blockdev-add", `{"driver": "qcow2", "file": {"driver": "file"}}`, "Invalid parameter type for 'file', expected: one of its alternatives"},
	} {
		err := s.Refuses(tc.command, json.RawMessage(tc.args))
		if got := errorText(err); got != tc.want {
			t.Errorf("Refuses(%s, %s) = %q, want %q", tc.command, tc.args, got, tc.want)
		}
	}
}

// What is not JSON, as arguments or as a schema, is said so, and so is a schema that names a type
// it does not describe or one of a kind QEMU's schema has none of.
func TestRefusesSaysWhatItCannotRead(t *testing.T) {
	if _, err := ParseSchema(json.RawMessage(`{"QMP": {}}`)); err == nil {
		t.Error("ParseSchema read a greeting as a schema")
	}
	s, err := ParseSchema(json.RawMessage(`[{"name": "a", "meta-type": "command", "arg-type": "9"},
		{"name": "b", "meta-type": "command", "arg-type": "w"}, {"name": "w", "meta-type": "weird"},
		{"name": "c", "meta-type": "command", "arg-type": "j"}, {"name": "j", "meta-type": "builtin", "json-type": "date"}]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command, args, want string
	}{
		{"a", `{}`, `query-qmp-schema names a type "9" it does not describe`},
		{"b", `{}`, "null: QEMU's is a weird, which Refuses does not know"},
		{"c", `{}`, "Invalid parameter type for 'null', expected: date"},
		{"a", `{`, "reading a's arguments: unexpected EOF"},
	} {
		if got := errorText(s.Refuses(tc.command, json.RawMessage(tc.args))); got != tc.want {
			t.Errorf("Refuses(%s, %s) = %q, want %q", tc.command, tc.args, got, tc.want)
		}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
