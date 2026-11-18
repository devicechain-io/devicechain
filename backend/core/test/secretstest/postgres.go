// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package secretstest provides isolated Postgres stores for cross-replica tests.
package secretstest

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Stores opens independent pools in one fresh schema, with the real migration
// and tenant callbacks. Missing Postgres is a failure, never a skip.
func Stores(t *testing.T) (*gorm.DB, [2]secrets.SecretStore) {
	t.Helper()
	env := func(k, fallback string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return fallback
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable connect_timeout=5", env("DC_IT_PGHOST", "localhost"), env("DC_IT_PGPORT", "5432"), env("DC_IT_PGUSER", "postgres"), env("DC_IT_PGPASSWORD", "devicechain"))
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := admin.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	schema := fmt.Sprintf("secrets_it_%d", time.Now().UnixNano())
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	kp, err := secrets.NewInstanceKeyProvider(make([]byte, 32))
	require.NoError(t, err)
	var stores [2]secrets.SecretStore
	for i := range stores {
		db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{})
		require.NoError(t, err)
		p, err := db.DB()
		require.NoError(t, err)
		p.SetMaxOpenConns(1)
		p.SetMaxIdleConns(1)
		t.Cleanup(func() { require.NoError(t, p.Close()) })
		require.NoError(t, rdb.RegisterTenantScoping(db))
		if i == 0 {
			require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
		}
		stores[i] = secrets.NewStore(db, kp)
	}
	return admin, stores
}
