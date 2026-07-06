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
// scheduler (N.D) stops re-notifying. It is an upsert tombstone (see markTerminal):
// idempotent, and it creates a resolved row even when the alarm was never notified so a
// later out-of-order RAISED cannot resurrect it into an escalating open row.
func (api *Api) MarkAcknowledged(ctx context.Context, alarmToken, alarmKey, severity string, at time.Time) error {
	return api.markTerminal(ctx, alarmToken, alarmKey, severity, "acknowledged_at", at)
}

// MarkCleared stamps ClearedAt on the alarm's state row so escalation stops and the
// retention sweep can prune it after the grace window. Upsert tombstone, like
// MarkAcknowledged.
func (api *Api) MarkCleared(ctx context.Context, alarmToken, alarmKey, severity string, at time.Time) error {
	return api.markTerminal(ctx, alarmToken, alarmKey, severity, "cleared_at", at)
}

// markTerminal records a terminal (acknowledged or cleared) transition on the alarm's
// state row, stamping the named column. It is an upsert tombstone: if the row already
// exists the stamp is set only when still NULL (idempotent under at-least-once
// redelivery); if no row exists yet it CREATES a resolved tombstone row carrying the
// stamp.
//
// Creating the tombstone is what closes the unordered-delivery race the escalation
// scheduler depends on: the dispatch pool is unordered, so a CLEARED/ACKNOWLEDGED can
// be processed before the RAISED that would create the row. Were this a plain
// update-if-exists, the terminal event would be a no-op and a later-delivered RAISED
// would create a fresh OPEN row (both stamps NULL) that the scheduler would then
// escalate forever, even though the alarm is already resolved. With the tombstone the
// row exists resolved first; the later RAISED's RecordNotification finds it and only
// bumps NotifyCount, preserving the terminal stamp, so the scheduler's
// "cleared_at IS NULL AND acknowledged_at IS NULL" filter permanently excludes it.
//
// The read-modify-write runs under a row lock (SELECT … FOR UPDATE) so concurrent
// terminal transitions for one alarm serialize; a concurrent first insert loses the
// (tenant_id, alarm_token) unique-index race and errors out, which the caller returns
// as a safe redelivery (the transition is idempotent).
func (api *Api) markTerminal(ctx context.Context, alarmToken, alarmKey, severity, column string, at time.Time) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		found := &NotificationState{}
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("alarm_token = ?", alarmToken).First(found)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				tombstone := &NotificationState{
					AlarmToken: alarmToken,
					AlarmKey:   alarmKey,
					Severity:   severity,
				}
				stamp(tombstone, column, at)
				return tx.Create(tombstone).Error
			}
			return result.Error
		}
		// Stamp only when still NULL so a redelivered terminal event does not move the
		// first-seen resolution time.
		if terminalStampIsNull(found, column) {
			stamp(found, column, at)
			return tx.Save(found).Error
		}
		return nil
	})
}

// stamp sets the named terminal timestamp column on a state row.
func stamp(s *NotificationState, column string, at time.Time) {
	v := sql.NullTime{Time: at, Valid: true}
	if column == "cleared_at" {
		s.ClearedAt = v
	} else {
		s.AcknowledgedAt = v
	}
}

// terminalStampIsNull reports whether the named terminal timestamp column is unset.
func terminalStampIsNull(s *NotificationState, column string) bool {
	if column == "cleared_at" {
		return !s.ClearedAt.Valid
	}
	return !s.AcknowledgedAt.Valid
}

// TouchSeverity records an alarm's new severity on its state row after a
// de-escalation (which does not itself notify). Update-if-exists: a no-op when the
// alarm was never notified.
func (api *Api) TouchSeverity(ctx context.Context, alarmToken, severity string) error {
	return api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ?", alarmToken).
		Update("severity", severity).Error
}

// OpenNotificationStates loads the caller tenant's escalation candidates: rows for
// alarms that have been notified at least once (LastNotifiedAt set) and are still
// neither acknowledged nor cleared. The escalation scheduler (N.D) weighs each against
// the tenant's escalation-enabled policies to decide re-notification. The set is the
// tenant's currently-open unresolved alarms — small operator-actionable data, not
// device-scale — so it is loaded unpaginated. The per-policy escalation cap
// (EscalationLevel) is evaluated in the scheduler, not filtered here.
func (api *Api) OpenNotificationStates(ctx context.Context) ([]*NotificationState, error) {
	found := make([]*NotificationState, 0)
	result := api.RDB.DB(ctx).
		Where("cleared_at IS NULL AND acknowledged_at IS NULL AND last_notified_at IS NOT NULL").
		Find(&found)
	return found, result.Error
}

// ClaimEscalation atomically claims the next escalation tier for an open alarm BEFORE
// the scheduler (N.D) delivers it, returning whether the claim was won. It is a
// compare-and-swap: a single predicated UPDATE advances EscalationLevel (and re-arms the
// notified stamps/count — an escalation IS a notification) only if the row is still at
// expectedLevel and still neither acknowledged nor cleared.
//
// Claiming before delivering is what makes the timer loop safe to run in EVERY replica
// and across a rolling update's overlapping old+new pods (unlike the durable consumer,
// the scheduler has no JetStream one-of-N delivery to lean on). Two pods that both plan
// the same tier race on this UPDATE; PostgreSQL row-locks it, so exactly one sees
// escalation_level == expectedLevel and wins (RowsAffected == 1) — the loser matches no
// row (RowsAffected == 0) and must not deliver. The tradeoff versus recording after the
// send is a rare MISSED escalation if a pod dies between winning the claim and sending —
// preferred over paging every operator twice on every deploy. A CAS loss also covers the
// alarm resolving between the scheduler's read and the claim.
func (api *Api) ClaimEscalation(ctx context.Context, alarmToken string, expectedLevel int, at time.Time) (bool, error) {
	result := api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ? AND escalation_level = ? AND acknowledged_at IS NULL AND cleared_at IS NULL",
			alarmToken, expectedLevel).
		Updates(map[string]any{
			"escalation_level":  gorm.Expr("escalation_level + 1"),
			"last_escalated_at": at,
			"last_notified_at":  at,
			"notify_count":      gorm.Expr("notify_count + 1"),
		})
	return result.RowsAffected == 1, result.Error
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
