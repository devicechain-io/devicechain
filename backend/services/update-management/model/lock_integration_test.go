// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The per-tenant quota lock, asked of a real PostgreSQL server.
//
// The SQLite tests prove the lock is requested before the held sum is read, but the lock
// itself is a no-op there. What it prevents is only observable with real concurrency: N
// creates that each read the same sum and each insert. Run against a throwaway server
// (hack/integration-tests.sh does this in CI):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Lock -v
package model

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
)

func itServer(t *testing.T) (string, int) {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err, "DC_IT_PGPORT must be a port number")
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	return host, port
}

// Twenty concurrent creates of 100 bytes against a 1000-byte ceiling admit exactly ten:
// the lock makes each create read a sum that includes every create committed before it.
func TestConcurrentCreatesNeverOvershootTheTenantCeilingOnPostgres(t *testing.T) {
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "updatelockit"
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
	// The instance id is fixed, so a server that ran this before still holds its rows.
	require.NoError(t, mgr.DB(core.WithSystemContext(ctx)).Exec("TRUNCATE TABLE artifacts").Error)

	store, err := blob.New(ctx, blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, instance)
	require.NoError(t, err)
	api := NewApi(mgr, store, Limits{MaxArtifactBytes: 1000, ConfiguredMaxArtifactBytes: 1000, MaxTenantArtifactBytes: 1000})
	api.Profiles = &fakeProfiles{known: map[string][]string{"acme": {"gateway"}}}

	const n = 20
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := validRequest("fw-"+strconv.Itoa(i), []byte("x"))
			r.Version, r.SizeBytes = strconv.Itoa(i), 100
			_, err := api.CreateArtifact(tenantCtx("acme"), r)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		}
	}
	require.Equal(t, 10, admitted, "the ceiling admits exactly ten 100-byte artifacts, however they interleave")
}
