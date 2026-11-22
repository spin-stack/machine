// SPDX-License-Identifier: Apache-2.0

package qmpdef

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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

// Probed says values are Enum values the module sends only after asking QEMU whether it has them,
// as Checkpoint asks query-migrate-capabilities before it sets x-defer-disk-flush: a released QEMU
// without one is still a QEMU the module drives, and CheckSupported passes it. Check holds them to
// the pinned QEMU as it holds any Enum value, so a pinned QEMU that lost the patch adding one fails.
//
// A struct member is said to be probed by its tag, qmpdef:"probed", with the same meaning; Check
// refuses one that is sent every time (not omitempty or omitzero), which would be no ask at all.
func Probed[T ~string](values ...T) {
	Enum(values...)
	t := reflect.TypeFor[T]()
	if probed[t] == nil {
		probed[t] = map[string]bool{}
	}
	for _, v := range values {
		probed[t][string(v)] = true
	}
}

// probed is every Probed value by its Go type. Like defs, only package initialisation writes it.
var probed = map[reflect.Type]map[string]bool{}

// Union says variants are every type a T holds wherever a definition sends one: a sealed interface
// standing for one of QEMU's unions, such as blockdev-add's BlockdevOptions. Check holds each
// variant to the union's base and to the variant its tag names, and a variant may encode itself
// to write its tag: Check reads the tag from the variant's zero value, as encoding/json writes it,
// and the rest from its fields. Call it from an init beside the variants.
func Union[T any](variants ...T) {
	t := reflect.TypeFor[T]()
	for _, v := range variants {
		unions[t] = append(unions[t], reflect.TypeOf(v))
		isVariant[reflect.TypeOf(v)] = true
	}
}

// unions is every Union's variants by the interface's Go type, and isVariant every variant. Like
// defs, only package initialisation writes them.
var (
	unions    = map[reflect.Type][]reflect.Type{}
	isVariant = map[reflect.Type]bool{}
)

// Check holds every definition the binary links to a QEMU's answer to query-qmp-schema, and names
// each disagreement. Each command and event must exist, neither deprecated nor unstable unless
// defined Unstable; each member a definition sends or reads must be one QEMU has, of a kind its
// Go type can carry; each member QEMU requires must be sent every time; each Enum value must be in
// the enum it reaches; and each name must be defined once.
//
// A Go type that encodes or decodes itself (json.Marshaler, json.Unmarshaler), an interface and a
// map are taken as they are: what they put on the wire is not in their type. An interface Union
// lists the variants of is held to QEMU's union through each variant.
func Check(schema json.RawMessage) error {
	s, err := check(schema, false)
	if err != nil {
		return err
	}
	return errors.Join(s.errs...)
}

// CheckSupported holds every definition the binary links to each supported release's QEMU, by the
// release's name, and names each disagreement with the release it is with: that every command,
// argument and capability the module sends unconditionally exists in every QEMU a machine it drives
// may run, which compat proves by resuming each release's checkpoint on KVM, here without a VM.
//
// It is Check but for two things. A Probed value, or a member tagged qmpdef:"probed", that a
// release's QEMU lacks is no disagreement: the module asks before it sends one. And a definition
// said Unstable for what only the pinned QEMU marks unstable is not refused where nothing is. A
// probed value or member every release has is refused: the ask and its fallback are dead, and go.
//
// The releases are internal/qmpdef/testdata/released, one file each that spin-machine-dev releases
// schema records. A release's file goes when compat stops proving its checkpoints resume: when its
// table first says a release is not meant to resume (the machine moved), or a release falls out of
// its window, that release's file and every older one's are deleted in the same change. A probed
// value or member left with no release lacking it then fails here, which is when its ask goes.
func CheckSupported(released map[string]json.RawMessage) error {
	if len(released) == 0 {
		return errors.New("no supported release's query-qmp-schema to hold the module's definitions to")
	}
	var errs []error
	// offered is the releases whose QEMU has each probed value or member.
	offered := map[string][]string{}
	for _, tag := range slices.Sorted(maps.Keys(released)) {
		s, err := check(released[tag], true)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", tag, err))
			continue
		}
		for _, e := range s.errs {
			errs = append(errs, fmt.Errorf("%s: %w", tag, e))
		}
		for probe, has := range s.probes {
			if has {
				offered[probe] = append(offered[probe], tag)
			}
		}
	}
	for _, probe := range slices.Sorted(maps.Keys(offered)) {
		if len(offered[probe]) == len(released) {
			errs = append(errs, fmt.Errorf("%s is probed, and every supported release's QEMU has it: the ask and its fallback can go", probe))
		}
	}
	return errors.Join(errs...)
}

// check walks every definition against schema. released is whether schema is a released QEMU's,
// held as CheckSupported holds one.
func check(schema json.RawMessage, released bool) (*schemaOf, error) {
	parsed, err := ParseSchema(schema)
	if err != nil {
		return nil, err
	}
	s := &schemaOf{types: parsed.types, reached: map[reflect.Type]bool{}, released: released, probes: map[string]bool{}}
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
	for t := range unions {
		if !s.reached[t] {
			s.fail("Union %s is reached by no definition, so nothing holds its variants to QEMU's", t)
		}
	}
	slices.SortFunc(s.errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return s, nil
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
	Members []member `json:"members"`
	// a union: an object whose member Tag names which of Variants it is
	Tag      string    `json:"tag"`
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
	Case string `json:"case"`
	Type string `json:"type"`
}

// schemaOf is one Check under way: the schema by type name, what it has found, and which Enums
// and Unions a definition reached.
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
	// released is whether the schema is a released QEMU's (CheckSupported), and probes is each
	// probed value or member a definition reached, by name, and whether the schema has it.
	released bool
	probes   map[string]bool
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
	if d.unstable && !s.used && !s.released {
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
	if variants, ok := unions[t]; ok {
		s.reached[t] = true
		for _, v := range variants {
			s.fit(path+" "+v.String(), v, name, send)
		}
		return
	}
	coder := unmarshaler
	if send {
		coder = marshaler
	}
	codes := t.Implements(coder) || reflect.PointerTo(t).Implements(coder)
	if t.Kind() == reflect.Interface || t.Kind() == reflect.Map || codes && !isVariant[t] {
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
		if probed[t][v] {
			s.probe(fmt.Sprintf("%s %q", t, v), ok)
		}
		if !ok {
			if !probed[t][v] || !s.released {
				s.fail("%s: QEMU's enum has no %q, which Enum %s says it has", path, v, t)
			}
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
	required := e.Members
	sent := map[string]bool{}
	// A union's members are its base's and its variant's. A type whose zero value names its
	// variant by the tag is held to that variant alone, and sends the tag every time; any other
	// may be any variant, so it may send what any of them has, and must send what the base
	// requires.
	which := tagOf(t, e.Tag)
	found := which == ""
	for _, v := range e.Variants {
		if which != "" && v.Case != which {
			continue
		}
		found = true
		for _, m := range s.types[v.Type].Members {
			if _, ok := members[m.Name]; !ok {
				members[m.Name] = m
			}
		}
		if which != "" {
			required = append(slices.Clone(required), s.types[v.Type].Members...)
			sent[e.Tag] = true
		}
	}
	if !found {
		s.fail("%s: QEMU's union has no %s %q", path, e.Tag, which)
		return
	}
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
		if f.probed {
			s.probe(path+"."+f.name, ok)
			if send && !f.omitted {
				s.fail("%s.%s is probed, and a %s sends it every time: a probed member is omitempty or omitzero", path, f.name, t)
			}
		}
		if !ok {
			if !f.probed || !s.released {
				s.fail("%s: QEMU has no member %q", path, f.name)
			}
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
	for _, m := range required {
		if m.Default == nil && !sent[m.Name] {
			s.fail("%s: QEMU requires %q, which a %s does not send every time", path, m.Name, t)
		}
	}
}

// probe notes that a definition reached the probed value or member name, and whether the schema
// has it.
func (s *schemaOf) probe(name string, has bool) {
	s.probes[name] = s.probes[name] || has
}

// tagOf is the variant of a union a t's zero value names by the member tag, as encoding/json
// writes it, or "" when it names none: a variant that encodes itself to write its tag says which
// it is that way. A zero value encoding/json cannot write, or that is not an object, names none.
func tagOf(t reflect.Type, tag string) string {
	b, _ := json.Marshal(reflect.Zero(t).Interface())
	var zero map[string]any
	_ = json.Unmarshal(b, &zero)
	which, _ := zero[tag].(string)
	return which
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
	probed  bool // tagged qmpdef:"probed": sent only after asking QEMU
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
		// Lookup, not Get: the pins gate reads a Get of a quoted name as a versions.yaml entry.
		probe, _ := f.Tag.Lookup("qmpdef")
		out = append(out, field{name: name, typ: f.Type,
			omitted: slices.Contains(options, "omitempty") || slices.Contains(options, "omitzero"),
			probed:  probe == "probed"})
	}
	return out
}
