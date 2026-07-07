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

// CreateTenantTokenIndex creates (idempotently) the per-tenant partial unique index
// on a token-referenced, tenant-scoped, soft-deletable entity's table: a token is
// unique within a tenant among LIVE (non-soft-deleted) rows only.
//
// This is the storage half of ADR-042 P1. It replaces the global UNIQUE(token) that
// rdb.TokenReference no longer declares as a struct tag, because that global unique:
//   - collided across tenants (tenant B could not hold a token tenant A held, and the
//     failed insert leaked that the token existed in another tenant), and
//   - counted soft-deleted rows, so deleting an entity locked its token forever.
//
// The composite (tenant_id, token) makes tokens per-tenant; the WHERE deleted_at IS
// NULL predicate excludes soft-deleted rows so a token frees on delete. GORM cannot
// express this via struct tags on the shared embedded structs (one shared index name
// would collide across a service's many token tables and would wrongly apply to
// non-token TenantScoped entities), so services create it explicitly.
//
// Call once per token model, immediately after AutoMigrate, in the service's schema
// migration. The index name is derived from the table so it is unique within the
// service's schema. Valid on Postgres and (the test harness) SQLite alike.
func CreateTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-token index: %w", err)
	}
	name := "uix_" + stmt.Table + "_tenant_token"
	return CreatePartialUniqueIndex(tx, model, name, "tenant_id", "token")
}

// CreateTenantExternalIdIndex creates (idempotently) the per-tenant partial unique
// index on an entity's optional external id (ADR-049): an externalId, WHEN PRESENT,
// is unique within a tenant among LIVE rows. It is the business-id analog of
// CreateTenantTokenIndex, with one extra predicate — external_id IS NOT NULL — so
// the many rows that carry no external id never collide with each other (NULLs are
// excluded from the uniqueness set entirely, on Postgres and SQLite alike). Because
// that second predicate cannot be expressed through CreatePartialUniqueIndex (which
// hardcodes only deleted_at IS NULL), the index SQL is built here directly.
//
// Call once per external-id model, immediately after AutoMigrate, in the service's
// schema migration. Valid on Postgres and (the test harness) SQLite alike.
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
