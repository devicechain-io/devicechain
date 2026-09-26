// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The erasure fence's per-transaction memo, against a real PostgreSQL server, where READ
// COMMITTED is real and a fence planted by another session is visible to a running
// transaction's next statement.
//
// It pins the one behaviour the memo changes ON PURPOSE, so that nobody quietly "fixes" it
// back or widens it: a transaction whose first write for a tenant preceded the plant keeps
// writing for that tenant and commits, while every transaction begun after the plant is
// refused — and the rows the first one committed are ordinary rows the purge's sweep and
// residual scan see. What makes that enough is the purge's settle window, which a sweep
// that deletes rows restarts; that property is pinned in user-management's coordinator
// tests (TestAStoreGoingDirtyRestartsTheSettleWindow), not here.
//
// It lives in package tenantpurge_test because it needs both rdb and tenantpurge, and
// tenantpurge imports rdb.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./tenantpurge/ -v
package tenantpurge_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// memoWidget is a tenant-scoped row; the migration below creates its table.
type memoWidget struct {
	ID uint `gorm:"primaryKey"`
	rdb.TenantScoped
	Name string
}

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newMemoArea runs a one-table functional area's real initialization — the production
// callback chain, the fence pool, the core-owned tables — against the test server.
func newMemoArea(t *testing.T) *rdb.RdbManager {
	t.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		t.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"),
		itEnv("DC_IT_PGPASSWORD", "postgres")
	const instance = "tpfencememo"
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		t.Fatalf("create the instance database: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "fence-memo"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20260925000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&memoWidget{}) },
		}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("initialize the area on the real server: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return mgr
}

func TestAMemoisedTransactionCommitsPastAFencePlantedMidway(t *testing.T) {
	mgr := newMemoArea(t)
	counter := rdbtest.NewStatementCounter(rdb.FenceTable)
	db := mgr.Database.Session(&gorm.Session{Logger: counter})
	// A tenant of its own per run, so a fence or a row a previous run left cannot decide
	// this one.
	tenant := fmt.Sprintf("memo%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	sys := core.WithSystemContext(context.Background())

	plan, err := tenantpurge.Classify(sys, mgr.Database)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}

	// 1. A transaction writes for the tenant: one fence read, clear.
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	counter.Reset()
	if err := tx.Create(&memoWidget{Name: "before"}).Error; err != nil {
		t.Fatalf("the first write: %v", err)
	}
	if _, fence := counter.Counts(); fence != 1 {
		t.Fatalf("the first write made %d fence read(s); want 1", fence)
	}

	// 2. The purge plants the fence on ANOTHER connection, and commits it.
	now := time.Now().UTC()
	if _, err := tenantpurge.PlantFence(sys, mgr.Database, plan, tenant, now, now, nil); err != nil {
		t.Fatalf("plant: %v", err)
	}

	// 3. The same transaction writes again: admitted on its first answer, with no read.
	counter.Reset()
	if err := tx.Create(&memoWidget{Name: "after"}).Error; err != nil {
		t.Fatalf("the memoised write after the plant was refused: %v — the memo is not in effect, "+
			"or it has been narrowed; either way this test's premise changed", err)
	}
	if _, fence := counter.Counts(); fence != 0 {
		t.Fatalf("the second write made %d fence read(s); want 0", fence)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	committed = true

	// 4. Every transaction begun after the plant is refused.
	if err := db.WithContext(ctx).Create(&memoWidget{Name: "late"}).Error; !errors.Is(err, rdb.ErrTenantPurged) {
		t.Fatalf("a write begun after the plant returned %v; want ErrTenantPurged", err)
	}

	// 5. The committed rows are ordinary rows the sweep deletes and the residual scan sees.
	res, err := tenantpurge.Sweep(sys, mgr.Database, plan, tenant, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Rows != 2 {
		t.Fatalf("the sweep deleted %d row(s); want the memoised transaction's 2", res.Rows)
	}
	residue, err := tenantpurge.Residue(sys, mgr.Database, plan, tenant)
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if residue.Rows != 0 {
		t.Fatalf("the residual scan found %d row(s) after the sweep; want 0", residue.Rows)
	}
}
