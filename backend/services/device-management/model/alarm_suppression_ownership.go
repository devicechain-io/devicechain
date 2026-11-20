// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

var suppressionRefresh = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "devicechain_devicemanagement_suppression_refresh_total",
	Help: "Suppression ownership visibility refresh outcomes.",
}, []string{"outcome"})

var suppressionRefreshWarnings = &zerolog.BurstSampler{Burst: 1, Period: time.Minute}

func init() {
	for _, outcome := range []string{"refreshed", "unchanged", "failed"} {
		suppressionRefresh.WithLabelValues(outcome).Add(0)
	}
}

func suppressionRefreshFailed(err error, id uint) {
	suppressionRefresh.WithLabelValues("failed").Inc()
	if suppressionRefreshWarnings.Sample(zerolog.WarnLevel) {
		log.Warn().Err(err).Uint("window_id", id).Msg("Could not refresh suppression visibility; ownership publication continues")
	}
}

// Refresh history as well as live windows, independently of ownership delivery.
// A stale stamp fails toward INVISIBILITY: list/search, byToken and device preview
// re-check CURRENT ownership via suppressionTargetsInScope/filterSuppressionWindowsInScope.
// Status and Alarm.suppression/raisedDuringSuppression redact through
// VisibleSuppressionWindowTokens, which uses that same current-ownership filter.
// Thus the former owner cannot see a window through its stale stamp. A failed
// refresh can hide it from the new owner's scoped members until ownership changes
// again or its targets are edited; an unscoped operator still sees the window.
func (api *Api) refreshSuppressionOwnership(ctx context.Context, facts []*DeviceOwnershipChangedEvent) error {
	tokens := make([]string, 0, len(facts))
	for _, fact := range facts {
		tokens = append(tokens, fact.DeviceToken)
	}
	if len(tokens) == 0 {
		return nil
	}
	ctx = suppressionFactContext(ctx)
	db := api.RDB.DB(ctx)
	var ids []uint
	query := db.Model(&AlarmSuppressionWindow{})
	if db.Dialector.Name() == "postgres" {
		// CASE guards the function itself: SQL may reorder separate WHERE terms.
		query = query.Where("EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(targets) = 'array' THEN targets ELSE '[]'::jsonb END) AS target WHERE target->>'type' = 'DEVICE' AND target->>'token' IN ?)", tokens)
	} else {
		query = query.Where("EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type(targets) = 'array' THEN targets ELSE '[]' END) AS target WHERE CASE WHEN target.type = 'object' THEN json_extract(target.value, '$.type') END = 'DEVICE' AND CASE WHEN target.type = 'object' THEN json_extract(target.value, '$.token') END IN ?)", tokens)
	}
	if err := query.Order("id").Pluck("id", &ids).Error; err != nil {
		suppressionRefreshFailed(err, 0)
		return err
	}
	// Decode separately so corrupt alarm_keys/targets on one row cannot prevent
	// the remaining windows from refreshing.
	windows := make([]AlarmSuppressionWindow, 0, len(ids))
	var failures error
	for _, id := range ids {
		var w AlarmSuppressionWindow
		if err := db.First(&w, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			suppressionRefreshFailed(err, id)
			failures = errors.Join(failures, err)
			continue
		}
		windows = append(windows, w)
	}
	devices, err := api.suppressionDeviceAnchors(ctx, windows)
	if err != nil {
		suppressionRefreshFailed(err, 0)
		return errors.Join(failures, err)
	}
	for _, w := range windows {
		if err := api.refreshSuppressionWindow(ctx, w, devices); err != nil {
			suppressionRefreshFailed(err, w.ID)
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

func (api *Api) refreshSuppressionWindow(ctx context.Context, w AlarmSuppressionWindow, devices map[string][]string) error {
	// Same bounded retry policy as End/Delete. Re-read AND recompute after a CAS
	// miss: the winning user edit may have changed targets or deleted the window.
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			var fresh AlarmSuppressionWindow
			if err := api.RDB.DB(ctx).First(&fresh, w.ID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil
				}
				return err
			}
			w = fresh
			// A competing edit may introduce DEVICE targets absent from the page's
			// original batch. Re-read their current anchors before retrying.
			var err error
			devices, err = api.suppressionDeviceAnchors(ctx, []AlarmSuppressionWindow{w})
			if err != nil {
				return err
			}
		}
		anchors := suppressionAnchorsFromDevices(ctx, &w, devices)
		if w.CreatorScoped {
			anchors = intersectSuppressionAnchors(anchors, w.CreatorScope)
		}
		if slices.Equal(anchors, w.ScopeAnchors) {
			suppressionRefresh.WithLabelValues("unchanged").Inc()
			return nil
		}
		result := w
		result.ScopeAnchors, result.Version = anchors, w.Version+1
		q := api.RDB.DB(ctx).Model(&w).Where("id = ? AND version = ?", w.ID, w.Version).
			Select("scope_anchors", "version", "updated_at").Updates(&result)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected > 0 {
			// Invalidate immediately: even if a later window fails, this write
			// committed and the audit callback journalled it.
			api.invalidateSuppressionWindows(ctx)
			suppressionRefresh.WithLabelValues("refreshed").Inc()
			return nil
		}
	}
	return &SuppressionConflictError{Token: w.Token}
}
