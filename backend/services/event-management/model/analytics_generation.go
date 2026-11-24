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
// the existing analytics advisory lock. A transaction commits the surface and
// its generation together: a failed reconciliation must not advertise success.
// The callback receives that transaction and must use it for every statement.
func withAnalyticsGeneration(ctx context.Context, mgr *rdb.RdbManager, generation int, metrics *AnalyticsMetrics, reconcile func(*gorm.DB) error) error {
	var stored struct{ Generation int }
	skipped := false
	err := mgr.WithAdvisoryLock(ctx, rdb.AdvisoryLockKey(analyticsLockName), func() error {
		return mgr.DB(core.WithSystemContext(ctx)).Transaction(func(tx *gorm.DB) error {
			if err := tx.Raw(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).Scan(&stored).Error; err != nil {
				return fmt.Errorf("reading the analytics surface generation: %w", err)
			}
			if stored.Generation > generation {
				skipped = true
				return nil
			}
			if err := reconcile(tx); err != nil {
				return err
			}
			return storeAnalyticsGeneration(tx, generation)
		})
	})
	if err == nil && skipped {
		log.Info().Int("storedGeneration", stored.Generation).Int("buildGeneration", generation).
			Str("schema", AnalyticsSchema).Msg("Skipping reconciliation of a newer SQL/BI analytics surface.")
		metrics.NewerSurfaceSkips.Inc()
	}
	return err
}

// The WHERE is a second line of defence: even a stale writer must never lower
// the generation. A rejected write also rolls back any DDL in its transaction.
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
