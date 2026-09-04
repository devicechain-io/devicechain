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
// enumerates *Api's own Update* methods and requires each to take a dedicated
// *UpdateRequest — the shape that has no token to disagree with and folds three states
// rather than two.
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
		req := m.Type.In(3).Elem().Name()
		if want, ok := exempt[m.Name]; ok {
			if req != want {
				t.Errorf("%s is exempt as taking %s but now takes %s — an exemption that no "+
					"longer describes the code is worse than none", m.Name, want, req)
			}
			continue
		}
		if !strings.HasSuffix(req, "UpdateRequest") {
			t.Errorf("%s takes %s: an update sharing its create input carries the token TWICE "+
				"and cannot tell an omitted field from a cleared one, so it is a full replace. "+
				"Convert it, or name it in the exemption above with the reason", m.Name, req)
		}
	}
	// The anti-vacuity control. Reflection over a renamed or embedded receiver could find
	// nothing at all, and a loop over nothing reports success.
	if seen < 15 {
		t.Fatalf("only %d Update* methods were found on *Api; this guard has stopped seeing "+
			"the surface it certifies", seen)
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
