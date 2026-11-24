// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
)

// The upsert that keeps the newer observation is the kind of statement that renders differently on
// PostgreSQL than on SQLite (a conflict clause with a WHERE, the table qualifier, the tenant predicate
// the guard adds), so it is asked of a real server, including under concurrency.
func TestInstalledVersionUpsertOnPostgres(t *testing.T) {
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "updateinstalledit"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "update-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	require.NoError(t, mgr.DB(core.WithSystemContext(ctx)).Exec("TRUNCATE TABLE device_installed_components").Error)
	store, err := blob.New(ctx, blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, instance)
	require.NoError(t, err)
	api := NewApi(mgr, store, Limits{MaxArtifactBytes: 1000, MaxTenantArtifactBytes: 1000})

	t0 := time.Now().UTC().Truncate(time.Millisecond)
	acme, globex := tenantCtx("acme"), tenantCtx("globex")
	version := func(c context.Context) string {
		rows, err := api.InstalledComponents(c, "dev-1")
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0].Version
	}
	record := func(c context.Context, v string, at time.Time) {
		require.NoError(t, api.recordInstalled(c, "dev-1", 1, "firmware", v, "", at, InstalledSourceInventory))
	}

	record(acme, "2.0.0", t0.Add(time.Minute))
	record(acme, "1.0.0", t0) // older: ignored
	require.Equal(t, "2.0.0", version(acme))
	record(acme, "9.9.9", t0.Add(time.Minute)) // equal: ignored
	require.Equal(t, "2.0.0", version(acme))
	record(acme, "2.1.0", t0.Add(2*time.Minute)) // newer: wins
	require.Equal(t, "2.1.0", version(acme))
	// Another tenant's device of the same token is its own row.
	record(globex, "7.0.0", t0)
	require.Equal(t, "7.0.0", version(globex))
	require.Equal(t, "2.1.0", version(acme))

	// Concurrent reports with distinct times converge on the newest, whatever the interleaving.
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, api.recordInstalled(acme, "dev-9", 1, "firmware", fmt.Sprintf("v%02d", i), "", t0.Add(time.Duration(i)*time.Second), InstalledSourceInventory))
		}(i)
	}
	wg.Wait()
	rows, err := api.InstalledComponents(acme, "dev-9")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, fmt.Sprintf("v%02d", n-1), rows[0].Version, "the newest observation won, however they interleaved")
}
