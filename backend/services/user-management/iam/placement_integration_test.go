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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// 🔴 Purge completion and ClearTenantOverride take their locks in ONE order: config row, then the
// pin. Completion used to delete the pin first and lock the config row after, the inverse, so a
// clear running against a completing purge deadlocked (40P01) and one of the two failed.
//
// The interleaving is forced rather than raced. A second connection plays ClearTenantOverride's
// first step by taking the config row, completion starts, and only then does that connection
// perform the step that comes second (the pin delete). Under the old order completion already
// holds the pin row, so the two wait on each other and Postgres aborts one; under the right
// order completion is still waiting for the config row, holds nothing the other needs, and both
// finish.
func TestCompleteTenantPurgeAndClearOverrideDoNotDeadlock(t *testing.T) {
	s := newPlacementPostgres(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	tier := seedTier(t, s, "dl-tier")
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "dl", TierID: tier.ID}))
	require.NoError(t, s.SetTenantOverride(ctx, "dl", 3))

	tenant, err := s.TenantByToken(ctx, "dl")
	require.NoError(t, err)
	epoch := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.BeginTenantPurge(ctx, tenant, epoch))
	tenant, err = s.TenantByToken(ctx, "dl")
	require.NoError(t, err)
	rec, err := s.EnsurePurgeRecord(ctx, "dl", epoch)
	require.NoError(t, err)

	// ClearTenantOverride's first step, on its own connection.
	other := s.sys(ctx).Begin()
	require.NoError(t, other.Error)
	defer other.Rollback()
	var cfg PartitionConfig
	require.NoError(t, other.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cfg, partitionConfigID).Error)

	completed := make(chan error, 1)
	go func() { completed <- s.CompleteTenantPurge(ctx, tenant, rec, 1, time.Now().UTC()) }()
	// Wait until completion is observably BLOCKED on the config row. Under the old order the pin
	// delete has already run by then (it precedes the lock), so the interleaving is the one that
	// deadlocks; a fixed sleep could let the other side win the race and pass for the wrong reason.
	require.Eventually(t, func() bool {
		var waiting int64
		err := s.sys(ctx).Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid() AND query ILIKE '%iam_partition_config%'`).
			Scan(&waiting).Error
		return err == nil && waiting > 0
	}, 15*time.Second, 25*time.Millisecond, "purge completion never blocked on the config row")

	// ... and its second step: the pin delete, then commit.
	del := other.Where("tenant_token = ?", "dl").Delete(&TenantVPartitionOverride{})
	require.NoError(t, del.Error, "ClearTenantOverride's pin delete was aborted: lock order inversion")
	require.NoError(t, other.Model(&PartitionConfig{}).Where("id = ?", partitionConfigID).UpdateColumn("generation", gorm.Expr("generation + 1")).Error)
	require.NoError(t, other.Commit().Error)

	select {
	case err := <-completed:
		require.NoError(t, err, "purge completion was aborted: lock order inversion")
	case <-time.After(15 * time.Second):
		t.Fatal("purge completion never finished")
	}
	var pins int64
	require.NoError(t, s.sys(ctx).Model(&TenantVPartitionOverride{}).Where("tenant_token = ?", "dl").Count(&pins).Error)
	require.Zero(t, pins)
}
