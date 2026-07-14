// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file defines the ADR-062 S2 membership subsystem's persisted shapes: the
// membership read-model (which entities belong to which rule-scoped group@version)
// and the facet-key reverse index (which group@versions a facet key affects). Both
// are device-management-local — the selector engine + entity_attributes live here
// (the ADR-061 mirror-not-import red line), so membership is computed where the
// facts are, then stamped onto the resolved event (S3) rather than shipped as a
// cross-service projection.

// EntityGroupMembership is one positive membership fact: the entity (entity_type,
// entity_id) satisfies the frozen selector of group_id@selector_version. Absence of a
// row means non-member (the table holds positives only). It is maintained
// incrementally (recompute on a facet attribute write, §recompute) and populated by a
// set-query backfill when a group@v is first referenced by a rule (§Register). The
// group's token is denormalized (GroupToken) so the resolver can stamp
// {group_token, version} onto an event without a group-row join on the hot path
// (ADR-044 denormalization pattern, mirroring the relationship edge's target_token).
//
// Only rule-referenced group@versions are ever materialized here — a group used only
// for browse/filter (ADR-061) carries no rows, so the subsystem is pay-per-use.
type EntityGroupMembership struct {
	gorm.Model
	rdb.TenantScoped

	// The member entity. entity_id is a global serial (per family table), so the
	// natural key needs no tenant column to stay collision-free across tenants.
	EntityType string `gorm:"not null;size:32;uniqueIndex:uix_entity_group_membership,priority:3;index:idx_egm_entity,priority:1"`
	EntityId   uint   `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:4;index:idx_egm_entity,priority:2"`

	// The scoped group version. group_id is tenant-unique; selector_version pins the
	// frozen selector the membership was computed against.
	GroupId         uint   `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:1;index:idx_egm_group,priority:1"`
	SelectorVersion int32  `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:2;index:idx_egm_group,priority:2"`
	GroupToken      string `gorm:"not null;size:255"`
}

// EntityGroupFacetRef is the facet-key→group@version reverse index (ADR-062 §3): it
// records that group_id@selector_version's frozen selector references facet_key, so a
// SHARED-scope write of facet_key on a member of MemberType need only recompute the
// group@versions this table lists for (facet_key, member_type) — O(groups-referencing-K)
// rather than every registered group. It is populated from the frozen selector's
// Keys() at register time and GC'd at deregister.
type EntityGroupFacetRef struct {
	gorm.Model
	rdb.TenantScoped

	// The referenced facet key + the family it selects. member_type is carried so a
	// recompute filters by family (row ids are per-table, so a device and an area with
	// the same id must not cross-contaminate a shared facet-key name).
	FacetKey   string `gorm:"not null;size:256;index:idx_egfr_lookup,priority:1;uniqueIndex:uix_entity_group_facet_ref,priority:3"`
	MemberType string `gorm:"not null;size:32;index:idx_egfr_lookup,priority:2"`

	// The group version that references the key. Unique per (group, version, key); the
	// group index backs deregister GC.
	GroupId         uint  `gorm:"not null;uniqueIndex:uix_entity_group_facet_ref,priority:1;index:idx_egfr_group,priority:1"`
	SelectorVersion int32 `gorm:"not null;uniqueIndex:uix_entity_group_facet_ref,priority:2;index:idx_egfr_group,priority:2"`
	// GroupToken is denormalized so an incremental recompute (which reads this reverse
	// row) can stamp the membership row's GroupToken without a group-row lookup.
	GroupToken string `gorm:"not null;size:255"`
}

// GroupMembership is the in-memory projection of a membership row a caller reads —
// the group's token + the frozen version the entity belongs to. The resolver (S3)
// stamps these onto the event as scope memberships.
type GroupMembership struct {
	GroupId         uint
	GroupToken      string
	SelectorVersion int32
}
