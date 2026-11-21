// SPDX-License-Identifier: Apache-2.0

// Package qmpdef is every QMP command and event this module sends or waits on, each defined once
// with the Go types of what it carries.
//
// A definition is a value that only this module can make: the package is internal, so a caller
// outside it sends what the machine defines and defines nothing of its own. Package qmp re-exports
// Command and EventType by alias, which lets a caller hold and send one but not make one.
//
// Every definition is registered as it is made, in a package-level var, so the list of what the
// module speaks is whatever a binary links. No list is kept by hand.
package qmpdef

// Command is one QMP command: its name, and the Go types of its arguments and of its answer. The
// zero value names no command, and qmp.Do refuses it.
type Command[A, R any] struct{ name string }

// Name is the command's name on the wire.
func (c Command[A, R]) Name() string { return c.name }

// EventType is one QMP event and the Go type of its data. The zero value names no event.
type EventType[T any] struct{ name string }

// Name is the event's name on the wire.
func (e EventType[T]) Name() string { return e.name }

// Define makes the command name, which takes A and answers R, and registers it. Call it in a
// package-level var, beside the code that sends the command.
func Define[A, R any](name string) Command[A, R] {
	register(def{name: name})
	return Command[A, R]{name}
}

// DefineEvent makes the event name, whose data is a T, and registers it. Call it in a
// package-level var, beside the code that waits on the event.
func DefineEvent[T any](name string) EventType[T] {
	register(def{name: name, event: true})
	return EventType[T]{name}
}

// def is one definition as the registry holds it.
type def struct {
	name  string
	event bool
}

// defs is every definition linked into the binary, by name. A name may hold more than one, which
// is a mistake the schema check reports rather than one a package-level var can refuse.
//
// Only package initialisation writes it, so it needs no lock: every read happens after.
var defs = map[string][]def{}

func register(d def) { defs[d.name] = append(defs[d.name], d) }

// IsEvent reports whether name is an event this module defines: a set a caller can count events
// by, which a QEMU that sends names of its own choosing cannot grow.
func IsEvent(name string) bool {
	for _, d := range defs[name] {
		if d.event {
			return true
		}
	}
	return false
}
