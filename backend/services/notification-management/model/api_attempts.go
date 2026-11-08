// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// RecordNotificationAttempt appends one row to the delivery ledger under the caller's
// tenant. The row is created as given; the caller sets CreatedAt to when the attempt
// started so a slow write does not misplace it in time.
func (api *Api) RecordNotificationAttempt(ctx context.Context, attempt *NotificationAttempt) error {
	return api.RDB.DB(ctx).Create(attempt).Error
}

// NotificationAttempts searches the caller tenant's delivery ledger, newest first.
func (api *Api) NotificationAttempts(ctx context.Context,
	criteria NotificationAttemptSearchCriteria) (*NotificationAttemptSearchResults, error) {
	since, err := parseAttemptTime("since", criteria.Since)
	if err != nil {
		return nil, err
	}
	until, err := parseAttemptTime("until", criteria.Until)
	if err != nil {
		return nil, err
	}
	results := make([]NotificationAttempt, 0)
	db, pag := api.RDB.ListOf(ctx, &NotificationAttempt{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AlarmToken != nil {
			result = result.Where("alarm_token = ?", *criteria.AlarmToken)
		}
		if criteria.ChannelToken != nil {
			result = result.Where("channel_token = ?", *criteria.ChannelToken)
		}
		if criteria.Status != nil {
			result = result.Where("status = ?", *criteria.Status)
		}
		if since != nil {
			result = result.Where("created_at >= ?", *since)
		}
		if until != nil {
			result = result.Where("created_at < ?", *until)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &NotificationAttemptSearchResults{Results: results, Pagination: pag}, nil
}

// parseAttemptTime reads an optional RFC 3339 bound, refusing one that does not parse
// rather than ignoring it (an ignored bound returns a plausible, wrong page).
func parseAttemptTime(name string, value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 timestamp: %w", name, err)
	}
	return &t, nil
}

// failureStatuses are the ledger outcomes that count against a channel's health. A
// NOT_ATTEMPTED row is deliberately absent: it says the PLATFORM did not reach the
// channel (a secret store outage, a spent dispatch budget), not that the channel failed,
// and counting it would blame an endpoint for a fault it had no part in.
var failureStatuses = []string{AttemptStatusFailed, AttemptStatusRefused}

// notBudgetCut excludes an attempt the whole-dispatch budget ended: like NOT_ATTEMPTED, it
// says the platform stopped, not that the channel failed.
const notBudgetCut = "(error_code IS NULL OR error_code <> ?)"

// ChannelHealthByToken reads one channel's recent delivery record from the ledger: when it
// last delivered, when it last failed, and how many attempts have failed since it last
// delivered. A channel the ledger has never seen reads as all-null and zero.
//
// Health is per channel TOKEN, the same key the ledger records, so a channel deleted and
// re-created under the same token inherits its predecessor's record until the next
// delivery. That is the same identity the operator addresses the channel by.
func (api *Api) ChannelHealthByToken(ctx context.Context, channelToken string) (*ChannelHealth, error) {
	health := &ChannelHealth{}
	base := func() *gorm.DB {
		return api.RDB.DB(ctx).Model(&NotificationAttempt{}).Where("channel_token = ?", channelToken)
	}

	var success NotificationAttempt
	res := base().Where("status = ?", AttemptStatusSent).
		Order("created_at DESC").Order("id DESC").Limit(1).Find(&success)
	if res.Error != nil {
		return nil, fmt.Errorf("reading last delivery for channel %q: %w", channelToken, res.Error)
	}
	if res.RowsAffected > 0 {
		health.LastSuccessAt = sql.NullTime{Time: success.CreatedAt, Valid: true}
	}

	var failure NotificationAttempt
	res = base().Where("status IN ?", failureStatuses).Where(notBudgetCut, AttemptErrorBudgetSpent).
		Order("created_at DESC").Order("id DESC").Limit(1).Find(&failure)
	if res.Error != nil {
		return nil, fmt.Errorf("reading last failure for channel %q: %w", channelToken, res.Error)
	}
	if res.RowsAffected > 0 {
		health.LastFailureAt = sql.NullTime{Time: failure.CreatedAt, Valid: true}
	}

	// Failures since the last success. Ordered by (created_at, id) so two rows in the same
	// instant still have a definite before and after.
	count := base().Where("status IN ?", failureStatuses).Where(notBudgetCut, AttemptErrorBudgetSpent)
	if health.LastSuccessAt.Valid {
		count = count.Where("(created_at > ? OR (created_at = ? AND id > ?))",
			success.CreatedAt, success.CreatedAt, success.ID)
	}
	if err := count.Count(&health.ConsecutiveFailures).Error; err != nil {
		return nil, fmt.Errorf("counting failures for channel %q: %w", channelToken, err)
	}
	return health, nil
}

// DistinctAttemptTenants lists the tenants that currently have ledger rows. It runs under
// a system context (the retention sweep's cross-tenant loop); the per-tenant prune that
// follows is tenant-scoped.
func (api *Api) DistinctAttemptTenants(ctx context.Context) ([]string, error) {
	var tenants []string
	err := api.RDB.DB(ctx).Model(&NotificationAttempt{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
	return tenants, err
}

// PruneNotificationAttempts deletes the caller tenant's ledger rows created before the
// cutoff. The ledger holds recipients, which are personal data, so it is kept for a
// window rather than for the life of the instance.
func (api *Api) PruneNotificationAttempts(ctx context.Context, before time.Time) (int64, error) {
	result := api.RDB.DB(ctx).Where("created_at < ?", before).Delete(&NotificationAttempt{})
	return result.RowsAffected, result.Error
}
