// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// DeviceProfileVersion is an immutable published snapshot of a device profile's
// whole capability set — its metric (ADR-016), command (ADR-043), alarm (ADR-041),
// and detection-rule (ADR-051) definitions frozen together as one unit (ADR-045
// decision 4). The live
// definition tables are the mutable DRAFT the author edits; publishing freezes the
// draft into the next monotonic version here, and a device resolves the profile's
// currently-active published version (DeviceProfile.ActiveVersion), never the draft.
//
// It mirrors the dashboard versioning machinery (ADR-039): append-only history, a
// unique (device_profile_id, version) so two concurrent publishes can't mint the
// same number (the loser's insert fails), and a snapshot stored opaquely. The
// snapshot here is a ProfileSnapshot JSON document — the definition lists
// serialized together — read back only by the platform, never client-facing.
type DeviceProfileVersion struct {
	gorm.Model
	rdb.TenantScoped

	// Parent profile + monotonic-per-profile version number, unique together.
	DeviceProfileId uint  `gorm:"not null;uniqueIndex:uix_device_profile_versions_profile_version,priority:1"`
	Version         int32 `gorm:"not null;uniqueIndex:uix_device_profile_versions_profile_version,priority:2"`

	// Optional user-supplied annotations for the version (may embed a semver string;
	// the platform does not parse it).
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	// The full capability snapshot at publish time (a serialized ProfileSnapshot).
	Snapshot datatypes.JSON `gorm:"not null"`

	// The identity that published this version (claims username, falling back to
	// email).
	PublishedBy string `gorm:"size:256"`
}

// ProfileSnapshot is the serialized capability set frozen into a
// DeviceProfileVersion.Snapshot: the profile's definition lists captured
// together. It is Go-internal (marshaled and unmarshaled only here, never SQL-built
// nor exposed over GraphQL), so the definition structs are stored whole.
//
// 🔴 THAT DOES NOT MAKE THE ENCODING A PRIVATE MATTER, WHICH THIS COMMENT USED TO CLAIM.
// It said the encoding "need only be self-consistent — a value round-trips because the
// same Go types read it back that wrote it". True inside one binary; false for every row
// that outlives one, and every row here does. A version is frozen at publish and NEVER
// rewritten, so a snapshot written by v0.11.0 is read years later by whatever is deployed
// then — by THAT release's structs, not the ones that wrote it. This is a DURABLE WIRE
// FORMAT that happens to be spelled in live models.
//
// Two consequences, and neither announces itself:
//
//   - Adding a field to one of the definition structs means every already-published
//     version decodes it as the zero value. Sometimes that is the right reading (a nil
//     Location means "declares no position", which is exactly what a pre-ADR-078 snapshot
//     meant); sometimes it is silently wrong. It is a decision to make, not to default to.
//   - These structs carry NO json tags, so the stored key IS the Go identifier. Renaming a
//     field — the most ordinary refactor there is, verified by the compiler at every use —
//     drops it from every stored snapshot without a word.
//
// TestProfileSnapshotStoredKeysArePinned pins the stored key set so both of those stop
// being silent. TestProfileSnapshotRoundTrip does NOT cover this and structurally cannot:
// it marshals and unmarshals with the same types in the same binary, so a rename renames
// both halves together and it passes unchanged. That was checked by making the change and
// watching it pass.
type ProfileSnapshot struct {
	Metrics  []*MetricDefinition  `json:"metrics"`
	Commands []*CommandDefinition `json:"commands"`
	// Rules are the DETECT rules (ADR-051 slice 4b) frozen into the version. Like the
	// other lists they are captured whole; event-processing compiles them when it
	// consumes the published-rule fact.
	Rules []*DetectionRule `json:"rules"`
	// Location is the profile's position declaration (ADR-078) frozen into this
	// version. 🔴 It is NOT a fourth list: it is a nullable SINGULAR struct, because a
	// device reports ITS OWN position once. nil means the profile does not declare
	// that its devices report position, and that nil is a real value the console reads
	// (no location declared ⇒ no map, no track, no last-fix panel) — so unlike the
	// three lists above it is deliberately NOT normalized away by
	// parseProfileSnapshot. `omitempty` keeps an undeclared profile's snapshot free of
	// a `"location":null` key, but absent and null both parse back to nil, so the
	// distinction that matters — nil versus a declared-but-empty `{}` — survives
	// either encoding.
	Location *LocationDeclaration `json:"location,omitempty"`
}
