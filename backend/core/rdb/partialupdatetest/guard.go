// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 🔴 THE EXHAUSTIVENESS CHECK OVER A SERVICE'S UPDATE SURFACE, AND THE REASON IT IS NOT A
// LIST OF FAMILY NAMES.
//
// A hand-written roster of converted families is a second copy of the truth, and the
// failure it cannot see is the one that matters: a NEW update method, added on the old
// full-replace shape, appears in neither the roster nor anything that reads it. So this
// enumerates the service's own Api's Update* methods and asks three things of each.
//
// # 🔴 THE NAME IS NOT ONE OF THEM, AND AN EARLIER VERSION CHECKED ONLY THE NAME
//
// It required the request type's name to end in "UpdateRequest", which two different
// mutants walked straight past:
//
//   - DELETING A FAMILY FROM THE REGISTRY left the suite green. The method still took a
//     well-named type, so the guard was satisfied; and the harness, which is the only
//     thing that drives the SEMANTICS, had simply stopped driving it. The conversion was
//     certified by its own name.
//   - A type CONVERTED IN NAME ONLY — a FooUpdateRequest of plain *string fields with
//     full-replace semantics — would pass identically. That is not hypothetical:
//     user-management's AdminTenantUpdateRequest is exactly that shape and says so in its
//     own comment.
//
// So the name check is gone and three structural ones replace it. Each is the absence of a
// specific way to be wrong, and the first is the one that links this guard to the harness
// — without it, everything here is a claim about spelling.
//
//  1. the request type is REGISTERED with the family list Run drives, so something drives
//     its three states against a real database;
//  2. it carries no Token field, so naming a second record is unrepresentable;
//  3. every exported field CARRIES THE THREE STATES — asked of the mechanism (a Set flag
//     plus the Nullable/ImplementsGraphQLType markers that route an explicit null through
//     the packer), not of a list of type names that would drift the moment core grew
//     another Optional. OptionalStringList, added after this guard was written, is
//     recognised by it for exactly that reason, and core's
//     TestOptionalStringListCarriesTheThreeStateMarkers is where that is measured rather
//     than assumed.
//
// A family that has not been converted must be named in the exemption map, which makes the
// residual a thing a reader can COUNT rather than infer.

// UpdateSurface is what one service supplies to
// AssertEveryUpdateTakesADedicatedRequest. A is the service's Api type; the guard derives
// the method set from it directly, so there is no value to pass and nothing to keep in
// step by hand.
type UpdateSurface[A any] struct {
	// Families is the registry Run drives. Derived from it rather than restated, so a
	// family removed from the registry stops being certified here on the same edit.
	Families []Family[A]
	// Exempt names the updates that have NOT been converted, mapping the method name to
	// the request type it still takes. An exemption whose type no longer matches FAILS:
	// one that no longer describes the code is worse than none.
	Exempt map[string]string
	// MinUpdateMethods is the anti-vacuity floor — the number of Update* methods this
	// service is known to have. Reflection over a renamed or embedded receiver could find
	// NOTHING at all, and a loop over nothing reports success.
	//
	// It is a per-service number rather than a constant because services differ by an
	// order of magnitude in how many updates they carry, and a floor set for the largest
	// would be unreachable for the smallest — which ends with the floor being deleted
	// rather than lowered. It must be greater than zero.
	MinUpdateMethods int
}

// AssertEveryUpdateTakesADedicatedRequest runs the guard over one service's Api.
func AssertEveryUpdateTakesADedicatedRequest[A any](t *testing.T, s UpdateSurface[A]) {
	t.Helper()
	if s.MinUpdateMethods <= 0 {
		t.Fatal("MinUpdateMethods must be greater than zero: it is the only thing standing " +
			"between this guard and a reflection walk that finds nothing and reports success")
	}
	if len(s.Families) == 0 {
		t.Fatal("no families were supplied, so every update in this service would be reported " +
			"as unregistered and the failure would name the guard rather than the code")
	}

	// THE LINK TO THE HARNESS.
	registered := map[reflect.Type]string{}
	for _, fam := range s.Families {
		rt := reflect.TypeOf(fam.NewRequest())
		if rt.Kind() != reflect.Ptr {
			t.Fatalf("family %q NewRequest() returned %s, want a pointer to a struct", fam.Name, rt)
		}
		registered[rt.Elem()] = fam.Name
	}

	// The Api type comes from the type parameter, so there is no value to construct and
	// nothing that can go stale. A is already the pointer type (*model.Api), so one Elem()
	// off **model.Api is what names it.
	apiType := reflect.TypeOf((*A)(nil)).Elem()
	if apiType.Kind() != reflect.Ptr || apiType.Elem().Kind() != reflect.Struct {
		t.Fatalf("the Api type parameter is %s, want a pointer to a struct — method "+
			"reflection would otherwise see a different method set than callers do", apiType)
	}

	usedExemptions := map[string]bool{}
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
		if want, ok := s.Exempt[m.Name]; ok {
			usedExemptions[m.Name] = true
			if reqType.Name() != want {
				t.Errorf("%s is exempt as taking %s but now takes %s — an exemption that no "+
					"longer describes the code is worse than none", m.Name, want, reqType.Name())
			}
			continue
		}
		if _, ok := registered[reqType]; !ok {
			t.Errorf("%s takes %s, which no registered family covers — so NOTHING drives its "+
				"three states against a database, and the only thing certifying it is the word "+
				"\"Update\" in its name. Register it, or name it in the exemption map with the "+
				"reason", m.Name, reqType.Name())
			continue
		}
		AssertCarriesTheThreeStates(t, m.Name, reqType)
	}

	// 🔴 AN EXEMPTION THAT MATCHES NOTHING IS THE SAME DEFECT AS ONE THAT NAMES THE WRONG
	// TYPE, and it is the one a conversion leaves behind: B3-b removes an exemption's
	// method from the unconverted set and the entry sits there afterwards, describing a
	// state of the world that has ended, while the residual a reader counts stays one too
	// high.
	var stale []string
	for name := range s.Exempt {
		if !usedExemptions[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("these exemptions match no Update* method on %s: %s — an exemption nothing "+
			"uses still counts against the residual a reader is asked to trust, so delete it "+
			"once its family is converted", apiType, strings.Join(stale, ", "))
	}

	// The anti-vacuity control.
	if seen < s.MinUpdateMethods {
		t.Fatalf("only %d Update* methods were found on %s, and this service is known to have "+
			"at least %d; this guard has stopped seeing the surface it certifies",
			seen, apiType, s.MinUpdateMethods)
	}
}

// AssertCarriesTheThreeStates checks the SHAPE of an update input: no token, and every
// exported field able to tell absent from null.
//
// 🔴 IT ASKS THE MECHANISM, NOT A LIST OF TYPE NAMES. What makes a field three-state is
// that graphql-go routes it through the unmarshaler packer — which needs the Nullable()
// marker and ImplementsGraphQLType — and that it records whether the caller mentioned it
// at all, which is the Set flag. A field with all three is three-state whatever it is
// called; a *string has none of them however it is named. Checking against a roster of
// "OptionalString, OptionalBool, …" would instead go quietly wrong the day core adds
// another, by certifying a field nothing in the roster covers.
func AssertCarriesTheThreeStates(t *testing.T, method string, reqType reflect.Type) {
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
