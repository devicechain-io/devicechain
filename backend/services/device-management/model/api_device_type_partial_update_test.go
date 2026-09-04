// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// What is left of the device type's own partial-update test after the generic half
// was extracted into the harness (partial_update_harness_test.go).
//
// Everything the harness now owns — that a rename leaves the other ten fields
// alone, that an explicit null clears only what it names, that an empty request
// changes nothing, that an unknown token is a not-found — is asserted for
// deviceType there, alongside every other converted family, and per FIELD rather
// than for the two this file used to name. What stays here is what is true of
// THIS family only: the profile reference is NULLABLE, unlike every other entity
// reference on the platform, so it has behaviours no generic property covers.
//
// The wire-shape half (token is not a member of the input; request is required)
// lives in graphql/partial_update_wire_test.go, and the proof that the three
// states survive the packer at all is in core's optional_test.go.

func seedTypeWithProfile(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-a"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{
		Token:        "sensor",
		Name:         strp("Original name"),
		ProfileToken: strp("profile-a"),
	}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
}

func reloadType(t *testing.T, api *Api, ctx context.Context) *DeviceType {
	t.Helper()
	matches, err := api.DeviceTypesByToken(ctx, []string{"sensor"})
	return requireOne(t, "device type", matches, err)
}

// profileToken is the one nullable reference among the converted families, and its
// three states carry more than the others: absent is the state that mattered most,
// because it is the reference a rename used to drop, silently un-declaring every
// capability the type's devices resolve through it.
func TestUpdateDeviceType_ProfileTokenSpecialCases(t *testing.T) {
	// Carried over from the pre-partial behaviour, where empty and omitted were the
	// same thing. They are no longer the same thing, so this is pinned rather than
	// assumed: an empty token still detaches, an omitted one no longer does.
	//
	// 🔴 This is also the one place the platform's two reference rules visibly
	// disagree. On a REQUIRED reference — assetTypeToken, deviceTypeToken — an empty
	// token is refused, because "no type" is not a state the row can be in. Here it
	// detaches. Both are right for their column, and neither can be inferred from the
	// other, which is why each is pinned where it lives.
	t.Run("an empty token detaches", func(t *testing.T) {
		api := newPartialUpdateApi(t, &Device{}, &DeviceType{}, &DeviceProfile{},
			&DeviceProfileVersion{}, &MetricDefinition{}, &CommandDefinition{},
			&DetectionRule{}, &DetectionRuleScopeRef{})
		ctx := partialUpdateCtx()
		seedTypeWithProfile(t, api, ctx)
		if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			ProfileToken: dcgraphql.OptionalStringOf("   "),
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if reloadType(t, api, ctx).ProfileId != nil {
			t.Fatal("an empty profileToken left the profile attached")
		}
	})

	t.Run("an unknown token refuses the WHOLE update", func(t *testing.T) {
		api := newPartialUpdateApi(t, &Device{}, &DeviceType{}, &DeviceProfile{},
			&DeviceProfileVersion{}, &MetricDefinition{}, &CommandDefinition{},
			&DetectionRule{}, &DetectionRuleScopeRef{})
		ctx := partialUpdateCtx()
		seedTypeWithProfile(t, api, ctx)

		_, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			Name:         dcgraphql.OptionalStringOf("Renamed"),
			ProfileToken: dcgraphql.OptionalStringOf("no-such-profile"),
		})
		if err == nil {
			t.Fatal("an unknown profile token was accepted, leaving a dangling reference")
		}
		// The refusal must be total. Applying the name and then failing on the profile
		// would be the worst of both designs — a caller who retries has already
		// half-applied the first attempt.
		if n := nullStr(reloadType(t, api, ctx).Name); n != "Original name" {
			t.Fatalf("the refused update still applied name = %q", n)
		}
	})

	// An absent profileToken must not RE-RESOLVE the type's existing profile token.
	// It would look equivalent, and is not: re-resolving turns a rename into a
	// profile detach whenever the referenced profile has since been deleted.
	t.Run("an absent token does not re-resolve", func(t *testing.T) {
		api := newPartialUpdateApi(t, &Device{}, &DeviceType{}, &DeviceProfile{},
			&DeviceProfileVersion{}, &MetricDefinition{}, &CommandDefinition{},
			&DetectionRule{}, &DetectionRuleScopeRef{})
		ctx := partialUpdateCtx()
		seedTypeWithProfile(t, api, ctx)
		before := reloadType(t, api, ctx).ProfileId

		if err := api.RDB.DB(ctx).Where("token = ?", "profile-a").
			Delete(&DeviceProfile{}).Error; err != nil {
			t.Fatalf("delete the adopted profile: %v", err)
		}
		if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			Name: dcgraphql.OptionalStringOf("Renamed"),
		}); err != nil {
			t.Fatalf("a rename failed after the adopted profile went away: %v", err)
		}
		after := reloadType(t, api, ctx).ProfileId
		if after == nil || before == nil || *after != *before {
			t.Fatalf("a rename moved the profile reference from %v to %v", before, after)
		}
	})
}
