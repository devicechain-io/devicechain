// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The structures the BASELINE migration creates, captured at the point in time it was written.
// They are private to this package and are NOT the live models in model/.
//
// They also REPLACE the schema/v1 package, which is deleted by this change. That package was the
// initial migration's snapshot layer — the right idea, but it had become dead weight the moment
// the chain collapsed, and nothing imported it any more. Two files both called some version of
// "the snapshot" is how the wrong one gets edited, which is the failure this whole convention
// exists to prevent. Its shapes live on below, with the mixins inlined rather than embedded.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time. When a model gains
// a column, the column arrives in a NEW migration appended after the baseline — these stay
// exactly as they are.
//
// Self-contained down to the mixins: gorm.Model, rdb.TenantScoped, rdb.TokenReference,
// rdb.NamedEntity, rdb.BrandedEntity, rdb.MetadataEntity and rdb.ExternalReference are INLINED
// rather than embedded. This area had ~111 embedded uses of them across 26 migrations, which is
// the largest single concentration of the hazard in the repo: a field added to a core mixin
// would have rewritten every one of those applied migrations from a PR that never touched
// device-management. Field order matches the embedded layout exactly, because physical column
// order is part of the schema.
//
// 🔴 THE RELATION FIELDS ARE LOAD-BEARING AND CREATE NO COLUMNS. They exist so AutoMigrate
// emits the FOREIGN KEY constraints inline at CREATE TABLE, and gorm names those constraints
// after the OWNER TABLE AND THE FIELD — `fk_device-management_device_types_devices` is the name
// of `deviceType.Devices`, not of anything on `devices`. Delete a relation and the constraint
// silently disappears; rename the field and the constraint is silently renamed. Neither shows
// up as a column difference.
//
// 🔴 EXACTLY ONE TYPE IN THIS AREA PINS A TableName — `facetKeyDef`, whose table was created by a
// pinned shape in the chain, so all of its indexes are unprefixed and consistently so. Pinning
// itself is fine and sometimes forced (ai-inference and user-management pin every shape). What is
// never fine is pinning on ONE of two shapes describing the same table: that derives two names for
// one index, which is how `alarms` and `entity_groups` each carried a duplicate until it was fixed.
// See the one-table-one-shape note in baseline.go.
//
// 🔑 WHY INLINED — stated carefully, because the obvious version of this argument is BACKWARDS.
// An earlier version of this comment claimed the golden gate now "largely catches" a core mixin
// edit, making the inlining rule belt-and-braces. That is false, and measurably so: nothing in
// this file is reachable from backend/core, so editing rdb.TenantScoped cannot change one byte of
// what this migration emits — there is no divergence for any gate to catch. INLINING IS WHAT
// REMOVES THE MIXIN FROM THE GATE'S FIELD OF VIEW. Crediting the gate for that coverage inverts
// cause and effect.
//
// The contrast is the useful part, and it is deliberate: core/secrets/migration.go DOES embed
// gorm.Model and rdb.TenantScoped, because the secret-store schema genuinely IS core's and a
// change to it SHOULD change all three consumers. Measured — adding a field to rdb.TenantScoped
// reddens migration-diff verify for exactly the three areas carrying a secret store, and no
// others. So the repo runs both patterns on purpose; the test is whose schema it is.
//
// What inlining therefore buys is not detection but the absence of the coupling: this migration
// cannot be rewritten from another module, so its meaning is fixed by this file alone. The cost it
// accepts, worth knowing: the snapshot silently diverges from the live model over time and nothing
// compares the two. That is intended — the snapshot is frozen by design — but no gate will tell you.
//
// Do not reach for a shared LOCAL base type either. These structs must be free to diverge; that
// divergence is the point, so there is no synchronisation requirement for DRY to serve, and a
// shared base only moves change-at-a-distance one level in — from another module to another
// migration in this package.

// ---------------------------------------------------------------------------
// The registry families (ADR-013): each is a tenant-scoped token entity.
// ---------------------------------------------------------------------------

// deviceType classifies devices and carries the profile binding (un-fused, ADR-045).
//
// ModelName maps to the `model` COLUMN while gorm derives its index name from the FIELD, so the
// index is `idx_device-management_device_types_model_name` over a column called `model`.
// Renaming the field renames the index; changing the column tag renames the column. They are
// deliberately different and both are the schema.
type deviceType struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

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

	// Names the devices FK — see the relation note above.
	Devices []device

	ProfileId    *uint          `gorm:"index"`
	Manufacturer sql.NullString `gorm:"size:128;index"`
	ModelName    sql.NullString `gorm:"column:model;size:128;index"`
}

// device is the addressable thing (ADR-013). external_id is the customer-owned business
// identifier (ADR-049) — a VIN, serial, asset tag — distinct from the token: opaque, nullable,
// and unique per tenant only WHEN PRESENT, which is why its index carries two predicates.
//
// It is LAST because it arrived in a later additive migration and AutoMigrate appends.
type device struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	DeviceTypeId uint
	DeviceType   *deviceType

	ExternalId sql.NullString `gorm:"index;size:256"`
}

// deviceCredential is how a device authenticates (ADR-014). It carries no name/description.
type deviceCredential struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Metadata *datatypes.JSON

	DeviceId uint `gorm:"not null;index"`
	// Names fk_device-management_device_credentials_device.
	Device *device

	CredentialType  string         `gorm:"not null;size:32;index"`
	CredentialId    string         `gorm:"not null;size:256"`
	CredentialValue sql.NullString `gorm:"size:4096"`
	Enabled         bool           `gorm:"not null;default:true"`
	ExpiresAt       sql.NullTime
}

// areaType / area, assetType / asset, customerType / customer are the three remaining
// registry families. Each type is branded; each instance carries only name/description.
type areaType struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

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

	Areas []area
}

type area struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	AreaTypeId uint
	AreaType   *areaType
}

type assetType struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

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

	Assets []asset
}

type asset struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	AssetTypeId uint
	AssetType   *assetType
}

type customerType struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

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

	Customers []customer
}

type customer struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	CustomerTypeId uint
	CustomerType   *customerType
}

// ---------------------------------------------------------------------------
// The typed relationship graph (ADR-013)
// ---------------------------------------------------------------------------

// entityRelationshipType describes a CLASS of relationship. Tracked marks types whose
// relationships are denormalized onto events for indexing.
type entityRelationshipType struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	Tracked bool `gorm:"not null;default:false;index"`
}

// entityRelationship is a single uniform edge: it addresses source and target by (type, id)
// rather than by eight typed foreign-key columns, so adding an entity type needs no schema
// change. Referential integrity for those references is enforced at the application layer —
// validated on write, resolved by typed loaders on read — precisely BECAUSE the columns are
// polymorphic and no FK can express them.
//
// target_token is LAST: a later migration denormalized the target's stable token onto the edge
// so a tracked relationship can be resolved without a second lookup.
type entityRelationship struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Metadata *datatypes.JSON

	SourceType         string `gorm:"not null;size:32;index:idx_entity_rel_source,priority:1"`
	SourceId           uint   `gorm:"not null;index:idx_entity_rel_source,priority:2"`
	TargetType         string `gorm:"not null;size:32;index:idx_entity_rel_target,priority:1"`
	TargetId           uint   `gorm:"not null;index:idx_entity_rel_target,priority:2"`
	RelationshipTypeId uint   `gorm:"not null"`
	// Names fk_device-management_entity_relationships_relationship_type.
	RelationshipType entityRelationshipType

	TargetToken sql.NullString `gorm:"size:128"`
}
