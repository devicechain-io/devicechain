// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// OptionalLocationDeclaration carries the three states of a profile's position
// declaration (ADR-078) through the packer, the way core's Optional* types carry a
// scalar. It is declared HERE rather than in core because the schema type it packs —
// DeviceLocationDeclarationInput — belongs to this service, and ImplementsGraphQLType
// is handed that name verbatim.
//
// # 🔴 IT IS THE FIELD THE CONVERSION CHANGES THE MEANING OF
//
// Under the full-replace input this replaces, a request carrying NO declaration
// CLEARED one that was there, and the schema said so in as many words. That made
// omission the clear operation, so any caller that restated a profile without
// carrying `location` forward silently un-declared position for every device built on
// it — which is exactly why the console had to grow deviceProfilePreserved(). Under
// three states:
//
//	ABSENT   the field was not in the request   -> leave the declaration alone
//	NULL     the field was sent as null         -> the profile no longer declares position
//	{...}    an object (including `{}`)         -> that declaration, replacing any prior one
//
// The two values the ADR-078 design rests on staying distinct still are: an explicit
// null encodes to SQL NULL ("does not report position") and `{}` encodes to the two
// bytes `{}` ("reports position, no expectations stated"). Nothing here collapses one
// into the other — see encodeLocationDeclaration, which is still the only writer.
//
// # Why the object is decoded by hand
//
// The library hands an unmarshalerPacker the RAW value for the field, which for an
// input object is the deserialized map — so the struct packer that would ordinarily
// build a *LocationDeclaration never runs. The schema still validates the object's
// entries before execution (an entry the type does not declare is a request error, on
// the literal path and, since the fork, on the variable path too), so what is decoded
// here has already been checked; the default branch below is the fail-closed backstop
// for the day that stops being true, never the primary guard.
//
// The scalar coercion is delegated to core's own Optional* unmarshalers rather than
// re-typed here. A query LITERAL and a VARIABLE arrive as different Go types for the
// same GraphQL Int (the parser's int32 versus encoding/json's float64 or
// json.Number), and that divergence is precisely the class the graphql-go fork exists
// to close — so the coercion has one implementation, not two.
type OptionalLocationDeclaration struct {
	// Set is true only when the field was PRESENT in the request, whether its value
	// was null or an object.
	Set bool
	// Value is nil when the field was present and explicitly null. It is meaningless
	// unless Set is true.
	Value *LocationDeclaration
}

// ImplementsGraphQLType names the schema input this packs. Only the one spelling is
// accepted, so a field wired to some other input object fails at SCHEMA CONSTRUCTION
// rather than reaching UnmarshalGraphQL with a map this type has no reading for.
func (OptionalLocationDeclaration) ImplementsGraphQLType(name string) bool {
	return name == "DeviceLocationDeclarationInput"
}

// Nullable marks this as a type that can accept an explicit null. REQUIRED — without
// it makePacker refuses a non-pointer Go type for a nullable schema field and the
// schema does not build, and null would never reach UnmarshalGraphQL at all.
func (OptionalLocationDeclaration) Nullable() {}

func (o *OptionalLocationDeclaration) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	fields, ok := input.(map[string]any)
	if !ok {
		return fmt.Errorf("expected a DeviceLocationDeclarationInput object, got %T", input)
	}
	decl := &LocationDeclaration{}
	for name, raw := range fields {
		switch name {
		case "expectedAccuracyMeters":
			var f dcgraphql.OptionalFloat64
			if err := f.UnmarshalGraphQL(raw); err != nil {
				return fmt.Errorf("expectedAccuracyMeters: %w", err)
			}
			decl.ExpectedAccuracyMeters = f.Value
		case "expectedUpdateIntervalSeconds":
			var n dcgraphql.OptionalInt32
			if err := n.UnmarshalGraphQL(raw); err != nil {
				return fmt.Errorf("expectedUpdateIntervalSeconds: %w", err)
			}
			decl.ExpectedUpdateIntervalSeconds = n.Value
		default:
			return fmt.Errorf("%q is not a field of DeviceLocationDeclarationInput", name)
		}
	}
	o.Value = decl
	return nil
}

// ApplyTo returns the declaration the profile should hold after this request: current
// when the field was absent, nil when it was explicitly null, otherwise what was sent.
//
// The result still goes through encodeLocationDeclaration, so a declaration stating
// something physically meaningless is refused at authoring time exactly as it is on
// create — the validation is not skipped just because the value came from an update.
func (o OptionalLocationDeclaration) ApplyTo(current *LocationDeclaration) *LocationDeclaration {
	if !o.Set {
		return current
	}
	return o.Value
}

// OptionalLocationDeclarationOf builds the "sent with a value" state, for the callers
// that build requests in Go rather than receiving them off the wire.
func OptionalLocationDeclarationOf(v LocationDeclaration) OptionalLocationDeclaration {
	return OptionalLocationDeclaration{Set: true, Value: &v}
}

// ClearedLocationDeclaration builds the "sent as null" state, which stops the profile
// declaring that its devices report position. The zero OptionalLocationDeclaration is
// the ABSENT state and leaves the stored declaration alone — the two are easy to
// confuse in a literal, so say which one you mean.
func ClearedLocationDeclaration() OptionalLocationDeclaration {
	return OptionalLocationDeclaration{Set: true}
}
