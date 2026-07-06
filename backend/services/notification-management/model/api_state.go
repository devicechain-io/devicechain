// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NotificationStatesByAlarmToken loads the notification state for one or more
// raised alarms by their alarm tokens. Returns an empty slice when none exist
// (the dispatcher has not yet notified about the alarm).
func (api *Api) NotificationStatesByAlarmToken(ctx context.Context, alarmTokens []string) ([]*NotificationState, error) {
	found := make([]*NotificationState, 0)
	result := api.RDB.DB(ctx).Find(&found, "alarm_token in ?", alarmTokens)
	return found, result.Error
}

// NotificationStates searches per-alarm notification state by criteria. This is
// the operator-facing "who was paged, and has it been acknowledged/escalated"
// surface the escalation scheduler (N.D) and the console read.
func (api *Api) NotificationStates(ctx context.Context,
	criteria NotificationStateSearchCriteria) (*NotificationStateSearchResults, error) {
	results := make([]NotificationState, 0)
	db, pag := api.RDB.ListOf(ctx, &NotificationState{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AlarmKey != nil {
			result = result.Where("alarm_key = ?", *criteria.AlarmKey)
		}
		if criteria.Severity != nil {
			result = result.Where("severity = ?", *criteria.Severity)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &NotificationStateSearchResults{Results: results, Pagination: pag}, nil
}

// RecordNotification upserts the per-alarm notification state after the dispatcher
// (N.C) delivers a RAISED/ESCALATED transition: it creates the row on first sight of
// a raised alarm and advances the notified timestamps + count (the throttle basis)
// and current severity. It is the write path of the escalation substrate (N.D reads
// FirstNotifiedAt/LastNotifiedAt/NotifyCount to decide re-notification).
//
// Concurrent dispatch workers can handle two transitions for one alarm at once, so
// the read-modify-write runs under a row lock (SELECT … FOR UPDATE) — like the
// device-state merge — so same-alarm updates serialize; a concurrent first insert
// loses the (tenant_id, alarm_token) unique-index race and errors out (the caller
// logs it: the delivery already happened, this row is best-effort bookkeeping).
func (api *Api) RecordNotification(ctx context.Context, alarmToken, alarmKey, severity string, at time.Time) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		found := &NotificationState{}
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("alarm_token = ?", alarmToken).First(found)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				created := &NotificationState{
					AlarmToken:      alarmToken,
					AlarmKey:        alarmKey,
					Severity:        severity,
					FirstNotifiedAt: sql.NullTime{Time: at, Valid: true},
					LastNotifiedAt:  sql.NullTime{Time: at, Valid: true},
					NotifyCount:     1,
				}
				return tx.Create(created).Error
			}
			return result.Error
		}
		found.NotifyCount++
		if !found.FirstNotifiedAt.Valid {
			found.FirstNotifiedAt = sql.NullTime{Time: at, Valid: true}
		}
		// Advance the last-notified stamp and the tracked severity only when this event
		// is newer than what we have, so an out-of-order redelivery (the pool is
		// unordered) cannot regress LastNotifiedAt — which would shorten the throttle
		// window — or overwrite the severity with a stale value. NotifyCount still
		// counts every delivery. Mirrors the device-state merge's monotonic guard.
		if !found.LastNotifiedAt.Valid || at.After(found.LastNotifiedAt.Time) {
			found.LastNotifiedAt = sql.NullTime{Time: at, Valid: true}
			found.Severity = severity
		}
		return tx.Save(found).Error
	})
}

// MarkAcknowledged stamps AcknowledgedAt on the alarm's state row so the escalation
// scheduler (N.D) stops re-notifying. It is an update-if-exists: a no-op when no row
// exists (the alarm was never notified) or one is already acknowledged, so it is
// idempotent and safe under the at-least-once/unordered delivery contract.
func (api *Api) MarkAcknowledged(ctx context.Context, alarmToken string, at time.Time) error {
	return api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ? AND acknowledged_at IS NULL", alarmToken).
		Update("acknowledged_at", at).Error
}

// MarkCleared stamps ClearedAt on the alarm's state row so escalation stops and the
// retention sweep can prune it after the grace window. Update-if-exists and
// idempotent, like MarkAcknowledged.
//
// Ordering caveat (an N.D precondition): the dispatch pool is unordered, so a CLEARED
// can be processed before the RAISED that would create the row. When that happens the
// clear is a no-op and a later-delivered RAISED creates a row with ClearedAt NULL —
// the escalation scheduler (N.D) must therefore re-verify an alarm's live state at
// escalation time rather than trust this projection alone, and such a row is not
// pruned by the cleared-only retention sweep until its alarm is cleared again. The
// interleaving requires a raise+auto-clear within one dispatch latency, so it is rare;
// closing it fully (an upsert tombstone) is deferred to N.D where it becomes load-bearing.
func (api *Api) MarkCleared(ctx context.Context, alarmToken string, at time.Time) error {
	return api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ? AND cleared_at IS NULL", alarmToken).
		Update("cleared_at", at).Error
}

// TouchSeverity records an alarm's new severity on its state row after a
// de-escalation (which does not itself notify). Update-if-exists: a no-op when the
// alarm was never notified.
func (api *Api) TouchSeverity(ctx context.Context, alarmToken, severity string) error {
	return api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ?", alarmToken).
		Update("severity", severity).Error
}

// DistinctStateTenants lists the tenants that currently have any notification-state
// rows. It runs under a system context (the retention sweep's cross-tenant maintenance
// loop) so it is not tenant-scoped; the per-tenant prune that follows IS scoped.
func (api *Api) DistinctStateTenants(ctx context.Context) ([]string, error) {
	var tenants []string
	err := api.RDB.DB(ctx).Model(&NotificationState{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
	return tenants, err
}

// PruneClearedStates hard-deletes the caller tenant's cleared state rows whose
// ClearedAt is older than before, bounding the table so per-alarm state never grows
// into an alarm index in the wrong service (ADR-041 history-home concern). It runs
// under a tenant context, so the rdb tenant-scope predicate keeps the delete to that
// tenant; Unscoped bypasses only the soft-delete filter so the rows are truly removed.
func (api *Api) PruneClearedStates(ctx context.Context, before time.Time) (int64, error) {
	result := api.RDB.DB(ctx).Unscoped().
		Where("cleared_at IS NOT NULL AND cleared_at < ?", before).
		Delete(&NotificationState{})
	return result.RowsAffected, result.Error
}
