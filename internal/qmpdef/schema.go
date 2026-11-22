// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// Schema is one QEMU's answer to query-qmp-schema, read: what Check holds the definitions to, and
// what Refuses says that QEMU would refuse.
type Schema struct{ types map[string]entry }

// ParseSchema reads query-qmp-schema's answer.
func ParseSchema(schema json.RawMessage) (*Schema, error) {
	var entries []entry
	if err := json.Unmarshal(schema, &entries); err != nil {
		return nil, fmt.Errorf("reading query-qmp-schema's answer: %w", err)
	}
	s := &Schema{types: map[string]entry{}}
	for _, e := range entries {
		s.types[e.Name] = e
	}
	return s, nil
}

// Refuses is what this QEMU would answer command with args with instead of running it, in QEMU's
// words, or nil when it would take them: a command it does not have, an argument it does not
// take, one it requires and was not sent, a value of the wrong JSON type, or an enum value it does
// not have. A fake QEMU answers from it as the QEMU whose schema it is would. What a command does
// with arguments it takes is not in a schema.
func (s *Schema) Refuses(command string, args json.RawMessage) error {
	e, ok := s.types[command]
	if !ok || e.MetaType != "command" {
		return fmt.Errorf("The command %s has not been found", command)
	}
	var v any = map[string]any{}
	if trimmed := bytes.TrimSpace(args); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		d := json.NewDecoder(bytes.NewReader(trimmed))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			return fmt.Errorf("reading %s's arguments: %w", command, err)
		}
	}
	return s.refuses("", "", v, e.ArgType)
}

// refuses holds v, the member full names (capabilities[0].capability) and name the last part of,
// to the schema's type typ. QEMU names a member in full, but for an enum's value, which it names
// by the member alone, and an array's element's, which it names "null".
func (s *Schema) refuses(full, name string, v any, typ string) error {
	e, ok := s.types[typ]
	if !ok {
		return fmt.Errorf("query-qmp-schema names a type %q it does not describe", typ)
	}
	invalid := func(want string) error {
		return fmt.Errorf("Invalid parameter type for '%s', expected: %s", orNull(full), want)
	}
	switch e.MetaType {
	case "builtin":
		if !isJSON(e.JSONType, v) {
			return invalid(e.JSONType)
		}
	case "enum":
		str, ok := v.(string)
		if !ok {
			return invalid("string")
		}
		if !slices.ContainsFunc(e.Members, func(m member) bool { return m.Name == str }) {
			return fmt.Errorf("Parameter '%s' does not accept value '%s'", orNull(name), str)
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return invalid("array")
		}
		for i, x := range list {
			if err := s.refuses(fmt.Sprintf("%s[%d]", full, i), "", x, e.ElementType); err != nil {
				return err
			}
		}
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return invalid("object")
		}
		return s.object(full, obj, e)
	case "alternate":
		for _, m := range e.Members {
			if s.refuses(full, name, v, m.Type) == nil {
				return nil
			}
		}
		return invalid("one of its alternatives")
	default:
		return fmt.Errorf("%s: QEMU's is a %s, which Refuses does not know", orNull(full), e.MetaType)
	}
	return nil
}

// object holds obj to e as QEMU's visitor does: each member in the schema's order, the base's and
// then those of the variant the base's tag names, and then any member it has none of.
func (s *Schema) object(full string, obj map[string]any, e entry) error {
	known := map[string]bool{}
	visit := func(members []member) error {
		for _, m := range members {
			known[m.Name] = true
			x, sent := obj[m.Name]
			if !sent {
				if m.Default == nil {
					return fmt.Errorf("Parameter '%s' is missing", child(full, m.Name))
				}
				continue
			}
			if err := s.refuses(child(full, m.Name), m.Name, x, m.Type); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(e.Members); err != nil {
		return err
	}
	if tag, ok := obj[e.Tag].(string); ok {
		for _, v := range e.Variants {
			if v.Case == tag {
				if err := visit(s.types[v.Type].Members); err != nil {
					return err
				}
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !known[k] {
			return fmt.Errorf("Parameter '%s' is unexpected", child(full, k))
		}
	}
	return nil
}

// isJSON is whether v, decoded with its numbers kept as json.Number, is of the JSON type QEMU
// names.
func isJSON(jsonType string, v any) bool {
	switch jsonType {
	case "value":
		return true
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "int":
		n, ok := v.(json.Number)
		_, err := n.Int64()
		return ok && err == nil
	}
	return false
}

func child(full, name string) string {
	if full == "" {
		return name
	}
	return full + "." + name
}

func orNull(name string) string {
	if name == "" {
		return "null"
	}
	return name
}
