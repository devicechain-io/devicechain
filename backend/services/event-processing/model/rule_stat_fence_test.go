// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The erasure fence on RecordFire, the one write DETECT makes per fire: what it costs,
// by value, and that it refuses a fenced tenant.
//
// 🔴 THIS IS THE ONLY HOT-PATH ROW GUARDING THE SECOND TENANT SPELLING. RuleStat carries
// its tenant in a plain `Tenant` column, not the `TenantId` of rdb.TenantScoped, and the
// fence classifies it only because tenantField recognises both. device-state's and
// event-management's fence tests all exercise TenantId; a change that taught the fence
// only that spelling would leave them green and THIS area unfenced — so the refusal test
// below is not redundant with theirs. Do not delete it as a duplicate.

// newFencedRuleStatStore builds the store over sqlite with the PRODUCTION callback chain,
// in production order (core/rdb/postgres.go): scoping, token grammar, audit journal,
// erasure fence. The database is shared-cache and named per test, so a fence planted
// outside a transaction lands in the database the fire reads.
func newFencedRuleStatStore(t *testing.T) (*RuleStatStore, *gorm.DB, *rdbtest.StatementCounter) {
	t.Helper()
	counter := rdbtest.NewStatementCounter(rdb.FenceTable)
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := db.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := rdb.RegisterAuditJournal(db); err != nil {
		t.Fatalf("register audit journal: %v", err)
	}
	if err := rdb.RegisterTenantFence(db); err != nil {
		t.Fatalf("register erasure fence: %v", err)
	}
	if err := db.AutoMigrate(&rdb.PurgedTenant{}, &rdb.AuditEvent{}, &RuleStat{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRuleStatStore(&rdb.RdbManager{Database: db}), db, counter
}

func fenceSysDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{NewDB: true}).WithContext(dccore.WithSystemContext(context.Background()))
}

const (
	fenceStatTenant = "acme"
	fenceStatRule   = "acme/thermostat@1/overheat"
)

func TestFenceCostOfRecordFire(t *testing.T) {
	s, _, counter := newFencedRuleStatStore(t)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	// The first fire inserts and a later one takes the ON CONFLICT update, but both are
	// ONE upsert statement behind ONE fence read.
	for i, at := range []time.Time{t0, t0.Add(time.Minute)} {
		counter.Reset()
		if err := s.RecordFire(context.Background(), fenceStatRule, fenceStatTenant, at, "raised"); err != nil {
			t.Fatalf("fire %d: %v", i+1, err)
		}
		if all, fence := counter.Counts(); all != 2 || fence != 1 {
			t.Errorf("fire %d made %d statements with %d fence reads; want 2 with 1", i+1, all, fence)
		}
	}
	if got := load(t, s, fenceStatTenant, fenceStatRule).FireCount; got != 2 {
		t.Errorf("FireCount = %d after two fires, want 2", got)
	}
}

func TestRecordFireForAFencedTenantIsRefused(t *testing.T) {
	s, db, counter := newFencedRuleStatStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := fenceSysDB(db).Create(&rdb.PurgedTenant{Token: fenceStatTenant, Epoch: now, PlantedAt: now}).Error; err != nil {
		t.Fatalf("plant a fence: %v", err)
	}
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	counter.Reset()
	err := s.RecordFire(context.Background(), fenceStatRule, fenceStatTenant, t0, "raised")
	if !errors.Is(err, rdb.ErrTenantPurged) {
		t.Fatalf("RecordFire for a fenced tenant returned %v; want ErrTenantPurged", err)
	}
	// The fence read alone: it refuses before the upsert is sent.
	if all, fence := counter.Counts(); all != 1 || fence != 1 {
		t.Errorf("a refused fire made %d statements with %d fence reads; want 1 with 1", all, fence)
	}
	got, err := s.LoadByIDs(context.Background(), fenceStatTenant, []string{fenceStatRule})
	if err != nil {
		t.Fatalf("LoadByIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a refused fire left a stat row: %+v", got)
	}

	// Negative control: lift the fence and the same fire lands, and the same read sees it.
	if err := fenceSysDB(db).Model(&rdb.PurgedTenant{}).Where("token = ? AND completed_at IS NULL", fenceStatTenant).
		Update("completed_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("lift the fence: %v", err)
	}
	if err := s.RecordFire(context.Background(), fenceStatRule, fenceStatTenant, t0, "raised"); err != nil {
		t.Fatalf("RecordFire with the fence lifted: %v", err)
	}
	if n := load(t, s, fenceStatTenant, fenceStatRule).FireCount; n != 1 {
		t.Errorf("FireCount = %d with the fence lifted, want 1", n)
	}
}

func TestRecordFireFailsClosedOnAnUnreadableFence(t *testing.T) {
	s, db, _ := newFencedRuleStatStore(t)
	if err := db.Migrator().DropTable(&rdb.PurgedTenant{}); err != nil {
		t.Fatalf("drop the fence table: %v", err)
	}
	err := s.RecordFire(context.Background(), fenceStatRule, fenceStatTenant,
		time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), "raised")
	if err == nil || !strings.Contains(err.Error(), "reading the erasure fence") {
		t.Fatalf("RecordFire over an unreadable fence returned %v; want a fence-read error", err)
	}
	var n int64
	if err := fenceSysDB(db).Model(&RuleStat{}).Count(&n).Error; err != nil {
		t.Fatalf("count stat rows: %v", err)
	}
	if n != 0 {
		t.Errorf("a refused fire left %d stat row(s)", n)
	}
}
