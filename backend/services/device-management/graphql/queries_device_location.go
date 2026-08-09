// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// DeviceLocationCapabilityResolver exposes whether a device reports its own position,
// as resolved through the profile version the device actually resolves (ADR-078).
type DeviceLocationCapabilityResolver struct {
	M model.DeviceLocationCapability
	S *SchemaResolver
	C context.Context
}

// Declared is derived from the declaration pointer itself rather than stored
// alongside it, so the boolean and the object can never contradict each other.
func (r *DeviceLocationCapabilityResolver) Declared() bool { return r.M.Declared() }

// Declaration is the frozen declaration, or null when nothing on record says this
// device reports a position.
//
// 🔴 A declaration with neither expectation set returns a PRESENT, empty object — it
// is the "reports position, no expectations stated" claim, and collapsing it to null
// here would erase the one distinction the nullable declaration exists to carry.
func (r *DeviceLocationCapabilityResolver) Declaration() *DeviceLocationDeclarationResolver {
	if r.M.Declaration == nil {
		return nil
	}
	return &DeviceLocationDeclarationResolver{M: *r.M.Declaration, S: r.S, C: r.C}
}

// DeviceLocationDeclaration answers "does THIS device report its position?" (ADR-078)
// in one hop — device → type → profile → ACTIVE PUBLISHED VERSION → declaration.
//
// It mirrors deviceCommandVocabulary deliberately, because it is the same question
// about a different capability, and it exists for the same three reasons:
//
//  1. DeviceProfile.location is the editable DRAFT. A device resolves the active
//     PUBLISHED version, so a console reading the draft would show and hide a position
//     panel on an unpublished edit.
//  2. DeviceProfileVersion exposes no location field at all (the snapshot is stored
//     opaquely), so without this there is no way to ask what a device resolves.
//  3. A client would otherwise need two extra SEQUENTIAL round-trips per device page
//     (Device carries only deviceType.token → getDeviceType → deviceProfilesByToken)
//     to decide whether to draw one panel.
//
// Gated on device:read, matching the command precedent. This is PROFILE METADATA, not
// position data: gating it on location:read would mean a user who may not see
// coordinates could not even be told the panel is irrelevant to this device — and
// would push the console back onto the draft, which is the failure this query exists
// to remove.
//
// An unresolvable device token returns null rather than an error, for the same reason
// deviceCommandVocabulary does: a stale token is a state a client legitimately holds,
// and erroring on a non-null field would null out every sibling result in a batched
// document over one dangling reference. A device that exists and declares nothing is a
// DIFFERENT answer — `declared: false` — and stays distinguishable from this null.
func (r *SchemaResolver) DeviceLocationDeclaration(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceLocationCapabilityResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	capability, err := r.GetApi(ctx).DeviceLocationDeclaration(ctx, args.DeviceToken)
	if err != nil {
		return nil, err
	}
	if !capability.DeviceExists {
		return nil, nil
	}
	return &DeviceLocationCapabilityResolver{M: *capability, S: r, C: ctx}, nil
}
