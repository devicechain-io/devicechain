// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/schema"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func limiterDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := sqlitetest.OpenMemory(t, &gorm.Config{})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	db = db.WithContext(core.WithSystemContext(context.Background()))
	for _, migration := range schema.Migrations {
		require.NoError(t, migration.Migrate(db))
	}
	return db
}

func TestInferenceBudgetIsSharedAcrossReplicasAndRestarts(t *testing.T) {
	db := limiterDB(t)
	def := governance.Limits{RatePerSecond: 0.000001, Burst: 3}
	build := func() inferenceGate {
		return buildRateLimiter(def, mscfg.InfrastructureConfiguration{}, nil, nil, db)
	}
	a, b := build(), build()
	require.True(t, a.Allow("acme"))
	require.True(t, b.Allow("acme"))
	require.True(t, a.Allow("acme"))
	require.False(t, b.Allow("acme"), "another replica cannot mint its own burst")
	require.False(t, build().Allow("acme"), "restarting a replica cannot reset spent budget")
	require.True(t, b.Allow("other"), "tenants have independent budgets")
	// Replaying the real appended migration must not refill an exhausted bucket.
	var before, after inferenceBucket
	require.NoError(t, db.Where("tenant_id = ?", "acme").Take(&before).Error)
	require.NoError(t, schema.NewInferenceBucketsMigration().Migrate(db))
	require.NoError(t, db.Where("tenant_id = ?", "acme").Take(&after).Error)
	require.Equal(t, before, after)
	require.False(t, build().Allow("acme"))
}

type inferenceGate interface{ Allow(string) bool }

func TestInferenceSameRevisionCanOnlyBeSpentOnce(t *testing.T) {
	db := limiterDB(t)
	def := governance.Limits{RatePerSecond: 0.000001, Burst: 2}
	a := buildRateLimiter(def, mscfg.InfrastructureConfiguration{}, nil, nil, db)
	b := buildRateLimiter(def, mscfg.InfrastructureConfiguration{}, nil, nil, db)
	require.True(t, a.Allow("acme"))
	// Force both replicas to read the same last token before either can update it.
	var reads atomic.Int32
	bothRead := make(chan struct{})
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("same_revision", func(tx *gorm.DB) {
		if tx.Statement.Table != "ai_inference_buckets" {
			return
		}
		n := reads.Add(1)
		if n == 2 {
			close(bothRead)
		}
		if n <= 2 {
			<-bothRead
		}
	}))
	results := make(chan bool, 2)
	go func() { results <- a.Allow("acme") }()
	go func() { results <- b.Allow("acme") }()
	admitted := 0
	for i := 0; i < 2; i++ {
		if <-results {
			admitted++
		}
	}
	require.Equal(t, 1, admitted)
}

func TestInferenceConcurrentReplicasCannotDoubleSpend(t *testing.T) {
	db := limiterDB(t)
	def := governance.Limits{RatePerSecond: 0.000001, Burst: 5}
	a := buildRateLimiter(def, mscfg.InfrastructureConfiguration{}, nil, nil, db)
	b := buildRateLimiter(def, mscfg.InfrastructureConfiguration{}, nil, nil, db)
	// Seed separately so the race exercises updates of the same revision.
	require.True(t, a.Allow("acme"))
	var admitted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(1)
		gate := a
		if i%2 == 0 {
			gate = b
		}
		go func() {
			defer wg.Done()
			<-start
			if gate.Allow("acme") {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(4), admitted.Load())
}

func TestInferenceStoreFailuresDenyAdmission(t *testing.T) {
	db := limiterDB(t)
	gate := buildRateLimiter(governance.Limits{RatePerSecond: 1, Burst: 10}, mscfg.InfrastructureConfiguration{}, nil, nil, db)
	require.False(t, gate.Allow(""))
	require.True(t, gate.Allow("acme"))
	require.NoError(t, db.Migrator().DropTable(&inferenceBucket{}))
	require.False(t, gate.Allow("acme"), "read failure must not fall back to a local bucket")
	require.False(t, gate.Allow("new"), "a missing store must not mint a new burst")
	nilStore := buildRateLimiter(governance.Limits{RatePerSecond: 1, Burst: 10}, mscfg.InfrastructureConfiguration{}, nil, nil, nil)
	require.False(t, nilStore.Allow("acme"))
}

func TestInferenceWriteAndClockFailuresDenyAdmission(t *testing.T) {
	for _, phase := range []string{"create", "update", "clock"} {
		t.Run(phase, func(t *testing.T) {
			db := limiterDB(t)
			gate := buildRateLimiter(governance.Limits{RatePerSecond: 1, Burst: 10}, mscfg.InfrastructureConfiguration{}, nil, nil, db)
			if phase == "update" {
				require.True(t, gate.Allow("acme"))
			}
			fail := func(tx *gorm.DB) { tx.AddError(errors.New("store unavailable")) }
			switch phase {
			case "create":
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail", fail))
			case "update":
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("fail", fail))
			case "clock":
				require.NoError(t, db.Callback().Row().Before("gorm:row").Register("fail", fail))
			}
			require.False(t, gate.Allow("acme"))
		})
	}
}

func TestInferenceRefillUsesDatabaseTimeAndClampsBurst(t *testing.T) {
	db := limiterDB(t)
	c := core.TenantCeiling{RatePerSecond: 0.000001, Burst: 3, Source: core.CeilingStatic}
	gate := &sharedInferenceLimiter{db: db, resolve: func(string) core.TenantCeiling { return c }}
	for i := 0; i < 3; i++ {
		require.True(t, gate.Allow("acme"))
	}
	require.False(t, gate.Allow("acme"))
	// Move the persisted timestamp back, without waiting for wall time. Both
	// replicas use the database's elapsed time to refill at the resolved rate.
	c.RatePerSecond = 0.001
	require.NoError(t, db.Model(&inferenceBucket{}).Where("tenant_id = ?", "acme").Update("updated_at", gorm.Expr("updated_at - 10000")).Error)
	for i := 0; i < 3; i++ {
		require.True(t, gate.Allow("acme"))
	}
	require.False(t, gate.Allow("acme"), "refill cannot exceed the burst")
	// Future timestamps must not manufacture credit when clocks reverse.
	require.NoError(t, db.Model(&inferenceBucket{}).Where("tenant_id = ?", "acme").Updates(map[string]interface{}{"tokens": 0, "updated_at": 9999999999.0}).Error)
	require.False(t, gate.Allow("acme"))
	// Reducing a burst clamps stored credit immediately.
	c.Burst = 1
	require.NoError(t, db.Model(&inferenceBucket{}).Where("tenant_id = ?", "acme").Update("tokens", 3).Error)
	require.True(t, gate.Allow("acme"))
	require.False(t, gate.Allow("acme"))
	c.Burst = 0
	require.False(t, gate.Allow("other"))
}
