// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// THE PARTIAL-UPDATE HARNESS.
//
// What a partial update means once it reaches storage, asserted the same way for
// every converted family. These drive the real Api against a real database, so the
// assertions are about rows, not about structs. The families themselves are
// declared in partial_update_families_test.go.
//
// # Why a harness and not one test file per family
//
// This started as api_device_type_partial_update_test.go — a good test, but a
// SHAPE. Copying that shape per family is how a suite drifts: the seventh copy
// tests fewer fields than the first, and nobody notices, because each copy passes.
// Worse, the original only exercised two fields BY NAME (name, icon) and asserted
// the rest were preserved. That catches a total full-replace, but not a conversion
// that forgot ONE field — send `icon`, have `manufacturer` written from a zero
// value, and every assertion in it still passes.
//
// So the family is DATA and the assertions are generic. Each family declares its
// fields once; the harness drives EVERY field through all three states and checks
// EVERY other field after each one. Adding a family is adding a row to
// partialUpdateFamilies(), not writing a test.
//
// # The properties, and what each one alone would miss
//
//  1. seed populates every declared field, and the seeded/replacement/cleared
//     values are pairwise distinct. This is the anti-vacuity control and it runs
//     first, because without it the rest are worthless: against a fixture with a
//     blank field, "preserved" and "was never set" are the same observation, and a
//     full replace passes.
//  2. setting ONE field changes that field and NOTHING else.
//  3. clearing ONE field (explicit null) clears it and NOTHING else. This is the
//     half a naive "ignore empty values" implementation gets wrong — that shape
//     preserves everything, which looks correct until someone needs to remove a
//     value and finds the API cannot express it.
//  4. an update naming nothing changes nothing.
//  5. the row is addressed by the token ARGUMENT.
//
// # What the harness deliberately does NOT cover
//
// The WIRE shape — that `token` is not a member of the update input, and that
// `request` is non-null — is only observable against the real schema, and lives in
// graphql/partial_update_wire_test.go. That the three states survive the packer at
// all is proved once, generically, in core's optional_test.go.

// ─── the family description ────────────────────────────────────────────────

// partialField is one field of one family, described well enough for the harness
// to seed it, read it back, set it and clear it without knowing its type.
type partialField struct {
	// name is the field as the SCHEMA spells it, so a failure names something a
	// caller can act on.
	name string
	// seeded is the value the family's seed writes; read must return exactly this
	// immediately after seeding.
	seeded string
	// replace is a DIFFERENT value, used for the "sent with a value" state.
	replace string
	// cleared is what read returns after an explicit null.
	cleared string
	// set puts this field into the "sent with a value" state on a fresh request.
	set func(req any, v string)
	// setNull puts the field into the "sent as null" state. Always present — the
	// refusal properties need to build that state too.
	setNull func(req any)
	// nullable says whether that null is HONOURED (a nullable column, so it clears)
	// or REFUSED (a reference on a NOT NULL column). It selects which property the
	// harness applies, so a family that declares it wrongly FAILS rather than
	// quietly skipping the field.
	nullable bool
}

// partialUpdateFamily is one converted entity family.
type partialUpdateFamily struct {
	// name is the entity as the schema names it: "assetType", "device".
	name string
	// token is the token the seed uses and the harness addresses by.
	token string
	// migrate lists the tables this family needs. Per-family rather than migrating
	// everything, so a family that quietly grows a dependency has to say so.
	migrate []any
	// seed creates the entity with EVERY declared field populated to its seeded
	// value, plus whatever it depends on (its type, its profile).
	seed func(t *testing.T, api *Api, ctx context.Context)
	// read reads the entity back FROM THE DATABASE — never from the value an update
	// returned, since a resolver that mutated its in-memory copy and never persisted
	// would satisfy every assertion made against its return value.
	read func(t *testing.T, api *Api, ctx context.Context) map[string]string
	// newRequest builds an empty update request: every field in the ABSENT state.
	newRequest func() any
	// update calls the family's real Api update method with that request.
	update func(api *Api, ctx context.Context, token string, req any) error
	fields []partialField
}

// nullMarker distinguishes a cleared column from one holding the empty string. They
// are different rows, and a test that cannot tell them apart is not testing clearing.
const nullMarker = "<null>"

func nullStr(v sql.NullString) string {
	if !v.Valid {
		return nullMarker
	}
	return v.String
}

func jsonStr(v *datatypes.JSON) string {
	if v == nil {
		return nullMarker
	}
	return string(*v)
}

// newPartialUpdateApi builds a SQLite-backed Api migrated for one family.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(tables...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

func partialUpdateCtx() context.Context {
	return core.WithTenant(context.Background(), "acme")
}

// requireOne is the reload guard every family's read shares: exactly one row, or
// the test is measuring something other than what it seeded.
func requireOne[E any](t *testing.T, what string, rows []*E, err error) *E {
	t.Helper()
	if err != nil {
		t.Fatalf("reload %s: %v", what, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one %s, got %d", what, len(rows))
	}
	return rows[0]
}

// ─── the generic properties ────────────────────────────────────────────────

// THE ANTI-VACUITY CONTROL, and it runs first because the others depend on it.
//
// A family whose seed left a field blank makes "the update preserved it" and "it was
// never set" the same observation, and a full replace would pass every other test
// here. A family whose replacement value equalled its seeded value would make "the
// update wrote it" unobservable the same way. Both failures are silent — the suite
// goes green having covered nothing — so both are asserted rather than trusted.
func TestPartialUpdate_SeedPopulatesEveryFieldDistinctly(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, fam.migrate...)
			ctx := partialUpdateCtx()
			fam.seed(t, api, ctx)
			got := fam.read(t, api, ctx)

			if len(fam.fields) == 0 {
				t.Fatal("a family with no declared fields asserts nothing")
			}
			for _, f := range fam.fields {
				v, ok := got[f.name]
				if !ok {
					t.Errorf("%s: read() reports no value — a field the harness cannot "+
						"observe is a field it cannot protect", f.name)
					continue
				}
				if v != f.seeded {
					t.Errorf("%s: declared as seeded %q but reads back %q — the fixture does "+
						"not hold what the family says it holds", f.name, f.seeded, v)
				}
				if f.seeded == "" || f.seeded == nullMarker {
					t.Errorf("%s: seeded blank, so \"preserved\" and \"never set\" are the "+
						"same observation and a full replace would pass", f.name)
				}
				if f.replace == f.seeded {
					t.Errorf("%s: the replacement value equals the seeded one, so \"the update "+
						"wrote it\" is unobservable", f.name)
				}
				if f.nullable && f.cleared == f.seeded {
					t.Errorf("%s: the cleared reading equals the seeded one, so \"the update "+
						"cleared it\" is unobservable", f.name)
				}
			}
			// Every field read() reports must be declared, or a column added by a later
			// change goes untested while the suite stays green.
			for k := range got {
				if !declaresField(fam, k) {
					t.Errorf("read() reports %q but no partialField declares it", k)
				}
			}
		})
	}
}

// THE HEADLINE PROPERTY. Setting one field changes that field and nothing else.
//
// Under the full-replace shape this replaces, sending one field wiped every other
// one — successfully, returning 200 with the emptied entity. Every field is driven,
// not just a representative one, because a conversion that forgets a SINGLE field
// leaves exactly that field written from a zero value on every update.
func TestPartialUpdate_SettingOneFieldLeavesEveryOtherAlone(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		for _, f := range fam.fields {
			t.Run(fam.name+"/"+f.name, func(t *testing.T) {
				api := newPartialUpdateApi(t, fam.migrate...)
				ctx := partialUpdateCtx()
				fam.seed(t, api, ctx)

				req := fam.newRequest()
				f.set(req, f.replace)
				if err := fam.update(api, ctx, fam.token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.read(t, api, ctx)
				if got[f.name] != f.replace {
					t.Fatalf("%s = %q, want %q — the field the caller SENT was not written",
						f.name, got[f.name], f.replace)
				}
				assertOthersHoldSeeded(t, fam, got, f.name,
					"a partial update erased %s: got %q, want %q — the update is still a full replace")
			})
		}
	}
}

// THE SECOND STATE. An explicit null clears, and clears only what it names.
func TestPartialUpdate_ClearingOneFieldClearsOnlyIt(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		for _, f := range fam.fields {
			if !f.nullable {
				continue
			}
			t.Run(fam.name+"/"+f.name, func(t *testing.T) {
				api := newPartialUpdateApi(t, fam.migrate...)
				ctx := partialUpdateCtx()
				fam.seed(t, api, ctx)

				req := fam.newRequest()
				f.setNull(req)
				if err := fam.update(api, ctx, fam.token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.read(t, api, ctx)
				if got[f.name] != f.cleared {
					t.Fatalf("%s = %q after an explicit null, want %q — a field that cannot be "+
						"cleared is a field that can never be corrected", f.name, got[f.name], f.cleared)
				}
				assertOthersHoldSeeded(t, fam, got, f.name,
					"clearing one field also changed %s: got %q, want %q")
			})
		}
	}
}

// THE CONTROL FOR THE WHOLE SEMANTIC. An update that names nothing changes nothing.
// If this fails, some field is being written from a zero value rather than from the
// stored one, and every partial update is silently erasing it.
func TestPartialUpdate_EmptyRequestChangesNothing(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, fam.migrate...)
			ctx := partialUpdateCtx()
			fam.seed(t, api, ctx)
			before := fam.read(t, api, ctx)

			if err := fam.update(api, ctx, fam.token, fam.newRequest()); err != nil {
				t.Fatalf("update: %v", err)
			}

			after := fam.read(t, api, ctx)
			for _, f := range fam.fields {
				if after[f.name] != before[f.name] {
					t.Errorf("an empty update changed %s from %q to %q",
						f.name, before[f.name], after[f.name])
				}
			}
		})
	}
}

// The row is addressed by the token ARGUMENT, so an argument naming nothing is a
// not-found — not a silent create, and not an update of some other row named in the
// payload. That last one is exactly what the pre-conversion code did for seven of
// these families: it located by request.Token and ignored this argument entirely.
func TestPartialUpdate_UnknownTokenIsNotFound(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, fam.migrate...)
			ctx := partialUpdateCtx()
			fam.seed(t, api, ctx)
			before := fam.read(t, api, ctx)

			if err := fam.update(api, ctx, "no-such-token", fam.newRequest()); err == nil {
				t.Fatal("updating an unknown token succeeded")
			}
			// And it left the seeded row alone — the half that catches a lookup falling
			// back to "the only row there is".
			after := fam.read(t, api, ctx)
			for _, f := range fam.fields {
				if after[f.name] != before[f.name] {
					t.Errorf("an update addressed to an unknown token changed the seeded row's %s", f.name)
				}
			}
		})
	}
}

// A reference on a NOT NULL column cannot be cleared, and the refusal must be a
// REFUSAL rather than a silent no-op. Driven off the same declaration as everything
// else: a field with no clear closure is one the family says cannot be cleared, so
// this asserts the API agrees.
func TestPartialUpdate_RequiredReferenceRefusesAnExplicitNull(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		for _, f := range fam.fields {
			if f.nullable {
				continue
			}
			t.Run(fam.name+"/"+f.name, func(t *testing.T) {
				api := newPartialUpdateApi(t, fam.migrate...)
				ctx := partialUpdateCtx()
				fam.seed(t, api, ctx)

				req := fam.newRequest()
				f.setNull(req)
				if err := fam.update(api, ctx, fam.token, req); err == nil {
					t.Fatalf("%s accepted an explicit null, which can only mean a dangling zero "+
						"FK or the request being quietly ignored", f.name)
				}
				// Total refusal: nothing was written.
				got := fam.read(t, api, ctx)
				for _, other := range fam.fields {
					if got[other.name] != other.seeded {
						t.Errorf("a refused update still wrote %s = %q", other.name, got[other.name])
					}
				}
			})
		}
	}
}

// An unknown reference token refuses the WHOLE update. Applying the other fields
// and then failing on the reference would be the worst of both designs — a caller
// who retries has already half-applied the first attempt.
func TestPartialUpdate_UnknownReferenceRefusesTheWholeUpdate(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		for _, f := range fam.fields {
			if f.nullable {
				continue
			}
			t.Run(fam.name+"/"+f.name, func(t *testing.T) {
				api := newPartialUpdateApi(t, fam.migrate...)
				ctx := partialUpdateCtx()
				fam.seed(t, api, ctx)

				req := fam.newRequest()
				f.set(req, "no-such-"+f.name)
				// Name another field too, so "nothing was written" is an observation and
				// not a tautology about a request that asked for nothing else.
				for _, other := range fam.fields {
					if other.name != f.name && other.nullable {
						other.set(req, other.replace)
						break
					}
				}
				if err := fam.update(api, ctx, fam.token, req); err == nil {
					t.Fatal("an unknown reference token was accepted, leaving a dangling reference")
				}
				got := fam.read(t, api, ctx)
				for _, other := range fam.fields {
					if got[other.name] != other.seeded {
						t.Errorf("the refused update still applied %s = %q", other.name, got[other.name])
					}
				}
			})
		}
	}
}

// ─── shared helpers ────────────────────────────────────────────────────────

func assertOthersHoldSeeded(t *testing.T, fam partialUpdateFamily, got map[string]string,
	touched string, msg string) {
	t.Helper()
	for _, other := range fam.fields {
		if other.name == touched {
			continue
		}
		if got[other.name] != other.seeded {
			t.Errorf(msg, other.name, got[other.name], other.seeded)
		}
	}
}

func declaresField(fam partialUpdateFamily, name string) bool {
	for _, f := range fam.fields {
		if f.name == name {
			return true
		}
	}
	return false
}

// ─── field constructors ────────────────────────────────────────────────────

// optionalStringField describes a nullable string column: settable, clearable,
// reading back as nullMarker once cleared. The set/clear closures take the request
// as `any` and a typed accessor, which is what lets one registry hold every family.
func optionalStringField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) partialField {
	return partialField{
		name: name, seeded: seeded, replace: replace, cleared: nullMarker,
		set:      func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		setNull:  func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
		nullable: true,
	}
}

// requiredRefField describes a reference on a NOT NULL column: settable, and a null
// on it is REFUSED rather than honoured. nullable:false is the family saying so,
// which is what routes the field into the two refusal properties instead of the
// clearing one.
func requiredRefField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) partialField {
	return partialField{
		name: name, seeded: seeded, replace: replace,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}
