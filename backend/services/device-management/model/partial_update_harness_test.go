// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"strings"
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
//  5. the row is addressed by the token ARGUMENT — an argument naming nothing is a
//     not-found, and it does not fall back to some other row.
//  6. a null on a field the family declares REQUIRED is refused, totally — including a
//     required BOOLEAN, where the zero value a fold would write is legal and therefore
//     invisible to everything downstream.
//  7. an unknown reference token refuses the WHOLE update, leaving nothing written.
//
// Property 3 runs over exactly the fields a family declares clearable, property 6 over
// exactly the ones it declares required, and property 7 over the required REFERENCES —
// so every field is covered by 3 or 6, and a family that declares a field in the wrong
// kind FAILS rather than falling through the gap between them.
//
// # What the harness deliberately does NOT cover
//
// The WIRE shape — that `token` is not a member of the update input, and that
// `request` is non-null — is only observable against the real schema, and lives in
// graphql/partial_update_wire_test.go. That the three states survive the packer at
// all is proved once, generically, in core's optional_test.go.

// ─── the family description ────────────────────────────────────────────────

// fieldKind says which of the three property sets a field is subject to. It replaces
// a `nullable bool`, and the split it adds is not cosmetic: a boolean flag conflated
// "an explicit null is refused" with "this is a reference that can name something
// unknown", so a NOT NULL column that is not a reference had nowhere to go. Declaring
// one as nullable made the harness assert it could be CLEARED — which for a required
// vocabulary column is the opposite of the rule — and declaring it as a reference made
// the harness send it the literal string "no-such-<field>" and expect a lookup failure.
//
// 🔴 A FIELD DECLARED IN THE WRONG KIND FAILS RATHER THAN BEING SKIPPED, which is what
// makes the kind worth declaring at all: every kind is covered by at least one property
// that a mis-declaration breaks.
type fieldKind int

const (
	// fieldClearable: a nullable column. Absent keeps, null clears, a value sets.
	fieldClearable fieldKind = iota
	// fieldRequiredValue: a NOT NULL column that is not a reference — a vocabulary
	// string, a required key, a flag. Absent keeps, a value sets, NULL IS REFUSED,
	// because the zero value it would otherwise be folded to is a legal value nothing
	// downstream could tell from a deliberate one.
	fieldRequiredValue
	// fieldRequiredRef: a reference on a NOT NULL FK. Everything fieldRequiredValue
	// has, plus: an unknown token refuses the WHOLE update, writing nothing.
	fieldRequiredRef
)

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
	// cleared is what read returns after an explicit null. Meaningful only for
	// fieldClearable — the other kinds refuse the null rather than reaching a reading.
	cleared string
	// set puts this field into the "sent with a value" state on a fresh request. The
	// value is always a string; a typed constructor parses it, so one registry can
	// hold booleans, ints and floats alongside strings.
	set func(req any, v string)
	// setNull puts the field into the "sent as null" state. Always present — the
	// refusal properties need to build that state too.
	setNull func(req any)
	// kind selects which properties apply. See fieldKind.
	kind fieldKind
	// partner names the field this one CANNOT MOVE WITHOUT, or "" for the usual case.
	//
	// 🔴 IT IS A THIRD INPUT CLASS, NOT A CONVENIENCE. A detection rule's group scope is
	// two columns validated as a pair: naming a token without a version, or a version
	// without a token, is a half-set scope and is REFUSED. Driving such a field one at a
	// time — which is what every property here does — would therefore assert that a legal
	// request fails, and the only way to make the suite green would be to stop driving the
	// field at all. So the harness moves the pair together, and "every OTHER field is
	// unchanged" excludes the partner rather than pretending it did not move.
	partner string
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
//
// 🔴 THE DSN IS A NAMED SHARED-CACHE DATABASE, NOT ":memory:", and the difference is not
// cosmetic. A bare ":memory:" gives every pooled connection its OWN database, so a family
// whose seed opens a transaction — publishing an entity group, minting a fence-set version
// — writes into one database and reads back from another, and the failure reads as a
// missing table rather than as the connection-per-database it is. Naming it after the test
// keeps the isolation that ":memory:" was chosen for.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+partialUpdateDSNName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	// 🔴 The token grammar too, so this fixture is not MORE PERMISSIVE than production.
	// It was: without this callback the harness accepted tokens the live callback
	// refuses, which is the shape that lets a defect through green — a check run
	// against a weaker world than the one it certifies. It is also why the fixture
	// tokens below are grammar-conforming rather than arbitrary strings.
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
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
				if f.kind == fieldClearable && f.cleared == f.seeded {
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
				partner := partnerOf(t, fam, f)
				if partner != nil {
					partner.set(req, partner.replace)
				}
				if err := fam.update(api, ctx, fam.token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.read(t, api, ctx)
				if got[f.name] != f.replace {
					t.Fatalf("%s = %q, want %q — the field the caller SENT was not written",
						f.name, got[f.name], f.replace)
				}
				if partner != nil && got[partner.name] != partner.replace {
					t.Fatalf("%s = %q, want %q — the partner the request moved with %s was not written",
						partner.name, got[partner.name], partner.replace, f.name)
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
			if f.kind != fieldClearable {
				continue
			}
			t.Run(fam.name+"/"+f.name, func(t *testing.T) {
				api := newPartialUpdateApi(t, fam.migrate...)
				ctx := partialUpdateCtx()
				fam.seed(t, api, ctx)

				req := fam.newRequest()
				f.setNull(req)
				partner := partnerOf(t, fam, f)
				if partner != nil {
					partner.setNull(req)
				}
				if err := fam.update(api, ctx, fam.token, req); err != nil {
					t.Fatalf("update: %v", err)
				}

				got := fam.read(t, api, ctx)
				if got[f.name] != f.cleared {
					t.Fatalf("%s = %q after an explicit null, want %q — a field that cannot be "+
						"cleared is a field that can never be corrected", f.name, got[f.name], f.cleared)
				}
				if partner != nil && got[partner.name] != partner.cleared {
					t.Fatalf("%s = %q after an explicit null, want %q — the partner cleared with %s "+
						"did not clear", partner.name, got[partner.name], partner.cleared, f.name)
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

// A field on a NOT NULL column cannot be cleared, and the refusal must be a REFUSAL
// rather than a silent no-op. Driven off the same declaration as everything else: a
// family that calls a field required is asserting the API agrees.
//
// 🔴 THE DANGEROUS CASE IS THE ONE THAT LOOKS HARMLESS. For a reference, folding a null
// to the zero value would write a dangling FK and probably fail somewhere downstream.
// For a required BOOLEAN it writes `false` — a legal value, indistinguishable from a
// deliberate one — so `enabled: null` would disable a credential or park a rule, report
// success, and leave nothing anywhere to say what happened. That is why this property
// covers every required field rather than only the references it started with.
func TestPartialUpdate_ARequiredFieldRefusesAnExplicitNull(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		for _, f := range fam.fields {
			if f.kind == fieldClearable {
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
			if f.kind != fieldRequiredRef {
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
					if other.name != f.name && other.kind == fieldClearable {
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
	partner := ""
	for _, f := range fam.fields {
		if f.name == touched {
			partner = f.partner
		}
	}
	for _, other := range fam.fields {
		if other.name == touched || (partner != "" && other.name == partner) {
			continue
		}
		if got[other.name] != other.seeded {
			t.Errorf(msg, other.name, got[other.name], other.seeded)
		}
	}
}

// partnerOf resolves a field's declared partner, FAILING when it names nothing. A
// partner that has been renamed away would otherwise silently become "no partner", and
// the pair would go back to being driven one at a time — the exact state the partner
// mechanism exists to prevent, arriving through the mechanism itself.
func partnerOf(t *testing.T, fam partialUpdateFamily, f partialField) *partialField {
	t.Helper()
	if f.partner == "" {
		return nil
	}
	for i := range fam.fields {
		if fam.fields[i].name == f.partner {
			if fam.fields[i].partner != f.name {
				t.Fatalf("%s names %s as its partner, but %s does not name it back — a pairing "+
					"only one side knows about is driven one-way", f.name, f.partner, f.partner)
			}
			return &fam.fields[i]
		}
	}
	t.Fatalf("%s names %q as its partner, but the family declares no such field", f.name, f.partner)
	return nil
}

// pairedWith declares the two-way partnership. It is a function rather than a field set
// by hand so the two halves cannot be written asymmetrically in the first place.
func pairedWith(a, b partialField) (partialField, partialField) {
	a.partner = b.name
	b.partner = a.name
	return a, b
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
		kind:    fieldClearable,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// requiredRefField describes a reference on a NOT NULL column: settable, a null on it
// is REFUSED rather than honoured, and an unknown token refuses the whole update.
func requiredRefField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) partialField {
	return partialField{
		name: name, seeded: seeded, replace: replace, kind: fieldRequiredRef,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// requiredStringField describes a NOT NULL string column that is NOT a reference — a
// vocabulary value, a required key. Settable, unclearable, and no lookup to fail.
func requiredStringField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) partialField {
	return partialField{
		name: name, seeded: seeded, replace: replace, kind: fieldRequiredValue,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// requiredBoolField describes a NOT NULL boolean column. The harness's uniform value
// representation is a string, so the seeded/replace readings are "true"/"false" and the
// setter parses — which is what lets one registry hold booleans beside strings without a
// second copy of every property.
//
// 🔴 It is deliberately NOT clearable. Folding a null to false here is the quietest
// possible data loss: false is a value a caller could legitimately have sent.
func requiredBoolField[R any](name string, seeded bool,
	pick func(*R) *dcgraphql.OptionalBool) partialField {
	return partialField{
		name: name, seeded: boolStr(seeded), replace: boolStr(!seeded), kind: fieldRequiredValue,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalBoolOf(v == "true") },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedBool() },
	}
}

// optionalFloat64Field describes a nullable Float column.
func optionalFloat64Field[R any](name string, seeded, replace float64,
	pick func(*R) *dcgraphql.OptionalFloat64) partialField {
	return partialField{
		name: name, seeded: floatStr(seeded), replace: floatStr(replace), cleared: nullMarker,
		kind:    fieldClearable,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalFloat64Of(mustFloat(v)) },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedFloat64() },
	}
}

// optionalInt32Field describes a nullable Int column.
func optionalInt32Field[R any](name string, seeded, replace int32,
	pick func(*R) *dcgraphql.OptionalInt32) partialField {
	return partialField{
		name: name, seeded: intStr(seeded), replace: intStr(replace), cleared: nullMarker,
		kind:    fieldClearable,
		set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalInt32Of(mustInt32(v)) },
		setNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedInt32() },
	}
}

// The string representations the harness compares. They are shared by the field
// constructors and by the families' read() closures, so a family cannot declare a
// seeded float as "1.5" while its read reports "1.50" — a difference that would fail
// the anti-vacuity control for a reason that has nothing to do with the update.
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func floatStr(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
func intStr(v int32) string     { return strconv.FormatInt(int64(v), 10) }

func mustFloat(v string) float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		panic("the harness declared a non-numeric Float value: " + v)
	}
	return f
}

func mustInt32(v string) int32 {
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		panic("the harness declared a non-integral Int value: " + v)
	}
	return int32(n)
}

// 🔴 THE EXHAUSTIVENESS CHECK, AND THE REASON IT IS NOT A HAND-WRITTEN LIST.
//
// Everything above is driven by the family's `fields` table, so a field MISSING from
// that table is a field nothing here tests — and the update could go on writing it
// from a zero value with the whole suite green. The check inside
// SeedPopulatesEveryFieldDistinctly is the wrong DIRECTION for this: it asserts
// read() ⊆ declared, so dropping a field from BOTH the table and read() satisfies it.
// That is a hand-written list cross-checked against a second hand-written list —
// the same guard twice, with two chances to be wrong in the same way.
//
// This asks the TYPE instead. Every exported field of the *UpdateRequest is a field a
// caller can send and the resolver must fold, so every one has to be declared; and a
// field ADDED to the request later fails here on the day it is added rather than the
// day someone notices it being erased.
func TestPartialUpdate_EveryRequestFieldIsDeclared(t *testing.T) {
	for _, fam := range partialUpdateFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			rt := reflect.TypeOf(fam.newRequest())
			if rt.Kind() != reflect.Ptr || rt.Elem().Kind() != reflect.Struct {
				t.Fatalf("newRequest() returned %s, want a pointer to a struct", rt)
			}
			rt = rt.Elem()

			declared := map[string]bool{}
			for _, f := range fam.fields {
				declared[strings.ToLower(f.name)] = true
			}
			for i := 0; i < rt.NumField(); i++ {
				sf := rt.Field(i)
				if sf.PkgPath != "" {
					continue // unexported: not reachable from the wire
				}
				if !declared[strings.ToLower(sf.Name)] {
					t.Errorf("%s.%s is a field callers can send, but no partialField declares "+
						"it — nothing in this harness would notice it being written from a zero "+
						"value on every update", rt.Name(), sf.Name)
				}
			}
			// The other direction, so the map cannot be satisfied by a table declaring
			// fields the request does not have (a rename leaving a stale row behind).
			if n := rt.NumField(); n != len(fam.fields) {
				t.Errorf("%s has %d exported fields but the family declares %d — the two lists "+
					"have diverged", rt.Name(), n, len(fam.fields))
			}
		})
	}
}

// 🔴 THE FIXTURE MUST BE NO MORE PERMISSIVE THAN PRODUCTION, and this is what says so.
//
// newPartialUpdateApi registers the token-grammar callback because production does.
// It used not to, and that is not a tidiness point: a harness weaker than the world
// it certifies passes things the real system refuses, so a defect can be green here
// and broken live. It let a whitespace-only profile token through.
//
// Without this test the registration is UNOBSERVED — a mutant deleting the call
// survived the entire suite, because every fixture token in this package is already
// grammar-conforming and nothing ever asked the callback to do anything. Asking it
// directly is the only way the fixture's strictness is pinned rather than assumed.
func TestPartialUpdateFixtureIsAsStrictAsProduction(t *testing.T) {
	api := newPartialUpdateApi(t, &EntityGroup{})
	ctx := partialUpdateCtx()

	for _, bad := range []string{"not a token", "-leading-hyphen", "trailing space "} {
		t.Run(bad, func(t *testing.T) {
			if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
				Token: bad, MemberType: "device",
			}); err == nil {
				t.Fatalf("the fixture accepted the token %q, which production refuses — every "+
					"assertion in this file is then being made against a weaker world than the "+
					"one it certifies", bad)
			}
		})
	}

	// The counterweight: strictness bought by refusing everything would be worthless,
	// and would make every other test in this file fail for the wrong reason.
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "a-valid_token1", MemberType: "device",
	}); err != nil {
		t.Fatalf("the fixture refused a grammar-conforming token: %v", err)
	}
}

// partialUpdateDSNName turns a test's name into something safe to sit in a SQLite URI.
// Subtest names carry "/" and the harness's carry "#" once the same name repeats, both of
// which a URI filename reads as structure rather than as a name.
func partialUpdateDSNName(t *testing.T) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, t.Name())
}
