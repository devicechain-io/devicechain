// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"

	"gorm.io/gorm"
)

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
	table := stmt.Table
	name := "uix_" + table + "_tenant_token"
	sql := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (tenant_id, token) WHERE deleted_at IS NULL",
		stmt.Quote(name), stmt.Quote(table),
	)
	return tx.Exec(sql).Error
}
