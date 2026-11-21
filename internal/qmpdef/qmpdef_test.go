// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"reflect"
	"testing"
)

// isolated gives the test a registry of its own: what it defines is gone after it, and what the
// binary links is out of its way.
func isolated(t *testing.T) {
	t.Helper()
	savedDefs, savedEnums := defs, enums
	defs, enums = map[string][]def{}, map[reflect.Type][]string{}
	t.Cleanup(func() { defs, enums = savedDefs, savedEnums })
}

// A definition carries its name to the wire, and only a defined event is an event: a command's
// name, or a name QEMU made up, is not.
func TestOnlyADefinedEventIsAnEvent(t *testing.T) {
	isolated(t)
	stop := Define[struct{}, struct{}]("x-stop")
	shutdown := DefineEvent[struct{ Guest bool }]("X_SHUTDOWN")
	if stop.Name() != "x-stop" || shutdown.Name() != "X_SHUTDOWN" {
		t.Errorf("names = %q, %q; want what they were defined with", stop.Name(), shutdown.Name())
	}
	for name, want := range map[string]bool{"X_SHUTDOWN": true, "x-stop": false, "X_MADE_UP": false} {
		if got := IsEvent(name); got != want {
			t.Errorf("IsEvent(%q) = %v, want %v", name, got, want)
		}
	}
}
