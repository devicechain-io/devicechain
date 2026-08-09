// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"math"

	"gorm.io/datatypes"
)

// LocationDeclaration is a device profile's statement that devices built on it
// report THEIR OWN position (ADR-078). It is deliberately SINGULAR and NULLABLE
// rather than a fourth definition list beside metrics, commands and rules: a device
// reports its position once, not many times over a keyed vocabulary, so there is
// nothing for a list to key on.
//
// The nullability is the load-bearing half, and it is not a stylistic preference:
//
//   - nil means "this kind of device does not report position", which is the whole
//     display rule — a console renders no map, no track, no last-fix panel for such
//     a profile without needing a separate boolean to consult.
//   - a list would force an EMPTY list and an ABSENT list to mean the same thing
//     today, and nothing could ever tell them apart again. The day one of them came
//     to mean "declared, not yet configured", every already-frozen profile version
//     would be unreadable — a published snapshot is immutable, so the distinction
//     could not be recovered by a backfill.
//
// 🔴 What the declaration is FOR is description and display: the accuracy an
// operator should expect, how often a fix should arrive, whether the console offers
// a map, and what a geofence rule may later be authored against. It NEVER governs
// permission, and the ingest path NEVER gates on it — a device that starts reporting
// position before anyone edits its profile keeps every fix it sent (see
// EventResolver.warnIfLocationUndeclared, which warns and stores rather than
// filtering).
//
// The platform-wide coordinate contract is fixed and is NOT per-device: WGS84 /
// EPSG:4326 decimal degrees, elevation metres above the ELLIPSOID, accuracy metres,
// speed metres per second, heading degrees clockwise from true north in [0, 360).
// A declaration cannot override it, which is why no field here restates it.
//
// Coordinate-system metadata (local / indoor / industrial frames) is a known future
// extension, and the RESERVATION for it is this type's storage shape rather than a
// dead field: the declaration is persisted as one nullable JSON document, so a frame
// descriptor lands additively — no column, no migration, no new table — and every
// already-frozen snapshot keeps parsing. A `coordinateSystem` string added now would
// be half-support: settable, unenforced, and readable as an override of the fixed
// contract above.
type LocationDeclaration struct {
	// ExpectedAccuracyMeters is the horizontal accuracy an operator should expect
	// from this kind of device, in metres (the contract's accuracy unit). Descriptive
	// only: a fix reporting worse accuracy is stored unchanged. Null means the profile
	// declares position without stating an expectation.
	ExpectedAccuracyMeters *float64 `json:"expectedAccuracyMeters,omitempty"`
	// ExpectedUpdateIntervalSeconds is how often a fix is expected to arrive. Also
	// descriptive: nothing raises an alarm from it here (an absence rule authored on
	// the profile is the mechanism for that, and it is authored independently).
	ExpectedUpdateIntervalSeconds *int32 `json:"expectedUpdateIntervalSeconds,omitempty"`
}

// DeviceLocationCapability answers "does THIS device report its position?" for one
// device token (ADR-078) — the location counterpart of CommandVocabulary, and built
// the same way and for the same reason: the console must be told what a device
// RESOLVES, which is its type's profile's active PUBLISHED version, never the draft
// an author happens to be editing.
//
// It exists because the draft is the only location surface a client could otherwise
// read (DeviceProfile.location), and reading it would make a location panel appear
// and disappear on an unpublished edit — the publish-boundary confusion
// DeviceCommandVocabulary was added to prevent for commands. It also collapses the
// three sequential round-trips a client would need (device → type → profile) into
// one hop.
type DeviceLocationCapability struct {
	// DeviceExists reports whether the token resolved to a live device. False makes
	// Declaration meaningless, and is what lets the GraphQL layer answer null for an
	// unresolvable token while still answering "undeclared" for a real device.
	DeviceExists bool
	// Declaration is the position declaration frozen into the profile version the
	// device currently resolves, or nil when nothing on record says this device
	// reports a position.
	//
	// 🔴 nil has three indistinguishable sources — the type has no profile, the
	// profile was never published, or the published version declares no location —
	// and they are deliberately one answer: all three mean the same thing to a
	// reader. A non-nil declaration with both fields unset is NOT one of them; it is
	// the "reports position, no expectations stated" claim, and it must survive
	// distinct from nil all the way to the client.
	Declaration *LocationDeclaration
}

// Declared reports whether the device's resolved profile version declares that it
// reports position. It is derived HERE, from the same pointer the declaration itself
// is read from, so a client's boolean and its object can never disagree.
func (c *DeviceLocationCapability) Declared() bool {
	return c != nil && c.Declaration != nil
}

// Location decodes the profile's stored position declaration (ADR-078), returning
// nil when the profile does not declare that its devices report position. It is the
// only way outside this package to read the declaration — the JSON column itself is
// an encoding detail, and a caller that reached past this would have to re-implement
// the null handling that keeps "undeclared" distinct from "declared, unconfigured".
func (p *DeviceProfile) Location() (*LocationDeclaration, error) {
	return decodeLocationDeclaration(p.LocationDeclaration)
}

// Validate rejects a declaration whose stated expectations are not physically
// meaningful. This is AUTHORING-time validation of the declaration itself — it runs
// when an operator writes the profile, never against a device's data. A nil
// declaration is valid: it is the "does not report position" case.
func (d *LocationDeclaration) Validate() error {
	if d == nil {
		return nil
	}
	if v := d.ExpectedAccuracyMeters; v != nil {
		if math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 {
			return fmt.Errorf("expectedAccuracyMeters must be a positive number of metres, got %v", *v)
		}
	}
	if v := d.ExpectedUpdateIntervalSeconds; v != nil && *v <= 0 {
		return fmt.Errorf("expectedUpdateIntervalSeconds must be a positive number of seconds, got %d", *v)
	}
	return nil
}

// encodeLocationDeclaration renders a declaration for storage in the profile's
// nullable JSON column, validating it first. A nil declaration encodes to a nil
// pointer — a SQL NULL, i.e. "does not report position" — and a declared-but-empty
// one encodes to the two bytes `{}`. Those are the two values the whole design rests
// on staying distinct, so nothing here collapses one into the other.
func encodeLocationDeclaration(decl *LocationDeclaration) (*datatypes.JSON, error) {
	if decl == nil {
		return nil, nil
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(decl)
	if err != nil {
		return nil, err
	}
	encoded := datatypes.JSON(raw)
	return &encoded, nil
}

// decodeLocationDeclaration reads a declaration back out of the profile's nullable
// JSON column. A SQL NULL reaches Go as either a nil pointer or (through some
// drivers) the literal four bytes `null`; both mean undeclared, so both decode to
// nil rather than to an empty declaration.
func decodeLocationDeclaration(raw *datatypes.JSON) (*LocationDeclaration, error) {
	if raw == nil || len(*raw) == 0 || string(*raw) == "null" {
		return nil, nil
	}
	decl := &LocationDeclaration{}
	if err := json.Unmarshal(*raw, decl); err != nil {
		return nil, fmt.Errorf("unable to parse device profile location declaration: %w", err)
	}
	return decl, nil
}
