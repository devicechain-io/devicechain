// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newMigratedDB runs the whole chain over a throwaway SQLite database. It walks
// Migrations calling Migrate directly, which is what the chain's DDL and DML need; the
// ID bookkeeping is gormigrate's and is not what these tests are about.
func newMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	for _, m := range Migrations {
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	return db
}

// 🔴 THE ROWS. This migration changes no DDL, so migration-diff cannot see it at all; only
// a test that seeds the empty endpoint an earlier release wrote and reads it back can.
func TestTheEndpointMigrationConvertsEmptyEndpointsToNull(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Exec(`INSERT INTO ai_providers (token, kind, endpoint, model, enabled)
		VALUES ('defaulted', 'anthropic', '', 'claude', true),
		       ('proxied', 'anthropic', 'https://proxy.example.invalid', 'claude', true),
		       ('already', 'anthropic', NULL, 'claude', true)`).Error)

	require.NoError(t, NewProviderEndpointNullMigration().Migrate(db))

	read := func(token string) *string {
		var endpoint *string
		require.NoError(t, db.Raw("SELECT endpoint FROM ai_providers WHERE token = ?", token).Scan(&endpoint).Error)
		return endpoint
	}
	require.Nil(t, read("defaulted"), "an empty endpoint must be NULL after the migration")
	require.Nil(t, read("already"))
	proxied := read("proxied")
	require.NotNil(t, proxied, "a real endpoint override must not be touched")
	require.Equal(t, "https://proxy.example.invalid", *proxied)

	// Re-runnable, as the chain requires.
	require.NoError(t, NewProviderEndpointNullMigration().Migrate(db))
	require.Equal(t, "https://proxy.example.invalid", *read("proxied"))
}

// Unimplemented must fail loudly: the rollback refuses rather than inventing empty strings.
func TestTheEndpointMigrationRefusesRollback(t *testing.T) {
	db := newMigratedDB(t)
	require.Error(t, NewProviderEndpointNullMigration().Rollback(db))
}
