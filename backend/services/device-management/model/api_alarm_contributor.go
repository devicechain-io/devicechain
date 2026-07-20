// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
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
//
// CONTRIBUTOR STRANDING (D6): the contributor identity (the ruleID passed here) is the VERSION-FREE
// stable rule key, minted by the REACT dispatcher (stableContributorID) — NOT the versioned composed
// id. A routine profile republish rotates the versioned id but not the stable key, so one logical rule
// maps to ONE contributor across versions: the new version's edges update and clear the SAME
// contributor rather than forking (and stranding) a fresh one per version. This closes the primary
// stranding case. RESIDUALS (all bounded, and all the same family — a raised contributor whose logical
// rule stops producing edges until the NEXT full raise→resolve cycle re-derives it; strictly better
// than the pre-fix PERMANENT strand, and closed by the same future explicit-teardown Resolved):
//   - a rule DELETED outright, or a reused-id def change whose new body never matches, until the
//     condition next cycles (needs a Resolved on DETECT RemoveRule + a Publisher orphan-publish exception);
//   - a condition that ceases in the WINDOW between a profile republish and the new version's first own
//     raise: the old version's frontier resolve is correctly dropped by the supersession gate
//     (event-processing dropSupersededDetections), and the new version never raised so it emits no
//     resolve, so the alarm holds ACTIVE until the new version's next full cycle.
//
// OPERATOR CLEAR vs the reference count (slice-6 product call, inert while the gate is off): an operator
// ClearAlarm sets state=CLEARED without touching the contributor set, so the next edge for that key
// re-derives ACTIVE from the still-raising contributors and REACTIVATES — a manual clear of a
// still-breaching alarm is transient by design (pure reference counting: the condition still holds). A
// RESOLVE edge can therefore emit a RAISED reactivation; whether operator clear should instead tombstone
// the set is a slice-6 product call.
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
	// Normalize the decision time to UTC so every stored contributor DecisionTs has one canonical zone:
	// the serialized set is then byte-stable, and .Equal-based ordering is unaffected either way.
	occurredTime = occurredTime.UTC()

	// Retry the read-modify-write IN PROCESS on a CAS conflict so a lost race does not consume a
	// delivery attempt: re-reading picks up the winner's version, and the fold is idempotent +
	// order-independent so it converges. A persistent conflict past this cap still surfaces the
	// conflict error, which is left unacked to the consumer's redelivery cap as the outer backstop — but under
	// the realistic "one message per rule edge" volume a single in-process retry almost always wins.
	const maxCASAttempts = 5
	var err error
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		if err = api.applyContributorEdgeOnce(ctx, deviceId, alarmKey, metricKey, ruleID, raised, severity, value, occurredTime); !errors.Is(err, errAlarmContributorConflict) {
			return err // success (nil), an idempotent no-op (nil), or a non-conflict error
		}
	}
	return err // exhausted in-process retries under sustained contention: left unacked for redelivery
}

// applyContributorEdgeOnce is one read-modify-write attempt of the contributor fold; it returns
// errAlarmContributorConflict when its optimistic CAS loses to a concurrent write, which the caller
// retries. Split out so the CAS retry re-reads the row (and its fresh version) each attempt.
func (api *Api) applyContributorEdgeOnce(ctx context.Context, deviceId uint,
	alarmKey, metricKey, ruleID string, raised bool, severity string, value *float64, occurredTime time.Time) error {
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
	// A CLEARED (all-tombstone) set, or an active set of only unknown-rank contributors (corrupt JSON),
	// has no derivable tier. Stamp INDETERMINATE rather than an empty string so the row always carries a
	// valid AlarmSeverity for the `severity: String!` API and severity filters/badges.
	if newSeverity == "" {
		newSeverity = string(AlarmSeverityIndeterminate)
	}
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
		TokenReference:     rdb.TokenReference{Token: uuid.New().String()},
		OriginatorType:     string(entity.TypeDevice),
		OriginatorId:       deviceId,
		AlarmKey:           alarmKey,
		MetricKey:          metricKey,
		Severity:           severity,
		RaisedTime:         occurredTime,
		LastValue:          lastValue,
		Contributors:       contributors,
		ContributorVersion: 1,
	}
	if anyActive {
		created.State = string(AlarmStateActive)
	} else {
		created.State = string(AlarmStateCleared)
		created.ClearedTime = sql.NullTime{Time: occurredTime, Valid: true}
	}
	// A concurrent create loses the per-(originator, alarmKey) partial unique index → a duplicate-key
	// error → the consumer leaves it unacked and the redelivery folds into the winner's row. So a create race
	// self-heals; it is the fold-into-an-existing-row race that needs the version CAS below.
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return err
	}
	if anyActive {
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(created, AlarmEventRaised, "", occurredTime))
	}
	return nil
}

// errAlarmContributorConflict is returned when the CAS write loses to a concurrent modification (a
// racing fold on another replica, or an operator ack/clear) — a RETRYABLE signal: the consumer leaves
// it unacked and the redelivery re-reads the moved row and re-folds. The fold (contributorSet.apply) is
// idempotent and order-independent, so the retry re-derives the correct state by construction.
var errAlarmContributorConflict = errors.New("alarm-edge: concurrent contributor modification, retry")

// updateAlarmFromContributors folds the re-derived state into an existing row under an OPTIMISTIC CAS
// on (state, contributor_version): the contributor set is an ACCUMULATOR, not the evaluator's
// self-healing scalar columns, so a lost write is permanent state divergence (an edge is delivered
// once and DETECT never re-emits it). The predicate therefore guards BOTH the state (a concurrent
// operator ack/clear) AND the version this fold read (a concurrent fold on another HA replica that
// shares the raise-alarm durable consumer). A RowsAffected 0 is a CONFLICT → errAlarmContributorConflict
// → retry (left unacked), NOT a silent drop. It handles the four transitions to {ACTIVE, CLEARED}: reactivate
// (CLEARED→ACTIVE, resets ack), escalate/de-escalate or value-update (ACTIVE→ACTIVE), clear
// (ACTIVE→CLEARED), and a tombstone-only contributor update that leaves the row CLEARED.
func (api *Api) updateAlarmFromContributors(ctx context.Context, existing *Alarm,
	severity string, anyActive bool, lastValue sql.NullFloat64, contributors []byte, occurredTime time.Time) error {
	prevState := existing.State
	prevSeverity := existing.Severity
	prevVersion := existing.ContributorVersion

	updates := map[string]interface{}{
		"contributors":        contributors,
		"contributor_version": prevVersion + 1,
	}
	// A value rides only on an edge that HAS one (a raise carrying a reading). When the edge carries
	// none (every resolve, and a silence-driven raise), keep the row's existing last value rather than
	// NULLing it — a resolve of one contributor must not erase a co-contributor's real reading.
	setLastValue := func() {
		if lastValue.Valid {
			updates["last_value"] = lastValue
		}
	}
	applyLastValue := func() {
		if lastValue.Valid {
			existing.LastValue = lastValue
		}
	}

	// mutate reflects the POST-transition fields onto the in-memory row just before the event is built
	// (a map-based Updates does not write them back), but only on a write that won the CAS.
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
		setLastValue()
		etype = AlarmEventRaised
		mutate = func() {
			existing.State, existing.Severity, existing.RaisedTime = string(AlarmStateActive), severity, occurredTime
			existing.ClearedTime, existing.Acknowledged = sql.NullTime{}, false
			existing.AcknowledgedTime, existing.AcknowledgedBy = sql.NullTime{}, sql.NullString{}
			applyLastValue()
		}
	case anyActive:
		// Stayed ACTIVE: track the current max-tier severity and latest value. Emit only on a severity
		// move — a value-only update is not a state change a subscriber needs.
		updates["severity"] = severity
		setLastValue()
		if t, changed := severityTransition(prevSeverity, severity); changed {
			etype = t
			mutate = func() { existing.Severity = severity; applyLastValue() }
		}
	case prevState == string(AlarmStateActive):
		// The last active contributor resolved: clear the alarm.
		updates["state"] = string(AlarmStateCleared)
		updates["cleared_time"] = sql.NullTime{Time: occurredTime, Valid: true}
		setLastValue()
		etype = AlarmEventCleared
		mutate = func() {
			existing.State = string(AlarmStateCleared)
			existing.ClearedTime = sql.NullTime{Time: occurredTime, Valid: true}
			applyLastValue()
		}
	default:
		// CLEARED→CLEARED: only the contributor set changed (a tombstone update — e.g. an out-of-order
		// resolve for a rule that never re-raised). Persist it (it guards a future equal-ts raise) but
		// emit no event; the row's visible state is unchanged.
	}

	res := api.RDB.DB(ctx).Model(existing).
		Where("state = ? AND contributor_version = ?", prevState, prevVersion).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// The CAS lost: a concurrent fold or operator transition changed (state, contributor_version)
		// since the read. Retry — do NOT drop the edge (the accumulator would lose it forever).
		return errAlarmContributorConflict
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
