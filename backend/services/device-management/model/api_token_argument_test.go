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
// The previous version of this file drove seven families through a reconcile rule and
// asserted each was WIRED to it. All seven have since been converted to a dedicated
// *UpdateRequest carrying no token at all, which makes the disagreement unrepresentable
// rather than refused — so there is nothing left in this service for that rule to govern,
// and the tests that drove it would now iterate an empty list and pass having asserted
// nothing.
//
// Deleting them rather than leaving the loop is deliberate: a table-driven test over an
// empty table is the most convincing kind of green there is. What replaces the claim is
// the guard below, which asks the TYPE rather than a list, so a family added tomorrow on
// the full-replace shape fails on the day it is added.
//
// The rule itself no longer exists. Every update mutation on the platform now takes a
// dedicated *UpdateRequest, so its last two callers — updateNotificationPolicy and
// updateDashboard — were converted out from under it and dcgraphql.ErrPayloadTokenDisagrees
// was deleted with them. What survives in core is only the RENAME floor
// (graphql.TestErrRenameTokenUnusable), which this service's RenameDeviceProfile states
// inline so its refusal can say what a blank costs a profile.

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

		// 🔴 THERE ARE NO EXEMPTIONS LEFT IN THIS SERVICE, and the empty map is
		// deliberate rather than an omission. The last one was UpdateDeviceProfile,
		// whose payload token was a RENAME CHANNEL — which is why it could not be
		// converted mechanically. The rename did not evaporate: it moved to
		// Api.RenameDeviceProfile, where the new token can mean only one thing, and the
		// update input then lost its token like every other. The guard fails on an
		// exemption that matches nothing, so this map cannot quietly regrow an entry
		// describing a state of the world that has ended.
		Exempt: map[string]string{},

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

// The rename's own rules — blank refused, same-token idempotent, collision refused by
// name, and refused outright once the profile is published or adopted — are pinned in
// api_profiles_rename_test.go, which is where they moved with the capability.
