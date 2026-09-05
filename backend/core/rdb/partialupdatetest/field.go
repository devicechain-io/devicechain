// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"database/sql"
	"encoding/json"
	"strconv"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

// FieldKind says which of the three property sets a field is subject to. It replaces a
// `nullable bool`, and the split it adds is not cosmetic: a boolean flag conflated "an
// explicit null is refused" with "this is a reference that can name something unknown",
// so a NOT NULL column that is not a reference had nowhere to go. Declaring one as
// nullable made the harness assert it could be CLEARED — which for a required vocabulary
// column is the opposite of the rule — and declaring it as a reference made the harness
// send it the literal string "no-such-<field>" and expect a lookup failure.
//
// 🔴 A FIELD DECLARED IN THE WRONG KIND FAILS RATHER THAN BEING SKIPPED, which is what
// makes the kind worth declaring at all: every kind is covered by at least one property
// that a mis-declaration breaks.
type FieldKind int

const (
	// Clearable: a nullable column. Absent keeps, null clears, a value sets.
	Clearable FieldKind = iota
	// RequiredValue: a NOT NULL column that is not a reference — a vocabulary string, a
	// required key, a flag. Absent keeps, a value sets, NULL IS REFUSED, because the zero
	// value it would otherwise be folded to is a legal value nothing downstream could
	// tell from a deliberate one.
	RequiredValue
	// RequiredRef: a reference on a NOT NULL FK. Everything RequiredValue has, plus: an
	// unknown token refuses the WHOLE update, writing nothing.
	RequiredRef
)

// Field is one field of one family, described well enough for the harness to seed it,
// read it back, set it and clear it without knowing its type.
type Field struct {
	// Name is the field as the SCHEMA spells it, so a failure names something a caller
	// can act on.
	Name string
	// Seeded is the value the family's seed writes; Read must return exactly this
	// immediately after seeding.
	Seeded string
	// Replace is a DIFFERENT value, used for the "sent with a value" state.
	Replace string
	// Cleared is what Read returns after an explicit null. Meaningful only for Clearable
	// — the other kinds refuse the null rather than reaching a reading.
	Cleared string
	// Set puts this field into the "sent with a value" state on a fresh request. The
	// value is always a string; a typed constructor parses it, so one registry can hold
	// booleans, ints, floats and lists alongside strings.
	Set func(req any, v string)
	// SetNull puts the field into the "sent as null" state. Always present — the refusal
	// properties need to build that state too.
	SetNull func(req any)
	// SetEmpty puts a LIST field into the "sent as []" state, and is nil for every other
	// kind. It exists because [] is a fourth wire state that folds onto the same stored
	// value as null (see dcgraphql.OptionalStringList), and a claim two spellings mean
	// one thing is worth nothing until both are driven.
	SetEmpty func(req any)
	// Kind selects which properties apply. See FieldKind.
	Kind FieldKind
	// Partner names the field this one CANNOT MOVE WITHOUT, or "" for the usual case.
	//
	// 🔴 IT IS A THIRD INPUT CLASS, NOT A CONVENIENCE. A detection rule's group scope is
	// two columns validated as a pair: naming a token without a version, or a version
	// without a token, is a half-set scope and is REFUSED. Driving such a field one at a
	// time — which is what every property here does — would therefore assert that a legal
	// request fails, and the only way to make the suite green would be to stop driving
	// the field at all. So the harness moves the pair together, and "every OTHER field is
	// unchanged" excludes the partner rather than pretending it did not move.
	Partner string
}

// PairedWith declares the two-way partnership. It is a function rather than a field set
// by hand so the two halves cannot be written asymmetrically in the first place.
func PairedWith(a, b Field) (Field, Field) {
	a.Partner = b.Name
	b.Partner = a.Name
	return a, b
}

// NullMarker distinguishes a cleared column from one holding the empty string. They are
// different rows, and a test that cannot tell them apart is not testing clearing.
const NullMarker = "<null>"

// NullString renders a nullable string column for the Read contract.
func NullString(v sql.NullString) string {
	if !v.Valid {
		return NullMarker
	}
	return v.String
}

// JSONString renders a nullable JSON column for the Read contract.
func JSONString(v *datatypes.JSON) string {
	if v == nil {
		return NullMarker
	}
	return string(*v)
}

// ─── field constructors ────────────────────────────────────────────────────

// OptionalStringField describes a nullable string column: settable, clearable, reading
// back as NullMarker once cleared. The set/clear closures take the request as `any` and a
// typed accessor, which is what lets one registry hold every family — and what makes a
// field renamed on the request a COMPILE ERROR in the family declaration rather than a
// silently skipped test.
func OptionalStringField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) Field {
	return Field{
		Name: name, Seeded: seeded, Replace: replace, Cleared: NullMarker,
		Kind:    Clearable,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// RequiredRefField describes a reference on a NOT NULL column: settable, a null on it is
// REFUSED rather than honoured, and an unknown token refuses the whole update.
func RequiredRefField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) Field {
	return Field{
		Name: name, Seeded: seeded, Replace: replace, Kind: RequiredRef,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// RequiredStringField describes a NOT NULL string column that is NOT a reference — a
// vocabulary value, a required key. Settable, unclearable, and no lookup to fail.
func RequiredStringField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) Field {
	return Field{
		Name: name, Seeded: seeded, Replace: replace, Kind: RequiredValue,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// RequiredBoolField describes a NOT NULL boolean column. The harness's uniform value
// representation is a string, so the seeded/replace readings are "true"/"false" and the
// setter parses — which is what lets one registry hold booleans beside strings without a
// second copy of every property.
//
// 🔴 It is deliberately NOT clearable. Folding a null to false here is the quietest
// possible data loss: false is a value a caller could legitimately have sent.
func RequiredBoolField[R any](name string, seeded bool,
	pick func(*R) *dcgraphql.OptionalBool) Field {
	return Field{
		Name: name, Seeded: BoolString(seeded), Replace: BoolString(!seeded), Kind: RequiredValue,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalBoolOf(v == "true") },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedBool() },
	}
}

// OptionalFloat64Field describes a nullable Float column.
func OptionalFloat64Field[R any](name string, seeded, replace float64,
	pick func(*R) *dcgraphql.OptionalFloat64) Field {
	return Field{
		Name: name, Seeded: FloatString(seeded), Replace: FloatString(replace), Cleared: NullMarker,
		Kind:    Clearable,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalFloat64Of(mustFloat(v)) },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedFloat64() },
	}
}

// OptionalInt32Field describes a nullable Int column.
func OptionalInt32Field[R any](name string, seeded, replace int32,
	pick func(*R) *dcgraphql.OptionalInt32) Field {
	return Field{
		Name: name, Seeded: IntString(seeded), Replace: IntString(replace), Cleared: NullMarker,
		Kind:    Clearable,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalInt32Of(mustInt32(v)) },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedInt32() },
	}
}

// OptionalStringListField describes a `[String!]` column — a set of authorities, redirect
// URIs, scopes.
//
// 🔴 IT IS DECLARED Clearable, AND ITS CLEARED READING IS "[]", NOT NullMarker. A list
// has no third stored outcome: null and [] are the same request spelled two ways, and
// both leave an EMPTY list rather than an absent one. Declaring it any other way would
// make the harness assert something the fold does not do — see
// dcgraphql.OptionalStringList, where the collapse is the decision.
//
// The extra state, [], is driven by its own property (EmptyListIsTheSameAsANull) off
// SetEmpty, because the claim that the two spellings agree is worth nothing until both
// are sent.
//
// 🔴 CLEARED IS FIXED AT "[]", WHICH CONSTRAINS THE FAMILY'S Read AND IS WORTH KNOWING
// BEFORE YOU WRITE ONE. A family whose column stores NULL for "emptied" — a nullable JSON
// or text column rather than a join table — must render that NULL as "[]" here, not as
// NullMarker, or ClearingOneFieldClearsOnlyIt fails on a reading that is actually correct.
// That does collapse the NULL/"[]" distinction RenderStringList otherwise keeps, and the
// collapse is the right one for this datatype: null and [] are the same request on the
// wire (see dcgraphql.OptionalStringList) so they are the same state in the column, and a
// harness that insisted on telling them apart would be asserting a difference the API has
// no way to express. What must still never collapse is "[]" against NullMarker for a
// column that is genuinely ABSENT, and against "" for one holding the empty string.
//
// A field seeded with an EMPTY list panics here rather than failing later: "preserved"
// and "never set" would be the same observation, which is the vacuity the whole harness
// is built to refuse, and the generic anti-vacuity control cannot see it — RenderStringList
// of an empty list is "[]", which is neither blank nor NullMarker.
//
// An empty REPLACEMENT panics for the neighbouring reason. It renders "[]", which is
// exactly the cleared reading, so "the update SET this list" and "the update EMPTIED it"
// become one observation — and the survivor that lets through is a real fold: an ApplyTo
// returning []string{} whenever Set is true, ignoring every non-empty list, passes
// SettingOneField, ClearingOneField AND EmptyList for that field. The generic
// Replace != Cleared check in the harness catches the same shape for every kind; this
// panic names it at the declaration, where the fix is.
func OptionalStringListField[R any](name string, seeded, replace []string,
	pick func(*R) *dcgraphql.OptionalStringList) Field {
	if len(seeded) == 0 {
		panic("partialupdatetest: list field " + name + " is seeded empty, so \"the update " +
			"preserved it\" and \"it was never set\" are the same observation")
	}
	if len(replace) == 0 {
		panic("partialupdatetest: list field " + name + " has an empty replacement, which " +
			"renders the same as the cleared reading, so \"the update set this list\" and " +
			"\"the update emptied it\" are the same observation")
	}
	return Field{
		Name: name, Seeded: RenderStringList(seeded), Replace: RenderStringList(replace),
		Cleared: RenderStringList(nil),
		Kind:    Clearable,
		Set: func(req any, v string) {
			*pick(req.(*R)) = dcgraphql.OptionalStringListOf(ParseStringList(v))
		},
		SetNull:  func(req any) { *pick(req.(*R)) = dcgraphql.ClearedStringList() },
		SetEmpty: func(req any) { *pick(req.(*R)) = dcgraphql.OptionalStringListOf([]string{}) },
	}
}

// ─── the string representations the harness compares ───────────────────────
//
// They are shared by the field constructors and by the families' Read closures, so a
// family cannot declare a seeded float as "1.5" while its read reports "1.50" — a
// difference that would fail the anti-vacuity control for a reason that has nothing to do
// with the update.

func BoolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func FloatString(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
func IntString(v int32) string     { return strconv.FormatInt(int64(v), 10) }

// RenderStringList is how a list column reads back into the harness's map[string]string.
//
// 🔴 IT IS JSON, AND THE REASON IS THE EMPTY CASE. The obvious rendering — join the
// entries on a separator and wrap in brackets — makes an EMPTY LIST and a ONE-ENTRY LIST
// HOLDING "" both read "[]". That is not a corner: "the caller emptied this" is a state
// this fold produces on purpose, so a rendering that cannot tell it from a list with one
// blank entry would report a difference as a match. JSON separates them ("[]" versus
// [""]) and stays unambiguous whatever the entries contain, including the commas that
// scopes and authorities routinely carry.
//
// The three readings a list column has to keep distinct are "[]" (emptied), "" (a column
// holding the empty string) and NullMarker (a column holding NULL). None of them collide
// under this rendering.
func RenderStringList(v []string) string {
	if v == nil {
		v = []string{}
	}
	out, err := json.Marshal(v)
	if err != nil {
		panic("partialupdatetest: rendering a string list: " + err.Error())
	}
	return string(out)
}

// ParseStringList is RenderStringList's inverse, used by the list field's Set closure —
// the harness carries every value as a string, so the round trip has to exist. It panics
// on anything RenderStringList did not produce, because the only caller is the harness
// itself and a silently-empty parse would mean the "sent with a value" state was actually
// sending nothing.
func ParseStringList(v string) []string {
	out := []string{}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		panic("partialupdatetest: " + strconv.Quote(v) + " is not a rendered string list: " + err.Error())
	}
	return out
}

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
