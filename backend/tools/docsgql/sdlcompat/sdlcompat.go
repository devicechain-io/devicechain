// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sdlcompat compares every served GraphQL schema with the same schema at a
// release, classifies each difference as breaking, dangerous or safe, and fails on a
// breaking one that has not been declared.
//
// WHY. The versioning policy promises that a minor release adds to the served schema
// and never removes or renames anything, changes a type, or makes an optional argument
// required. Before this check nothing would notice a break: the console is regenerated
// from the same tree in the same pull request, so every in-repo consumer moves with
// the schema and stays green. The client that breaks is the one outside the repo, and
// it finds out after the release.
//
// THE BASELINE is the served set at a release tag, read from a checked-out copy of that
// release's services tree (sdldesc.LoadSources reads every spelling a release used). The
// caller chooses the tag; hack/check-graphql-compat.sh picks the nearest stable release
// reachable from HEAD.
//
// THE RULES follow the GraphQL spec's notion of what an existing, valid client operation
// can observe:
//
//   - OUTPUT positions (object and interface fields) may NARROW: a nullable field may
//     become non-null, because every client already handles a value. Widening a
//     non-null field to nullable breaks a client that relied on it.
//   - INPUT positions (arguments, input-object fields) may WIDEN: a required argument may
//     become optional. Making an optional one required, or adding a required one,
//     breaks every caller that does not send it.
//   - Anything a client can name — a type, field, argument, input field, enum value,
//     union member, implemented interface, directive — may not disappear.
//   - DANGEROUS changes are legal but can surprise a careless reader: a new enum value
//     (a client must treat an unknown value as "not handled yet", which the policy tells
//     it to), a new union member or interface, a changed default. They are reported and
//     never fail.
//
// DECLARING A BREAK. A break that is intended is declared, one line per change, in
// breaks.txt beside this file. See ParseDeclarations for the format and Evaluate for
// how a declaration expires.
package sdlcompat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-docsgql/sdldesc"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// Severity is how much a change can hurt an existing client.
type Severity string

const (
	// Breaking changes can make an existing, valid client operation fail or change
	// meaning. They fail the check unless declared.
	Breaking Severity = "breaking"
	// Dangerous changes are compatible, but a client that does not follow the
	// tolerant-reader rules can misbehave. Reported, never failing.
	Dangerous Severity = "dangerous"
	// Safe changes are additions no existing operation can observe.
	Safe Severity = "safe"
)

// The change kinds. A declaration names one of the breaking kinds exactly.
const (
	SchemaRemoved                = "schema-removed"
	SchemaAdded                  = "schema-added"
	RootOperationChanged         = "root-operation-changed"
	TypeRemoved                  = "type-removed"
	TypeAdded                    = "type-added"
	TypeKindChanged              = "type-kind-changed"
	FieldRemoved                 = "field-removed"
	FieldAdded                   = "field-added"
	FieldTypeChanged             = "field-type-changed"
	FieldTypeNarrowed            = "field-type-narrowed"
	ArgumentRemoved              = "argument-removed"
	ArgumentAdded                = "argument-added"
	RequiredArgumentAdded        = "required-argument-added"
	ArgumentTypeChanged          = "argument-type-changed"
	ArgumentTypeWidened          = "argument-type-widened"
	ArgumentDefaultChanged       = "argument-default-changed"
	ArgumentBecameRequired       = "argument-became-required"
	InputFieldRemoved            = "input-field-removed"
	InputFieldAdded              = "input-field-added"
	RequiredInputFieldAdded      = "required-input-field-added"
	InputFieldTypeChanged        = "input-field-type-changed"
	InputFieldTypeWidened        = "input-field-type-widened"
	InputFieldDefaultChanged     = "input-field-default-changed"
	InputFieldBecameRequired     = "input-field-became-required"
	EnumValueRemoved             = "enum-value-removed"
	EnumValueAdded               = "enum-value-added"
	UnionMemberRemoved           = "union-member-removed"
	UnionMemberAdded             = "union-member-added"
	InterfaceRemoved             = "interface-removed"
	InterfaceAdded               = "interface-added"
	DeprecationAdded             = "deprecation-added"
	DeprecationRemoved           = "deprecation-removed"
	DirectiveRemoved             = "directive-removed"
	DirectiveAdded               = "directive-added"
	DirectiveArgumentRemoved     = "directive-argument-removed"
	RequiredDirectiveArgAdded    = "required-directive-argument-added"
	DirectiveArgumentTypeChanged = "directive-argument-type-changed"
	DirectiveArgBecameRequired   = "directive-argument-became-required"
	DirectiveLocationRemoved     = "directive-location-removed"
)

// severities is the classification of every kind. A kind missing from it is a
// programming error, caught by a test, so a new kind cannot ship unclassified.
var severities = map[string]Severity{
	SchemaRemoved:                Breaking,
	SchemaAdded:                  Safe,
	RootOperationChanged:         Breaking,
	TypeRemoved:                  Breaking,
	TypeAdded:                    Safe,
	TypeKindChanged:              Breaking,
	FieldRemoved:                 Breaking,
	FieldAdded:                   Safe,
	FieldTypeChanged:             Breaking,
	FieldTypeNarrowed:            Safe,
	ArgumentRemoved:              Breaking,
	ArgumentAdded:                Safe,
	RequiredArgumentAdded:        Breaking,
	ArgumentTypeChanged:          Breaking,
	ArgumentTypeWidened:          Safe,
	ArgumentDefaultChanged:       Dangerous,
	ArgumentBecameRequired:       Breaking,
	InputFieldRemoved:            Breaking,
	InputFieldAdded:              Safe,
	RequiredInputFieldAdded:      Breaking,
	InputFieldTypeChanged:        Breaking,
	InputFieldTypeWidened:        Safe,
	InputFieldDefaultChanged:     Dangerous,
	InputFieldBecameRequired:     Breaking,
	EnumValueRemoved:             Breaking,
	EnumValueAdded:               Dangerous,
	UnionMemberRemoved:           Breaking,
	UnionMemberAdded:             Dangerous,
	InterfaceRemoved:             Breaking,
	InterfaceAdded:               Dangerous,
	DeprecationAdded:             Safe,
	DeprecationRemoved:           Safe,
	DirectiveRemoved:             Breaking,
	DirectiveAdded:               Safe,
	DirectiveArgumentRemoved:     Breaking,
	RequiredDirectiveArgAdded:    Breaking,
	DirectiveArgumentTypeChanged: Breaking,
	DirectiveArgBecameRequired:   Breaking,
	DirectiveLocationRemoved:     Breaking,
}

// SeverityOf returns a kind's severity, and false for a kind this package does not
// define.
func SeverityOf(kind string) (Severity, bool) {
	s, ok := severities[kind]
	return s, ok
}

// Change is one difference between a schema at the baseline and the same schema now.
type Change struct {
	// Schema is the published name ("device-management", "user-management-admin").
	Schema string
	// Coord is the schema coordinate of the element that changed: Type, Type.field,
	// Type.field(arg:), Enum.VALUE, Input.field, @directive, @directive(arg:). A union
	// member is Union.Member, an implemented interface Type.Interface and a directive
	// location @directive.LOCATION, each distinguished from a field by its kind. A
	// removed or added schema is "*".
	Coord    string
	Kind     string
	Severity Severity
	// Detail says what changed, in words, with the old and new types where they apply.
	Detail string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %s %s — %s", c.Schema, c.Coord, c.Kind, c.Detail)
}

// Parsed is one served schema, parsed the way the services parse it.
type Parsed struct {
	Name string
	Path string
	AST  *ast.Schema
}

// Load reads and parses every schema served under servicesDir.
func Load(servicesDir string) ([]Parsed, error) {
	srcs, err := sdldesc.LoadSources(servicesDir)
	if err != nil {
		return nil, err
	}
	out := make([]Parsed, 0, len(srcs))
	for _, s := range srcs {
		a, err := Parse(s.SDL)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.Path, err)
		}
		out = append(out, Parsed{Name: s.Name, Path: s.Path, AST: a})
	}
	return out, nil
}

// Parse parses one SDL document with the options the services use.
func Parse(sdl string) (*ast.Schema, error) {
	s, err := graphql.ParseSchema(sdl, nil, graphql.UseStringDescriptions(), graphql.UseFieldResolvers())
	if err != nil {
		return nil, err
	}
	return s.ASTSchema(), nil
}

// builtins are the names graphql-go adds to every schema, learned from an empty one so
// that a library upgrade adding one cannot make every schema report a change.
var builtinTypes, builtinDirectives = func() (map[string]bool, map[string]bool) {
	s := graphql.MustParseSchema(`type Query { x: Int }`, nil, graphql.UseFieldResolvers()).ASTSchema()
	ts, ds := map[string]bool{}, map[string]bool{}
	for n := range s.Types {
		if n != "Query" {
			ts[n] = true
		}
	}
	for n := range s.Directives {
		ds[n] = true
	}
	return ts, ds
}()

// Compare diffs every schema served at the baseline against the schema of the same
// published name now. A schema that is no longer served is one breaking change, not
// one per element: the client calling it fails on the endpoint, before any field.
func Compare(base, head []Parsed) []Change {
	heads := map[string]*ast.Schema{}
	for _, h := range head {
		heads[h.Name] = h.AST
	}
	bases := map[string]bool{}
	var out []Change
	for _, b := range base {
		bases[b.Name] = true
		h, ok := heads[b.Name]
		if !ok {
			out = append(out, change(b.Name, "*", SchemaRemoved, "the schema is no longer served"))
			continue
		}
		out = append(out, Diff(b.Name, b.AST, h)...)
	}
	for _, h := range head {
		if !bases[h.Name] {
			out = append(out, change(h.Name, "*", SchemaAdded, "a new schema is served"))
		}
	}
	sortChanges(out)
	return out
}

func change(schema, coord, kind, detail string) Change {
	sev, ok := severities[kind]
	if !ok {
		// Unreachable unless a kind constant is used without a severity, which
		// TestEveryKindIsClassified refuses. Classify it as breaking rather than
		// letting an unknown change pass.
		sev = Breaking
	}
	return Change{Schema: schema, Coord: coord, Kind: kind, Severity: sev, Detail: detail}
}

func sortChanges(cs []Change) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Schema != cs[j].Schema {
			return cs[i].Schema < cs[j].Schema
		}
		if cs[i].Coord != cs[j].Coord {
			return cs[i].Coord < cs[j].Coord
		}
		return cs[i].Kind < cs[j].Kind
	})
}

// Diff classifies every difference between two versions of one schema.
func Diff(schema string, old, cur *ast.Schema) []Change {
	var out []Change
	add := func(coord, kind, detail string) { out = append(out, change(schema, coord, kind, detail)) }

	for _, op := range []string{"query", "mutation", "subscription"} {
		o, n := rootName(old, op), rootName(cur, op)
		if o != "" && o != n {
			if n == "" {
				add(o, RootOperationChanged, fmt.Sprintf("the schema no longer has a %s root (was %s)", op, o))
			} else {
				add(o, RootOperationChanged, fmt.Sprintf("the %s root type changed from %s to %s", op, o, n))
			}
		}
	}

	for _, name := range sortedKeys(old.Types) {
		if builtinTypes[name] {
			continue
		}
		ot := old.Types[name]
		nt, ok := cur.Types[name]
		if !ok {
			add(name, TypeRemoved, fmt.Sprintf("%s %s was removed", strings.ToLower(ot.Kind()), name))
			continue
		}
		if ot.Kind() != nt.Kind() {
			add(name, TypeKindChanged, fmt.Sprintf("%s changed from %s to %s", name, ot.Kind(), nt.Kind()))
			continue
		}
		switch o := ot.(type) {
		case *ast.ObjectTypeDefinition:
			n := nt.(*ast.ObjectTypeDefinition)
			diffFields(add, name, o.Fields, n.Fields)
			diffInterfaces(add, name, inames(o.Interfaces), inames(n.Interfaces))
		case *ast.InterfaceTypeDefinition:
			n := nt.(*ast.InterfaceTypeDefinition)
			diffFields(add, name, o.Fields, n.Fields)
			diffInterfaces(add, name, inames(o.Interfaces), inames(n.Interfaces))
		case *ast.InputObject:
			n := nt.(*ast.InputObject)
			diffInputValues(add, name, ".", "", InputKinds, o.Values, n.Values)
		case *ast.EnumTypeDefinition:
			n := nt.(*ast.EnumTypeDefinition)
			ov, nv := map[string]*ast.EnumValueDefinition{}, map[string]*ast.EnumValueDefinition{}
			for _, v := range o.EnumValuesDefinition {
				ov[v.EnumValue] = v
			}
			for _, v := range n.EnumValuesDefinition {
				nv[v.EnumValue] = v
			}
			for _, v := range o.EnumValuesDefinition {
				coord := name + "." + v.EnumValue
				nvv, ok := nv[v.EnumValue]
				if !ok {
					add(coord, EnumValueRemoved, fmt.Sprintf("enum value %s was removed from %s", v.EnumValue, name))
					continue
				}
				diffDeprecation(add, coord, v.Directives, nvv.Directives)
			}
			for _, v := range n.EnumValuesDefinition {
				if _, ok := ov[v.EnumValue]; !ok {
					add(name+"."+v.EnumValue, EnumValueAdded,
						fmt.Sprintf("enum value %s was added to %s; a client must treat a value it does not know as not handled yet", v.EnumValue, name))
				}
			}
		case *ast.Union:
			n := nt.(*ast.Union)
			om, nm := setOf(unames(o.UnionMemberTypes)), setOf(unames(n.UnionMemberTypes))
			for _, m := range unames(o.UnionMemberTypes) {
				if !nm[m] {
					add(name+"."+m, UnionMemberRemoved, fmt.Sprintf("%s is no longer a member of union %s", m, name))
				}
			}
			for _, m := range unames(n.UnionMemberTypes) {
				if !om[m] {
					add(name+"."+m, UnionMemberAdded, fmt.Sprintf("%s was added to union %s", m, name))
				}
			}
		case *ast.ScalarTypeDefinition:
			// A scalar is its name; there is nothing inside it to compare.
		default:
			// A kind this check cannot compare must not pass as unchanged.
			add(name, TypeKindChanged, fmt.Sprintf("%s is a %s, which this check does not know how to compare; teach sdlcompat the kind", name, ot.Kind()))
		}
	}
	for _, name := range sortedKeys(cur.Types) {
		if builtinTypes[name] {
			continue
		}
		if _, ok := old.Types[name]; !ok {
			add(name, TypeAdded, fmt.Sprintf("%s %s was added", strings.ToLower(cur.Types[name].Kind()), name))
		}
	}

	for _, name := range sortedDirectives(old.Directives) {
		if builtinDirectives[name] {
			continue
		}
		od := old.Directives[name]
		coord := "@" + name
		nd, ok := cur.Directives[name]
		if !ok {
			add(coord, DirectiveRemoved, fmt.Sprintf("directive @%s was removed", name))
			continue
		}
		diffInputValues(add, coord, "(", ":)", DirectiveKinds, od.Arguments, nd.Arguments)
		nl := setOf(nd.Locations)
		for _, l := range od.Locations {
			if !nl[l] {
				add(coord+"."+l, DirectiveLocationRemoved, fmt.Sprintf("directive @%s may no longer be used on %s", name, l))
			}
		}
	}
	for _, name := range sortedDirectives(cur.Directives) {
		if builtinDirectives[name] {
			continue
		}
		if _, ok := old.Directives[name]; !ok {
			add("@"+name, DirectiveAdded, fmt.Sprintf("directive @%s was added", name))
		}
	}
	sortChanges(out)
	return out
}

type adder func(coord, kind, detail string)

func diffFields(add adder, typ string, old, cur ast.FieldsDefinition) {
	for _, of := range old {
		coord := typ + "." + of.Name
		nf := cur.Get(of.Name)
		if nf == nil {
			add(coord, FieldRemoved, fmt.Sprintf("field %s was removed from %s (it was %s)", of.Name, typ, of.Type))
			continue
		}
		switch {
		case of.Type.String() == nf.Type.String():
		case outputCompatible(of.Type, nf.Type):
			add(coord, FieldTypeNarrowed, fmt.Sprintf("type narrowed from %s to %s", of.Type, nf.Type))
		default:
			add(coord, FieldTypeChanged, fmt.Sprintf("type changed from %s to %s", of.Type, nf.Type))
		}
		diffInputValues(add, coord, "(", ":)", ArgumentKinds, of.Arguments, nf.Arguments)
		diffDeprecation(add, coord, of.Directives, nf.Directives)
	}
	for _, nf := range cur {
		if old.Get(nf.Name) == nil {
			add(typ+"."+nf.Name, FieldAdded, fmt.Sprintf("field %s: %s was added to %s", nf.Name, nf.Type, typ))
		}
	}
}

func diffInterfaces(add adder, typ string, old, cur []string) {
	o, n := setOf(old), setOf(cur)
	for _, i := range old {
		if !n[i] {
			add(typ+"."+i, InterfaceRemoved, fmt.Sprintf("%s no longer implements %s", typ, i))
		}
	}
	for _, i := range cur {
		if !o[i] {
			add(typ+"."+i, InterfaceAdded, fmt.Sprintf("%s now implements %s", typ, i))
		}
	}
}

// InputValueKinds names the kinds an input-value list reports, which differ by where
// the list lives: arguments of a field, fields of an input object, or arguments of a
// directive.
type InputValueKinds struct {
	Removed, Added, RequiredAdded, TypeChanged, TypeWidened, DefaultChanged string
	// BecameRequired is a non-null value that LOST its default: its type is unchanged,
	// but every caller that relied on the default by omitting it now fails validation.
	BecameRequired string
}

var (
	// ArgumentKinds are the kinds for a field's arguments.
	ArgumentKinds = InputValueKinds{ArgumentRemoved, ArgumentAdded, RequiredArgumentAdded,
		ArgumentTypeChanged, ArgumentTypeWidened, ArgumentDefaultChanged, ArgumentBecameRequired}
	// InputKinds are the kinds for an input object's fields.
	InputKinds = InputValueKinds{InputFieldRemoved, InputFieldAdded, RequiredInputFieldAdded,
		InputFieldTypeChanged, InputFieldTypeWidened, InputFieldDefaultChanged, InputFieldBecameRequired}
	// DirectiveKinds are the kinds for a directive's arguments. A directive argument's
	// default and a widened type are not worth their own kinds; they report as an
	// argument's would.
	DirectiveKinds = InputValueKinds{DirectiveArgumentRemoved, ArgumentAdded, RequiredDirectiveArgAdded,
		DirectiveArgumentTypeChanged, ArgumentTypeWidened, ArgumentDefaultChanged, DirectiveArgBecameRequired}
)

// diffInputValues compares arguments or input fields. The coordinate of an element is
// owner+open+name+close: Type.field(arg:) for an argument, Input.field for an input
// field.
func diffInputValues(add adder, owner, open, closing string, k InputValueKinds, old, cur ast.ArgumentsDefinition) {
	for _, ov := range old {
		coord := owner + open + ov.Name.Name + closing
		nv := cur.Get(ov.Name.Name)
		if nv == nil {
			add(coord, k.Removed, fmt.Sprintf("%s was removed (it was %s)", ov.Name.Name, ov.Type))
			continue
		}
		switch {
		case ov.Type.String() == nv.Type.String():
		case inputCompatible(ov.Type, nv.Type):
			add(coord, k.TypeWidened, fmt.Sprintf("type widened from %s to %s", ov.Type, nv.Type))
		default:
			add(coord, k.TypeChanged, fmt.Sprintf("type changed from %s to %s", ov.Type, nv.Type))
		}
		_, oldNonNull := ov.Type.(*ast.NonNull)
		_, newNonNull := nv.Type.(*ast.NonNull)
		if od, nd := valueString(ov.Default), valueString(nv.Default); od != nd && oldNonNull && newNonNull && nv.Default == nil {
			// `limit: Int! = 10` -> `limit: Int!`: the type string is unchanged, so only
			// the default comparison sees it, and a changed default is merely dangerous.
			// But a caller that omitted the value was relying on the default, and now
			// sends an operation that fails validation: that is a required value added.
			add(coord, k.BecameRequired, fmt.Sprintf("default %s was removed from a non-null value, which is now required; every caller that omitted it fails", od))
		} else if od != nd {
			add(coord, k.DefaultChanged, fmt.Sprintf("default changed from %s to %s", orNone(od), orNone(nd)))
		}
		diffDeprecation(add, coord, ov.Directives, nv.Directives)
	}
	for _, nv := range cur {
		if old.Get(nv.Name.Name) != nil {
			continue
		}
		coord := owner + open + nv.Name.Name + closing
		if _, required := nv.Type.(*ast.NonNull); required && nv.Default == nil {
			add(coord, k.RequiredAdded, fmt.Sprintf("required %s: %s was added; every existing caller omits it", nv.Name.Name, nv.Type))
		} else {
			add(coord, k.Added, fmt.Sprintf("optional %s: %s was added", nv.Name.Name, nv.Type))
		}
	}
}

func diffDeprecation(add adder, coord string, old, cur ast.DirectiveList) {
	o, n := old.Get("deprecated") != nil, cur.Get("deprecated") != nil
	switch {
	case !o && n:
		add(coord, DeprecationAdded, "marked @deprecated")
	case o && !n:
		add(coord, DeprecationRemoved, "no longer @deprecated")
	}
}

// outputCompatible reports whether a field whose type changed from old to cur still
// satisfies every client that read the old type: only a gain of non-null is allowed,
// at any list depth.
func outputCompatible(old, cur ast.Type) bool {
	if n, ok := cur.(*ast.NonNull); ok {
		if o, ok := old.(*ast.NonNull); ok {
			return outputCompatible(o.OfType, n.OfType)
		}
		return outputCompatible(old, n.OfType)
	}
	if _, ok := old.(*ast.NonNull); ok {
		return false
	}
	if o, ok := old.(*ast.List); ok {
		n, ok := cur.(*ast.List)
		return ok && outputCompatible(o.OfType, n.OfType)
	}
	if _, ok := cur.(*ast.List); ok {
		return false
	}
	return namedOf(old) == namedOf(cur)
}

// inputCompatible reports whether an argument or input field whose type changed from
// old to cur still accepts every value a client could send for the old type: only a
// loss of non-null is allowed, at any list depth.
func inputCompatible(old, cur ast.Type) bool {
	if o, ok := old.(*ast.NonNull); ok {
		if n, ok := cur.(*ast.NonNull); ok {
			return inputCompatible(o.OfType, n.OfType)
		}
		return inputCompatible(o.OfType, cur)
	}
	if _, ok := cur.(*ast.NonNull); ok {
		return false
	}
	if o, ok := old.(*ast.List); ok {
		n, ok := cur.(*ast.List)
		return ok && inputCompatible(o.OfType, n.OfType)
	}
	if _, ok := cur.(*ast.List); ok {
		return false
	}
	return namedOf(old) == namedOf(cur)
}

func namedOf(t ast.Type) string {
	if n, ok := t.(ast.NamedType); ok {
		return n.TypeName()
	}
	return t.String()
}

func rootName(s *ast.Schema, op string) string {
	if t, ok := s.RootOperationTypes[op]; ok && t != nil {
		return t.TypeName()
	}
	return ""
}

func valueString(v ast.Value) string {
	if v == nil {
		return ""
	}
	return v.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func sortedKeys(m map[string]ast.NamedType) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedDirectives(m map[string]*ast.DirectiveDefinition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func inames(l []*ast.InterfaceTypeDefinition) []string {
	out := make([]string, 0, len(l))
	for _, i := range l {
		out = append(out, i.Name)
	}
	return out
}

func unames(l []*ast.ObjectTypeDefinition) []string {
	out := make([]string, 0, len(l))
	for _, o := range l {
		out = append(out, o.Name)
	}
	return out
}

func setOf(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, s := range l {
		m[s] = true
	}
	return m
}
