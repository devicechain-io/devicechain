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
// nor exposed over GraphQL), so the definition structs are stored whole and the
// encoding need only be self-consistent — a value round-trips because the same Go
// types read it back that wrote it.
type ProfileSnapshot struct {
	Metrics  []*MetricDefinition  `json:"metrics"`
	Commands []*CommandDefinition `json:"commands"`
	Alarms   []*AlarmDefinition   `json:"alarms"`
	// Rules are the DETECT rules (ADR-051 slice 4b) frozen into the version. Like the
	// other lists they are captured whole; event-processing compiles them when it
	// consumes the published-rule fact.
	Rules []*DetectionRule `json:"rules"`
}
