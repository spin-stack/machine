// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// monitorScript is a QEMU's QMP as QMPSchema meets it, at descriptor 3: the greeting, the answer to
// qmp_capabilities, and schema as the answer to query-qmp-schema.
func monitorScript(schema string) string {
	return `printf '{"QMP": {"version": {"qemu": {"micro": 90, "minor": 1, "major": 11}, "package": ""}, "capabilities": []}}\n' >&3
while read -r line <&3; do
	id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
	case "$line" in
	*qmp_capabilities*) printf '{"return": {}, "id": %s}\n' "$id" >&3 ;;
	*query-qmp-schema*) printf '{"return": %s, "id": %s}\n' '` + schema + `' "$id" >&3 ;;
	esac
done`
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// QMPSchema is what the QEMU answers query-qmp-schema with.
func TestQMPSchemaIsQEMUsAnswer(t *testing.T) {
	qemu := filepath.Join(t.TempDir(), "qemu")
	const schema = `[{"name": "int", "meta-type": "builtin", "json-type": "int"}]`
	script(t, qemu, monitorScript(schema))
	got, err := QMPSchema(ctx(t), qemu)
	if err != nil {
		t.Fatal(err)
	}
	var have, want any
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(schema), &want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("QMPSchema = %s, want %s", got, schema)
	}
}

// A QEMU that will not start says why, in the error.
func TestQMPSchemaSaysWhatQEMUSaid(t *testing.T) {
	qemu := filepath.Join(t.TempDir(), "qemu")
	script(t, qemu, `echo "qemu: -object monitor-qmp: invalid object type" >&2; exit 1`)
	if _, err := QMPSchema(ctx(t), qemu); err == nil || !strings.Contains(err.Error(), "invalid object type") {
		t.Errorf("QMPSchema of a QEMU that would not start = %v, want what it said", err)
	}
	if _, err := QMPSchema(ctx(t), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("QMPSchema started a QEMU that is not there")
	}
}

// A QEMU tree that passes VerifyQEMU and whose binary lacks a command the module defines fails
// the qemu part's check, naming the command.
func TestAQEMUThatLacksACommandFailsItsCheck(t *testing.T) {
	out := t.TempDir()
	qemuTree(t, out)
	tcg := filepath.Join(out, "bin", "qemu-system-x86_64-tcg")
	if err := os.Rename(tcg, tcg+".answers"); err != nil {
		t.Fatal(err)
	}
	script(t, tcg, `case "$1 $2" in
"-machine none") `+monitorScript(`[]`)+` ;;
*) exec "$0.answers" "$@" ;;
esac`)
	err := checkQEMU(Options{Out: out})
	if err == nil || !strings.Contains(err.Error(), "qmp_capabilities: QEMU has no command") {
		t.Errorf("the check of a QEMU with no commands = %v, want qmp_capabilities named", err)
	}
}
