// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// DetectionRuleScopeRef is the reference-counting join between a LIVE (published,
// enabled) detection rule and the dynamic entity-group version it scopes to (ADR-062 S4).
// One row exists per scoped enabled rule in a device profile's ACTIVE published version;
// the rows are re-synced whenever the active version changes (publish / rollback) and torn
// down when the profile is deleted.
//
// It is the source of truth for two decisions the read-model enrollment depends on, both
// keyed on PUBLISHED state — NOT the mutable draft (an author disabling or re-scoping a
// draft rule must never tear the read-model out from under the still-live published rule;
// the divergence only takes effect at the next publish):
//
//   - how many live rules reference {GroupToken}@{GroupVersion} (the enrollment refcount:
//     the S2 read-model for a group@v is materialized while this count is > 0 and GC'd when
//     it reaches 0), computed across ALL profiles so a group referenced by two profiles
//     stays enrolled until the last un-references it;
//   - whether a group is referenced at all (the DeleteEntityGroup fail-closed guard).
//
// A profile has exactly one active version, so (device_profile_id, rule_token) is the
// natural key — a re-sync replaces the profile's whole set. device_profile_id is a global
// serial that already implies the tenant, so the unique index needs no tenant column.
type DetectionRuleScopeRef struct {
	gorm.Model
	rdb.TenantScoped

	// The owning profile + the rule's authoring token (unique per profile, ADR-042).
	DeviceProfileId uint   `gorm:"not null;uniqueIndex:uix_dr_scope_ref,priority:1"`
	RuleToken       string `gorm:"not null;size:255;uniqueIndex:uix_dr_scope_ref,priority:2"`

	// The pinned group@version the rule scopes to. Indexed for the refcount + delete-guard
	// lookups.
	GroupToken   string `gorm:"not null;size:255;index:idx_dr_scope_ref_group,priority:1"`
	GroupVersion int32  `gorm:"not null;index:idx_dr_scope_ref_group,priority:2"`
}
