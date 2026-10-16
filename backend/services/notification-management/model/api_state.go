// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NotificationStatesByAlarmToken loads the notification state for one or more
// raised alarms by their alarm tokens. Returns an empty slice when none exist
// (the dispatcher has not yet notified about the alarm).
func (api *Api) NotificationStatesByAlarmToken(ctx context.Context, alarmTokens []string) ([]*NotificationState, error) {
	found := make([]*NotificationState, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "alarm_token", alarmTokens); err != nil {
		return nil, err
	}
	return found, nil
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
//
// raisedTime names the alarm CYCLE the notification belongs to (the event's RaisedTime;
// see NotificationState.CycleRaisedAt). A notification for a later cycle than the row's
// starts that cycle first — clearing the previous cycle's acknowledgement, clear and
// escalation tier — so a re-raised alarm is escalated like a new one. One for an earlier
// cycle is a stale redelivery and changes nothing.
func (api *Api) RecordNotification(ctx context.Context, alarmToken, alarmKey, severity string,
	raisedTime, at time.Time) error {
	raised := cycleKey(raisedTime)
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
					CycleRaisedAt:   raised,
				}
				return tx.Create(created).Error
			}
			return result.Error
		}
		if !enterCycle(found, raised) {
			// A notification about an EARLIER cycle (an unordered redelivery). The row
			// describes a newer cycle; nothing about this one belongs on it.
			return nil
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
// later out-of-order RAISED cannot resurrect it into an escalating open row. raisedTime
// names the alarm cycle acknowledged (see markTerminal).
func (api *Api) MarkAcknowledged(ctx context.Context, alarmToken, alarmKey, severity string,
	raisedTime, at time.Time) error {
	return api.markTerminal(ctx, alarmToken, alarmKey, severity, "acknowledged_at", raisedTime, at)
}

// MarkCleared stamps ClearedAt on the alarm's state row so escalation stops and the
// retention sweep can prune it after the grace window. Upsert tombstone, like
// MarkAcknowledged.
func (api *Api) MarkCleared(ctx context.Context, alarmToken, alarmKey, severity string,
	raisedTime, at time.Time) error {
	return api.markTerminal(ctx, alarmToken, alarmKey, severity, "cleared_at", raisedTime, at)
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
//
// 🔴 "SET ONLY WHEN NULL" IS PER CYCLE, NOT PER ROW. An alarm token outlives a clear:
// device-management re-raises the SAME token on a fresh cycle, so a row stamped by cycle
// one used to keep those stamps for every later cycle — OpenNotificationStates excluded
// it, and the second cycle of an alarm was never escalated. raisedTime (the event's
// RaisedTime) names the cycle the transition belongs to:
//
//   - a LATER cycle than the row's starts it (enterCycle resets the previous cycle's
//     stamps and tier) and then stamps — so a cycle-two acknowledgement that overtakes
//     cycle two's RAISED still leaves a resolved row for that RAISED to find;
//   - the SAME cycle stamps only when still NULL, exactly as before;
//   - an EARLIER cycle is a stale redelivery and changes nothing — so cycle one's
//     late acknowledgement cannot silence cycle two.
func (api *Api) markTerminal(ctx context.Context, alarmToken, alarmKey, severity, column string,
	raisedTime, at time.Time) error {
	raised := cycleKey(raisedTime)
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		found := &NotificationState{}
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("alarm_token = ?", alarmToken).First(found)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				tombstone := &NotificationState{
					AlarmToken:    alarmToken,
					AlarmKey:      alarmKey,
					Severity:      severity,
					CycleRaisedAt: raised,
				}
				stamp(tombstone, column, at)
				return tx.Create(tombstone).Error
			}
			return result.Error
		}
		before := found.CycleRaisedAt
		if !enterCycle(found, raised) {
			// A transition of an EARLIER cycle: stale, and it must not touch this one.
			return nil
		}
		// Stamp only when still NULL (within this cycle) so a redelivered terminal event
		// does not move the first-seen resolution time.
		if terminalStampIsNull(found, column) {
			stamp(found, column, at)
			return tx.Save(found).Error
		}
		if !before.Valid && found.CycleRaisedAt.Valid {
			// Nothing to stamp, but the row adopted a cycle it had none recorded for
			// (see enterCycle); keep that, so the next event can be ordered.
			return tx.Save(found).Error
		}
		return nil
	})
}

// OnLaterCycleThan reports whether the row describes a LATER alarm cycle than the one
// raised at `raised` (an event's RaisedTime). An alarm only re-raises from cleared, so an
// event of an earlier cycle is about an alarm that has since been cleared. A row with no
// cycle recorded, or an event naming none, cannot be ordered and reports false.
func (s *NotificationState) OnLaterCycleThan(raised time.Time) bool {
	k := cycleKey(raised)
	return s != nil && s.CycleRaisedAt.Valid && k.Valid && k.Time.Before(s.CycleRaisedAt.Time)
}

// cycleKey normalizes an event's RaisedTime into the value stored as CycleRaisedAt.
//
// 🔴 THE TRUNCATION IS LOAD-BEARING. PostgreSQL keeps microseconds, and the events of ONE
// cycle do not agree below that: device-management builds a RAISED from its in-memory row
// (the detection's nanosecond time) but an ACKNOWLEDGED or CLEARED from the row as read
// back from its database (microseconds). Compared raw, every later event of a cycle would
// look like a NEWER cycle than its own RAISED and reset it. A zero time names no cycle.
func cycleKey(raised time.Time) sql.NullTime {
	if raised.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: raised.UTC().Truncate(time.Microsecond), Valid: true}
}

// enterCycle reconciles a state row with the cycle an event belongs to, and reports
// whether the event should be applied to it. A later cycle resets the row's per-cycle
// fields (see NotificationState.CycleRaisedAt) and is applied; the same cycle is applied
// as-is; an earlier cycle is stale and is not.
//
// An event that names no cycle cannot be ordered, so it is applied with the old per-row
// semantics.
//
// 🔴 A ROW WITH NO CYCLE RECORDED (written before cycles were tracked) is ordered by its
// CLEAR. Such a row kept its FIRST cycle's stamps for ever, so on an upgraded instance
// every alarm that has cleared at least once carries a NULL-cycle row stamped cleared.
// A cycle raised AFTER that clear is a later cycle — an alarm only re-raises from cleared
// — and resets the row like any other new cycle; adopting it onto the old stamps would
// leave that cycle unescalated, which is the defect the cycle key exists to fix. Anything
// else (a cycle raised before the clear, a row never cleared) is adopted WITHOUT a reset:
// it is the row's own cycle redelivered, or a cycle nothing can place, and resetting
// there could let a stale RAISED reopen an acknowledged alarm. The clear is the stamp to
// order by because it and the raise are both detection event times; an acknowledgement is
// stamped when an operator acts, which is a different clock.
func enterCycle(s *NotificationState, raised sql.NullTime) bool {
	if !raised.Valid {
		return true
	}
	if !s.CycleRaisedAt.Valid {
		if s.ClearedAt.Valid && s.ClearedAt.Time.Before(raised.Time) {
			startCycle(s, raised)
		} else {
			s.CycleRaisedAt = raised
		}
		return true
	}
	current := s.CycleRaisedAt.Time
	switch {
	case raised.Time.Before(current):
		return false
	case raised.Time.After(current):
		startCycle(s, raised)
	}
	return true
}

// startCycle resets a state row's per-cycle fields for the cycle raised at `raised`.
// LastNotifiedAt is kept: it is the throttle basis, not per-cycle progress.
func startCycle(s *NotificationState, raised sql.NullTime) {
	s.AcknowledgedAt = sql.NullTime{}
	s.ClearedAt = sql.NullTime{}
	s.EscalationLevel = 0
	s.LastEscalatedAt = sql.NullTime{}
	s.FirstNotifiedAt = sql.NullTime{}
	s.NotifyCount = 0
	s.CycleRaisedAt = raised
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
//
// The claim is also pinned to the CYCLE the scheduler read (expectedCycle, the row's
// CycleRaisedAt). A new cycle resets the tier to 0, so between the read and the claim the
// alarm could clear and re-raise and land back on the very level the scheduler expects;
// without the cycle in the predicate that claim would win and escalate the new cycle on
// the old cycle's clock, moments after its first page.
func (api *Api) ClaimEscalation(ctx context.Context, alarmToken string, expectedLevel int,
	expectedCycle sql.NullTime, at time.Time) (bool, error) {
	q := api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ? AND escalation_level = ? AND acknowledged_at IS NULL AND cleared_at IS NULL",
			alarmToken, expectedLevel)
	if expectedCycle.Valid {
		q = q.Where("cycle_raised_at = ?", expectedCycle.Time)
	} else {
		q = q.Where("cycle_raised_at IS NULL")
	}
	result := q.
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

// PruneResolvedStates hard-deletes the caller tenant's RESOLVED state rows — acknowledged,
// cleared, or both — whose terminal stamps are all older than before. It bounds the table
// so per-alarm state never grows into an alarm index in the wrong service (ADR-041
// history-home concern). It runs under a tenant context, so the rdb tenant-scope predicate
// keeps the delete to that tenant; Unscoped bypasses only the soft-delete filter so the
// rows are truly removed.
//
// 🔴 IT USED TO FILTER ON cleared_at ALONE, AND THAT LEFT AN IMMORTAL CLASS. markTerminal
// writes a tombstone row for EVERY acknowledged or cleared alarm, in every tenant,
// including a tenant with no notification policies at all — because the tombstone is what
// closes the unordered-delivery race, not a record of a page (see markTerminal). So an
// alarm that a human acknowledged and that then stopped on its own, with no CLEARED ever
// arriving, left a row with acknowledged_at set and cleared_at null: settled forever,
// invisible to escalation (OpenNotificationStates excludes it), and matched by no prune
// predicate. One row per acknowledged-and-never-cleared alarm, kept for the life of the
// instance, is exactly the unbounded alarm index the sweep exists to prevent.
//
// 🔑 THE FIX IS TO WIDEN THE PRUNE, NOT TO STOP WRITING THE ROWS — and that was the real
// decision here, because "only write a tombstone for a tenant that has a policy" looks
// cheaper. It is wrong twice over. The tombstone's job is the RACE, not the page: a
// tenant that adds its first policy between an alarm's CLEARED and its late-arriving
// RAISED would, without the row, open a fresh state row for an alarm that is already
// resolved and escalate it forever. And deciding whether to write would mean a policy
// query on every lifecycle event, which is new latency and a new failure mode on the
// cheapest path this service has. A resolved row is bounded data with a retention window;
// a missing one is a correctness hole.
//
// The predicate is "terminal, and every stamp it does carry is older than the cutoff", so
// a row acknowledged long ago but cleared a minute ago still survives its full grace
// window.
//
// 🔴 WHAT PRUNING COSTS, SAID PLAINLY, BECAUSE THE ROW HAS A MACHINE READER. An earlier
// draft of this comment claimed a resolved row's only remaining reader is a human. It is
// not: RecordNotification reads the row on every RAISED/ESCALATED for that token and
// PRESERVES the terminal stamp, which is the whole mechanism keeping the scheduler
// suppressed after an acknowledgement (OpenNotificationStates excludes any row with either
// stamp set). Deleting the row is what re-arms it.
//
// And the reader is not hypothetical, because an alarm token OUTLIVES a clear.
// device-management reactivates the SAME alarm row on a fresh cycle — same token, state
// back to ACTIVE, Acknowledged reset — so the tombstone for token T is read by every later
// transition of T. (A transition of a NEWER cycle resets the stamps rather than honouring
// them, see enterCycle; within one cycle they hold.) The concrete case: an operator acknowledges a known-bad sensor whose alarm
// stays ACTIVE. Past the retention window the sweep removes the acked-only row; a later
// transition for that token then finds no state, opens a fresh row, and re-pages up to the
// escalation cap while device-management still shows the alarm acknowledged.
//
// That window is accepted rather than closed, and the trade is deliberate: before the
// widening the row was IMMORTAL, so the re-page could not happen — and neither could the
// table ever stop growing. An unbounded table is the worse of the two. What follows from
// it is a CONTRACT, not a footnote: an acknowledgement suppresses escalation for the
// RETENTION WINDOW, not forever. StateRetentionSeconds is therefore the knob that decides
// how long an ack is honoured, which is why lowering it is not purely a storage decision.
//
// 🔑 DO NOT RE-DERIVE SAFETY FROM THE STREAM'S RETENTION. The same earlier draft argued the
// 7d window "outlives the alarm stream's own retention, so no redelivery can arrive to find
// the tombstone gone". Those two numbers are EQUAL, not ordered, and only one of them is a
// knob: messaging's stream max age is an unexported 7d constant, while StateRetentionSeconds
// is operator-configurable and nothing pins the relation. Set it to a day and the sentence
// is false with no test noticing.
func (api *Api) PruneResolvedStates(ctx context.Context, before time.Time) (int64, error) {
	result := api.RDB.DB(ctx).Unscoped().
		Where("(cleared_at IS NOT NULL OR acknowledged_at IS NOT NULL)").
		Where("(cleared_at IS NULL OR cleared_at < ?)", before).
		Where("(acknowledged_at IS NULL OR acknowledged_at < ?)", before).
		Delete(&NotificationState{})
	return result.RowsAffected, result.Error
}
