// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The upsert guard, asked of a real PostgreSQL server: how it treats a DO UPDATE ... WHERE
// that is false for a conflicting row, and what RowsAffected it reports.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run TenantUpsert -v
package rdb

import (
	"context"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

func upsertItDB(t *testing.T, instance string) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "tenant-upsert"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20261009000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(upsertModels()...) },
		}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres",
				"sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return mgr.Database
}

func TestTenantUpsertMatrixPostgres(t *testing.T) {
	runTenantUpsertMatrix(t, upsertItDB(t, "rdbtenantupsert"))
}

func TestTenantUpsertNegativeControlPostgres(t *testing.T) {
	db := upsertItDB(t, "rdbtenantupsertneg")
	sabotageUpsertGuard(t, db)
	runUpsertNegativeControl(t, db)
}
