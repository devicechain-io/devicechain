// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
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

// 🔴 THE EXHAUSTIVENESS CHECK OVER THIS SERVICE'S UPDATE SURFACE.
//
// The guard itself is core's putest.AssertEveryUpdateTakesADedicatedRequest — it
// enumerates *Api's own Update* methods and asks three structural things of each: that
// the request type is REGISTERED with partialUpdateFamilies() so something drives its
// three states against a real database; that it carries no Token field; and that every
// exported field carries the three states. Its header says which mutants walked past the
// name-only version it replaced, and why none of the three is a check on spelling.
//
// What is local is what only this service can say: which updates have NOT been
// converted, and how many Update* methods reflection must find before the walk is
// believable.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Api]{
		Families: partialUpdateFamilies(),

		// The one update in this service still sharing its create input. A device
		// profile's token is a RENAME channel (allowed while the profile is unused,
		// refused once it is published or adopted), and a dedicated update input carrying
		// no token would delete that capability rather than convert it — so this family
		// needs a rename channel designed, not a mechanical conversion. Its token rule is
		// pinned below.
		Exempt: map[string]string{
			"UpdateDeviceProfile": "DeviceProfileCreateRequest",
		},

		// The anti-vacuity floor. Reflection over a renamed or embedded receiver could
		// find nothing at all, and a loop over nothing reports success.
		//
		// 17 is what the walk actually counts today, not a round number under it. The
		// floor now bounds EVERY Update* method rather than only the ones matching a
		// parameter shape, so it can be set to the measured count — and a floor set at
		// the measurement is the only one that notices a single update vanishing.
		MinUpdateMethods: 17,
	})
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
