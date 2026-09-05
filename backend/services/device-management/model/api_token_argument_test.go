// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"reflect"
	"strings"
	"testing"
)

// THE TOKEN ARGUMENT NAMES THE ROW. Nothing else does.
//
// Every update mutation in this service declares `token: String!`, and that argument
// used to do three different things depending on which one you called:
//
//   - NINE of the seventeen ignored it outright and located the row by the token inside
//     the request payload. Sending a `token` argument naming one entity and a
//     `request.token` naming another silently updated the SECOND and returned it with a
//     200 — a mandatory argument that was pure decoration, which is worse than an absent
//     one, because a client that gets the argument right and the payload wrong has no way
//     to find out.
//   - SEVEN located by the argument and then ASSIGNED the payload token over it, so the
//     payload still moved the row — and an empty payload token, which the schema permits
//     (String! admits ""), blanked the entity's token entirely.
//   - ONE, updateGeoFence, reconciled the two and refused a mismatch.
//
// # 🔴 THE RECONCILING FAMILIES ARE GONE FROM THIS SERVICE, AND THE TESTS WENT WITH THEM
//
// The previous version of this file drove seven families through a reconcile rule
// (dcgraphql.ErrPayloadTokenDisagrees) and asserted each was WIRED to it. All seven have
// since been converted to a dedicated *UpdateRequest carrying no token at all, which
// makes the disagreement unrepresentable rather than refused — so there is nothing left
// in this service for that rule to govern, and the tests that drove it would now iterate
// an empty list and pass having asserted nothing.
//
// Deleting them rather than leaving the loop is deliberate: a table-driven test over an
// empty table is the most convincing kind of green there is. What replaces the claim is
// the guard below, which asks the TYPE rather than a list, so a family added tomorrow on
// the full-replace shape fails on the day it is added.
//
// The rule itself is still exercised exhaustively in core (graphql.TestErrPayloadTokenDisagrees,
// TestErrRenameTokenUnusable) and still governs the update mutations in
// dashboard-management and notification-management, which have not been converted.

// 🔴 THE EXHAUSTIVENESS CHECK, AND THE REASON IT IS NOT A LIST OF FAMILY NAMES.
//
// A hand-written roster of converted families is a second copy of the truth, and the
// failure it cannot see is the one that matters: a NEW update method, added on the old
// full-replace shape, appears in neither the roster nor anything that reads it. So this
// enumerates *Api's own Update* methods and asks three things of each.
//
// # 🔴 THE NAME IS NOT ONE OF THEM, AND AN EARLIER VERSION CHECKED ONLY THE NAME
//
// It required the request type's name to end in "UpdateRequest", which two different
// mutants walked straight past:
//
//   - DELETING A FAMILY FROM partialUpdateFamilies() left the suite green. The method
//     still took a well-named type, so this guard was satisfied; and the harness, which
//     is the only thing that drives the SEMANTICS, had simply stopped driving it. The
//     conversion was certified by its own name.
//   - A type CONVERTED IN NAME ONLY — a FooUpdateRequest of plain *string fields with
//     full-replace semantics — would pass identically. That is not hypothetical:
//     user-management's AdminTenantUpdateRequest is exactly that shape and says so in
//     its own comment.
//
// So the name check is gone and three structural ones replace it. Each is the absence
// of a specific way to be wrong, and the first is the one that links this guard to the
// harness — without it, everything here is a claim about spelling.
//
//  1. the request type is REGISTERED with partialUpdateFamilies(), so something drives
//     its three states against a real database;
//  2. it carries no Token field, so naming a second record is unrepresentable;
//  3. every exported field CARRIES THE THREE STATES — asked of the mechanism (a Set
//     flag plus the Nullable/ImplementsGraphQLType markers that route an explicit null
//     through the packer), not of a list of type names that would drift the moment core
//     grew a sixth Optional.
//
// A family that has not been converted must be named in the exemption below, which makes
// the residual a thing a reader can count rather than infer.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	// The one update in this service still sharing its create input. A device profile's
	// token is a RENAME channel (allowed while the profile is unused, refused once it is
	// published or adopted), and a dedicated update input carrying no token would delete
	// that capability rather than convert it — so this family needs a rename channel
	// designed, not a mechanical conversion. Its token rule is pinned below.
	exempt := map[string]string{
		"UpdateDeviceProfile": "DeviceProfileCreateRequest",
	}

	// THE LINK TO THE HARNESS. Derived from the registry rather than restated, so a
	// family removed from it stops being certified here on the same edit.
	registered := map[reflect.Type]string{}
	for _, fam := range partialUpdateFamilies() {
		rt := reflect.TypeOf(fam.newRequest())
		if rt.Kind() != reflect.Ptr {
			t.Fatalf("family %q newRequest() returned %s, want a pointer to a struct", fam.name, rt)
		}
		registered[rt.Elem()] = fam.name
	}

	apiType := reflect.TypeOf(&Api{})
	seen := 0
	for i := 0; i < apiType.NumMethod(); i++ {
		m := apiType.Method(i)
		if !strings.HasPrefix(m.Name, "Update") {
			continue
		}
		// An update takes (receiver, ctx, token, request). Anything else is not the shape
		// this rule is about — a bulk or projection writer, say — and is skipped rather
		// than mis-reported.
		if m.Type.NumIn() != 4 || m.Type.In(3).Kind() != reflect.Ptr {
			continue
		}
		seen++
		reqType := m.Type.In(3).Elem()
		if want, ok := exempt[m.Name]; ok {
			if reqType.Name() != want {
				t.Errorf("%s is exempt as taking %s but now takes %s — an exemption that no "+
					"longer describes the code is worse than none", m.Name, want, reqType.Name())
			}
			continue
		}
		if _, ok := registered[reqType]; !ok {
			t.Errorf("%s takes %s, which no family in partialUpdateFamilies() registers — so "+
				"NOTHING drives its three states against a database, and the only thing "+
				"certifying it is the word \"Update\" in its name. Register it, or name it in "+
				"the exemption above with the reason", m.Name, reqType.Name())
			continue
		}
		assertCarriesTheThreeStates(t, m.Name, reqType)
	}
	// The anti-vacuity control. Reflection over a renamed or embedded receiver could find
	// nothing at all, and a loop over nothing reports success.
	if seen < 15 {
		t.Fatalf("only %d Update* methods were found on *Api; this guard has stopped seeing "+
			"the surface it certifies", seen)
	}
}

// assertCarriesTheThreeStates checks the SHAPE of an update input: no token, and every
// exported field able to tell absent from null.
//
// 🔴 IT ASKS THE MECHANISM, NOT A LIST OF TYPE NAMES. What makes a field three-state is
// that graphql-go routes it through the unmarshaler packer — which needs the Nullable()
// marker and ImplementsGraphQLType — and that it records whether the caller mentioned it
// at all, which is the Set flag. A field with all three is three-state whatever it is
// called; a *string has none of them however it is named. Checking against a roster of
// "OptionalString, OptionalBool, …" would instead go quietly wrong the day core adds a
// sixth, by certifying a field nothing in the roster covers.
func assertCarriesTheThreeStates(t *testing.T, method string, reqType reflect.Type) {
	t.Helper()
	type nullableMarker interface{ Nullable() }
	type graphqlTyped interface{ ImplementsGraphQLType(string) bool }

	for i := 0; i < reqType.NumField(); i++ {
		f := reqType.Field(i)
		if f.PkgPath != "" {
			continue // unexported: not reachable from the wire
		}
		if f.Name == "Token" {
			t.Errorf("%s's input %s has a Token field: the mutation's own argument names the "+
				"record, and a second token in the payload is the disagreement this whole "+
				"conversion removes", method, reqType.Name())
			continue
		}
		v := reflect.New(f.Type).Elem().Interface()
		_, nullable := v.(nullableMarker)
		_, typed := v.(graphqlTyped)
		// 🔴 THE KIND IS CHECKED BEFORE FieldByName, WHICH PANICS ON A NON-STRUCT. The
		// field this guard exists to catch is a *string, so the very shape it is looking
		// for is the one that would take the panic — and a panic aborts the whole test
		// BINARY, so every test after this one would stop running and the report would be
		// a stack trace instead of the sentence below. A guard whose failure mode is worse
		// than the defect is not one to rely on.
		hasSet, setIsBool := false, false
		if f.Type.Kind() == reflect.Struct {
			var set reflect.StructField
			set, hasSet = f.Type.FieldByName("Set")
			setIsBool = hasSet && set.Type.Kind() == reflect.Bool
		}
		if !nullable || !typed || !setIsBool {
			t.Errorf("%s's input %s.%s is a %s, which cannot tell an ABSENT field from an "+
				"explicit null — so the type is named like a partial update and behaves like a "+
				"full replace", method, reqType.Name(), f.Name, f.Type)
		}
	}
}

// ─── the rename exception ──────────────────────────────────────────────────

// updateDeviceProfile is the documented exception and takes the RENAME rule: a differing
// payload token names the profile's NEW token, and only a BLANK one is refused. Pinned
// here because an exception nobody can find is one the next change deletes by accident.
//
// That a rename is refused once the profile is published or adopted is a separate,
// pre-existing guard with its own test (TestUpdateDeviceProfile_RejectsRenameAfterPublish).
func TestUpdateDeviceProfile_RefusesABlankPayloadToken(t *testing.T) {
	// 🔴 Whitespace is included because the token GRAMMAR does not catch it — this is the
	// fixture-weaker-than-production hole that let it through: "   " reached the row and
	// left the profile findable by nothing.
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newPartialUpdateApi(t, deviceProfileTables...)
			ctx := partialUpdateCtx()
			seedDeviceProfile(t, api, ctx, "prof")

			if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileCreateRequest{
				Token: blank, Name: strp("Renamed"),
			}); err == nil {
				t.Fatalf("a blank payload token %q was accepted, which blanks the profile's token", blank)
			}
			rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
			p := requireOne(t, "device profile", rows, ferr)
			if p.Token != "prof" {
				t.Fatalf("token moved to %q", p.Token)
			}
		})
	}
}

// …and the counterweight: a real rename still works, so the refusal above has not been
// bought by removing the capability.
func TestUpdateDeviceProfile_ADifferingTokenStillRenames(t *testing.T) {
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")

	if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileCreateRequest{
		Token: "prof2", Name: strp("Renamed"),
	}); err != nil {
		t.Fatalf("a rename was refused: %v", err)
	}
	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof2"})
	if requireOne(t, "device profile", rows, ferr).Token != "prof2" {
		t.Fatal("the rename did not take")
	}
}
