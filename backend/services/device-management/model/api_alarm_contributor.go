// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
)

// This file is the DB-facing half of the ADR-057 alarm-object integrator (the pure reduction lives in
// alarm_contributor.go). It applies one DETECT+REACT edge — a rule's raise or resolve — to a device's
// (device, alarmKey) alarm by folding it into the row's contributor set and re-deriving the row's
// state + severity: the alarm is ACTIVE at the max tier over the rules currently raising it and CLEARS
// when the last one resolves. It REPLACES the retired measurement evaluator's level-triggered
// clear-on-unsatisfied semantics (api_alarm_eval.go, deleted at slice 6) with edge-integration, so a
// rule-driven alarm is the same first-class Alarm object with the same ack/clear, graph rollup, and
// alarm-events→notification flow (ADR-041/017).

// ApplyAlarmContributorEdge applies one rule's edge to the (device, alarmKey) alarm and persists the
// re-derived state (ADR-057). It is the KEPT device-management boundary the raise-alarm consumer calls
// for BOTH edges (edge == AlarmEdgeRaised | AlarmEdgeResolved). ruleID is the contributor identity; a
// raise carries the tier (severity) it raises at and, for a value-bearing rule, the triggering value.
//
// It is idempotent and order-independent by construction: the contributor reduction ignores a stale
// edge and lets a resolve win a raise at an equal event time (RaiseAlarmRequest ordering contract), so
// an at-least-once, out-of-order REACT stream re-derives one deterministic alarm state. severity is
// re-checked fail-closed even though the consumer validates, so no caller can drive a malformed tier
// into the row. deviceId is already resolved (the consumer resolves the token through the cached
// accessor and distinguishes a deleted device from a store error), so this does not re-resolve.
func (api *Api) ApplyAlarmContributorEdge(ctx context.Context, deviceId uint,
	alarmKey, metricKey, ruleID, edge, severity string, value *float64, occurredTime time.Time) error {
	if alarmKey == "" || ruleID == "" {
		return fmt.Errorf("alarm-edge requires a non-empty alarm key and rule id")
	}
	// Only an explicit resolved token is a falling edge; empty (legacy) or "raised" is a raise — the
	// same default as the wire contract, so an unstamped edge can never be mis-read as a clear.
	raised := edge != AlarmEdgeResolved
	if raised && !AlarmSeverity(severity).Valid() {
		return fmt.Errorf("alarm-edge: invalid raise severity %q", severity)
	}

	existing, err := api.alarmByOriginatorKey(ctx, string(entity.TypeDevice), deviceId, alarmKey)
	if err != nil {
		return err
	}

	cs := contributorSet{}
	if existing != nil {
		if cs, err = decodeContributors(existing.Contributors); err != nil {
			return err
		}
	}
	if !cs.apply(ruleID, raised, severity, occurredTime) {
		return nil // stale or duplicate edge: nothing changed, nothing to persist (idempotent)
	}
	newSeverity, anyActive := cs.activeSeverity()
	encoded, err := cs.encode()
	if err != nil {
		return err
	}

	// A raised edge carries the triggering reading; a resolve carries none. A nil value leaves the
	// row's last value NULL rather than writing a fabricated 0 (mirrors the evaluator's contract).
	var lastValue sql.NullFloat64
	if value != nil {
		lastValue = sql.NullFloat64{Float64: *value, Valid: true}
	}

	if existing == nil {
		return api.createAlarmFromContributors(ctx, deviceId, alarmKey, metricKey,
			newSeverity, anyActive, lastValue, encoded, occurredTime)
	}
	return api.updateAlarmFromContributors(ctx, existing, newSeverity, anyActive, lastValue, encoded, occurredTime)
}

// createAlarmFromContributors persists a brand-new alarm row for a device that had none. The common
// case is anyActive (the first raise) → an ACTIVE row + a RAISED event. The rare case is a resolve
// arriving before its raise (REACT redelivery reordering): the derived state is CLEARED, and the row
// is still created — as a tombstone-bearing CLEARED row — so the later, event-time-OLDER raise is
// rejected as stale rather than wrongly (re)raising the alarm. No event is emitted for that case: the
// alarm never became visibly active.
func (api *Api) createAlarmFromContributors(ctx context.Context, deviceId uint,
	alarmKey, metricKey, severity string, anyActive bool, lastValue sql.NullFloat64,
	contributors []byte, occurredTime time.Time) error {
	created := &Alarm{
		TokenReference: rdb.TokenReference{Token: uuid.New().String()},
		OriginatorType: string(entity.TypeDevice),
		OriginatorId:   deviceId,
		AlarmKey:       alarmKey,
		MetricKey:      metricKey,
		Severity:       severity,
		RaisedTime:     occurredTime,
		LastValue:      lastValue,
		Contributors:   contributors,
	}
	if anyActive {
		created.State = string(AlarmStateActive)
	} else {
		created.State = string(AlarmStateCleared)
		created.ClearedTime = sql.NullTime{Time: occurredTime, Valid: true}
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return err
	}
	if anyActive {
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(created, AlarmEventRaised, "", occurredTime))
	}
	return nil
}

// updateAlarmFromContributors folds the re-derived state into an existing row under the ADR-041 2.D
// from-state-predicated UPDATE (exactly-once: a concurrent operator ack/clear that moved the row
// between the read and this write yields RowsAffected 0, so we neither claim a state that did not
// happen nor emit a phantom event). It handles all four transitions from the row's current state to
// {ACTIVE, CLEARED}: reactivate (CLEARED→ACTIVE, resets ack), escalate/de-escalate or value-update
// (ACTIVE→ACTIVE), clear (ACTIVE→CLEARED), and a tombstone-only contributor update that leaves the row
// CLEARED (CLEARED→CLEARED, no event).
func (api *Api) updateAlarmFromContributors(ctx context.Context, existing *Alarm,
	severity string, anyActive bool, lastValue sql.NullFloat64, contributors []byte, occurredTime time.Time) error {
	prevState := existing.State
	prevSeverity := existing.Severity

	updates := map[string]interface{}{"contributors": contributors}

	// pending defers the alarm state-change event until AFTER the from-state-predicated UPDATE confirms
	// a row actually changed: the event reads the alarm's POST-transition fields, which a map-based
	// Updates does not write back into the struct, so `mutate` reflects the new state onto the
	// in-memory row just before the event is built — but only on a write that won the race.
	var etype AlarmEventType
	var mutate func()
	switch {
	case anyActive && prevState == string(AlarmStateCleared):
		// Reactivation: a fresh alarm cycle. Reset the acknowledgment and clear the cleared-time.
		updates["state"] = string(AlarmStateActive)
		updates["severity"] = severity
		updates["raised_time"] = occurredTime
		updates["cleared_time"] = sql.NullTime{}
		updates["acknowledged"] = false
		updates["acknowledged_time"] = sql.NullTime{}
		updates["acknowledged_by"] = sql.NullString{}
		updates["last_value"] = lastValue
		etype = AlarmEventRaised
		mutate = func() {
			existing.State, existing.Severity, existing.RaisedTime = string(AlarmStateActive), severity, occurredTime
			existing.ClearedTime, existing.Acknowledged = sql.NullTime{}, false
			existing.AcknowledgedTime, existing.AcknowledgedBy, existing.LastValue = sql.NullTime{}, sql.NullString{}, lastValue
		}
	case anyActive:
		// Stayed ACTIVE: track the current max-tier severity and latest value. Emit only on a severity
		// move — a value-only update is not a state change a subscriber needs.
		updates["severity"] = severity
		updates["last_value"] = lastValue
		if t, changed := severityTransition(prevSeverity, severity); changed {
			etype = t
			mutate = func() { existing.Severity, existing.LastValue = severity, lastValue }
		}
	case prevState == string(AlarmStateActive):
		// The last active contributor resolved: clear the alarm.
		updates["state"] = string(AlarmStateCleared)
		updates["cleared_time"] = sql.NullTime{Time: occurredTime, Valid: true}
		updates["last_value"] = lastValue
		etype = AlarmEventCleared
		mutate = func() {
			existing.State = string(AlarmStateCleared)
			existing.ClearedTime, existing.LastValue = sql.NullTime{Time: occurredTime, Valid: true}, lastValue
		}
	default:
		// CLEARED→CLEARED: only the contributor set changed (a tombstone update — e.g. an out-of-order
		// resolve for a rule that never re-raised). Persist it (it guards a future equal-ts raise) but
		// emit no event; the row's visible state is unchanged.
	}

	res := api.RDB.DB(ctx).Model(existing).Where("state = ?", prevState).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	// A concurrent operator ack/clear (or delete cascade) moved the row between the read and this
	// write: don't claim the transition or emit its event.
	if res.RowsAffected == 0 {
		return nil
	}
	if mutate != nil {
		mutate()
		prev := prevSeverity
		if etype != AlarmEventEscalated && etype != AlarmEventDeescalated {
			prev = "" // previousSeverity is meaningful only on a severity move
		}
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(existing, etype, prev, occurredTime))
	}
	return nil
}
