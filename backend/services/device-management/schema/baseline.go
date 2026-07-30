// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary, a live validator or a live constant is a migration whose behaviour changes
// when those do.
import (
	"fmt"
	"strings"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// baselineTables is every table this baseline owns, ordered so a Rollback drops dependents
// before what they reference.
var baselineTables = []string{
	"detection_rule_scope_refs",
	"detection_rules",
	"entity_group_facet_refs",
	"entity_group_memberships",
	"entity_group_versions",
	"entity_groups",
	"facet_keys",
	"entity_attributes",
	"alarms",
	"device_claims",
	"provisioning_profiles",
	"command_definitions",
	"metric_definitions",
	"device_profile_versions",
	"device_profiles",
	"device_credentials",
	"entity_relationships",
	"entity_relationship_types",
	"devices",
	"device_types",
	"areas",
	"area_types",
	"assets",
	"asset_types",
	"customers",
	"customer_types",
}

// tokenIndexModels is every tenant-scoped TOKEN entity, each of which gets the ADR-042 P1
// per-tenant partial unique index on token.
//
// 🔴 THE ABSENCES IN THIS LIST ARE AS DELIBERATE AS THE ENTRIES. A table is missing from it for
// exactly one of two reasons, and neither is an oversight:
//
//   - it has no token, because it is reached through a parent or by a natural key —
//     device_profile_versions, entity_group_versions, entity_group_memberships,
//     entity_group_facet_refs, entity_attributes, facet_keys, device_claims,
//     detection_rule_scope_refs;
//   - it has a token but its uniqueness is expressed differently. There is no such case here;
//     if one is ever added, say so at the call site rather than leaving a silent gap, because a
//     missing entry means two live rows can share a token within a tenant and nothing complains
//     until a lookup returns the wrong one.
var tokenIndexModels = []any{
	&deviceType{}, &device{}, &deviceCredential{},
	&areaType{}, &area{},
	&assetType{}, &asset{},
	&customerType{}, &customer{},
	&entityRelationshipType{}, &entityRelationship{},
	&deviceProfile{}, &metricDefinition{}, &commandDefinition{},
	&provisioningProfile{}, &alarm{}, &detectionRule{}, &entityGroup{},
}

// NewBaselineSchema materializes the whole device-management schema in one step: the registry
// families and the uniform relationship graph (ADR-013), device profiles with their versioned
// metric/command/rule authoring surface (ADR-045/016/043), alarm objects (ADR-057), entity
// attributes and the dynamic entity groups built on them (ADR-061), and provisioning/claim
// (ADR-045).
//
// This REPLACES the former TWENTY-SIX migration chain — by far the largest of the nine areas —
// collapsed pre-GA. Until v1.0.0 there is no released version to upgrade FROM. Equivalence is
// proven by hack/migration-diff.sh verify against the golden captured from that chain: 271
// statements, 26 tables, 95 indexes.
//
// 🔴 FOUR TABLES THE CHAIN CREATED ARE DELIBERATELY NOT CREATED HERE. device_groups,
// asset_groups, customer_groups and area_groups were the per-family group tables, folded into the
// single entity_groups (ADR-061) by a later migration that dropped them. A baseline that
// re-created them would reproduce a schema the chain does not end with — and because they would
// be EXTRA rather than missing, a check that only looked for what it expected would never notice.
//
// 🔴 ONE MIGRATION IN THE CHAIN WAS A BACKFILL AND IS DROPPED, NOT FOLDED.
// definitions_to_profile moved metric and command definitions from the device TYPE onto the
// profile (ADR-045's un-fusing), rewriting rows that already existed. A baseline runs against an
// empty schema where there is nothing to move. Nothing is lost: the columns it wrote to are
// created here in their final position, and the DDL half of that migration is folded in.
//
// The distinction matters because migration-diff CANNOT see it — pg_dump --schema-only captures
// no rows, so dropping a genuine SEED would pass just as quietly. This area has no seed: nothing
// in it is unusable without a row.
//
// 🔴 A SECOND BACKFILL WAS DROPPED, AND IT TOOK A TEST WITH IT. THAT IS A REAL COVERAGE LOSS,
// SO IT IS RECORDED HERE RATHER THAN LEFT TO BE NOTICED LATER.
//
// The command-key-uniqueness migration did two things: it DE-DUPLICATED existing
// (tenant, profile, command_key) rows, then created the unique index over them. Only the second
// half survives here — an empty schema has nothing to de-duplicate. Its test
// (command_key_unique_migration_test.go) went with it, and that test said, correctly, that the
// migration-diff harness could not prove what it proved: the harness runs a chain against an
// empty database, so the de-duplication was a no-op there and only the CREATE INDEX was ever
// exercised. It seeded real duplicates and asserted the migration survived them.
//
// Nothing is lost for a FRESH install, which is the only kind this baseline serves. What is lost
// is the guarantee for a pre-GA database that already holds duplicates — and that database is
// exactly the one the pre-GA convention says is recreated rather than migrated. If that
// convention ever stops holding, this is one of the places that has to be revisited.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this baseline, it
// is recreated (dcctl destroy + bootstrap).
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and do not
// "update" the snapshot types it builds from — see baseline_snapshot.go. Anything appended must
// be individually re-runnable, since migrations run with UseTransaction:false and replay from the
// top after a failure.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot*.go, never the live models. Parents
			// before children so the inline FOREIGN KEYs resolve.
			if err := tx.AutoMigrate(
				&deviceType{}, &device{}, &deviceCredential{},
				&areaType{}, &area{},
				&assetType{}, &asset{},
				&customerType{}, &customer{},
				&entityRelationshipType{}, &entityRelationship{},
				&deviceProfile{}, &deviceProfileVersion{},
				&metricDefinition{}, &commandDefinition{},
				&provisioningProfile{}, &deviceClaim{},
				&alarm{},
				&detectionRule{}, &detectionRuleScopeRef{},
				&entityAttribute{}, &facetKeyDef{},
				&entityGroup{}, &entityGroupVersion{},
				&entityGroupMembership{}, &entityGroupFacetRef{},
			); err != nil {
				return err
			}

			// The two pinned partial shapes, applied AFTER the real ones exactly as the chain
			// applied them, so the duplicate unprefixed indexes they create are reproduced
			// rather than silently dropped. See the note on alarmContributorsPinned.
			if err := tx.AutoMigrate(&alarmContributorsPinned{}, &entityGroupsPinned{}); err != nil {
				return err
			}

			// ADR-042 P1: a token is unique within a tenant among LIVE rows only.
			for _, m := range tokenIndexModels {
				if err := createTenantTokenIndex(tx, m); err != nil {
					return err
				}
			}

			// ADR-049: an external id, WHEN PRESENT, is unique within a tenant among live rows.
			// The second predicate is what keeps the many rows carrying no external id from
			// colliding with each other, and it is why this cannot go through the token helper.
			if err := createPartialUniqueIndexWhere(tx, &device{}, "uix_devices_tenant_external_id",
				"deleted_at IS NULL AND external_id IS NOT NULL", "tenant_id", "external_id"); err != nil {
				return err
			}

			// The remaining partial unique indexes, each over LIVE rows only. GORM's struct-tag
			// `unique` counts soft-deleted rows, so a tombstone would keep its slot locked
			// forever and a lookup could still match it.
			for _, idx := range []struct {
				model   any
				name    string
				columns []string
			}{
				// ADR-014: the credential resolve lookup. Scoped per-tenant — resolution always
				// runs under a tenant — which also avoids a cross-tenant credential-id
				// existence oracle.
				{&deviceCredential{}, "idx_device_credential_lookup", []string{"tenant_id", "credential_type", "credential_id"}},
				// One live alarm per (originator, alarm key): this is what makes the
				// integrator's upsert a clean conflict target.
				{&alarm{}, "uix_alarm_originator_key", []string{"tenant_id", "originator_type", "originator_id", "alarm_key"}},
				// A command key is unique per profile within a tenant.
				{&commandDefinition{}, "uix_command_definitions_tenant_profile_key", []string{"tenant_id", "device_profile_id", "command_key"}},
				// A facet key is addressed by its natural key, with no token.
				{&facetKeyDef{}, "uix_facet_keys_tenant_member_key", []string{"tenant_id", "member_type", "attr_key"}},
			} {
				if err := createPartialUniqueIndexWhere(tx, idx.model, idx.name, "deleted_at IS NULL", idx.columns...); err != nil {
					return err
				}
			}

			// The two facet-lookup indexes that back selector evaluation (ADR-061): one for
			// existence/membership by key, one for value matching. Both partial on live rows so
			// they stay small, and both written out because gorm cannot express a partial index
			// by tag.
			for _, stmt := range []string{
				`CREATE INDEX IF NOT EXISTS ix_entity_attributes_facet_lookup ` +
					`ON "device-management".entity_attributes (tenant_id, entity_type, scope, attr_key, entity_id) ` +
					`WHERE deleted_at IS NULL;`,
				`CREATE INDEX IF NOT EXISTS ix_entity_attributes_facet_value ` +
					`ON "device-management".entity_attributes (tenant_id, entity_type, scope, attr_key, value) ` +
					`WHERE deleted_at IS NULL;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, baselineTables)
		},
	}
}

// dropTables drops the named tables, ignoring absent ones.
func dropTables(tx *gorm.DB, tables []string) error {
	for _, table := range tables {
		if err := tx.Migrator().DropTable(table); err != nil {
			return err
		}
	}
	return nil
}

// createTenantTokenIndex creates the per-tenant partial unique index on a token-referenced,
// tenant-scoped, soft-deletable table.
//
// 🔴 THIS AND createPartialUniqueIndexWhere ARE DELIBERATE COPIES of the rdb helpers, NOT an
// oversight. Calling core would put this migration's output — index NAMES and WHERE predicates,
// both of which are schema — under the control of code in another module. The day core renames an
// index or widens a predicate, this already-applied migration starts creating something different
// on fresh installs while every existing database keeps the old one, from a PR that never touched
// device-management. The snapshot rule outranks DRY inside a migration. Do not "clean this up".
func createTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-token index: %w", err)
	}
	bare := stmt.Table
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	return createPartialUniqueIndexWhere(tx, model, "uix_"+bare+"_tenant_token",
		"deleted_at IS NULL", "tenant_id", "token")
}

// createPartialUniqueIndexWhere creates a UNIQUE index over the rows matching where.
func createPartialUniqueIndexWhere(tx *gorm.DB, model any, name, where string, columns ...string) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for partial unique index %s: %w", name, err)
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = stmt.Quote(c)
	}
	return tx.Exec(fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE %s",
		stmt.Quote(name), stmt.Quote(stmt.Table), strings.Join(quoted, ", "), where,
	)).Error
}
