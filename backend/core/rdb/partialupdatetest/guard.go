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
//
// # 🔴 AND NOTHING IS SKIPPED, WHICH IS THE FOURTH THING AND WAS ONCE THE HOLE
//
// Before any of the three above can be asked, the update input has to be FOUND, and the
// first version of this guard found it by position — parameter 3 of exactly 4. Every
// method not in that shape was walked past in silence: it landed in neither the counted
// set nor the exemptions, so it was certified by nothing, and the anti-vacuity floor
// could not see it because the floor bounds what was counted. Three of this platform's
// updates carry a trailing `expectedUpdatedAt *string` and four take their input by
// value; all seven were invisible. See requestParamOf and NotAnEntityUpdate for the two
// halves of the replacement — locate by SHAPE, and make every remaining mismatch a named
// failure rather than a `continue`.

// UpdateSurface is what one service supplies to
// AssertEveryUpdateTakesADedicatedRequest. A is the service's Api type; the guard derives
// the method set from it directly, so there is no value to pass and nothing to keep in
// step by hand.
type UpdateSurface[A any] struct {
	// Families is the registry Run drives. Derived from it rather than restated, so a
	// family removed from the registry stops being certified here on the same edit.
	Families []Family[A]
	// Exempt names the updates that ARE entity updates but have NOT been converted,
	// mapping the method name to the request type it still takes. An exemption whose type
	// no longer matches FAILS: one that no longer describes the code is worse than none.
	Exempt map[string]string
	// NotAnEntityUpdate names the Update* methods this rule does not govern at all,
	// mapping the method name to WHY — a bulk writer, a projection refresher, a
	// scalar-argument edit with no input object to convert.
	//
	// 🔴 IT EXISTS BECAUSE THE GUARD USED TO SKIP THESE SILENTLY, AND THAT IS THE FAILURE
	// IT WAS BUILT TO PREVENT, ARRIVING THROUGH ITS OWN SHAPE FILTER. The earlier version
	// walked past any method that was not exactly (receiver, ctx, token, *request):
	// anything with a trailing `expectedUpdatedAt *string`, anything taking its input by
	// value, anything with a leading scope argument. Such a method landed in neither the
	// walked set nor the exemptions, so it was certified by NOTHING — and the anti-vacuity
	// floor could not catch it BY CONSTRUCTION, because the floor bounds the methods that
	// were walked and a skipped one is not among them. One 4-argument update beside a
	// 5-argument one was enough: the floor was met by the first and the second was
	// invisible, so an unconverted full-replace update sat under a green guard.
	//
	// So nothing is skipped now. A method matching neither this map nor the request shape
	// is a FAILURE that names itself, and the reason recorded here is what a reader gets
	// instead of a silence they would have to reconstruct.
	NotAnEntityUpdate map[string]string
	// MinUpdateMethods is the anti-vacuity floor — the number of Update* methods this
	// service is known to have, counting the exempt ones and the ones named above.
	// Reflection over a renamed or embedded receiver could find NOTHING at all, and a
	// loop over nothing reports success.
	//
	// It is a per-service number rather than a constant because services differ by an
	// order of magnitude in how many updates they carry, and a floor set for the largest
	// would be unreachable for the smallest — which ends with the floor being deleted
	// rather than lowered. It must be greater than zero.
	MinUpdateMethods int
}

// requestParamOf locates the update input among a method's parameters.
//
// 🔴 IT LOOKS FOR A SHAPE, NOT A POSITION, and that is the whole point. "Parameter 3 of
// 4" is a description of one service's convention, not of what an update input IS, and
// three of this platform's updates already carry a trailing `expectedUpdatedAt *string`
// while four take a leading scope argument. A positional rule reads those as "not an
// update" and certifies them by omission.
//
// A candidate is a struct, or a pointer to one. That excludes the receiver (skipped
// outright), context.Context (an interface), and every scalar argument a real update
// carries — `token string`, `scope string`, `expectedUpdatedAt *string`, `firstName,
// lastName *string` — because a pointer to a non-struct is not an input object.
//
// # A BY-VALUE INPUT IS ACCEPTED, DELIBERATELY
//
// user-management passes its admin inputs by value (`in RoleMutableInput`). Refusing
// that shape would mean the guard's answer to four real updates was "change the
// signature", which is a demand about calling convention rather than about the semantic
// this rule governs: the three-state check is structural, so it reads a struct type
// exactly as well by value as through a pointer. What the guard has to refuse is an
// input it cannot FIND, not one that arrives without an indirection.
//
// Ambiguity is a refusal rather than a guess. Two struct parameters means the guard
// would have to pick, and picking wrongly would certify the wrong type while reporting
// success.
func requestParamOf(m reflect.Method) (reflect.Type, []reflect.Type) {
	var candidates []reflect.Type
	for i := 1; i < m.Type.NumIn(); i++ { // 1: skip the receiver
		p := m.Type.In(i)
		switch {
		case p.Kind() == reflect.Struct:
			candidates = append(candidates, p)
		case p.Kind() == reflect.Ptr && p.Elem().Kind() == reflect.Struct:
			candidates = append(candidates, p.Elem())
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, candidates
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

	// A reason is required, and an empty one is refused rather than accepted as a
	// formality: "not an entity update" without the because is a skip with a nicer name,
	// which is what this map exists to replace.
	for name, why := range s.NotAnEntityUpdate {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is named in NotAnEntityUpdate with no reason — an unexplained "+
				"exclusion is the silent skip this map replaced, spelled differently", name)
		}
		// The two maps say opposite things: Exempt means "an entity update, not yet
		// converted", NotAnEntityUpdate means "not an entity update at all". A method in
		// both would take the exclusion branch and mark the exemption used on the way
		// past, so the contradiction would resolve itself into silence — which is how a
		// converted family's exemption survives the conversion that should have deleted
		// it.
		if _, both := s.Exempt[name]; both {
			t.Errorf("%s is in both Exempt and NotAnEntityUpdate, which say opposite things "+
				"about it: one calls it an unconverted entity update, the other says the rule "+
				"does not govern it. Decide which, because as it stands the exclusion wins and "+
				"the exemption is never checked against the code", name)
		}
	}

	used := map[string]bool{}
	seen := 0
	for i := 0; i < apiType.NumMethod(); i++ {
		m := apiType.Method(i)
		if !strings.HasPrefix(m.Name, "Update") {
			continue
		}
		seen++

		// Outside the rule entirely, by an explicit decision someone had to write down.
		if _, ok := s.NotAnEntityUpdate[m.Name]; ok {
			used[m.Name] = true
			continue
		}

		reqType, ambiguous := requestParamOf(m)
		if reqType == nil {
			// 🔴 NOT A SKIP. The message says which of the two things is wrong — the
			// method has no input object to convert, or it has several and the guard
			// will not pick — and names the map that resolves it either way.
			if len(ambiguous) == 0 {
				t.Errorf("%s takes no struct parameter, so there is no update input for this "+
					"rule to certify: %s. Either it is not an entity update — name it in "+
					"NotAnEntityUpdate with the reason — or its arguments are loose scalars "+
					"that need collecting into a dedicated *UpdateRequest", m.Name, m.Type)
			} else {
				names := make([]string, 0, len(ambiguous))
				for _, c := range ambiguous {
					names = append(names, c.String())
				}
				t.Errorf("%s takes %d struct parameters (%s), so this guard cannot tell which "+
					"one is the update input — and picking would certify the wrong type while "+
					"reporting success. Give it one input object, or name it in "+
					"NotAnEntityUpdate with the reason",
					m.Name, len(ambiguous), strings.Join(names, ", "))
			}
			continue
		}

		if want, ok := s.Exempt[m.Name]; ok {
			used[m.Name] = true
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

	// 🔴 AN ENTRY THAT MATCHES NOTHING IS THE SAME DEFECT AS ONE THAT NAMES THE WRONG
	// TYPE, and it is the one a conversion leaves behind: B3-b removes an exemption's
	// method from the unconverted set and the entry sits there afterwards, describing a
	// state of the world that has ended, while the residual a reader counts stays one too
	// high. The same applies to a NotAnEntityUpdate entry outliving its method.
	assertNoStaleEntries(t, apiType, "exemptions match no Update* method on %s: %s — an "+
		"exemption nothing uses still counts against the residual a reader is asked to trust, "+
		"so delete it once its family is converted", s.Exempt, used)
	assertNoStaleEntries(t, apiType, "NotAnEntityUpdate entries match no Update* method on "+
		"%s: %s — an exclusion for a method that no longer exists silences nothing and hides "+
		"the next method to take that name", s.NotAnEntityUpdate, used)

	// The anti-vacuity control. It now bounds EVERY Update* method rather than only the
	// ones that matched a shape, which is what makes it able to notice the walk shrinking.
	if seen < s.MinUpdateMethods {
		t.Fatalf("only %d Update* methods were found on %s, and this service is known to have "+
			"at least %d; this guard has stopped seeing the surface it certifies",
			seen, apiType, s.MinUpdateMethods)
	}
}

func assertNoStaleEntries(t *testing.T, apiType reflect.Type, msg string,
	entries map[string]string, used map[string]bool) {
	t.Helper()
	var stale []string
	for name := range entries {
		if !used[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("these "+msg, apiType, strings.Join(stale, ", "))
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
