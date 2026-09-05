// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// EntityRelationshipType is the metadata describing a class of relationship
// (ADR-013). A single type table serves every entity family; Tracked marks the
// types whose relationships are denormalized onto events for indexing.
type EntityRelationshipType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
	Tracked bool
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, with
// the per-tenant token breaking ties uniquely. created_at alone is not total — a
// bootstrap that seeds several types in one statement shares a timestamp — so the
// token carries the uniqueness the pagination contract requires.
func (EntityRelationshipType) DefaultOrder() string {
	return "entity_relationship_types.created_at DESC, entity_relationship_types.token ASC"
}

// Data required to create an entity relationship type.
type EntityRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
	Tracked     bool
}

// A PARTIAL update to an entity relationship type: omit a field to leave the stored
// value alone, send null to clear it, send a value to set it.
//
// There is deliberately no Token — the type is identified by the mutation's `token`
// argument, and an update cannot move it.
//
// Tracked is an OptionalBool rather than a bool for the reason the whole conversion
// exists: a plain bool reads false for "the caller said false" and for "the caller
// said nothing", so every edit of the NAME used to un-track the type.
type EntityRelationshipTypeUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	Metadata    dcgraphql.OptionalString
	// Tracked sits on a NOT NULL column with no "unset" reading, so an explicit null
	// is refused rather than folded to false — false is a legal value, and silently
	// writing it would stop denormalizing the type's relationships onto events while
	// reporting success.
	Tracked dcgraphql.OptionalBool
}

// Search criteria for locating entity relationship types.
type EntityRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for an entity relationship type search.
type EntityRelationshipTypeSearchResults struct {
	Results    []EntityRelationshipType
	Pagination rdb.SearchResultsPagination
}

// EntityRelationship is a single uniform relationship edge (ADR-013): it
// addresses its source and target by (type, id) rather than by one of eight
// typed foreign-key columns. Referential integrity for the source/target
// references is enforced at the application layer (validated on write via the
// entity-type registry, resolved by typed loaders on read).
type EntityRelationship struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity
	SourceType string
	SourceId   uint
	TargetType string
	TargetId   uint
	// TargetToken denormalizes the target entity's stable per-tenant token
	// alongside its resolved row id. It is stamped from the create request's target
	// token (which was just resolved to TargetId) so the event resolver can emit
	// token-addressed anchors (ADR-044) without an extra id→token lookup on the
	// ingest hot path — the target token rides the row (and its RelationshipsBySource
	// cache entry) for free.
	TargetToken        string `gorm:"size:128"`
	RelationshipTypeId uint
	RelationshipType   EntityRelationshipType
}

// DefaultOrder implements rdb.Sortable with the registry default: newest edge first,
// token as the unique tiebreak.
//
// 🔴 THE QUALIFICATION IS LOAD-BEARING HERE, not a stylistic nod to the contract.
// staticGroupMembers pages this model through a closure that JOINs
// entity_relationship_types, and both tables carry id, token, created_at and
// deleted_at. A bare `token ASC` there is not a differently-ordered page, it is
// `ERROR: column reference "token" is ambiguous` — a 500 on every static group's
// member list. This is the query the Sortable contract's rule 2 was written for.
func (EntityRelationship) DefaultOrder() string {
	return "entity_relationships.created_at DESC, entity_relationships.token ASC"
}

// Data required to create an entity relationship. The source and target name an
// entity by its type (one of entity.Type) and token; both are resolved and
// validated against the entity-type registry on write.
type EntityRelationshipCreateRequest struct {
	Token            string
	SourceType       string
	Source           string
	TargetType       string
	Target           string
	RelationshipType string
	Metadata         *string
}

// Search criteria for locating entity relationships. Source is matched by the
// already-resolved (SourceType, SourceId); callers holding only a source token
// resolve it first. Tracked filters by the relationship type's Tracked flag.
type EntityRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceType       *string
	Source           *string // source entity token (GraphQL-facing; resolved to SourceId)
	SourceId         *uint
	TargetType       *string
	Target           *string // target entity token (GraphQL-facing; resolved to TargetId)
	TargetId         *uint
	RelationshipType *string
	Tracked          *bool
}

// Results for an entity relationship search.
type EntityRelationshipSearchResults struct {
	Results    []EntityRelationship
	Pagination rdb.SearchResultsPagination
}
