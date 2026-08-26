// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// geoFenceGeometryBlob is the SNAPSHOT of the content-addressed geometry archive as of
// this migration (ADR-078). It is not model.GeoFenceGeometryBlob and must never be
// replaced by it: a migration's shapes are a point in time, and pointing one at a live
// model silently rewrites an already-applied migration whenever that model changes.
//
// The mixins (gorm.Model, rdb.TenantScoped) are INLINED rather than embedded, matching
// every other shape in this package, so a field added to a core mixin cannot rewrite
// this migration from a PR that never touched device-management.
//
// # What this table is for, and why it is not a second home for the same fact
//
// geo_fences.geometry is the CURRENT authored geometry of a fence: mutable, and the
// only thing the console and the authoring API ever read. This table is the IMMUTABLE
// archive of every geometry document that has ever been part of a frozen fence set,
// addressed by the SHA-256 of the document itself. Different facts, different
// lifecycles — the same relationship geo_fence_set_versions already has to geo_fences.
//
// It exists because the frozen fence set used to inline every fence's geometry into
// every version's snapshot. A tenant with a hundred fences who edits one of them stored
// a hundred geometry documents to record a one-fence change, and did it again on every
// subsequent edit. Addressing by content stores each distinct geometry once, no matter
// how many versions name it, and makes a version's snapshot a list of references whose
// size is a function of the FENCE COUNT alone.
//
// 🔴 THE HASH IS OVER THE DOCUMENT AS THE DATABASE HANDS IT BACK, NOT AS THE AUTHOR
// WROTE IT. jsonb is not a byte store — it parses each number and reprints it — so the
// request text and the stored text differ, and by an unbounded amount. Canonicalising
// before storing (see model.validateGeoFenceGeometry) is what makes the stored form
// stable, and the mint path hashes what it READ, so the blob and the reference that
// names it are computed from the same bytes by construction. A caller that hashes
// authored text instead would compute an address nothing is stored at.
//
// 🔴 A PLAIN UNIQUE INDEX, NOT THE PARTIAL LIVE-ROWS-ONLY FORM THE TOKEN ENTITIES USE,
// for the same reason geo_fence_set_versions uses one: these rows are never deleted,
// soft or otherwise, so there is no tombstone to keep out of the index and nothing to
// free for reuse. A content address is deliberately never reused — it cannot be, since
// the same address means the same bytes.
type geoFenceGeometryBlob struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128;uniqueIndex:uix_geo_fence_geometry_blobs_tenant_hash,priority:1"`

	// Hash is the lowercase hex SHA-256 of Document — 64 characters, fixed.
	Hash string `gorm:"not null;size:64;uniqueIndex:uix_geo_fence_geometry_blobs_tenant_hash,priority:2"`

	// Document is the canonical geometry envelope (kind + GeoJSON geometry), verbatim.
	Document datatypes.JSON `gorm:"not null"`
}

// NewGeoFenceGeometryBlobsSchema creates the content-addressed geofence geometry archive
// (ADR-078): one row per distinct geometry document a tenant has ever frozen into a
// fence-set version, keyed by the hash of the document.
//
// This is an APPENDED migration. The baseline is frozen — its table list and its
// snapshot structs stay exactly as they are, and this table is created here, after it,
// which is the same position a fresh install and an upgraded one both put it in.
// Physical order is part of what the golden schema diff compares.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so
// this must be individually re-runnable. AutoMigrate is: it creates a table only when it
// is absent and adds only missing columns, and CREATE UNIQUE INDEX is emitted with IF
// NOT EXISTS. Nothing here depends on a statement before it.
//
// # The backfill this migration said was unnecessary — see NewGeoFenceSnapshotBackfill
//
// 🔴 THIS SECTION USED TO SAY NO BACKFILL WAS POSSIBLE TO WANT, because pre-GA an existing
// instance is recreated rather than migrated. That reasoning is true of a BASELINE and false
// of an APPENDED migration, which is what this one is: v0.11.0 and v0.12.0 were each reached
// with `helm upgrade` and the published upgrade guide promises it. So there ARE historical
// snapshots, they inline their geometry, and decoding one into the reference form yields an
// EMPTY hash that no document can be archived under — which kills geofence evaluation on an
// upgraded instance until somebody edits a fence. NewGeoFenceSnapshotBackfill rewrites them.
//
// Worth stating twice over because migration-diff compares pg_dump --schema-only and CANNOT
// see rows: a migration that backfills nothing and one that backfills wrongly look identical
// to it, and so did this table with every historical snapshot still pointing nowhere.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it directly
// with no exemption to register. That is checked by the coverage gate that runs alongside
// migration-diff verify, not asserted here.
func NewGeoFenceGeometryBlobsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260824000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&geoFenceGeometryBlob{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"geo_fence_geometry_blobs"})
		},
	}
}
