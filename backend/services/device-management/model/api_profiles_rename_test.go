// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE RENAME SAFETY TESTS FOR A DEVICE PROFILE.
//
// They are RE-POINTED at renameDeviceProfile rather than deleted. The rename used to
// live inside updateDeviceProfile's payload token; it now has its own mutation, and
// these are the only evidence that the guard which makes a rename safe — refusing one
// once the profile is published or adopted — moved with it. Deleting them would have
// removed the proof while leaving the claim standing.

// A profile token is immutable once the profile has published versions (ADR-051 slice 4b-3):
// a rename would silently unscope every rule already published under the old token. A rename
// BEFORE any publish is allowed, and an ordinary update after publish is unaffected.
func TestRenameDeviceProfile_RejectsRenameAfterPublish(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)

	// Before any publish, a rename is allowed.
	if _, err := api.RenameDeviceProfile(ctx, "prof", "prof2"); err != nil {
		t.Fatalf("rename before publish should be allowed: %v", err)
	}

	// Publish creates a version, freezing the token into the version key.
	if _, err := api.PublishDeviceProfile(ctx, "prof2", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// After publish, a rename is rejected.
	_, err := api.RenameDeviceProfile(ctx, "prof2", "prof3")
	assert.Error(t, err, "rename after publish must be rejected")

	// An ordinary update after publish is still allowed — the refusal above is about
	// the TOKEN, not about editing a published profile's draft.
	_, err = api.UpdateDeviceProfile(ctx, "prof2", &DeviceProfileUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed Display Only"),
	})
	assert.NoError(t, err, "an update after publish must still work")
}

// The other half of the same guard: a profile ADOPTED by a device type is in use even
// if nothing has been published, because the dead-man roster keys on the stable profile
// token from adoption onward.
func TestRenameDeviceProfile_RejectsRenameOnceAdopted(t *testing.T) {
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")

	profiles, err := api.DeviceProfilesByToken(ctx, []string{"prof"})
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	adopting := &DeviceType{}
	adopting.Token = "adopting"
	adopting.ProfileId = &profiles[0].ID
	require.NoError(t, api.RDB.DB(ctx).Create(adopting).Error)

	_, err = api.RenameDeviceProfile(ctx, "prof", "prof2")
	require.Error(t, err, "a rename of an adopted profile must be refused")

	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
	if requireOne(t, "device profile", rows, ferr).Token != "prof" {
		t.Fatal("the refused rename moved the token anyway")
	}
}

// 🔴 A BLANK NEW TOKEN IS REFUSED, WHITESPACE INCLUDED. A blank one used to be written
// straight onto the row through the update payload, leaving a live profile addressable
// by nothing and returning success. Whitespace is in the table because the token
// grammar does not treat "   " as absent.
func TestRenameDeviceProfile_RefusesABlankNewToken(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newPartialUpdateApi(t, deviceProfileTables...)
			ctx := partialUpdateCtx()
			seedDeviceProfile(t, api, ctx, "prof")

			if _, err := api.RenameDeviceProfile(ctx, "prof", blank); err == nil {
				t.Fatalf("a blank new token %q was accepted, which blanks the profile's token", blank)
			}
			rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
			p := requireOne(t, "device profile", rows, ferr)
			if p.Token != "prof" {
				t.Fatalf("token moved to %q", p.Token)
			}
		})
	}
}

// …and the counterweight: a real rename still works, so the refusals above have not
// been bought by removing the capability.
func TestRenameDeviceProfile_RenamesAnUnusedProfile(t *testing.T) {
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")

	renamed, err := api.RenameDeviceProfile(ctx, "prof", "prof2")
	require.NoError(t, err, "a rename was refused")
	assert.Equal(t, "prof2", renamed.Token)

	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof2"})
	if requireOne(t, "device profile", rows, ferr).Token != "prof2" {
		t.Fatal("the rename did not take")
	}
}

// Renaming to the token the profile already has is an idempotent SUCCESS, so the retry
// of a rename that half-failed is safe. It must not be an error a client has to
// special-case, and it must not fall into the collision check below and refuse the
// profile its own name.
func TestRenameDeviceProfile_SameTokenIsANoOpSuccess(t *testing.T) {
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")

	same, err := api.RenameDeviceProfile(ctx, "prof", "prof")
	require.NoError(t, err, "renaming a profile to its own token must succeed")
	assert.Equal(t, "prof", same.Token)

	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
	assert.Equal(t, "prof", requireOne(t, "device profile", rows, ferr).Token)
}

// A token another profile already holds is refused BY NAME, rather than left to arrive
// as a unique-index violation the caller has to decode. The full-replace update had no
// such check at all.
func TestRenameDeviceProfile_RefusesATokenAlreadyInUse(t *testing.T) {
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")
	seedDeviceProfile(t, api, ctx, "taken")

	_, err := api.RenameDeviceProfile(ctx, "prof", "taken")
	require.Error(t, err, "renaming onto an existing profile's token must be refused")
	assert.Contains(t, err.Error(), "already in use",
		"the refusal must name the collision rather than surface as a constraint violation")

	// Neither profile moved.
	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
	assert.Equal(t, "prof", requireOne(t, "device profile", rows, ferr).Token)
	rows, ferr = api.DeviceProfilesByToken(ctx, []string{"taken"})
	assert.Equal(t, "taken", requireOne(t, "device profile", rows, ferr).Token)
}

// An unknown token is a not-found, not a silent create and not "the only profile there
// is". The rename addresses the row by its `token` argument like every update does.
func TestRenameDeviceProfile_UnknownTokenIsNotFound(t *testing.T) {
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")

	_, err := api.RenameDeviceProfile(ctx, "no-such-profile", "prof2")
	require.Error(t, err, "renaming an unknown profile succeeded")

	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
	assert.Equal(t, "prof", requireOne(t, "device profile", rows, ferr).Token,
		"a rename addressed to an unknown token moved the seeded profile")
}
