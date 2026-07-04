// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// EntityRelationshipType is the metadata describing a class of relationship
// (ADR-013). A single table serves every entity type; the Tracked flag marks
// types whose relationships are denormalized onto events for indexing.
type EntityRelationshipType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
	Tracked bool `gorm:"not null;default:false;index"`
}

// EntityRelationship is a single uniform relationship edge (ADR-013): it
// addresses its source and target by (type, id) rather than by one of eight
// typed foreign-key columns, so adding a new entity type needs no schema change.
// Referential integrity for the source/target references is enforced at the
// application layer (validated on write, resolved by typed loaders on read).
type EntityRelationship struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity
	SourceType         string `gorm:"not null;size:32;index:idx_entity_rel_source,priority:1"`
	SourceId           uint   `gorm:"not null;index:idx_entity_rel_source,priority:2"`
	TargetType         string `gorm:"not null;size:32;index:idx_entity_rel_target,priority:1"`
	TargetId           uint   `gorm:"not null;index:idx_entity_rel_target,priority:2"`
	RelationshipTypeId uint   `gorm:"not null"`
	RelationshipType   EntityRelationshipType
}

// Represents a device type.
type DeviceType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Devices []Device
}

// Represents a device.
type Device struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceTypeId uint
	DeviceType   *DeviceType
}

// Represents a group of devices.
type DeviceGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// DeviceCredential holds rotatable authentication material resolved at connect
// time to the owning device (ADR-014). The (tenant_id, credential_type,
// credential_id) tuple is unique among LIVE rows only — enforced by a partial
// unique index (idx_device_credential_lookup, WHERE deleted_at IS NULL) created in
// the schema migration, NOT a struct-tag unique index (which counts soft-deleted
// tombstones, locking a rotated credential's slot forever and letting a tombstone
// still be resolved). Uniqueness is per-tenant because resolution always runs
// under a tenant (the ADR-025 callout parses it from the MQTT username; the event
// resolver carries it on the pipeline context), so the tenant-scoped DB callback
// already confines every lookup — a global constraint would only add a cross-tenant
// squatting/existence oracle with no auth benefit.
type DeviceCredential struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	DeviceId        uint `gorm:"not null;index"`
	Device          *Device
	CredentialType  string         `gorm:"not null;size:32;index"`
	CredentialId    string         `gorm:"not null;size:256"`
	CredentialValue sql.NullString `gorm:"size:4096"`
	Enabled         bool           `gorm:"not null;default:true"`
	ExpiresAt       sql.NullTime
}

// Data required to create an asset type.
type AssetTypeCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	ImageUrl        *string
	Icon            *string
	BackgroundColor *string
	ForegroundColor *string
	BorderColor     *string
	Metadata        *string
}

// Represents an asset type.
type AssetType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Assets []Asset
}

// Search criteria for locating asset types.
type AssetTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for asset type search.
type AssetTypeSearchResults struct {
	Results    []AssetType
	Pagination rdb.SearchResultsPagination
}

// Data required to create an asset.
type AssetCreateRequest struct {
	Token          string
	Name           *string
	Description    *string
	AssetTypeToken string
	Metadata       *string
}

// Represents an asset.
type Asset struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	AssetTypeId uint
	AssetType   *AssetType
}

// Search criteria for locating assets.
type AssetSearchCriteria struct {
	rdb.Pagination
	AssetTypeToken *string
}

// Results for asset search.
type AssetSearchResults struct {
	Results    []Asset
	Pagination rdb.SearchResultsPagination
}

// Represents a group of assets.
type AssetGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Represents an area type.
type AreaType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Areas []Area
}

// Represents an area.
type Area struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	AreaTypeId uint
	AreaType   *AreaType
}

// Represents a group of areas.
type AreaGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Represents a customer type.
type CustomerType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Customers []Customer
}

// Represents a customer.
type Customer struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	CustomerTypeId uint
	CustomerType   *CustomerType
}

// Represents a group of customers.
type CustomerGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}
