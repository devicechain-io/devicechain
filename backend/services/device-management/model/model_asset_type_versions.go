// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AssetTypeVersion is an immutable published snapshot of an asset type's property
// contract (ADR-072). AssetType.PropertySchema is the mutable DRAFT an author
// edits; publishing freezes that draft into the next monotonic version here, and an
// asset is validated against its type's currently-active published version
// (AssetType.ActiveVersion), never against the draft.
//
// It is the fifth instance of the draft/publish/rollback machinery in this
// codebase, after dashboard-management, outbound-connectors, device profiles
// (ADR-045) and entity groups (ADR-062). There is no shared helper to reuse — those
// four are copy-and-adapt descendants of each other, and each one's comments say
// so — so this follows them structurally rather than factoring them together: same
// append-only history, same unique (asset_type_id, version) rejecting the loser of
// a concurrent publish, same CreatedAt-as-publishedAt, same "the version carries no
// token because it is addressed by parent + integer".
//
// It takes the POINTER variant (device profiles, entity groups) rather than the
// re-draft variant (dashboards, connectors), because it has a runtime consumer:
// every asset write resolves a specific version to validate against, so rollback
// has to be an instant, non-destructive flip that leaves the draft alone.
type AssetTypeVersion struct {
	gorm.Model
	rdb.TenantScoped

	// Parent type + monotonic-per-type version number, unique together.
	AssetTypeId uint  `gorm:"not null;uniqueIndex:uix_asset_type_versions_type_version,priority:1"`
	Version     int32 `gorm:"not null;uniqueIndex:uix_asset_type_versions_type_version,priority:2"`

	// Optional user-supplied annotations for the version (may embed a semver string;
	// the platform does not parse it).
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	// PropertySchema is the frozen []ParameterSpec contract, captured from the draft
	// at publish and never rewritten.
	//
	// 🔴 Unlike DeviceProfileVersion.Snapshot this is NOT a Go-struct dump whose
	// stored keys are Go identifiers. ParameterSpec carries explicit json tags, so
	// the stored key set is declared rather than inherited from whatever the fields
	// happen to be named — which is the trap ProfileSnapshot's own comment documents
	// and pins a test against. Renaming a Go field here is free; changing a tag is a
	// data migration.
	PropertySchema datatypes.JSON `gorm:"not null"`

	// The identity that published this version (claims username, falling back to
	// email).
	PublishedBy string `gorm:"size:256"`
}
