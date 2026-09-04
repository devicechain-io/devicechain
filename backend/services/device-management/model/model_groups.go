// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/entity"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// MembershipMode discriminates how an EntityGroup's members are determined
// (ADR-061). The two modes are mutually exclusive per group, so a group is never
// half-static/half-derived.
type MembershipMode string

const (
	// MembershipStatic — members are an explicit list, carried by "member"
	// relationship edges (group → member), exactly as the legacy per-family
	// groups worked.
	MembershipStatic MembershipMode = "static"
	// MembershipDynamic — members are the entities whose attributes satisfy a
	// saved CEL selector, resolved eval-on-read. The selector engine (G3) is
	// required to create one; G1 rejects this mode.
	MembershipDynamic MembershipMode = "dynamic"
)

// GroupMemberTypes is the set of entity families a group may collect. It is the
// non-group entity types — a group cannot be a member family of another group.
var GroupMemberTypes = []entity.Type{
	entity.TypeDevice, entity.TypeAsset, entity.TypeArea, entity.TypeCustomer,
}

// ValidGroupMemberType reports whether t is an entity family an EntityGroup may
// collect (any recognized non-group type).
func ValidGroupMemberType(t entity.Type) bool {
	return t.Valid() && t != entity.TypeGroup
}

// EntityGroup is the uniform, member-family-agnostic group (ADR-061), folding the
// four former per-family groups (DeviceGroup/AssetGroup/AreaGroup/CustomerGroup)
// into one table. Its member family lives in MemberType (validated against the
// entity-type registry) rather than in four distinct entity identities.
//
// A group has one of two membership modes: static (explicit members over the
// existing "member" relationship edge) or dynamic (a saved CEL Selector resolved
// eval-on-read). The Selector columns are carried now; the selector engine that
// compiles and resolves them lands in G3.
type EntityGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	// MemberType is the entity family this group collects (device|asset|area|
	// customer), one of entity.Type; validated on write.
	MemberType string `gorm:"not null;size:32;index"`
	// MembershipMode is "static" or "dynamic".
	MembershipMode string `gorm:"not null;size:16"`
	// Selector is the CEL membership predicate, non-null iff MembershipMode is
	// "dynamic".
	Selector sql.NullString
	// SelectorSchema records the selector-env SchemaVersion the Selector was
	// checked against (0 for static groups).
	SelectorSchema int `gorm:"not null;default:0"`
	// ActiveVersion is the published EntityGroupVersion (ADR-062 S1) a rule-scoped
	// resolve reads the frozen selector from — the currently-live membership rule.
	// Null until the group is first published: the live Selector column is the
	// mutable DRAFT (a browse/filter consumer resolves it eval-on-read directly), and
	// only a rule scope needs the frozen version. Static groups are never versioned,
	// so this stays null for them. Publish advances the pointer; rollback flips it.
	ActiveVersion sql.NullInt32
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. This orders the GROUPS themselves; a group's resolved MEMBERS
// are a different read entirely (ResolveGroupMembers), ordered by the member family's
// own clause.
func (EntityGroup) DefaultOrder() string {
	return "entity_groups.created_at DESC, entity_groups.token ASC"
}

// Data required to create or replace an entity group.
type EntityGroupCreateRequest struct {
	Token           string
	MemberType      string
	MembershipMode  *string // defaults to "static" when nil
	Selector        *string // required (and only permitted) when mode is "dynamic"
	Name            *string
	Description     *string
	ImageUrl        *string
	Icon            *string
	BackgroundColor *string
	ForegroundColor *string
	BorderColor     *string
	Metadata        *string
}

// A PARTIAL update to an entity group: omit a field to leave the stored value alone,
// send null to clear a nullable one, send a value to set it.
//
// # 🔴 THREE FIELDS ARE ABSENT ON PURPOSE, AND THAT IS THE RULING ON THIS FAMILY
//
// Token, MemberType and MembershipMode are all IMMUTABLE, and the full-replace input
// carried all three with a guard refusing any change. Under three-state semantics such
// a field can only ever be a no-op or an error — there is no request that names it
// usefully — which is the case for removing it from the input rather than for teaching
// the guard a third state. The immutability is then enforced by unrepresentability,
// which is strictly stronger than a refusal: a caller cannot write the request at all,
// and the wire test that says so cannot pass vacuously the way a guard's test can.
//
// The reasons the three are immutable are unchanged. A rename would strand every
// reference held by token. Changing MemberType would leave a group collecting a family
// its members do not belong to. Converting a static group to dynamic (or back) would
// orphan its members, since the two modes store membership in entirely different places.
//
// # The selector is the one field whose three states needed deciding
//
// A dynamic group's selector is live-editable, and it is the ONLY thing that makes the
// group mean anything — so it is REQUIRED rather than nullable. Omit it and the stored
// predicate stands; send a new one and it is re-compiled and cost-gated before it
// replaces the old; send null and it is refused, because a dynamic group with no
// selector matches nothing and there is no way back to a valid group from there. A
// static group must never carry one at all, so a value on a static group is refused too.
type EntityGroupUpdateRequest struct {
	// Selector is the CEL membership predicate, required (and only permitted) for a
	// dynamic group. A provided selector is re-compiled and cost-gated before it
	// replaces the stored one.
	Selector        dcgraphql.OptionalString
	Name            dcgraphql.OptionalString
	Description     dcgraphql.OptionalString
	ImageUrl        dcgraphql.OptionalString
	Icon            dcgraphql.OptionalString
	BackgroundColor dcgraphql.OptionalString
	ForegroundColor dcgraphql.OptionalString
	BorderColor     dcgraphql.OptionalString
	Metadata        dcgraphql.OptionalString
}

// Search criteria for locating entity groups. MemberType and MembershipMode are
// optional filters (nil = any).
type EntityGroupSearchCriteria struct {
	rdb.Pagination
	MemberType     *string
	MembershipMode *string
}

// Results for entity group search.
type EntityGroupSearchResults struct {
	Results    []EntityGroup
	Pagination rdb.SearchResultsPagination
}
