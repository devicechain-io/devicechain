// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// THE PARTIAL-UPDATE HARNESS.
//
// What a partial update means once it reaches storage, asserted the same way for every
// converted family in every service. These drive the real Api against a real database, so
// the assertions are about rows, not about structs. The families themselves are declared
// by each service, as data.
//
// # Why a harness and not one test file per family
//
// This started as one service's api_device_type_partial_update_test.go — a good test, but
// a SHAPE. Copying that shape per family is how a suite drifts: the seventh copy tests
// fewer fields than the first, and nobody notices, because each copy passes. Worse, the
// original only exercised two fields BY NAME (name, icon) and asserted the rest were
// preserved. That catches a total full-replace, but not a conversion that forgot ONE
// field — send `icon`, have `manufacturer` written from a zero value, and every assertion
// in it still passes.
//
// So the family is DATA and the assertions are generic. Each family declares its fields
// once; the harness drives EVERY field through all three states and checks EVERY other
// field after each one. Adding a family is adding a row to a service's registry, not
// writing a test.
//
// The same argument is why this package is shared rather than copied into each service.
// A per-service copy is the per-family copy one level up, with the same failure: the
// eighth service's copy certifies less than the first's, and every one of them is green.
//
// # The properties, and what each one alone would miss
//
//  1. seed populates every declared field, and the seeded/replacement/cleared values are
//     pairwise distinct. This is the anti-vacuity control and it runs first, because
//     without it the rest are worthless: against a fixture with a blank field,
//     "preserved" and "was never set" are the same observation, and a full replace passes.
//  2. setting ONE field changes that field and NOTHING else.
//  3. clearing ONE field (explicit null) clears it and NOTHING else. This is the half a
//     naive "ignore empty values" implementation gets wrong — that shape preserves
//     everything, which looks correct until someone needs to remove a value and finds the
//     API cannot express it.
//  4. an update naming nothing changes nothing.
//  5. the row is addressed by the token ARGUMENT — an argument naming nothing is a
//     not-found, and it does not fall back to some other row.
//  6. a null on a field the family declares REQUIRED is refused, totally — including a
//     required BOOLEAN, where the zero value a fold would write is legal and therefore
//     invisible to everything downstream.
//  7. an unknown reference token refuses the WHOLE update, leaving nothing written.
//  8. for a LIST field, an empty list and an explicit null agree. They are one request
//     spelled two ways, and a claim to that effect is worth nothing until both are sent.
//
// Property 3 runs over exactly the fields a family declares clearable, property 6 over
// exactly the ones it declares required, and property 7 over the required REFERENCES — so
// every field is covered by 3 or 6, and a family that declares a field in the wrong kind
// FAILS rather than falling through the gap between them.
//
// Two more run alongside them: EveryRequestFieldIsDeclared asks the request TYPE whether
// the family's table is complete, and FixtureIsAsStrictAsProduction asks whether the
// fixture refuses what production refuses.
//
// # What the harness deliberately does NOT cover
//
// The WIRE shape — that `token` is not a member of the update input, and that `request`
// is non-null — is only observable against the real schema and lives with each service's
// schema tests. That the three states survive the packer at all is proved once,
// generically, in core's graphql/optional_test.go and optional_list_test.go.

// Family is one converted entity family. A is the service's Api type (e.g. *model.Api):
// the harness never inspects it, but keeping it a type parameter rather than an interface
// is what makes a family's Seed, Read and Update closures compile against the real methods
// — so a renamed method or a renamed request field is a COMPILE ERROR in the declaration
// rather than a test that quietly stops covering anything.
type Family[A any] struct {
	// Name is the entity as the schema names it: "assetType", "device".
	Name string
	// Token is the token the seed uses and the harness addresses by.
	Token string
	// Migrate lists the tables this family needs. Per-family rather than migrating
	// everything, so a family that quietly grows a dependency has to say so.
	Migrate []any
	// Seed creates the entity with EVERY declared field populated to its seeded value,
	// plus whatever it depends on (its type, its profile).
	Seed func(t *testing.T, api A, ctx context.Context)
	// Read reads the entity back FROM THE DATABASE — never from the value an update
	// returned, since a resolver that mutated its in-memory copy and never persisted
	// would satisfy every assertion made against its return value.
	Read func(t *testing.T, api A, ctx context.Context) map[string]string
	// NewRequest builds an empty update request: every field in the ABSENT state.
	NewRequest func() any
	// Update calls the family's real Api update method with that request.
	Update func(api A, ctx context.Context, token string, req any) error
	// Fields is the family's field table. Every exported field of the request type must
	// appear here; EveryRequestFieldIsDeclared is what enforces it.
	Fields []Field
}

// Suite is everything one service supplies to Run.
type Suite[A any] struct {
	// NewApi builds a fresh Api over a throwaway database migrated for the given tables.
	// NewSQLiteDB does the database half; the service wraps it in its own Api type.
	NewApi func(t *testing.T, tables ...any) A
	// Context is the tenant context the families run under. See TenantContext.
	Context func() context.Context
	// Families is the registry. Converting the next family is adding a row to it.
	Families []Family[A]

	// CreateWithToken attempts to CREATE some entity of this service's under the given
	// token, returning the API's error unchanged. It is what FixtureIsAsStrictAsProduction
	// drives, and it is REQUIRED.
	//
	// 🔴 WITHOUT IT THE FIXTURE'S STRICTNESS IS UNOBSERVED, which is how it came to be
	// weaker than production in the first place: every fixture token a service declares is
	// already grammar-conforming, so nothing ever asks the token-grammar callback to do
	// anything, and a mutant deleting its registration survives the entire suite. Asking
	// it directly is the only way the fixture's strictness is pinned rather than assumed.
	CreateWithToken func(t *testing.T, api A, ctx context.Context, token string) error
	// StrictnessTables are the tables CreateWithToken needs migrated.
	StrictnessTables []any
	// ValidToken is a grammar-conforming token CreateWithToken must ACCEPT. It is the
	// counterweight: strictness bought by refusing everything would be worthless, and
	// would make every other property fail for the wrong reason.
	ValidToken string
}

// Run drives every property over every family.
//
// The subtests are named after the properties, so a single one is still addressable:
//
//	go test ./model -run 'TestPartialUpdate/SettingOneFieldLeavesEveryOtherAlone/device'
func Run[A any](t *testing.T, s Suite[A]) {
	t.Helper()
	requireSuite(t, s)

	t.Run("SeedPopulatesEveryFieldDistinctly", func(t *testing.T) { seedPopulatesEveryFieldDistinctly(t, s) })
	t.Run("SettingOneFieldLeavesEveryOtherAlone", func(t *testing.T) { settingOneFieldLeavesEveryOtherAlone(t, s) })
	t.Run("ClearingOneFieldClearsOnlyIt", func(t *testing.T) { clearingOneFieldClearsOnlyIt(t, s) })
	t.Run("EmptyListIsTheSameAsANull", func(t *testing.T) { emptyListIsTheSameAsANull(t, s) })
	t.Run("EmptyRequestChangesNothing", func(t *testing.T) { emptyRequestChangesNothing(t, s) })
	t.Run("UnknownTokenIsNotFound", func(t *testing.T) { unknownTokenIsNotFound(t, s) })
	t.Run("ARequiredFieldRefusesAnExplicitNull", func(t *testing.T) { aRequiredFieldRefusesAnExplicitNull(t, s) })
	t.Run("UnknownReferenceRefusesTheWholeUpdate", func(t *testing.T) { unknownReferenceRefusesTheWholeUpdate(t, s) })
	t.Run("EveryRequestFieldIsDeclared", func(t *testing.T) { everyRequestFieldIsDeclared(t, s) })
	t.Run("FixtureIsAsStrictAsProduction", func(t *testing.T) { fixtureIsAsStrictAsProduction(t, s) })
}

// requireSuite is the anti-vacuity control for Run ITSELF. Every property below iterates
// the family list, and a loop over nothing reports success — so a suite that supplied no
// families, or that lost its fixture builder to a nil field, would go green having driven
// nothing at all.
func requireSuite[A any](t *testing.T, s Suite[A]) {
	t.Helper()
	if s.NewApi == nil {
		t.Fatal("Suite.NewApi is nil: nothing can be driven")
	}
	if s.Context == nil {
		t.Fatal("Suite.Context is nil: the tenant-scope callback fails closed, so every " +
			"family would fail for a reason that has nothing to do with partial updates")
	}
	if len(s.Families) == 0 {
		t.Fatal("Suite.Families is empty: every property here iterates it, and a table-driven " +
			"test over an empty table is the most convincing kind of green there is")
	}
	if s.CreateWithToken == nil {
		t.Fatal("Suite.CreateWithToken is nil: the fixture's strictness would then be assumed " +
			"rather than pinned, and a fixture weaker than production certifies a world that " +
			"is not the one being shipped")
	}
	if s.ValidToken == "" {
		t.Fatal("Suite.ValidToken is empty: the strictness property needs a token the fixture " +
			"must ACCEPT, or it is satisfied by a fixture that refuses everything")
	}
	seen := map[string]bool{}
	for _, fam := range s.Families {
		if fam.Name == "" {
			t.Fatal("a family has no Name, so its subtests cannot be told apart")
		}
		if seen[fam.Name] {
			t.Fatalf("two families are named %q: their subtests share a name, which means they "+
				"share a fixture DATABASE name too", fam.Name)
		}
		seen[fam.Name] = true
	}
}

// ─── the generic properties ────────────────────────────────────────────────

// THE ANTI-VACUITY CONTROL, and it runs first because the others depend on it.
//
// A family whose seed left a field blank makes "the update preserved it" and "it was
// never set" the same observation, and a full replace would pass every other property
// here. A family whose replacement value equalled its seeded value would make "the update
// wrote it" unobservable the same way. Both failures are silent — the suite goes green
// having covered nothing — so both are asserted rather than trusted.
func seedPopulatesEveryFieldDistinctly[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		t.Run(fam.Name, func(t *testing.T) {
			api := s.NewApi(t, fam.Migrate...)
			ctx := s.Context()
			fam.Seed(t, api, ctx)
			got := fam.Read(t, api, ctx)

			if len(fam.Fields) == 0 {
				t.Fatal("a family with no declared fields asserts nothing")
			}
			for _, f := range fam.Fields {
				v, ok := got[f.Name]
				if !ok {
					t.Errorf("%s: Read() reports no value — a field the harness cannot "+
						"observe is a field it cannot protect", f.Name)
					continue
				}
				if v != f.Seeded {
					t.Errorf("%s: declared as seeded %q but reads back %q — the fixture does "+
						"not hold what the family says it holds", f.Name, f.Seeded, v)
				}
				if f.Seeded == "" || f.Seeded == NullMarker {
					t.Errorf("%s: seeded blank, so \"preserved\" and \"never set\" are the "+
						"same observation and a full replace would pass", f.Name)
				}
				if f.Replace == f.Seeded {
					t.Errorf("%s: the replacement value equals the seeded one, so \"the update "+
						"wrote it\" is unobservable", f.Name)
				}
				if f.Kind == Clearable && f.Cleared == f.Seeded {
					t.Errorf("%s: the cleared reading equals the seeded one, so \"the update "+
						"cleared it\" is unobservable", f.Name)
				}
			}
			// Every field Read() reports must be declared, or a column added by a later
			// change goes untested while the suite stays green.
			for k := range got {
				if !declaresField(fam, k) {
					t.Errorf("Read() reports %q but no Field declares it", k)
				}
			}
		})
	}
}

// THE HEADLINE PROPERTY. Setting one field changes that field and nothing else.
//
// Under the full-replace shape this replaces, sending one field wiped every other one —
// successfully, returning 200 with the emptied entity. Every field is driven, not just a
// representative one, because a conversion that forgets a SINGLE field leaves exactly
// that field written from a zero value on every update.
func settingOneFieldLeavesEveryOtherAlone[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		for _, f := range fam.Fields {
			t.Run(fam.Name+"/"+f.Name, func(t *testing.T) {
				api := s.NewApi(t, fam.Migrate...)
				ctx := s.Context()
				fam.Seed(t, api, ctx)

				req := fam.NewRequest()
				f.Set(req, f.Replace)
				partner := partnerOf(t, fam, f)
				if partner != nil {
					partner.Set(req, partner.Replace)
				}
				if err := fam.Update(api, ctx, fam.Token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.Read(t, api, ctx)
				if got[f.Name] != f.Replace {
					t.Fatalf("%s = %q, want %q — the field the caller SENT was not written",
						f.Name, got[f.Name], f.Replace)
				}
				if partner != nil && got[partner.Name] != partner.Replace {
					t.Fatalf("%s = %q, want %q — the partner the request moved with %s was not written",
						partner.Name, got[partner.Name], partner.Replace, f.Name)
				}
				assertOthersHoldSeeded(t, fam, got, f.Name,
					"a partial update erased %s: got %q, want %q — the update is still a full replace")
			})
		}
	}
}

// THE SECOND STATE. An explicit null clears, and clears only what it names.
func clearingOneFieldClearsOnlyIt[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		for _, f := range fam.Fields {
			if f.Kind != Clearable {
				continue
			}
			t.Run(fam.Name+"/"+f.Name, func(t *testing.T) {
				api := s.NewApi(t, fam.Migrate...)
				ctx := s.Context()
				fam.Seed(t, api, ctx)

				req := fam.NewRequest()
				f.SetNull(req)
				partner := partnerOf(t, fam, f)
				if partner != nil {
					partner.SetNull(req)
				}
				if err := fam.Update(api, ctx, fam.Token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.Read(t, api, ctx)
				if got[f.Name] != f.Cleared {
					t.Fatalf("%s = %q after an explicit null, want %q — a field that cannot be "+
						"cleared is a field that can never be corrected", f.Name, got[f.Name], f.Cleared)
				}
				if partner != nil && got[partner.Name] != partner.Cleared {
					t.Fatalf("%s = %q after an explicit null, want %q — the partner cleared with %s "+
						"did not clear", partner.Name, got[partner.Name], partner.Cleared, f.Name)
				}
				assertOthersHoldSeeded(t, fam, got, f.Name,
					"clearing one field also changed %s: got %q, want %q")
			})
		}
	}
}

// THE FOURTH WIRE STATE, which only a LIST has: []. It folds onto the same stored value
// as an explicit null, and this is where that claim is measured rather than asserted.
//
// It matters because a client sends [] by accident and null almost never: a form with
// nothing selected serializes as an empty array. If the two ever stopped agreeing, the
// spelling a real client sends would be the one nobody tested.
//
// This property is SILENT for a family with no list fields, which is correct — but a
// service whose registry declares a list field and whose constructor forgot SetEmpty
// would then be silently skipped, so the loop fails on a list-shaped field with no
// SetEmpty rather than passing over it.
func emptyListIsTheSameAsANull[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		for _, f := range fam.Fields {
			if f.SetEmpty == nil {
				continue
			}
			t.Run(fam.Name+"/"+f.Name, func(t *testing.T) {
				api := s.NewApi(t, fam.Migrate...)
				ctx := s.Context()
				fam.Seed(t, api, ctx)

				req := fam.NewRequest()
				f.SetEmpty(req)
				if err := fam.Update(api, ctx, fam.Token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.Read(t, api, ctx)
				if got[f.Name] != f.Cleared {
					t.Fatalf("%s = %q after an empty list, want %q — [] and null are one request "+
						"spelled two ways, and [] is the one a form actually sends",
						f.Name, got[f.Name], f.Cleared)
				}
				assertOthersHoldSeeded(t, fam, got, f.Name,
					"emptying one list also changed %s: got %q, want %q")
			})
		}
	}
}

// THE CONTROL FOR THE WHOLE SEMANTIC. An update that names nothing changes nothing. If
// this fails, some field is being written from a zero value rather than from the stored
// one, and every partial update is silently erasing it.
func emptyRequestChangesNothing[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		t.Run(fam.Name, func(t *testing.T) {
			api := s.NewApi(t, fam.Migrate...)
			ctx := s.Context()
			fam.Seed(t, api, ctx)
			before := fam.Read(t, api, ctx)

			if err := fam.Update(api, ctx, fam.Token, fam.NewRequest()); err != nil {
				t.Fatalf("update: %v", err)
			}

			after := fam.Read(t, api, ctx)
			for _, f := range fam.Fields {
				if after[f.Name] != before[f.Name] {
					t.Errorf("an empty update changed %s from %q to %q",
						f.Name, before[f.Name], after[f.Name])
				}
			}
		})
	}
}

// The row is addressed by the token ARGUMENT, so an argument naming nothing is a
// not-found — not a silent create, and not an update of some other row named in the
// payload. That last one is exactly what the pre-conversion code did for seven families
// in device-management: it located by request.Token and ignored this argument entirely.
func unknownTokenIsNotFound[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		t.Run(fam.Name, func(t *testing.T) {
			api := s.NewApi(t, fam.Migrate...)
			ctx := s.Context()
			fam.Seed(t, api, ctx)
			before := fam.Read(t, api, ctx)

			if err := fam.Update(api, ctx, "no-such-token", fam.NewRequest()); err == nil {
				t.Fatal("updating an unknown token succeeded")
			}
			// And it left the seeded row alone — the half that catches a lookup falling
			// back to "the only row there is".
			after := fam.Read(t, api, ctx)
			for _, f := range fam.Fields {
				if after[f.Name] != before[f.Name] {
					t.Errorf("an update addressed to an unknown token changed the seeded row's %s", f.Name)
				}
			}
		})
	}
}

// A field on a NOT NULL column cannot be cleared, and the refusal must be a REFUSAL
// rather than a silent no-op. Driven off the same declaration as everything else: a
// family that calls a field required is asserting the API agrees.
//
// 🔴 THE DANGEROUS CASE IS THE ONE THAT LOOKS HARMLESS. For a reference, folding a null
// to the zero value would write a dangling FK and probably fail somewhere downstream. For
// a required BOOLEAN it writes `false` — a legal value, indistinguishable from a
// deliberate one — so `enabled: null` would disable a credential or park a rule, report
// success, and leave nothing anywhere to say what happened. That is why this property
// covers every required field rather than only the references it started with.
func aRequiredFieldRefusesAnExplicitNull[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		for _, f := range fam.Fields {
			if f.Kind == Clearable {
				continue
			}
			t.Run(fam.Name+"/"+f.Name, func(t *testing.T) {
				api := s.NewApi(t, fam.Migrate...)
				ctx := s.Context()
				fam.Seed(t, api, ctx)

				req := fam.NewRequest()
				f.SetNull(req)
				if err := fam.Update(api, ctx, fam.Token, req); err == nil {
					t.Fatalf("%s accepted an explicit null, which can only mean a dangling zero "+
						"FK or the request being quietly ignored", f.Name)
				}
				// Total refusal: nothing was written.
				got := fam.Read(t, api, ctx)
				for _, other := range fam.Fields {
					if got[other.Name] != other.Seeded {
						t.Errorf("a refused update still wrote %s = %q", other.Name, got[other.Name])
					}
				}
			})
		}
	}
}

// An unknown reference token refuses the WHOLE update. Applying the other fields and then
// failing on the reference would be the worst of both designs — a caller who retries has
// already half-applied the first attempt.
func unknownReferenceRefusesTheWholeUpdate[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		for _, f := range fam.Fields {
			if f.Kind != RequiredRef {
				continue
			}
			t.Run(fam.Name+"/"+f.Name, func(t *testing.T) {
				api := s.NewApi(t, fam.Migrate...)
				ctx := s.Context()
				fam.Seed(t, api, ctx)

				req := fam.NewRequest()
				f.Set(req, "no-such-"+f.Name)
				// Name another field too, so "nothing was written" is an observation and not
				// a tautology about a request that asked for nothing else.
				for _, other := range fam.Fields {
					if other.Name != f.Name && other.Kind == Clearable {
						other.Set(req, other.Replace)
						break
					}
				}
				if err := fam.Update(api, ctx, fam.Token, req); err == nil {
					t.Fatal("an unknown reference token was accepted, leaving a dangling reference")
				}
				got := fam.Read(t, api, ctx)
				for _, other := range fam.Fields {
					if got[other.Name] != other.Seeded {
						t.Errorf("the refused update still applied %s = %q", other.Name, got[other.Name])
					}
				}
			})
		}
	}
}

// 🔴 THE EXHAUSTIVENESS CHECK, AND THE REASON IT IS NOT A HAND-WRITTEN LIST.
//
// Everything above is driven by the family's Fields table, so a field MISSING from that
// table is a field nothing here tests — and the update could go on writing it from a zero
// value with the whole suite green. The check inside SeedPopulatesEveryFieldDistinctly is
// the wrong DIRECTION for this: it asserts Read() ⊆ declared, so dropping a field from
// BOTH the table and Read() satisfies it. That is a hand-written list cross-checked
// against a second hand-written list — the same guard twice, with two chances to be wrong
// in the same way.
//
// This asks the TYPE instead. Every exported field of the *UpdateRequest is a field a
// caller can send and the resolver must fold, so every one has to be declared; and a field
// ADDED to the request later fails here on the day it is added rather than the day someone
// notices it being erased.
func everyRequestFieldIsDeclared[A any](t *testing.T, s Suite[A]) {
	for _, fam := range s.Families {
		t.Run(fam.Name, func(t *testing.T) {
			rt := reflect.TypeOf(fam.NewRequest())
			if rt.Kind() != reflect.Ptr || rt.Elem().Kind() != reflect.Struct {
				t.Fatalf("NewRequest() returned %s, want a pointer to a struct", rt)
			}
			rt = rt.Elem()

			declared := map[string]bool{}
			for _, f := range fam.Fields {
				declared[strings.ToLower(f.Name)] = true
			}
			for i := 0; i < rt.NumField(); i++ {
				sf := rt.Field(i)
				if sf.PkgPath != "" {
					continue // unexported: not reachable from the wire
				}
				if !declared[strings.ToLower(sf.Name)] {
					t.Errorf("%s.%s is a field callers can send, but no Field declares it — "+
						"nothing in this harness would notice it being written from a zero "+
						"value on every update", rt.Name(), sf.Name)
				}
			}
			// The other direction, so the map cannot be satisfied by a table declaring
			// fields the request does not have (a rename leaving a stale row behind).
			if n := rt.NumField(); n != len(fam.Fields) {
				t.Errorf("%s has %d exported fields but the family declares %d — the two lists "+
					"have diverged", rt.Name(), n, len(fam.Fields))
			}
		})
	}
}

// 🔴 THE FIXTURE MUST BE NO MORE PERMISSIVE THAN PRODUCTION, and this is what says so.
//
// NewSQLiteDB registers the token-grammar callback because production does. It used not
// to, and that is not a tidiness point: a harness weaker than the world it certifies
// passes things the real system refuses, so a defect can be green here and broken live.
// It let a whitespace-only profile token through.
//
// Without this the registration is UNOBSERVED — a mutant deleting the call survived an
// entire service's suite, because every fixture token in it is already grammar-conforming
// and nothing ever asked the callback to do anything. Asking it directly is the only way
// the fixture's strictness is pinned rather than assumed.
func fixtureIsAsStrictAsProduction[A any](t *testing.T, s Suite[A]) {
	for _, bad := range []string{"not a token", "-leading-hyphen", "trailing space "} {
		t.Run(bad, func(t *testing.T) {
			api := s.NewApi(t, s.StrictnessTables...)
			if err := s.CreateWithToken(t, api, s.Context(), bad); err == nil {
				t.Fatalf("the fixture accepted the token %q, which production refuses — every "+
					"assertion in this harness is then being made against a weaker world than "+
					"the one it certifies", bad)
			}
		})
	}

	// The counterweight: strictness bought by refusing everything would be worthless, and
	// would make every other property fail for the wrong reason.
	t.Run("and still accepts a valid one", func(t *testing.T) {
		api := s.NewApi(t, s.StrictnessTables...)
		if err := s.CreateWithToken(t, api, s.Context(), s.ValidToken); err != nil {
			t.Fatalf("the fixture refused the grammar-conforming token %q: %v", s.ValidToken, err)
		}
	})
}

// ─── shared helpers ────────────────────────────────────────────────────────

func assertOthersHoldSeeded[A any](t *testing.T, fam Family[A], got map[string]string,
	touched string, msg string) {
	t.Helper()
	partner := ""
	for _, f := range fam.Fields {
		if f.Name == touched {
			partner = f.Partner
		}
	}
	for _, other := range fam.Fields {
		if other.Name == touched || (partner != "" && other.Name == partner) {
			continue
		}
		if got[other.Name] != other.Seeded {
			t.Errorf(msg, other.Name, got[other.Name], other.Seeded)
		}
	}
}

// partnerOf resolves a field's declared partner, FAILING when it names nothing. A partner
// that has been renamed away would otherwise silently become "no partner", and the pair
// would go back to being driven one at a time — the exact state the partner mechanism
// exists to prevent, arriving through the mechanism itself.
func partnerOf[A any](t *testing.T, fam Family[A], f Field) *Field {
	t.Helper()
	if f.Partner == "" {
		return nil
	}
	for i := range fam.Fields {
		if fam.Fields[i].Name == f.Partner {
			if fam.Fields[i].Partner != f.Name {
				t.Fatalf("%s names %s as its partner, but %s does not name it back — a pairing "+
					"only one side knows about is driven one-way", f.Name, f.Partner, f.Partner)
			}
			return &fam.Fields[i]
		}
	}
	t.Fatalf("%s names %q as its partner, but the family declares no such field", f.Name, f.Partner)
	return nil
}

func declaresField[A any](fam Family[A], name string) bool {
	for _, f := range fam.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}
