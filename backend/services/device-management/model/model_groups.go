// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/entity"
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
