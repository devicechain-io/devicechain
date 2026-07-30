// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// Continuation of baseline_snapshot.go — the profile, authoring, alarm and grouping shapes.
// Split only for readability; every note at the top of that file applies here too, in
// particular that the mixins are INLINED and that field order is physical column order.

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Device profile + its versioned authoring surface (ADR-045)
// ---------------------------------------------------------------------------

// deviceProfile is the un-fused capability definition a device type binds to.
type deviceProfile struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	Category      sql.NullString `gorm:"size:64;index"`
	Provenance    sql.NullString `gorm:"size:256"`
	ActiveVersion *int32
}

// deviceProfileVersion is an immutable published snapshot of a profile, addressed by parent +
// monotonic version. It carries no token — a version is reached through its parent — so it has
// no per-tenant token index.
type deviceProfileVersion struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DeviceProfileId uint           `gorm:"not null;uniqueIndex:uix_device_profile_versions_profile_version,priority:1"`
	Version         int32          `gorm:"not null;uniqueIndex:uix_device_profile_versions_profile_version,priority:2"`
	Label           sql.NullString `gorm:"size:128"`
	Description     sql.NullString `gorm:"size:1024"`
	Snapshot        datatypes.JSON `gorm:"not null"`
	PublishedBy     sql.NullString `gorm:"size:256"`
}

// metricDefinition declares one measurement a profile's devices may report (ADR-016).
// Measurements are numeric-only BY DESIGN, which is why min/max are numeric and there is an
// enum for the closed-set case rather than a free-text value type.
type metricDefinition struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	MetricKey       string `gorm:"not null;size:128;uniqueIndex:idx_metric_definition_profile_key,priority:2"`
	DataType        string `gorm:"not null;size:16"`
	Unit            sql.NullString
	MinValue        sql.NullFloat64
	MaxValue        sql.NullFloat64
	Enum            *datatypes.JSON
	Descriptor      sql.NullString
	DeviceProfileId uint `gorm:"not null;uniqueIndex:idx_metric_definition_profile_key,priority:1"`
}

// commandDefinition declares one command a profile's devices accept, with its parameter schema
// (ADR-043). command-delivery validates an enqueue against this vocabulary.
type commandDefinition struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	CommandKey      string `gorm:"not null;size:128;uniqueIndex:idx_command_definition_profile_key,priority:2"`
	ParameterSchema *datatypes.JSON
	DeviceProfileId uint `gorm:"not null;uniqueIndex:idx_command_definition_profile_key,priority:1"`
}

// provisioningProfile is the pre-shared key material a device claims itself with (ADR-045).
type provisioningProfile struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	// `unique` here is a CONSTRAINT, not an index: gorm emits
	// uni_device-management_provisioning_profiles_provision_key. It is global rather than
	// per-tenant and counts soft-deleted rows — unlike every token index in this area — which is
	// a real inconsistency, but it is the schema the chain built and equivalence is the claim.
	ProvisionKey    string `gorm:"not null;size:256;unique"`
	ProvisionSecret string `gorm:"not null;size:256"`
	Strategy        string `gorm:"not null;size:32"`
	DeviceTypeId    uint   `gorm:"not null;index"`
	CredentialType  string `gorm:"not null;size:32"`
	Enabled         bool   `gorm:"not null"`
	ExpiresAt       sql.NullTime
}

// deviceClaim is one device's in-flight claim against a provisioning profile. The unique index
// on device_id is PLAIN, not partial: a device has at most one claim ever.
type deviceClaim struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DeviceId            uint   `gorm:"not null;uniqueIndex:idx_device_claim_device"`
	ClaimSecret         string `gorm:"not null;size:256"`
	Status              string `gorm:"not null;size:32"`
	ExpiresAt           sql.NullTime
	ClaimedTime         sql.NullTime
	ClaimedByCustomerId *uint
}

// ---------------------------------------------------------------------------
// Alarms (ADR-057) and DETECT rule authoring
// ---------------------------------------------------------------------------

// alarm is the level-state integrator: detection and authoring live in event-processing, and
// this object is what a human acknowledges and what history is kept against.
//
// contributors / contributor_version are LAST because they arrived in a later additive
// migration. The version is an optimistic-concurrency guard: the integrator reference-counts
// the rules currently raising an alarm in the contributor set — deriving state (cleared when the
// set empties) and severity (max over the active set) — and CAS-writes on this column so a
// concurrent fold cannot clobber the accumulator.
type alarm struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Metadata *datatypes.JSON

	OriginatorType   string    `gorm:"not null;size:32"`
	OriginatorId     uint      `gorm:"not null"`
	AlarmKey         string    `gorm:"not null;size:128"`
	MetricKey        string    `gorm:"not null;size:128"`
	State            string    `gorm:"not null;size:16"`
	Acknowledged     bool      `gorm:"not null;default:false"`
	Severity         string    `gorm:"not null;size:16"`
	RaisedTime       time.Time `gorm:"not null"`
	ClearedTime      sql.NullTime
	AcknowledgedTime sql.NullTime
	AcknowledgedBy   sql.NullString `gorm:"size:256"`
	LastValue        sql.NullFloat64
	Message          sql.NullString `gorm:"size:1024"`

	Contributors       *datatypes.JSON `gorm:"type:jsonb"`
	ContributorVersion uint            `gorm:"not null;default:0"`
}

// detectionRule is a rule AUTHORED on a profile. The engine that runs it lives in
// event-processing; this is the authoring record, versioned with its profile.
//
// authoring_graph is the visual canvas's own representation (ADR-053), kept alongside the
// compiled definition so a rule authored visually can be reopened visually. entity_group_token /
// entity_group_version are the optional group scope (ADR-062) and are LAST, additive, and
// NULLABLE here — unlike event-processing's projection of the same scope, which defaults them,
// because there a NULL would have to mean something to the engine.
type detectionRule struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	DeviceProfileId uint           `gorm:"not null;index"`
	Definition      datatypes.JSON `gorm:"not null"`
	Enabled         bool           `gorm:"not null;default:true"`
	AuthoringGraph  *datatypes.JSON

	EntityGroupToken   sql.NullString `gorm:"type:text"`
	EntityGroupVersion *int32
}

// detectionRuleScopeRef records which group version a rule is scoped to, so a group republish
// can find the rules that must be re-evaluated. Keyed by (profile, rule token) — one scope per
// rule — with a lookup index by group.
type detectionRuleScopeRef struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DeviceProfileId uint   `gorm:"not null;uniqueIndex:uix_dr_scope_ref,priority:1"`
	RuleToken       string `gorm:"not null;size:255;uniqueIndex:uix_dr_scope_ref,priority:2"`
	GroupToken      string `gorm:"not null;size:255;index:idx_dr_scope_ref_group,priority:1"`
	GroupVersion    int32  `gorm:"not null;index:idx_dr_scope_ref_group,priority:2"`
}

// ---------------------------------------------------------------------------
// Entity attributes + dynamic groups (ADR-061)
// ---------------------------------------------------------------------------

// entityAttribute is a scoped key/value on any entity. value is deliberately a very wide varchar
// rather than text so the column has a declared ceiling.
type entityAttribute struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	EntityType  string         `gorm:"not null;size:32;index:idx_entity_attr_key,priority:1"`
	EntityId    uint           `gorm:"not null;index:idx_entity_attr_key,priority:2"`
	Scope       string         `gorm:"not null;size:16;index:idx_entity_attr_key,priority:3"`
	AttrKey     string         `gorm:"not null;size:256;index:idx_entity_attr_key,priority:4"`
	ValueType   string         `gorm:"not null;size:16"`
	Value       sql.NullString `gorm:"size:65536"`
	LastUpdated time.Time      `gorm:"not null"`
}

// facetKeyDef is a per-tenant declaration that an attribute key, for a member family, is a
// classification facet (ADR-061 G2). Addressed by its NATURAL key — there is no token — so
// uniqueness is a per-tenant partial index over live rows, which lets a deleted facet's key be
// reused.
//
// 🔴 IT PINS TableName BECAUSE THE CHAIN DID, and that pin STAYS. facet_keys was created by a
// migration whose local struct pinned the name, so every index on this table is UNPREFIXED
// (idx_facet_keys_member_type, not idx_device-management_facet_keys_member_type). Dropping the pin
// would rename all three for no gain: this shape is the table's ONLY creator, so the names are
// internally consistent and nothing duplicates them. Pinning is legitimate — two whole areas
// (ai-inference, user-management) pin every shape out of necessity, one because gorm's snake_case
// splits the "AI" initialism into `a_iproviders` and the other to carry the `iam_` prefix. What is
// never legitimate is pinning on ONE of two shapes describing the same table; see baseline.go.
type facetKeyDef struct {
	// gorm.Model INLINED, like every other snapshot in the tree. It is upstream's type, so
	// embedding it would let a gorm release change what this frozen migration emits — a smaller
	// risk than for our own core mixins, but the same shape of risk, and this was one of only
	// three places in the repo that took it.
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	MemberType string `gorm:"not null;size:32;index"`
	Key        string `gorm:"column:attr_key;not null;size:128"`
	ValueType  string `gorm:"not null;size:16"`
	Source     string `gorm:"not null;size:16;default:attribute"`
	Values     *datatypes.JSON
	Label      sql.NullString
}

func (facetKeyDef) TableName() string { return "facet_keys" }

// entityGroup is a membership set, static or selector-driven (ADR-061). The four per-family group
// tables it replaced (device_groups, asset_groups, customer_groups, area_groups) are NOT created
// by this baseline at all — see the fold note in baseline.go.
type entityGroup struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	// These carry `index` again, and the history is worth one line because the tags flipped twice.
	// entity_groups used to be described by TWO shapes — this one plus a pinned partial — so
	// tagging here produced a prefixed SECOND index beside the pinned shape's unprefixed one, and
	// the tags had to come off. The pinned shape is gone (one table, one shape), so this shape is
	// now the sole source and the tags belong here. The names moved with them:
	// idx_entity_groups_token became idx_device-management_entity_groups_token, matching every
	// other derived index in the area.
	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	ImageUrl        sql.NullString `gorm:"size:512"`
	Icon            sql.NullString `gorm:"size:128"`
	BackgroundColor sql.NullString `gorm:"size:32"`
	ForegroundColor sql.NullString `gorm:"size:32"`
	BorderColor     sql.NullString `gorm:"size:32"`

	Metadata *datatypes.JSON

	MemberType     string         `gorm:"not null;size:32;index"`
	MembershipMode string         `gorm:"not null;size:16"`
	Selector       sql.NullString `gorm:"type:text"`
	SelectorSchema uint           `gorm:"not null;default:0"`
	ActiveVersion  *int32
}

// entityGroupVersion is an immutable published snapshot of a group's selector.
type entityGroupVersion struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	EntityGroupId  uint           `gorm:"not null;uniqueIndex:uix_entity_group_versions_group_version,priority:1"`
	Version        int32          `gorm:"not null;uniqueIndex:uix_entity_group_versions_group_version,priority:2"`
	Selector       string         `gorm:"not null;type:text"`
	MemberType     string         `gorm:"not null;size:32"`
	SelectorSchema uint           `gorm:"not null"`
	Label          sql.NullString `gorm:"size:128"`
	Description    sql.NullString `gorm:"size:1024"`
	PublishedBy    sql.NullString `gorm:"size:256"`
}

// entityGroupMembership is the materialized membership read-model, keyed by the group VERSION so
// a republish can build the new set before cutting over.
type entityGroupMembership struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	EntityType      string `gorm:"not null;size:32;uniqueIndex:uix_entity_group_membership,priority:3;index:idx_egm_entity,priority:1"`
	EntityId        uint   `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:4;index:idx_egm_entity,priority:2"`
	GroupId         uint   `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:1;index:idx_egm_group,priority:1"`
	SelectorVersion int32  `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:2;index:idx_egm_group,priority:2"`
	GroupToken      string `gorm:"not null;size:255"`
}

// entityGroupFacetRef records which facets a group version's selector reads, so a facet change
// can find the groups it invalidates.
type entityGroupFacetRef struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	FacetKey        string `gorm:"not null;size:256;uniqueIndex:uix_entity_group_facet_ref,priority:3;index:idx_egfr_lookup,priority:1"`
	MemberType      string `gorm:"not null;size:32;index:idx_egfr_lookup,priority:2"`
	GroupId         uint   `gorm:"not null;uniqueIndex:uix_entity_group_facet_ref,priority:1;index:idx_egfr_group,priority:1"`
	SelectorVersion int32  `gorm:"not null;uniqueIndex:uix_entity_group_facet_ref,priority:2;index:idx_egfr_group,priority:2"`
	GroupToken      string `gorm:"not null;size:255"`
}
