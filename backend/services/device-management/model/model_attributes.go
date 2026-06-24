// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Attribute scope vocabulary (ADR-012). CLIENT = device-reported; SERVER =
// platform-only, hidden from the device; SHARED = platform-set, the device
// reads + subscribes (the server->device remote-config / OTA-target bridge).
type AttributeScope string

const (
	AttributeScopeClient AttributeScope = "CLIENT"
	AttributeScopeServer AttributeScope = "SERVER"
	AttributeScopeShared AttributeScope = "SHARED"
)

// Valid reports whether the scope names one of the known attribute scopes.
func (s AttributeScope) Valid() bool {
	switch s {
	case AttributeScopeClient, AttributeScopeServer, AttributeScopeShared:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (s AttributeScope) String() string {
	return string(s)
}

// Attribute value type vocabulary. The value is stored as text plus a type tag
// so consumers can coerce it; this also seeds the semantic-metric direction
// (ADR-016).
type AttributeValueType string

const (
	AttributeValueString  AttributeValueType = "STRING"
	AttributeValueLong    AttributeValueType = "LONG"
	AttributeValueDouble  AttributeValueType = "DOUBLE"
	AttributeValueBoolean AttributeValueType = "BOOLEAN"
	AttributeValueJson    AttributeValueType = "JSON"
)

// Valid reports whether the type names one of the known attribute value types.
func (t AttributeValueType) Valid() bool {
	switch t {
	case AttributeValueString, AttributeValueLong, AttributeValueDouble,
		AttributeValueBoolean, AttributeValueJson:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t AttributeValueType) String() string {
	return string(t)
}

// EntityAttribute is a single current-state key-value for an entity, in one of
// three scopes (ADR-012). It addresses its owner by uniform (entity_type,
// entity_id) (ADR-013); referential integrity for that reference is enforced at
// the application layer via the entity-type registry. Unlike events, this is a
// mutable current-state row: writing the same (entity, scope, key) upserts.
type EntityAttribute struct {
	gorm.Model
	rdb.TenantScoped
	EntityType  string
	EntityId    uint
	Scope       string
	AttrKey     string
	ValueType   string
	Value       sql.NullString
	LastUpdated time.Time
}

// Data required to set (upsert) an entity attribute. The entity names its owner
// by its type (one of entity.Type) and token; both are resolved and validated
// against the entity-type registry on write.
type EntityAttributeSetRequest struct {
	EntityType string
	Entity     string // owner entity token (GraphQL-facing; resolved to EntityId)
	Scope      string
	AttrKey    string
	ValueType  string
	Value      *string
}

// Search criteria for locating entity attributes. The owner is matched by the
// resolved (EntityType, EntityId); callers supplying only a token in Entity have
// it resolved first.
type EntityAttributeSearchCriteria struct {
	rdb.Pagination
	EntityType *string
	Entity     *string // owner entity token (GraphQL-facing; resolved to EntityId)
	EntityId   *uint
	Scope      *string
	AttrKeys   *[]string
}

// Results for an entity attribute search.
type EntityAttributeSearchResults struct {
	Results    []EntityAttribute
	Pagination rdb.SearchResultsPagination
}
