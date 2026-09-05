// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"sort"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/introspection"
)

// 🔴 THE ONE WAY TO DESTROY THE ABSENT STATE THAT LIVES NOWHERE THIS PACKAGE COULD SEE.
//
// Every other property here is about Go: the request TYPE, its fields, the folds, the
// rows they produce. A default value is not in Go. It is a token in the SDL —
// `ingestBurst: Int = 5` — and it silently converts a three-state field into a two-state
// one at the layer between them.
//
// core's optional.go states the mechanism and calls it "easy to introduce by accident
// later", which is exactly what a warning comment is worth. In makeStructPacker:
//
//	if v.Default != nil { ft, _ = unwrapNonNull(ft); ft = &ast.NonNull{OfType: ft} }
//
// A field with a default is packed as NON-NULL, so an explicit null becomes an error
// rather than a clear; and StructPacker.Pack seeds every result with
// `v.Elem().Set(p.defaultStruct)`, so the default is written into the struct for an
// ABSENT field and arrives with Set=true. Absent and "sent the default" become the same
// request, and every update omitting the field WRITES the default.
//
// # 🔴 WHY NO SERVICE-SIDE TEST CATCHES IT, AND WHY THIS HAD TO BE HERE
//
// It was demonstrated rather than reasoned about: `ingestBurst: Int = 5` applied to
// user-management's AdminTenantUpdateRequest left that service's whole update surface
// green, and the mutant makes every `updateTenant` that renames a tenant write a burst
// override of 5 into it. Each layer misses it for its own reason, and the reasons do not
// overlap:
//
//   - a resolver test calls the resolver with a Go STRUCT, so the packer — where the
//     default is applied — never runs;
//   - a wire test that only checks a document's SHAPE never reads storage, and is
//     usually addressed to a record that does not exist;
//   - the service harness (Run, above) starts BELOW the packer, driving the Api
//     directly;
//   - core's own optional_test.go proves the three states survive for the Go TYPE, which
//     is true and says nothing about a DECLARATION in a service's SDL.
//
// So it is asked of the served schema, generically, once. A service wires it and gets
// the guard for every update input it declares — including the ones it adds tomorrow,
// since the input set is DERIVED from the mutation field rather than listed.
//
// # 🔴 THIS IS NOT HYGIENE. CONVERTING AN INPUT IS WHAT OPENS THE HOLE.
//
// The defect is only REACHABLE on a converted input, and that is the argument for wiring
// this everywhere rather than treating it as optional tidiness.
//
// A default makes the packer treat the field as NON-NULL and seed it into the result
// before packing. For a field whose Go type is a POINTER — every unconverted input on
// this platform — the schema then does not BUILD: `name: String = "x"` against a *string
// fails schema construction with "could not unmarshal … incompatible type". The library
// refuses the mistake outright, so a full-replace input is protected by the schema itself
// and needs no guard.
//
// An Optional* field absorbs the default cleanly and the schema builds. So the moment an
// input is converted, a token that used to be a loud startup failure becomes a silent
// change of meaning — every update omitting the field starts WRITING the default. The
// conversion removes the barrier, which makes this guard the conversion's companion
// rather than a check somebody might also run: an area that converts its inputs and does
// not wire this has traded a compile-time refusal for a runtime one nothing looks for.

// UpdateSchema is one served schema, as the service serves it.
//
// The SDL and the resolver root are taken rather than a built *graphql.Schema so the
// parse happens the way production's does: a schema that would not build for the real
// root is a failure this reports rather than one a test fixture routes around.
type UpdateSchema struct {
	// Name identifies the schema in a failure — "admin", "tenant", "settings" — since a
	// service serving several would otherwise report an input type with no way to tell
	// which document declared it.
	Name string
	// SDL is the schema text the service embeds and serves.
	SDL string
	// Root is the resolver root the service parses it against.
	Root any
	// MinUpdateMutations is the anti-vacuity floor: how many `update*` mutations this
	// schema is known to serve.
	//
	// 🔴 IT IS REQUIRED FOR THE SAME REASON THE OTHER FLOOR IS. Everything below walks
	// the mutations whose name begins with "update", and a schema whose mutations were
	// renamed, or whose Mutation type this walk failed to reach, yields an empty list —
	// and a loop over nothing reports success. The floor is what stands between this
	// guard and a green run over zero inputs.
	MinUpdateMutations int
}

// AssertNoUpdateInputCarriesAnSDLDefault requires that no field reachable from any
// `update*` mutation's input objects declares a default value.
//
// # WHAT IT WALKS, AND WHY IT IS DERIVED RATHER THAN LISTED
//
// The input set comes from the SCHEMA: every argument of every mutation whose name
// begins with "update", unwrapped through NonNull and List to an INPUT_OBJECT, and then
// every input object reachable from that one. A hand-written roster of input type names
// would be a second copy of the truth with the usual failure — an input added tomorrow
// appears in neither the roster nor anything that reads it.
//
// Nested inputs are followed because a default is just as destructive one level down,
// and a walk that stopped at the top level would report success for the field it could
// not see. Cycles are possible in a GraphQL input graph and are handled by the visited
// set rather than by trusting that none exist.
//
// # IT DOES NOT LOOK AT CREATE INPUTS, DELIBERATELY
//
// A default on a CREATE input is a legitimate thing: a create has no stored value to
// preserve, so "the caller said nothing" and "the caller asked for the usual" are
// genuinely the same request. It is only an update, where absent has to mean LEAVE IT
// ALONE, that a default destroys a state.
//
// The package header states the sharper reason the scope is right: a default is only
// reachable at all on a converted input, because the schema refuses to build one against
// a pointer field. Widening this to every input would add no coverage and would make it a
// rule someone eventually turns off.
func AssertNoUpdateInputCarriesAnSDLDefault(t *testing.T, schemas ...UpdateSchema) {
	t.Helper()
	if len(schemas) == 0 {
		t.Fatal("no schemas were supplied, so this guard would walk nothing and report success")
	}
	for _, s := range schemas {
		assertOneSchemaHasNoUpdateInputDefaults(t, s)
	}
}

func assertOneSchemaHasNoUpdateInputDefaults(t *testing.T, s UpdateSchema) {
	t.Helper()
	if s.Name == "" {
		t.Fatal("an UpdateSchema has no Name, so a failure could not say which document declared " +
			"the input it names")
	}
	if s.MinUpdateMutations <= 0 {
		t.Fatalf("%s: MinUpdateMutations must be greater than zero — it is the only thing "+
			"standing between this guard and a walk that finds no update mutations and reports "+
			"success", s.Name)
	}

	schema, err := graphql.ParseSchema(s.SDL, s.Root)
	if err != nil {
		t.Fatalf("%s: the schema does not parse against its own resolver root: %v", s.Name, err)
	}
	mutation := schema.Inspect().MutationType()
	if mutation == nil {
		t.Fatalf("%s: the schema declares no Mutation type, so this guard is reading nothing", s.Name)
	}
	fields := mutation.Fields(&struct{ IncludeDeprecated bool }{})
	if fields == nil || len(*fields) == 0 {
		t.Fatalf("%s: the Mutation type reports no fields, so this guard is reading nothing", s.Name)
	}

	seen := map[string]bool{}
	found := 0
	for _, f := range *fields {
		if !strings.HasPrefix(f.Name(), "update") {
			continue
		}
		found++
		for _, arg := range f.Args(&struct{ IncludeDeprecated bool }{}) {
			// 🔴 AN ARGUMENT'S OWN DEFAULT IS CHECKED TOO, not just the input object's
			// fields. `updateProfile(firstName: String = "")` is the same defect one
			// level out, and it is the shape a mutation still taking loose scalars would
			// have — which is exactly the kind this arc is converting.
			if arg.DefaultValue() != nil {
				t.Errorf("%s: %s's argument %s declares an SDL default (%s). A default is "+
					"packed as NON-NULL and seeded into the result, so an ABSENT argument "+
					"arrives as if the caller had sent it — which is the absent state, deleted",
					s.Name, f.Name(), arg.Name(), *arg.DefaultValue())
			}
			walkInputForDefaults(t, s.Name, f.Name(), arg.Type(), seen)
		}
	}

	if found < s.MinUpdateMutations {
		t.Fatalf("%s: only %d update* mutations were found, and this schema is known to serve at "+
			"least %d; this guard has stopped seeing the surface it certifies",
			s.Name, found, s.MinUpdateMutations)
	}
	// An update surface with no input objects at all is not a pass, it is a walk that
	// examined nothing. It is what a schema of loose-scalar updates looks like, and this
	// arc exists to convert those.
	if len(seen) == 0 {
		t.Errorf("%s: %d update* mutations were walked and NONE takes an input object, so this "+
			"guard checked no fields. Either the mutations still take loose scalar arguments — "+
			"which is the shape being converted — or the walk is not reaching them",
			s.Name, found)
	}
}

// walkInputForDefaults follows one argument's type to its input object and checks every
// field, then every input object those fields reach.
func walkInputForDefaults(t *testing.T, schema, mutation string, typ *introspection.Type, seen map[string]bool) {
	t.Helper()
	// Unwrap NonNull and List to whatever is underneath. `request: FooUpdateRequest!` and
	// `patches: [FooPatch!]!` both carry an input object a default can sit in.
	for typ != nil && typ.Kind() != "INPUT_OBJECT" {
		typ = typ.OfType()
	}
	if typ == nil {
		return // a scalar or an enum: nothing with fields to default
	}
	name := typ.Name()
	if name == nil || seen[*name] {
		return
	}
	seen[*name] = true

	inputFields := typ.InputFields(&struct{ IncludeDeprecated bool }{})
	if inputFields == nil {
		return
	}
	var offenders []string
	for _, v := range *inputFields {
		if v.DefaultValue() != nil {
			offenders = append(offenders, v.Name()+" = "+*v.DefaultValue())
		}
		walkInputForDefaults(t, schema, mutation, v.Type(), seen)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("%s: input %s, reachable from %s, declares SDL defaults on %s.\n"+
			"A default is packed as NON-NULL — so an explicit null becomes an ERROR rather than "+
			"a clear — and StructPacker seeds it into the result before packing, so an ABSENT "+
			"field arrives with Set=true holding the default. Every update omitting the field "+
			"then WRITES it. Remove the default; a partial update's absent state is the whole "+
			"point of the input, and no Go-side test in this package can see this",
			schema, *name, mutation, strings.Join(offenders, ", "))
	}
}
