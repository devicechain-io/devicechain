// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// geoFence is the SNAPSHOT of the authored geofence entity as of this migration
// (ADR-078). It is not model.GeoFence and must never be replaced by it: a migration's
// shapes are a point in time, and pointing one at a live model silently rewrites an
// already-applied migration whenever that model changes — which breaks FRESH installs
// while every existing database applies cleanly and looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped, rdb.TokenReference, rdb.NamedEntity,
// rdb.MetadataEntity) are INLINED rather than embedded, matching every other shape in
// this package: a field added to a core mixin must not rewrite this migration from a PR
// that never touched device-management. Field order matches the embedded layout exactly,
// because physical column order is part of the schema.
//
// 🔴 NO TableName(). Pinning one on a shape that describes a table no other shape
// describes would make gorm derive UNPREFIXED index names here while every sibling table
// gets prefixed ones — the same one-table-two-names hazard baseline.go records for
// `alarms` and `entity_groups`, arrived at from the other direction.
//
// 🔴 geometry IS A PLAIN jsonb COLUMN, NOT A SPATIAL ONE, and there is no PostGIS
// anywhere. Containment is evaluated in Go by the DETECT engine; nothing in SQL ever
// looks inside this column, so a geometry type or a GiST index would buy nothing and
// would pin the schema to an extension. It is also what lets a later geometry kind (a
// polygon plus an altitude band, a set of cuboids) land with NO migration at all: the
// kind discriminator lives inside the document, so a new kind is a new value, not a new
// column.
type geoFence struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	Geometry datatypes.JSON `gorm:"not null"`
}

// geoFenceSetVersion is the SNAPSHOT of the tenant-wide fence-set version table
// (ADR-078): one append-only row per version, carrying the frozen fence set that
// version names. A resolved LOCATION event is stamped with `version`, which is what
// makes geofence evaluation replay-correct — the event names the fences it was resolved
// against instead of being re-tested against whatever fences exist later.
//
// The (tenant_id, version) unique index is declared by tag here so AutoMigrate emits it
// with the table: two concurrent fence edits both computing max+1 must not both succeed,
// because two different fence sets sharing one version number is a state no stamp could
// ever disambiguate. Note it is a PLAIN unique index, not the partial live-rows-only
// form the token entities use: these rows are never deleted, soft or otherwise, so there
// is no tombstone to keep out of the index and nothing to free for reuse — a version
// number is deliberately never reused.
type geoFenceSetVersion struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128;uniqueIndex:uix_geo_fence_set_versions_tenant_version,priority:1"`

	Version  int32          `gorm:"not null;uniqueIndex:uix_geo_fence_set_versions_tenant_version,priority:2"`
	Snapshot datatypes.JSON `gorm:"not null"`
	MintedAt time.Time      `gorm:"not null"`
}

// NewGeoFencesSchema creates the geofence authoring tables (ADR-078): geo_fences, the
// authored regions, and geo_fence_set_versions, the append-only tenant-wide version
// whose number is stamped onto resolved location events.
//
// This is an APPENDED migration. The baseline is frozen — its table list and its
// snapshot structs stay exactly as they are, and these two tables are created here,
// after it, which is the same position a fresh install and an upgraded one both put them
// in. Physical order is part of what the golden schema diff compares.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so
// this must be individually re-runnable. AutoMigrate is: it creates a table only when it
// is absent and adds only missing columns, and CREATE UNIQUE INDEX is emitted with IF
// NOT EXISTS. Nothing here depends on a statement before it.
//
// # No seed, and none is wanted
//
// A tenant starts with no fences and therefore at fence-set version 0 — which is a real
// answer ("this tenant has never had a fence"), distinct from a minted version whose
// snapshot happens to be empty ("every fence was deleted"). Seeding a version 0 row
// would collapse that distinction on every tenant, forever. Worth stating explicitly
// because migration-diff compares pg_dump --schema-only and CANNOT see rows: a migration
// that seeds nothing and one that seeds the wrong thing look identical to it.
//
// # Tenant purge
//
// Both tables carry tenant_id, so the catalog-driven tenant purge classifies them
// directly with no exemption to register. That is checked by the coverage gate that runs
// alongside migration-diff verify, not asserted here.
func NewGeoFencesSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260810000000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&geoFence{}, &geoFenceSetVersion{}); err != nil {
				return err
			}
			// ADR-042 P1: a token is unique within a tenant among LIVE rows only. The
			// helper is the baseline's deliberate local copy — index names and predicates
			// are schema, and a migration must not let another module rewrite them.
			return createTenantTokenIndex(tx, &geoFence{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"geo_fence_set_versions", "geo_fences"})
		},
	}
}
