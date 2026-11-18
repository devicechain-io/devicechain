// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// inferenceBucket is internal durable admission state, never a served resource.
// Keep rows across restarts: deleting an exhausted bucket would mint a fresh burst.
type inferenceBucket struct {
	TenantId  string  `gorm:"primaryKey"`
	Tokens    float64 `gorm:"type:double precision;not null"`
	UpdatedAt float64 `gorm:"type:double precision;not null"`
	Revision  int64   `gorm:"not null"`
}

func (inferenceBucket) TableName() string { return "ai_inference_buckets" }

type sharedInferenceLimiter struct {
	db         *gorm.DB
	resolve    core.TenantCeilingResolver
	unresolved func(core.CeilingSource)
}

// Allow uses the database clock and compare-and-set, so independent replicas cannot
// spend the same token. No local fallback: failed or contended storage denies the call.
// RateGate predates context-aware admission; bound all its database work here.
func (l *sharedInferenceLimiter) Allow(tenant string) bool {
	if tenant == "" || l.db == nil {
		return false
	}
	c := l.resolve(tenant)
	if c.Burst < 1 || c.RatePerSecond <= 0 || math.IsNaN(c.RatePerSecond) || math.IsInf(c.RatePerSecond, 0) {
		return false
	}
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 2*time.Second)
	defer cancel()
	db := l.db.WithContext(ctx)
	for attempt := 0; attempt < 8; attempt++ {
		var b inferenceBucket
		err := db.Where("tenant_id = ?", tenant).Take(&b).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return false
		}
		var now float64
		// Postgres is the production datastore. SQLite exercises the same CAS path
		// in unit tests; neither path relies on a replica's wall clock. Each dialect
		// gets its own literal statement so the raw-SQL guard can read it.
		clock := db.Raw("SELECT EXTRACT(EPOCH FROM clock_timestamp())::double precision")
		if db.Dialector.Name() == "sqlite" {
			clock = db.Raw("SELECT (julianday('now') - 2440587.5) * 86400.0")
		}
		if clock.Scan(&now).Error != nil {
			return false
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			b = inferenceBucket{TenantId: tenant, Tokens: float64(c.Burst) - 1, UpdatedAt: now, Revision: 1}
			result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&b)
			if result.Error != nil {
				return false
			}
			if result.RowsAffected == 0 {
				continue
			}
		} else {
			// Clock reversal cannot accrue credit twice. A burst reduction clamps
			// existing credit, while a raise does not manufacture a fresh bucket.
			now = math.Max(now, b.UpdatedAt)
			tokens := math.Min(float64(c.Burst), b.Tokens+(now-b.UpdatedAt)*c.RatePerSecond)
			if tokens < 1 {
				return false
			}
			result := db.Model(&inferenceBucket{}).Where("tenant_id = ? AND revision = ?", tenant, b.Revision).
				Updates(map[string]interface{}{"tokens": tokens - 1, "updated_at": now, "revision": b.Revision + 1})
			if result.Error != nil {
				return false
			}
			if result.RowsAffected == 0 {
				continue
			}
		}
		if l.unresolved != nil && (c.Source == core.CeilingPending || c.Source == core.CeilingUnreachable || c.Source == core.CeilingUnknownTenant) {
			l.unresolved(c.Source)
		}
		return true
	}
	return false
}
