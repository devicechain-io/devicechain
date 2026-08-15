// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewListOrderIndexesSchema adds one covering index per high-cardinality table in this
// area for the ORDER BY that rdb.ListOf now injects into every paged read.
//
// # Why these four and not all of them
//
// Every model in the platform now declares a DefaultOrder(), which means ~35 tables
// acquired an ORDER BY no index supports. For most of them that is fine: they are
// control-plane-small (device types, area types, relationship types — tens of rows per
// tenant), and a sort over tens of rows is free. Deep OFFSET paging is O(offset) whether
// or not an index supplies the order, so an index does not rescue a caller asking for
// page 900 either. The four tables here are the ones whose PER-TENANT cardinality is
// plausibly large — devices is the fleet itself; entity_relationships is the typed graph
// over that fleet, so it grows FASTER than the fleet does; entity_attributes carries one
// row per (entity, scope, key) and is therefore a multiple of the fleet again; geo_fences
// is authored rather than ingested but is read on the DETECT path. On those, an
// unsupported sort is a full scan plus a sort of the tenant's entire live set, on the
// FIRST page, which is the page everybody reads.
//
// 🔴 THIS MIGRATION DECLARES NO SNAPSHOT STRUCT, and that is deliberate rather than an
// omission of the rule (compare command-delivery's NewTenantStatusIndexSchema, which
// records the same reasoning). What it touches is four indexes over named columns, and
// every column name is written literally in the DDL below — that IS the snapshot,
// complete and frozen. A struct would be worse: gorm derives a table name from the TYPE
// name, so a second shape mapping to `devices` needs a TableName(), and a TableName()
// bypasses this area's TablePrefix — the index would then be created against an
// unqualified table resolved through search_path. AutoMigrate on a whole-row snapshot
// would also re-assert columns this change has no business touching.
//
// # Column order is the DefaultOrder(), verbatim, behind tenant_id
//
//	devices               created_at DESC, token ASC   -> (tenant_id, created_at DESC, token ASC)
//	entity_relationships  created_at DESC, token ASC   -> (tenant_id, created_at DESC, token ASC)
//	entity_attributes     created_at DESC, id DESC     -> (tenant_id, created_at DESC, id DESC)
//	geo_fences            created_at DESC, token ASC   -> (tenant_id, created_at DESC, token ASC)
//
// entity_attributes is the odd one and it is not a typo: that table has no token — an
// attribute is addressed by its natural key (entity, scope, key) — so its tiebreak is
// `id DESC`, and the index has to match that, direction included.
//
// tenant_id LEADS every one of them because the tenant predicate is ALWAYS present: the
// rdb tenant-scope callback rejects a tenant-scoped query with no tenant in context, so
// there is no plan where these tables are read across tenants. Leading with created_at
// instead would interleave every tenant's rows and make one large tenant's history the
// thing every other tenant's first page scans past.
//
// The trailing directions matter more than they look. Postgres can scan a btree
// backwards, so an index matching the sort exactly or matching its exact REVERSE both
// serve it — but a mismatched INTERLEAVING (say `created_at DESC, id ASC` against a sort
// wanting `created_at DESC, id DESC`) serves neither, and the planner silently falls back
// to the sort it was supposed to avoid. Each line below is the model's string, in order,
// with its own direction.
//
// # Partial on deleted_at IS NULL
//
// All four models embed gorm.Model, so gorm appends `deleted_at IS NULL` to every read
// ListOf issues. The predicate keeps tombstones out of the index (they are never listed)
// and, more importantly, lets the scan stay index-only: a column that is neither in the
// index nor implied by its predicate has to be rechecked against the heap, one fetch per
// row returned. The area's existing hand-written indexes — ix_entity_attributes_facet_lookup,
// uix_devices_tenant_token — are partial for exactly this reason, and these match them.
//
// # Re-runnability, and why not CONCURRENTLY
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so
// each statement must be individually re-runnable; IF NOT EXISTS is what makes the replay
// a no-op. CREATE INDEX CONCURRENTLY is deliberately NOT used, and not merely for lack of
// precedent in this tree: a CONCURRENTLY build that fails leaves the index behind marked
// INVALID, and the replay's `IF NOT EXISTS` then sees a same-named relation and skips it
// forever — a planner-ignored index, permanently recorded as applied. A plain build takes
// a table-level lock against writes for the duration, which pre-GA (an instance is
// recreated, not migrated) is a cost measured in the seconds a fresh install spends on
// empty tables.
//
// # Naming
//
// ix_<table>_list_order, following the ix_ prefix this area's hand-written indexes
// already use (ix_entity_attributes_facet_*). Not the gorm-derived
// idx_device-management_<table>_<col> form: that shape belongs to indexes AutoMigrate
// emits from a tag, and these are authored here.
func NewListOrderIndexesSchema() *gormigrate.Migration {
	// Each entry is one table's ListOf order, expressed as an index. The DDL is
	// written out per table rather than generated from a column list, because an index
	// name, its target table and its predicate are all schema.
	statements := []string{
		`CREATE INDEX IF NOT EXISTS ix_devices_list_order ` +
			`ON "device-management".devices (tenant_id, created_at DESC, token ASC) ` +
			`WHERE deleted_at IS NULL;`,
		`CREATE INDEX IF NOT EXISTS ix_entity_relationships_list_order ` +
			`ON "device-management".entity_relationships (tenant_id, created_at DESC, token ASC) ` +
			`WHERE deleted_at IS NULL;`,
		`CREATE INDEX IF NOT EXISTS ix_entity_attributes_list_order ` +
			`ON "device-management".entity_attributes (tenant_id, created_at DESC, id DESC) ` +
			`WHERE deleted_at IS NULL;`,
		`CREATE INDEX IF NOT EXISTS ix_geo_fences_list_order ` +
			`ON "device-management".geo_fences (tenant_id, created_at DESC, token ASC) ` +
			`WHERE deleted_at IS NULL;`,
	}

	return &gormigrate.Migration{
		ID: "20260815000000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Rollback drops all four. Losing them leaves every table intact and every
		// list returning the same rows in the same order — the reads just sort for
		// themselves again. Re-runnable in this direction too, which the
		// UseTransaction:false doctrine requires in both.
		Rollback: func(tx *gorm.DB) error {
			for _, name := range []string{
				"ix_geo_fences_list_order",
				"ix_entity_attributes_list_order",
				"ix_entity_relationships_list_order",
				"ix_devices_list_order",
			} {
				if err := tx.Exec(`DROP INDEX IF EXISTS "device-management".` + name + `;`).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
