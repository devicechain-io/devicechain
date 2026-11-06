// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The placement store on a REAL PostgreSQL, through the real migration chain. Whether a reader can
// see a table that never existed is a property of the server's isolation and row locks, which
// SQLite cannot answer.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run Placement ./iam/ -v
package iam

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/stretchr/testify/require"
)

func newPlacementPostgres(t *testing.T) *Store {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	ctx := context.Background()
	instance := fmt.Sprintf("umpl%d", time.Now().UnixNano())
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "user-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
		}},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return NewStore(mgr)
}

// 🔑 A reader never sees a torn table. The writer keeps one invariant across every change: after
// the k-th change the generation is k and vpartition 0 sits on shard group k. A reader that
// straddled a change (reading the config row, then assignments committed afterwards) would see
// group != generation. Without the config row's FOR SHARE in Placement this fails within a few
// hundred reads under READ COMMITTED.
func TestPlacementNeverReadsATornTableUnderAConcurrentWriter(t *testing.T) {
	s := newPlacementPostgres(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	const changes = 400
	var done atomic.Bool
	var reads, torn atomic.Int64
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !done.Load() {
				table, err := s.Placement(ctx)
				if err != nil {
					t.Errorf("Placement: %v", err)
					return
				}
				reads.Add(1)
				if uint64(table.Groups[0]) != table.Generation {
					torn.Add(1)
				}
			}
		}()
	}
	for k := 1; k <= changes; k++ {
		require.NoError(t, s.AssignVPartition(ctx, 0, k))
	}
	done.Store(true)
	wg.Wait()

	t.Logf("%d reads against %d changes, %d torn", reads.Load(), changes, torn.Load())
	require.Positive(t, reads.Load())
	require.Zero(t, torn.Load(), "a reader saw a placement that never existed")
}

// Two pins racing for the last slot cannot both land: the config row serializes them.
func TestPlacementPinLimitHoldsUnderConcurrentWriters(t *testing.T) {
	s := newPlacementPostgres(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	rows := make([]TenantVPartitionOverride, 0, partition.MaxOverrides-1)
	for i := 0; i < partition.MaxOverrides-1; i++ {
		rows = append(rows, TenantVPartitionOverride{TenantToken: fmt.Sprintf("p%d", i), VPartition: 1})
	}
	require.NoError(t, s.sys(ctx).CreateInBatches(&rows, 500).Error)
	const contenders = 6
	tier := seedTier(t, s, "it-tier")
	for i := 0; i < contenders; i++ {
		require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: fmt.Sprintf("c%d", i), TierID: tier.ID}))
	}
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.SetTenantOverride(ctx, fmt.Sprintf("c%d", i), 2) == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, ok.Load(), "exactly one pin fits the last slot")
	table, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Len(t, table.Overrides, partition.MaxOverrides)
}
