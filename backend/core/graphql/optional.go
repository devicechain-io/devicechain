// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"fmt"

	"github.com/graph-gophers/graphql-go"
)

// Optional* input types distinguish the THREE states a GraphQL input field can
// be in. A plain pointer can only express two, which is why every update* mutation
// on the platform is a full replace: a caller sending {token, name} to rename an
// entity erases its metadata, its externalId and everything else it did not mention,
// and gets a 200 OK for it.
//
//	ABSENT   the field was not in the request   -> leave the stored value alone
//	NULL     the field was sent as null         -> clear the stored value
//	VALUE    the field was sent with a value    -> set it
//
// # Why this shape, and not a pointer or a wrapper the resolver inspects
//
// graphql-go's StructPacker.Pack iterates the SCHEMA's declared input fields and
// packs only those present in the request map:
//
//	for _, f := range p.fields {
//	    if value, ok := values[f.name]; ok { ... }
//	}
//
// An absent field is skipped entirely, leaving the Go struct field at its zero
// value. An explicit null IS present in the map, with the value nil, and is packed.
// A *string therefore reads nil for both and cannot tell them apart — the
// distinction is destroyed inside the library, before any resolver runs.
//
// The one hook the library offers is decode.Unmarshaler: a field whose Go type
// implements it gets an unmarshalerPacker, and because Pack is never called for an
// absent field, UnmarshalGraphQL runs ONLY when the field was sent. So `Set` is
// true exactly when the caller mentioned the field. That is the whole mechanism.
//
// # The marker method is required, and its absence fails at startup
//
// Nullable() looks like decoration and is not. For a NULLABLE schema field (String,
// not String!) held in a NON-POINTER Go type, makePacker takes its isNullable
// branch, and isNullable tests for NullUnmarshaller — decode.Unmarshaler PLUS a
// marker method Nullable(). Without it, makePacker returns "…is not a pointer or a
// nullable type" and THE SCHEMA FAILS TO BUILD. That is the right direction to fail
// in, but it is inexplicable to whoever hits it first, so it is pinned by a test:
// see TestMissingNullableMarkerFailsSchemaConstruction.
//
// Nullable() is also what makes an explicit null reach UnmarshalGraphQL at all.
// Both nullPacker.Pack and unmarshalerPacker.Pack short-circuit a nil value with
// `value == nil && !isNullable(...)`; satisfying the interface is what routes null
// through to us as UnmarshalGraphQL(nil) instead of a zero value or an error.
//
// # 🔴 An optional field must NEVER carry an SDL default value
//
// This one is not in the library's documentation and is easy to introduce by
// accident later. In makeStructPacker:
//
//	if v.Default != nil { ft, _ = unwrapNonNull(ft); ft = &ast.NonNull{OfType: ft} }
//
// A field with a default is packed as NON-NULL, which skips the isNullable branch
// entirely — so an explicit null becomes an error rather than a clear. Worse,
// StructPacker.Pack seeds every result with `v.Elem().Set(p.defaultStruct)`, so the
// default is written into the struct for an ABSENT field, arriving with Set=true.
// Absent then becomes indistinguishable from "sent the default", which is precisely
// the bug these types exist to remove. Pinned by TestSDLDefaultDefeatsTheAbsentState.
//
// # Using them in a resolver
//
// ApplyTo folds the three states onto the stored value, so a resolver reads as one
// line per field and the semantics live here rather than being re-derived (and
// eventually re-derived differently) at every call site. Its counterpart for a column
// that cannot be cleared is ApplyToRequired, in optional_required.go:
//
//	found.Name = request.Name.ApplyTo(found.Name)
//	found.Metadata = request.Metadata.ApplyTo(found.Metadata)

// OptionalString carries a nullable String input field through the packer with its
// present/absent distinction intact.
type OptionalString struct {
	// Set is true only when the field was PRESENT in the request, whether its
	// value was null or not.
	Set bool
	// Value is nil when the field was present and explicitly null. It is
	// meaningless unless Set is true.
	Value *string
}

func (OptionalString) ImplementsGraphQLType(name string) bool { return name == "String" }

// Nullable marks this as a type that can accept an explicit null. REQUIRED — see
// the package comment above; without it the schema will not build.
func (OptionalString) Nullable() {}

func (o *OptionalString) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	s, ok := input.(string)
	if !ok {
		return fmt.Errorf("expected a String, got %T", input)
	}
	o.Value = &s
	return nil
}

// ApplyTo returns the value the field should hold after this request: current when
// the field was absent, nil when it was explicitly null, otherwise the new value.
func (o OptionalString) ApplyTo(current *string) *string {
	if !o.Set {
		return current
	}
	return o.Value
}

// 🔴 THERE IS DELIBERATELY NO ApplyToValue, AND ITS ABSENCE IS A DESIGN DECISION
// RATHER THAN AN OMISSION.
//
// Each of these types used to carry one: ApplyTo for a NON-POINTER column, folding an
// explicit null to the zero value on the reasoning that a model with no way to spell
// NULL has no other reading available. Every call site it was written for turned out
// to be a NOT NULL column, where the zero value is not a state the entity may be in —
// so what it actually did was accept "clear this required field" and write a value the
// create path would have refused, successfully. On a Boolean that is invisible:
// `enabled: null` becomes false, and false is a value the caller could have sent.
//
// ApplyToRequired in optional_required.go is what those call sites need, and it
// REFUSES the null instead. Deleting ApplyToValue rather than documenting it as a trap
// is the same move as dropping an immutable field from an update input: a fold nobody
// can reach cannot be reached by accident, and a warning comment only works on someone
// who reads it first.
//
// If a genuinely nullable-in-the-model-but-not-in-the-column case ever appears, the
// honest fix is to make the column nullable, not to bring this back.

// OptionalStringOf builds a field in the "sent with a value" state. Constructors
// exist for the callers that build requests in Go rather than receiving them off
// the wire — tests, dcctl, and the SDKs — because the alternative,
// `OptionalString{Set: true, Value: &v}`, requires an addressable variable per
// field and makes forgetting Set (which silently means "leave alone") a one-token
// mistake in a struct literal.
func OptionalStringOf(v string) OptionalString { return OptionalString{Set: true, Value: &v} }

// ClearedString builds a field in the "sent as null" state, which CLEARS the
// stored value. The zero OptionalString is the absent state and leaves it alone —
// the two are easy to confuse in a literal, so say which one you mean.
func ClearedString() OptionalString { return OptionalString{Set: true} }

// OptionalBool carries a nullable Boolean input field.
type OptionalBool struct {
	Set   bool
	Value *bool
}

func (OptionalBool) ImplementsGraphQLType(name string) bool { return name == "Boolean" }
func (OptionalBool) Nullable()                              {}

func (o *OptionalBool) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	b, ok := input.(bool)
	if !ok {
		return fmt.Errorf("expected a Boolean, got %T", input)
	}
	o.Value = &b
	return nil
}

func (o OptionalBool) ApplyTo(current *bool) *bool {
	if !o.Set {
		return current
	}
	return o.Value
}

// OptionalInt32 carries a nullable Int input field. GraphQL's Int is 32-bit by
// specification, so the Go type is int32 rather than int — a 64-bit field would
// accept values the schema cannot round-trip.
type OptionalInt32 struct {
	Set   bool
	Value *int32
}

func (OptionalInt32) ImplementsGraphQLType(name string) bool { return name == "Int" }
func (OptionalInt32) Nullable()                              {}

func (o *OptionalInt32) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	// The unmarshalerPacker hands us the value the query/variable decoder produced
	// and does NOT run it through the library's own scalar coercion, so the concrete
	// type depends on how the request arrived: a query literal is decoded by the
	// parser, a variable by encoding/json. Accepting the whole plausible set here is
	// what keeps a literal and a variable behaving identically — which is exactly
	// the class of divergence that made the unknown-input-field fork necessary.
	switch v := input.(type) {
	case int32:
		o.Value = &v
	case int:
		n := int32(v)
		o.Value = &n
	case float64:
		n := int32(v)
		if float64(n) != v {
			return fmt.Errorf("expected an Int, got the non-integral value %v", v)
		}
		o.Value = &n
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return fmt.Errorf("expected an Int, got %q", v.String())
		}
		if int64(int32(n)) != n {
			return fmt.Errorf("expected a 32-bit Int, got %d", n)
		}
		i := int32(n)
		o.Value = &i
	default:
		return fmt.Errorf("expected an Int, got %T", input)
	}
	return nil
}

func (o OptionalInt32) ApplyTo(current *int32) *int32 {
	if !o.Set {
		return current
	}
	return o.Value
}

// OptionalFloat64 carries a nullable Float input field.
type OptionalFloat64 struct {
	Set   bool
	Value *float64
}

func (OptionalFloat64) ImplementsGraphQLType(name string) bool { return name == "Float" }
func (OptionalFloat64) Nullable()                              {}

func (o *OptionalFloat64) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	switch v := input.(type) {
	case float64:
		o.Value = &v
	case int32:
		f := float64(v)
		o.Value = &f
	case int:
		f := float64(v)
		o.Value = &f
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return fmt.Errorf("expected a Float, got %q", v.String())
		}
		o.Value = &f
	default:
		return fmt.Errorf("expected a Float, got %T", input)
	}
	return nil
}

func (o OptionalFloat64) ApplyTo(current *float64) *float64 {
	if !o.Set {
		return current
	}
	return o.Value
}

// OptionalID carries a nullable ID input field.
type OptionalID struct {
	Set   bool
	Value *graphql.ID
}

func (OptionalID) ImplementsGraphQLType(name string) bool { return name == "ID" }
func (OptionalID) Nullable()                              {}

func (o *OptionalID) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	switch v := input.(type) {
	case string:
		id := graphql.ID(v)
		o.Value = &id
	case graphql.ID:
		o.Value = &v
	default:
		return fmt.Errorf("expected an ID, got %T", input)
	}
	return nil
}

func (o OptionalID) ApplyTo(current *graphql.ID) *graphql.ID {
	if !o.Set {
		return current
	}
	return o.Value
}

// Constructors for the remaining scalars, matching OptionalStringOf/ClearedString.
// The full set exists so that a conversion reaching for OptionalBoolOf does not
// find only the String pair and quietly fall back to a raw struct literal.

func OptionalBoolOf(v bool) OptionalBool { return OptionalBool{Set: true, Value: &v} }
func ClearedBool() OptionalBool          { return OptionalBool{Set: true} }

func OptionalInt32Of(v int32) OptionalInt32 { return OptionalInt32{Set: true, Value: &v} }
func ClearedInt32() OptionalInt32           { return OptionalInt32{Set: true} }

func OptionalFloat64Of(v float64) OptionalFloat64 { return OptionalFloat64{Set: true, Value: &v} }
func ClearedFloat64() OptionalFloat64             { return OptionalFloat64{Set: true} }

func OptionalIDOf(v graphql.ID) OptionalID { return OptionalID{Set: true, Value: &v} }
func ClearedID() OptionalID                { return OptionalID{Set: true} }
