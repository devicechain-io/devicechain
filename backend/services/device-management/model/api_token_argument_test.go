// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
)

// THE TOKEN ARGUMENT NAMES THE ROW. Nothing else does.
//
// Every update mutation in this service declares `token: String!`, and until this
// change that argument did three different things depending on which one you called:
//
//   - NINE of the seventeen ignored it outright and located the row by the token
//     inside the request payload. Sending a `token` argument naming one entity and a
//     `request.token` naming another silently updated the SECOND and returned it with
//     a 200 — a mandatory argument that was pure decoration, which is worse than an
//     absent one, because a client that gets the argument right and the payload wrong
//     has no way to find out.
//   - SEVEN located by the argument and then ASSIGNED the payload token over it, so
//     the payload still moved the row — and an empty payload token, which the schema
//     permits (String! admits ""), blanked the entity's token entirely.
//   - ONE, updateGeoFence, reconciled the two and refused a mismatch.
//
// The rule is now geoFence's, everywhere. Seven families went further and dropped the
// payload token from their input altogether (see partial_update_families_test.go),
// which makes the disagreement unrepresentable; the ones still sharing a create input
// reconcile it here. updateDeviceProfile is the single documented exception and has
// its own tests: a profile rename is a real capability, guarded by its in-use check.

// The helper, at its three boundaries. It is small enough that this is exhaustive
// rather than representative.
func TestErrPayloadTokenDisagrees(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		payload  string
		wantsErr bool
	}{
		{"agreeing tokens pass", "a", "a", false},
		// Under a shared create input a caller with nothing to say about identity has
		// no other way to say it, so an empty payload token is "unspecified", not a
		// request to blank the row.
		{"an empty payload token is unspecified", "a", "", false},
		{"a disagreeing payload token is refused", "a", "b", true},
		// The case the old code got exactly backwards: it would have located and
		// updated "b" here.
		{"case matters", "a", "A", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := errPayloadTokenDisagrees("thing", tc.token, tc.payload)
			if tc.wantsErr && err == nil {
				t.Fatal("expected a refusal")
			}
			if !tc.wantsErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// The wiring, against a real database, for one of the two families that actually
// located by the payload token. Two rows exist; the update names one in the argument
// and the other in the payload. Nothing may move.
//
// The two-row fixture is what makes this an observation rather than a tautology: with
// a single seeded row, "the other row was untouched" is vacuous, and a lookup that
// fell back to "the only group there is" would pass.
func TestUpdateEntityGroup_RefusesADisagreeingPayloadToken(t *testing.T) {
	api := newPartialUpdateApi(t, &EntityGroup{})
	ctx := partialUpdateCtx()

	for _, tok := range []string{"group-a", "group-b"} {
		if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
			Token: tok, MemberType: "device", Name: strp("Original " + tok),
		}); err != nil {
			t.Fatalf("seed %q: %v", tok, err)
		}
	}

	_, err := api.UpdateEntityGroup(ctx, "group-a", &EntityGroupCreateRequest{
		Token: "group-b", MemberType: "device", Name: strp("Hijacked"),
	})
	if err == nil {
		t.Fatal("an update whose payload named a different group was accepted — the token " +
			"argument is decoration again")
	}

	for _, tok := range []string{"group-a", "group-b"} {
		rows, ferr := api.EntityGroupsByToken(ctx, []string{tok})
		g := requireOne(t, "entity group", rows, ferr)
		if got := nullStr(g.Name); got != "Original "+tok {
			t.Errorf("the refused update still changed %s: name = %q", tok, got)
		}
	}
}

// The other half of that family's rule, and the counterweight to the test above: a
// payload token that AGREES with the argument is still a perfectly good update. Without
// this, tightening the guard until every update was refused would leave the test above
// passing for the wrong reason.
func TestUpdateEntityGroup_AnAgreeingPayloadTokenStillUpdates(t *testing.T) {
	api := newPartialUpdateApi(t, &EntityGroup{})
	ctx := partialUpdateCtx()

	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "group-a", MemberType: "device", Name: strp("Original"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := api.UpdateEntityGroup(ctx, "group-a", &EntityGroupCreateRequest{
		Token: "group-a", MemberType: "device", Name: strp("Renamed"),
	}); err != nil {
		t.Fatalf("an agreeing update was refused: %v", err)
	}

	rows, ferr := api.EntityGroupsByToken(ctx, []string{"group-a"})
	g := requireOne(t, "entity group", rows, ferr)
	if got := nullStr(g.Name); got != "Renamed" {
		t.Fatalf("name = %q, want %q", got, "Renamed")
	}
	if g.Token != "group-a" {
		t.Fatalf("token moved to %q", g.Token)
	}
}

// An EMPTY payload token no longer blanks the row's token. This was reachable on
// every family that assigned the payload token over the stored one, and the schema
// permits it: `token: String!` admits the empty string.
func TestUpdateEntityGroup_AnEmptyPayloadTokenDoesNotBlankTheRow(t *testing.T) {
	api := newPartialUpdateApi(t, &EntityGroup{})
	ctx := partialUpdateCtx()

	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "group-a", MemberType: "device", Name: strp("Original"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := api.UpdateEntityGroup(ctx, "group-a", &EntityGroupCreateRequest{
		MemberType: "device", Name: strp("Renamed"),
	}); err != nil {
		t.Fatalf("an update with no payload token was refused: %v", err)
	}

	rows, ferr := api.EntityGroupsByToken(ctx, []string{"group-a"})
	g := requireOne(t, "entity group", rows, ferr)
	if g.Token != "group-a" {
		t.Fatalf("an empty payload token moved the row's token to %q, leaving it addressable "+
			"by nothing", g.Token)
	}
}

// updateDeviceProfile is the documented exception — a rename is real there — but the
// blanking hazard is not, so it gets the empty-token refusal instead of the
// reconcile. Pinned here beside the rule it departs from, since an exception nobody
// can find is one the next change deletes by accident.
func TestUpdateDeviceProfile_RefusesAnEmptyPayloadToken(t *testing.T) {
	api := newPartialUpdateApi(t, &Device{}, &DeviceType{}, &DeviceProfile{},
		&DeviceProfileVersion{}, &MetricDefinition{}, &CommandDefinition{},
		&DetectionRule{}, &DetectionRuleScopeRef{})
	ctx := partialUpdateCtx()

	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "prof"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileCreateRequest{
		Name: strp("Renamed"),
	}); err == nil {
		t.Fatal("an empty payload token was accepted, which blanks the profile's token")
	}

	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
	p := requireOne(t, "device profile", rows, ferr)
	if p.Token != "prof" {
		t.Fatalf("token moved to %q", p.Token)
	}
}
