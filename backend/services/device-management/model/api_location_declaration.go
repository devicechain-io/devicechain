// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"gorm.io/gorm"
)

// locationDeclarationForProfile reads a profile's DRAFT position declaration
// (ADR-078) — what the author is editing, which is what a publish freezes. A
// profile that does not declare position yields nil, which is a value with meaning
// rather than an absence to paper over.
func (api *Api) locationDeclarationForProfile(ctx context.Context, profileId uint) (*LocationDeclaration, error) {
	profiles, err := api.DeviceProfilesById(ctx, []uint{profileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return decodeLocationDeclaration(profiles[0].LocationDeclaration)
}

// LocationDeclarationByDeviceType resolves what a DEVICE resolves: the position
// declaration (ADR-078) carried by its type's profile's active PUBLISHED version
// (ADR-045 decision 4), not the mutable draft — so an edit to the declaration takes
// effect when it is published, exactly like every other capability on the profile.
//
// nil means the device's profile does not declare that it reports position. That is
// a normal, non-error state with three sources that are deliberately indistinguishable
// here — the type has no profile, the profile has never been published, or the
// published version simply declares no location — because all three mean the same
// thing to a reader: nothing on record says this device reports a position.
//
// 🔴 This is a DESCRIPTIVE lookup. Nothing may gate ingest on it: a nil result means
// "undeclared", never "not permitted", and the location event is stored either way.
// Its only consumer is the resolver's once-per-(device, profile version) warning.
func (api *Api) LocationDeclarationByDeviceType(ctx context.Context, deviceTypeId uint) (*LocationDeclaration, error) {
	profileId, ok, err := api.profileIdForDeviceType(ctx, deviceTypeId)
	if err != nil || !ok {
		return nil, err
	}
	snap, err := api.activeProfileSnapshot(ctx, profileId)
	if err != nil {
		return nil, err
	}
	return snap.Location, nil
}
