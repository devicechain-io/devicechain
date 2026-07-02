// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
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

// Data required to create an entity relationship type.
type EntityRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
	Tracked     bool
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
	SourceType         string
	SourceId           uint
	TargetType         string
	TargetId           uint
	RelationshipTypeId uint
	RelationshipType   EntityRelationshipType
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
