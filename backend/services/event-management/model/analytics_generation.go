// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// AnalyticsSurfaceGeneration versions the entire analytics surface, including
// functions, view projections, and grants. Bump whenever its definition changes.
// Generation 0 denotes the unversioned surface from releases before v1.10.
// A later release may widen a view only once every overlapping version knows
// this protocol; pods predating v1.10 do not consult this generation.
const AnalyticsSurfaceGeneration = 1

// withAnalyticsGeneration keeps the comparison, DDL and generation write under
// the existing analytics advisory lock, so every generation-aware pod serializes
// on it and an older one can never act on a comparison a newer one has outdated.
// The generation is written only after reconcile returns nil, so a failed
// reconciliation never advertises success.
//
// 🔴 THE RECONCILE IS DELIBERATELY NOT WRAPPED IN A TRANSACTION. Each repair it
// makes (a view rebuilt with its tenant predicate, a stray grant revoked) is a
// tenant-isolation fix that must stay applied even when a LATER step refuses the
// boot: a failed grant, a refused platform role. One transaction around the
// whole reconcile rolled those fixes back with the failure and left the leak in
// place while the pods crash-looped (TestIntegrationAnalyticsPrefixArmRefusesThePlatformRole
// measured it). The lock, not a transaction, makes the compare-and-write safe;
// the WHERE in storeAnalyticsGeneration is the second line of defence against a
// writer that bypassed it.
func withAnalyticsGeneration(ctx context.Context, mgr *rdb.RdbManager, generation int, metrics *AnalyticsMetrics, reconcile func(*gorm.DB) error) error {
	var stored struct{ Generation int }
	skipped := false
	err := mgr.WithAdvisoryLock(ctx, rdb.AdvisoryLockKey(analyticsLockName), func() error {
		// System context: the analytics surface and its generation row are instance-wide
		// DDL and bookkeeping with no tenant rows, serialized by the advisory lock above.
		db := mgr.DB(core.WithSystemContext(ctx))
		if err := db.Raw(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).Scan(&stored).Error; err != nil {
			return fmt.Errorf("reading the analytics surface generation: %w", err)
		}
		if stored.Generation > generation {
			skipped = true
			return nil
		}
		if err := reconcile(db); err != nil {
			return err
		}
		return storeAnalyticsGeneration(db, generation)
	})
	if err == nil && skipped {
		log.Info().Int("storedGeneration", stored.Generation).Int("buildGeneration", generation).
			Str("schema", AnalyticsSchema).Msg("Skipping reconciliation of a newer SQL/BI analytics surface.")
		metrics.NewerSurfaceSkips.Inc()
	}
	return err
}

// The WHERE is a second line of defence: even a stale writer must never lower
// the generation.
func storeAnalyticsGeneration(db *gorm.DB, generation int) error {
	result := db.Exec(`INSERT INTO "event-management".analytics_surface_generation (id, generation)
		VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET generation = EXCLUDED.generation
		WHERE analytics_surface_generation.generation <= EXCLUDED.generation`, generation)
	if result.Error != nil {
		return fmt.Errorf("storing the analytics surface generation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("refusing to lower the analytics surface generation to %d", generation)
	}
	return nil
}
