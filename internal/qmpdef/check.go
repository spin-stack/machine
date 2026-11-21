// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Feature is what a definition says of itself beyond its types.
type Feature int

// Unstable is a definition that sends what QEMU marks unstable (x-query-ramblock): an x- command
// or member QEMU may change in any release. Check refuses an unstable command or member a
// definition does not say this of, and a definition that says it of nothing unstable.
const Unstable Feature = 1

// Enum says values are members of the QAPI enum T stands for wherever a definition sends or reads
// a T, so that Check fails when QEMU's enum lacks one. Call it from an init beside the constants.
//
// query-qmp-schema names its types by number, never as the QAPI source does, so it is the Go type
// that ties the constants to the enum: Check finds the enum by walking from the command.
func Enum[T ~string](values ...T) {
	t := reflect.TypeFor[T]()
	for _, v := range values {
		enums[t] = append(enums[t], string(v))
	}
}

// enums is every Enum's values by their Go type. Like defs, only package initialisation writes it.
var enums = map[reflect.Type][]string{}

// Check holds every definition the binary links to a QEMU's answer to query-qmp-schema, and names
// each disagreement. Each command and event must exist, neither deprecated nor unstable unless
// defined Unstable; each member a definition sends or reads must be one QEMU has, of a kind its
// Go type can carry; each member QEMU requires must be sent every time; each Enum value must be in
// the enum it reaches; and each name must be defined once.
//
// A Go type that encodes or decodes itself (json.Marshaler, json.Unmarshaler), an interface and a
// map are taken as they are: what they put on the wire is not in their type.
func Check(schema json.RawMessage) error {
	var entries []entry
	if err := json.Unmarshal(schema, &entries); err != nil {
		return fmt.Errorf("reading query-qmp-schema's answer: %w", err)
	}
	s := schemaOf{types: map[string]entry{}, reached: map[reflect.Type]bool{}}
	for _, e := range entries {
		s.types[e.Name] = e
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if n := len(defs[name]); n > 1 {
			s.fail("%s is defined %d times, and a command or event is defined once", name, n)
		}
		for _, d := range defs[name] {
			s.check(d)
		}
	}
	for t := range enums {
		if !s.reached[t] {
			s.fail("Enum %s is reached by no definition, so nothing holds its values to QEMU's", t)
		}
	}
	slices.SortFunc(s.errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errors.Join(s.errs...)
}

// entry is one SchemaInfo of query-qmp-schema's answer: a command, an event or a type, which the
// other entries name by a number that changes from one QEMU build to the next.
type entry struct {
	Name     string   `json:"name"`
	MetaType string   `json:"meta-type"`
	Features []string `json:"features"`
	// command and event
	ArgType string `json:"arg-type"`
	RetType string `json:"ret-type"`
	// builtin
	JSONType string `json:"json-type"`
	// array
	ElementType string `json:"element-type"`
	// object, enum and alternate
	Members  []member  `json:"members"`
	Variants []variant `json:"variants"`
}

type member struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Default is present, as null, on an optional member.
	Default  json.RawMessage `json:"default"`
	Features []string        `json:"features"`
}

type variant struct {
	Type string `json:"type"`
}

// schemaOf is one Check under way: the schema by type name, what it has found, and which Enums
// a definition reached.
type schemaOf struct {
	types   map[string]entry
	errs    []error
	reached map[reflect.Type]bool
	// seen is every Go type the definition being walked has held to a schema type already, so
	// that a type that holds itself (an image and its backing image) is walked once.
	seen map[visit]bool
	// unstable is whether the definition being walked is Unstable, and used whether it reached
	// anything QEMU marks so.
	unstable, used bool
}

type visit struct {
	t    reflect.Type
	name string
	send bool
}

func (s *schemaOf) fail(format string, args ...any) {
	s.errs = append(s.errs, fmt.Errorf(format, args...))
}

func (s *schemaOf) check(d def) {
	kind := "command"
	if d.event {
		kind = "event"
	}
	e, ok := s.types[d.name]
	if !ok || e.MetaType != kind {
		s.fail("%s: QEMU has no %s of that name", d.name, kind)
		return
	}
	s.unstable, s.used, s.seen = d.unstable, false, map[visit]bool{}
	s.features(d.name, e.Features)
	if d.event {
		s.fit(d.name+" data", d.ret, e.ArgType, false)
	} else {
		s.fit(d.name+" arguments", d.args, e.ArgType, true)
		s.fit(d.name+" answer", d.ret, e.RetType, false)
	}
	if d.unstable && !s.used {
		s.fail("%s is defined Unstable, and QEMU marks nothing it sends or reads unstable", d.name)
	}
}

// features fails what QEMU says will go (deprecated), and what it says may change (unstable) when
// the definition did not say so of itself.
func (s *schemaOf) features(path string, f []string) {
	if slices.Contains(f, "deprecated") {
		s.fail("%s is deprecated in this QEMU", path)
	}
	if slices.Contains(f, "unstable") {
		s.used = true
		if !s.unstable {
			s.fail("%s is unstable in this QEMU, and its definition does not say Unstable", path)
		}
	}
}

var (
	marshaler   = reflect.TypeFor[json.Marshaler]()
	unmarshaler = reflect.TypeFor[json.Unmarshaler]()
)

// fit holds the Go type t to the schema's type named name, at path. send is whether the module
// sends a t (arguments) or reads one (answers and event data).
func (s *schemaOf) fit(path string, t reflect.Type, name string, send bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	coder := unmarshaler
	if send {
		coder = marshaler
	}
	if t.Kind() == reflect.Interface || t.Kind() == reflect.Map || t.Implements(coder) || reflect.PointerTo(t).Implements(coder) {
		return
	}
	v := visit{t, name, send}
	if s.seen[v] {
		return
	}
	s.seen[v] = true
	e, ok := s.types[name]
	if !ok {
		s.fail("%s: query-qmp-schema names a type %q it does not describe", path, name)
		return
	}
	switch e.MetaType {
	case "builtin":
		if !carries(e.JSONType, t.Kind()) {
			s.fail("%s: QEMU's is a JSON %s, which a Go %s is not", path, e.JSONType, t)
		}
	case "enum":
		s.enum(path, t, e)
	case "array":
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			s.fail("%s: QEMU's is an array, which a Go %s is not", path, t)
			return
		}
		s.fit(path+"[]", t.Elem(), e.ElementType, send)
	case "object":
		s.object(path, t, e, send)
	case "alternate":
		s.alternate(path, t, e, send)
	default:
		s.fail("%s: QEMU's is a %s, which this check does not know", path, e.MetaType)
	}
}

// carries is whether a Go value of kind k encodes as, and decodes from, the JSON type QEMU names.
func carries(jsonType string, k reflect.Kind) bool {
	switch jsonType {
	case "value":
		return true
	case "string":
		return k == reflect.String
	case "boolean":
		return k == reflect.Bool
	case "int":
		return reflect.Int <= k && k <= reflect.Uint64
	case "number":
		return reflect.Int <= k && k <= reflect.Float64
	}
	return false
}

func (s *schemaOf) enum(path string, t reflect.Type, e entry) {
	if t.Kind() != reflect.String {
		s.fail("%s: QEMU's is an enum, which a Go %s is not", path, t)
		return
	}
	values, ok := enums[t]
	if !ok {
		return
	}
	s.reached[t] = true
	members := map[string]member{}
	for _, m := range e.Members {
		members[m.Name] = m
	}
	for _, v := range values {
		m, ok := members[v]
		if !ok {
			s.fail("%s: QEMU's enum has no %q, which Enum %s says it has", path, v, t)
			continue
		}
		s.features(fmt.Sprintf("%s %q", path, v), m.Features)
	}
}

func (s *schemaOf) object(path string, t reflect.Type, e entry, send bool) {
	if t.Kind() != reflect.Struct {
		s.fail("%s: QEMU's is an object, which a Go %s is not", path, t)
		return
	}
	members := map[string]member{}
	for _, m := range e.Members {
		members[m.Name] = m
	}
	// A union's members are its base's and each variant's: which apply depends on the value.
	for _, v := range e.Variants {
		for _, m := range s.types[v.Type].Members {
			if _, ok := members[m.Name]; !ok {
				members[m.Name] = m
			}
		}
	}
	sent := map[string]bool{}
	for _, f := range fields(t) {
		m, ok := member{}, false
		if send {
			m, ok = members[f.name]
		} else {
			// encoding/json reads a member into the field whose name it matches regardless of case.
			for name, candidate := range members {
				if strings.EqualFold(name, f.name) {
					m, ok = candidate, true
				}
			}
		}
		if !ok {
			s.fail("%s: QEMU has no member %q", path, f.name)
			continue
		}
		if !f.omitted {
			sent[m.Name] = true
		}
		s.features(path+"."+m.Name, m.Features)
		s.fit(path+"."+m.Name, f.typ, m.Type, send)
	}
	if !send {
		return
	}
	for _, m := range e.Members {
		if m.Default == nil && !sent[m.Name] {
			s.fail("%s: QEMU requires %q, which a %s does not send every time", path, m.Name, t)
		}
	}
}

// alternate fits t to whichever of QEMU's alternatives it fits without a disagreement.
func (s *schemaOf) alternate(path string, t reflect.Type, e entry, send bool) {
	found := s.errs
	for _, m := range e.Members {
		s.errs = nil
		s.fit(path, t, m.Type, send)
		if len(s.errs) == 0 {
			s.errs = found
			return
		}
	}
	s.errs = found
	s.fail("%s: a Go %s is none of QEMU's alternatives", path, t)
}

// field is one member a struct puts on the wire, as encoding/json names it.
type field struct {
	name    string
	typ     reflect.Type
	omitted bool // omitempty or omitzero: not sent every time
}

// fields is what encoding/json makes of a struct's fields: an untagged embedded struct's fields
// are the struct's own, "-" is none, and the tag's name, or else the field's, is the member's.
func fields(t reflect.Type) []field {
	var out []field
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _ := f.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		embedded := f.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		if f.Anonymous && name == "" && embedded.Kind() == reflect.Struct {
			out = append(out, fields(embedded)...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		options := strings.Split(opts, ",")
		out = append(out, field{name: name, typ: f.Type,
			omitted: slices.Contains(options, "omitempty") || slices.Contains(options, "omitzero")})
	}
	return out
}
