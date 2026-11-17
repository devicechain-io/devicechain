// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const ShareSweepInterval = 15 * time.Minute

// ShareSweep removes shares that still name deleted anchors when a deletion fact was lost,
// dead-lettered, or predates this release. Customer and group tokens are retired on delete,
// so this sweep repairs missed cleanup rather than guarding against token inheritance.
type ShareSweep struct {
	API     *model.Api
	Changes *prometheus.CounterVec
}

// NewShareSweep builds the single-runner periodic task using the API's existing checker.
func NewShareSweep(ms *core.Microservice, api *model.Api) *core.PeriodicTask {
	s := &ShareSweep{API: api, Changes: ms.NewCounterVec("share_sweep_changes_total", "Dashboard shares removed by the existence sweep.", []string{"action"})}
	for _, action := range []string{"deleted"} {
		s.Changes.WithLabelValues(action).Add(0)
	}
	return core.NewPeriodicTask(ms.FunctionalArea, "share-sweep", ShareSweepInterval, s.Run,
		core.NewNoOpLifecycleCallbacks(), ms.NewPeriodicTaskMetrics("share_sweep"))
}

// shareSweepLockName names the sweep's advisory lock. Its key is shared by old and
// new replicas during a rolling upgrade, so the name must never change.
const shareSweepLockName = "dashboard-management-share-sweep"

// Run permits one replica to sweep at a time.
func (s *ShareSweep) Run(ctx context.Context) error {
	return s.API.RDB.SingletonPass(ctx, shareSweepLockName, func() error { return s.sweep(ctx) })
}

func (s *ShareSweep) sweep(ctx context.Context) error {
	// This is the sole cross-tenant read: enumerate only tenant IDs, then restore tenant scoping.
	var tenants []string
	if err := s.API.RDB.DB(core.WithSystemContext(ctx)).Model(&model.DashboardShare{}).Distinct("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return err
	}
	failed := 0
	var last error
	for _, tenant := range tenants {
		if _, err := s.sweepTenant(core.WithTenant(ctx, tenant), tenant); err != nil {
			failed++
			last = err
			log.Warn().Err(err).Str("tenant", tenant).Msg("Dashboard share sweep skipped tenant")
		}
	}
	return core.PassResult(len(tenants), failed, last)
}

type sweepAnchor struct {
	AnchorKind  string
	AnchorToken string
}

// sweepTenant obtains every page before writing, so a failed/partial lookup changes no shares.
func (s *ShareSweep) sweepTenant(ctx context.Context, tenant string) (int64, error) {
	// Take T0 before any lookup so shares inserted while it runs are preserved.
	cutoff := time.Now().UTC()
	var missing []sweepAnchor
	for _, kind := range []string{auth.AnchorKindCustomer, auth.AnchorKindGroup} {
		cursor := ""
		for {
			var page []sweepAnchor
			if err := s.API.RDB.DB(ctx).Model(&model.DashboardShare{}).Distinct("anchor_kind", "anchor_token").
				Where("anchor_kind = ? AND anchor_token > ?", kind, cursor).Order("anchor_token").Limit(rdb.MaxLookupKeys).Find(&page).Error; err != nil {
				return 0, err
			}
			if len(page) == 0 {
				break
			}
			tokens := make([]string, len(page))
			for i, a := range page {
				tokens[i] = a.AnchorToken
			}
			absent, err := s.API.ShareTargets.Missing(ctx, tenant, kind, tokens)
			if err != nil {
				return 0, err
			}
			for _, token := range absent {
				missing = append(missing, sweepAnchor{AnchorKind: kind, AnchorToken: token})
			}
			cursor = page[len(page)-1].AnchorToken
		}
	}
	var deleted int64
	err := s.API.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, a := range missing {
			res := tx.Where("anchor_kind = ? AND anchor_token = ? AND created_at < ?", a.AnchorKind, a.AnchorToken, cutoff).Delete(&model.DashboardShare{})
			if res.Error != nil {
				return res.Error
			}
			deleted += res.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if s.Changes != nil {
		s.Changes.WithLabelValues("deleted").Add(float64(deleted))
	}
	if deleted > 0 {
		log.Info().Str("tenant", tenant).Int64("deleted", deleted).Msg("Repaired dashboard shares")
	}
	return deleted, nil
}
