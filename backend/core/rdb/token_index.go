// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// CreatePartialUniqueIndex creates (idempotently) a UNIQUE index on the given
// columns of a soft-deletable entity's table, restricted to LIVE rows via
// WHERE deleted_at IS NULL. GORM's struct-tag `unique` index counts soft-deleted
// rows, so a deleted row keeps its slot locked forever and a lookup can still
// match a tombstone; the partial predicate frees the slot on delete and keeps the
// uniqueness invariant scoped to rows that are actually resolvable. GORM cannot
// express a partial index via struct tags, so callers create these explicitly,
// once per model, immediately after AutoMigrate. Valid on Postgres and (the test
// harness) SQLite alike.
//
// 🔴 THE INDEX NAME THIS EMITS AND ITS `WHERE deleted_at IS NULL` PREDICATE ARE
// SCHEMA, AND THEY ARE FROZEN. NewSecretStoreSchema (core/secrets/migration.go) is a
// live caller inside an already-applied migration. Widen the predicate, or change how
// the caller's name reaches the statement, and that migration starts building a
// different index on FRESH installs while every existing database keeps the one it was
// given — from a change that never touched core/secrets. Both then report a clean
// migration and hold different schemas, which is exactly the divergence the "never edit
// an existing migration" rule exists to prevent. The emitted statement is pinned by
// TestPartialUniqueIndexStatementIsFrozen (here) and, end to end through the real
// migration, by TestSecretsHandleIndexIsFrozen (core/secrets). A change to this function
// that moves either assertion is a schema change to an applied migration: the change is
// wrong, not the assertion.
//
// 🔴 A CALLER INSIDE A MIGRATION MUST PASS A LOCALLY-DECLARED SNAPSHOT STRUCT AND AN
// EXPLICIT INDEX NAME — NEVER A LIVE MODEL. `model any` is parsed only to resolve the
// table name and the dialect's quoting; passing a live model puts the migration's output
// under the control of whatever that model becomes later. NewSecretStoreSchema is the
// worked example: an inline `type Secret struct` and the literal
// "uix_secrets_tenant_scope_name".
func CreatePartialUniqueIndex(tx *gorm.DB, model any, name string, columns ...string) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for partial unique index %s: %w", name, err)
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = stmt.Quote(c)
	}
	sql := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE deleted_at IS NULL",
		stmt.Quote(name), stmt.Quote(stmt.Table), strings.Join(quoted, ", "),
	)
	return tx.Exec(sql).Error
}

// CreateTenantTokenIndex installs the per-tenant partial unique index on a
// token-referenced, tenant-scoped, soft-deletable entity's table: a token is unique
// within a tenant among LIVE (non-soft-deleted) rows only. The composite
// (tenant_id, token) makes tokens per-tenant — a global UNIQUE(token) collided across
// tenants, and the failed insert leaked that the token existed in another tenant — and
// the WHERE deleted_at IS NULL predicate excludes soft-deleted rows, so a token frees on
// delete instead of staying locked forever (ADR-042 P1).
//
// 🔴 THIS IS A TEST FIXTURE. IT IS NOT THE MIGRATION PATH AND MUST NOT BE CALLED FROM
// ONE — it has no non-test callers, and that is the intended state. An index NAME and a
// WHERE predicate are schema; a migration that sources them from another module starts
// building something different on fresh installs the day that module changes, while
// every existing database keeps the old shape, from a change that never touched the
// service. Every service needing this index therefore declares its own copy inside its
// own migration, against its own snapshot struct. Those copies are deliberate, and their
// comments say so; do not "clean them up" into a call to this function.
//
// What this IS for is the other half of that arrangement. A service's unit tests run
// against SQLite with no migration chain, so the index a duplicate-token assertion
// depends on does not exist unless the test installs it — and installing it from one
// place keeps every such test honest about the shape the migration builds.
//
// The name it derives, uix_<table>_tenant_token, matches what those migrations build,
// because gorm hands back an UNQUALIFIED stmt.Table even under the production
// NamingStrategy's "<functional-area>." TablePrefix — the qualified form goes to
// stmt.TableExpr instead. Nothing here would notice a gorm release changing that: these
// fixtures only ever run on SQLite, where IsUniqueViolation matches on COLUMNS rather
// than on the index name, so a fixture index that started carrying the prefix would be
// invisible to every test that installs one. The caller that WOULD notice is
// CreatePartialUniqueIndex inside the secrets migration, whose ON clause quotes
// stmt.Table: a prefix left in place there collapses into one dotted identifier and the
// migration fails with "relation does not exist". So the property is pinned by
// TestFixtureIndexNameIgnoresTheSchemaPrefix — to surface a gorm change as a test
// failure on the bump, rather than as a migration that cannot find its own table.
func CreateTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-token index: %w", err)
	}
	name := "uix_" + stmt.Table + "_tenant_token"
	return CreatePartialUniqueIndex(tx, model, name, "tenant_id", "token")
}

// CreateTenantExternalIdIndex installs the per-tenant partial unique index on an
// entity's optional external id (ADR-049): an externalId, WHEN PRESENT, is unique
// within a tenant among LIVE rows. It is the business-id analog of
// CreateTenantTokenIndex, with one extra predicate — external_id IS NOT NULL — so the
// many rows carrying no external id never collide with each other (NULLs are excluded
// from the uniqueness set entirely, on Postgres and SQLite alike). That second predicate
// cannot be expressed through CreatePartialUniqueIndex, which hardcodes only
// deleted_at IS NULL, so the statement is built here directly.
//
// 🔴 THIS IS A TEST FIXTURE, ON THE SAME TERMS AS CreateTenantTokenIndex: not the
// migration path, no non-test callers, and each service's migration declares its own
// copy against its own snapshot struct. See that function's comment for the reasoning.
func CreateTenantExternalIdIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-external-id index: %w", err)
	}
	name := "uix_" + stmt.Table + "_tenant_external_id"
	sql := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s, %s) "+
			"WHERE deleted_at IS NULL AND external_id IS NOT NULL",
		stmt.Quote(name), stmt.Quote(stmt.Table),
		stmt.Quote("tenant_id"), stmt.Quote("external_id"),
	)
	return tx.Exec(sql).Error
}
