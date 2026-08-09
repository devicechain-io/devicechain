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

// DeviceLocationDeclaration resolves device → type → the profile's active PUBLISHED
// position declaration (ADR-078), the listing surface a console needs to decide
// whether a device gets a position panel at all.
//
// It is built ON TOP of LocationDeclarationByDeviceType rather than resolving the
// snapshot itself, exactly as ValidateCommandEnqueue is built on
// DeviceCommandVocabulary: what the console is told and what the ingest warning
// judges against are then the same reading, and cannot drift into a console that
// hides a panel for a device the platform is meanwhile recording positions for.
//
// A token that resolves to no device returns DeviceExists=false rather than an error.
// That is a state a client legitimately holds — a saved view outlives the device it
// was pointed at — and it stays distinct from a real device that simply declares
// nothing, which is the distinction the whole nullable design rests on.
//
// 🔴 Still DESCRIPTIVE. An undeclared device's positions are stored anyway, so a
// consumer must never read "undeclared" as "has no position" — see the console
// panel, which hides itself only when the declaration is absent AND no position
// exists.
func (api *Api) DeviceLocationDeclaration(ctx context.Context, deviceToken string) (*DeviceLocationCapability, error) {
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return &DeviceLocationCapability{DeviceExists: false}, nil
	}

	declaration, err := api.LocationDeclarationByDeviceType(ctx, devices[0].DeviceTypeId)
	if err != nil {
		return nil, err
	}
	return &DeviceLocationCapability{DeviceExists: true, Declaration: declaration}, nil
}
