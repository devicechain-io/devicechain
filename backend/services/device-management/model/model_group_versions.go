// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// EntityGroupVersion is an immutable published snapshot of a DYNAMIC entity group's
// membership rule (ADR-062 S1). A dynamic group's live Selector column is the
// mutable DRAFT an author edits; publishing freezes that draft into the next
// monotonic version here, and a rule scopes to a specific "{group}@{version}" so its
// target set can never silently shift under a later selector edit (the reason
// ADR-062 forces group versioning). A STATIC group is never versioned — it has no
// selector to freeze.
//
// It mirrors the device-profile versioning machinery (ADR-045 slice c /
// model_profile_versions.go): append-only history, a unique (entity_group_id,
// version) so two concurrent publishes can't mint the same number (the loser's
// insert fails), and the pointer to the active version on the parent group
// (EntityGroup.ActiveVersion). Unlike a profile version — whose snapshot is an
// opaque JSON capability blob read only by the platform — a group version's
// "snapshot" is just the frozen selector plus the family it collects and the
// selector-env schema it was checked against, so a version is self-describing: a
// rule-scoped resolve reads the frozen Selector here, never the live (draft) group
// row.
type EntityGroupVersion struct {
	gorm.Model
	rdb.TenantScoped

	// Parent group + monotonic-per-group version number, unique together.
	EntityGroupId uint  `gorm:"not null;uniqueIndex:uix_entity_group_versions_group_version,priority:1"`
	Version       int32 `gorm:"not null;uniqueIndex:uix_entity_group_versions_group_version,priority:2"`

	// Selector is the FROZEN lowerable-CEL membership predicate captured at publish —
	// the draft group's Selector re-compiled + cost-gated one last time before
	// freezing, so a published version can never carry an un-vetted selector.
	Selector string `gorm:"not null"`
	// MemberType is the entity family the group collects, frozen with the version so
	// the row is self-describing (a group's family is immutable anyway, but freezing
	// it means a version read needs no live-group join).
	MemberType string `gorm:"not null;size:32"`
	// SelectorSchema is the selector-env SchemaVersion the frozen Selector was
	// checked against.
	SelectorSchema int `gorm:"not null"`

	// Optional user-supplied annotations for the version (may embed a semver string;
	// the platform does not parse it).
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	// The identity that published this version (claims username, falling back to
	// email).
	PublishedBy string `gorm:"size:256"`
}
