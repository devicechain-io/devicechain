// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// parseFiniteFloat parses s as a finite float64. A non-numeric or non-finite value
// (NaN/±Inf) is rejected: it can neither be compared meaningfully against a threshold
// nor stored, and a sentinel like "NaN" must not silently clear a live alarm (NaN
// fails every ordered comparison) or fire a NEQ tier.
func parseFiniteFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// This file is the DB-facing half of the SIMPLE alarm evaluator (ADR-041): it loads
// a device's alarm rules, evaluates them against an incoming resolved measurement
// (via the pure logic in alarm_eval.go), and upserts the resulting Alarm state. It
// is driven by the discrete resolved-events consumer (processor.AlarmEvaluator),
// which supplies a tenant-scoped context — every query/write here inherits the
// per-tenant DB scope from that context.

// EvaluateMeasurementAlarms evaluates the SIMPLE alarm rules of the device that
// produced a resolved measurements payload and upserts the resulting alarm state.
// For each alarm key whose watched metric appears in the payload it raises,
// escalates, or auto-clears a single Alarm row keyed by (device, alarmKey). A metric
// the payload doesn't carry leaves that key's alarm untouched (no data, no decision);
// a non-numeric value is skipped (STRING metrics carry no numeric alarm). An unknown
// device or a profile with no alarm rules is a no-op.
//
// occurredTime is the measurement's event time and is stamped as the alarm's
// raised/cleared time so the alarm timeline reflects when the condition changed, not
// when it was processed.
func (api *Api) EvaluateMeasurementAlarms(ctx context.Context, deviceToken string,
	payload *ResolvedMeasurementsPayload, occurredTime time.Time) error {
	if payload == nil {
		return nil
	}

	// Reduce the payload to the latest numeric value per metric key. A payload may
	// carry several readings; the last one wins for alarm evaluation (the current
	// value is what a threshold alarm cares about).
	values := make(map[string]float64)
	for _, entry := range payload.Entries {
		for _, mx := range entry.Entries {
			if v, ok := parseFiniteFloat(mx.Value); ok {
				values[mx.Name] = v
			}
		}
	}
	if len(values) == 0 {
		return nil
	}

	// Resolve the source device by its token (ADR-044): the wire carries the token,
	// not the row id. This is one indexed lookup per measurement message — the same
	// cost as the DevicesById call it replaces (this method is on *Api, so the call
	// binds to the uncached accessor even when the evaluator holds a CachedApi). The
	// numeric id drives every id-keyed internal below (alarm originator, threshold
	// attributes) — those stay device-management-local references.
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
	deviceId := devices[0].ID
	deviceTypeId := devices[0].DeviceTypeId

	defs, err := api.AlarmDefinitionsByDeviceType(ctx, deviceTypeId)
	if err != nil {
		return err
	}
	if len(defs) == 0 {
		return nil
	}

	// Group the enabled SIMPLE rules by alarm key; all tiers of a key share a metric
	// key (enforced at rule declaration), so it is safe to read the metric off any
	// tier.
	byKey := make(map[string][]*AlarmDefinition)
	for _, d := range defs {
		if !d.Enabled || d.ConditionType != string(AlarmConditionSimple) {
			continue
		}
		byKey[d.AlarmKey] = append(byKey[d.AlarmKey], d)
	}

	for alarmKey, tiers := range byKey {
		metricKey := tiers[0].MetricKey
		value, ok := values[metricKey]
		if !ok {
			continue
		}
		severity, satisfied, err := highestSatisfiedSeverity(tiers, value, metricKey,
			func(t *AlarmDefinition) (float64, bool, error) {
				return api.resolveThreshold(ctx, deviceId, t)
			})
		if err != nil {
			return err
		}
		if satisfied {
			if err := api.raiseOrEscalateAlarm(ctx, deviceId, alarmKey, metricKey,
				severity, value, occurredTime); err != nil {
				return err
			}
		} else {
			if err := api.autoClearAlarm(ctx, deviceId, alarmKey, value, occurredTime); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveThreshold returns the numeric threshold for a rule: the static Threshold if
// set, otherwise a dynamic value read from the device's entity attribute named by
// ThresholdAttr. Dynamic resolution uses SERVER-then-SHARED scope precedence: an
// alarm threshold is platform-set configuration, so a server-scope value (hidden
// from the device) wins, with a shared-scope value (device-readable config) as
// fallback. A missing attribute yields no threshold, so the tier is skipped rather
// than firing on a bogus bound.
//
// ELIGIBILITY IS TYPE-DRIVEN, identical to the DETECT fact producer (numericAttributeValue,
// ADR-051 slice 4c-3): only a DOUBLE- or LONG-typed attribute is a numeric threshold. A
// STRING attribute that merely parses as a number ("50") is NOT eligible — the attribute's
// declared type, not the accident of its bytes, decides. This keeps the alarm engine and the
// DETECT engine in lockstep: DETECT never projects a non-numeric-typed attribute (the producer
// emits a removal), so a STRING threshold that the old value-only parse honored here would fire
// an alarm while the equivalent DETECT rule stayed inert. A non-eligible SERVER value therefore
// falls through to the SHARED scope exactly as it does in DETECT's flatten.
func (api *Api) resolveThreshold(ctx context.Context, deviceId uint, rule *AlarmDefinition) (float64, bool, error) {
	if rule.Threshold.Valid {
		return rule.Threshold.Float64, true, nil
	}
	if !rule.ThresholdAttr.Valid || rule.ThresholdAttr.String == "" {
		return 0, false, nil
	}
	for _, scope := range []AttributeScope{AttributeScopeServer, AttributeScopeShared} {
		s := string(scope)
		attrs, err := api.EntityAttributesByEntity(ctx, string(entity.TypeDevice), deviceId, &s)
		if err != nil {
			// A DB failure is not "no threshold": surfacing it lets the caller retry
			// rather than skip the tier and spuriously auto-clear a live alarm.
			return 0, false, err
		}
		for _, a := range attrs {
			if a.AttrKey != rule.ThresholdAttr.String {
				continue
			}
			var val *string
			if a.Value.Valid {
				val = &a.Value.String
			}
			// numericAttributeValue is the single source of truth for "is this a numeric
			// threshold" (DOUBLE/LONG, present, finite) — the same gate the DETECT producer
			// applies, so the two engines agree on which attributes are dynamic thresholds.
			if v, ok := numericAttributeValue(a.ValueType, val); ok {
				return v, true, nil
			}
		}
	}
	return 0, false, nil
}

// alarmByOriginatorKey returns the single live alarm for (originator, alarmKey), or
// (nil, nil) when none exists. The partial unique index guarantees at most one live
// row; the default soft-delete scope and the tenant callback confine the lookup to
// live, in-tenant rows.
func (api *Api) alarmByOriginatorKey(ctx context.Context, originatorType string,
	originatorId uint, alarmKey string) (*Alarm, error) {
	var found Alarm
	result := api.RDB.DB(ctx).Where(
		"originator_type = ? AND originator_id = ? AND alarm_key = ?",
		originatorType, originatorId, alarmKey).First(&found)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, result.Error
	}
	return &found, nil
}

// raiseOrEscalateAlarm brings the alarm for (device, alarmKey) to ACTIVE at severity,
// creating it if absent, reactivating it if previously CLEARED, or updating its
// severity/value in place if already ACTIVE (ADR-041 dec 3/4: one row per key, no
// duplicate on re-crossing). Severity tracks the current highest-satisfied tier —
// it escalates as the value worsens and de-escalates while still in alarm — because
// the row should reflect the present condition, not a high-water mark that outlives
// it. A reactivation resets the acknowledgment: a fresh alarm cycle has not been seen
// yet. Writes are column-limited so a concurrent operator ack/clear isn't clobbered.
func (api *Api) raiseOrEscalateAlarm(ctx context.Context, deviceId uint,
	alarmKey, metricKey, severity string, value float64, occurredTime time.Time) error {
	existing, err := api.alarmByOriginatorKey(ctx, string(entity.TypeDevice), deviceId, alarmKey)
	if err != nil {
		return err
	}
	lastValue := sql.NullFloat64{Float64: value, Valid: true}

	if existing == nil {
		created := &Alarm{
			TokenReference: rdb.TokenReference{Token: uuid.New().String()},
			OriginatorType: string(entity.TypeDevice),
			OriginatorId:   deviceId,
			AlarmKey:       alarmKey,
			MetricKey:      metricKey,
			State:          string(AlarmStateActive),
			Severity:       severity,
			RaisedTime:     occurredTime,
			LastValue:      lastValue,
		}
		if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
			return err
		}
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(created, AlarmEventRaised, "", occurredTime))
		return nil
	}

	if existing.State == string(AlarmStateCleared) {
		// Out-of-order guard: a measurement older than the clear it would undo must
		// not reactivate the alarm (workers/redelivery can reorder a device's
		// measurements). Without this a stale high reading delivered after a newer
		// low reading would re-raise a just-cleared alarm and leave it stuck ACTIVE.
		if existing.ClearedTime.Valid && occurredTime.Before(existing.ClearedTime.Time) {
			return nil
		}
		// Predicate the flip on the from-state and gate the emit on a row actually
		// changing: a concurrent operator ClearAlarm / evaluator worker, or a delete
		// cascade, may have moved the row between the read above and this write, in
		// which case RowsAffected is 0 and we must neither claim the new state nor
		// emit a phantom RAISED for a transition that did not happen.
		res := api.RDB.DB(ctx).Model(existing).Where("state = ?", string(AlarmStateCleared)).
			Updates(map[string]interface{}{
				"last_value":        lastValue,
				"severity":          severity,
				"state":             string(AlarmStateActive),
				"raised_time":       occurredTime,
				"cleared_time":      sql.NullTime{},
				"acknowledged":      false,
				"acknowledged_time": sql.NullTime{},
				"acknowledged_by":   sql.NullString{},
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		// Reflect the reactivation on the in-memory row so the emitted event carries
		// the new state (a map Updates does not write back into the struct).
		existing.State = string(AlarmStateActive)
		existing.Severity = severity
		existing.RaisedTime = occurredTime
		existing.ClearedTime = sql.NullTime{}
		existing.Acknowledged = false
		existing.AcknowledgedTime = sql.NullTime{}
		existing.AcknowledgedBy = sql.NullString{}
		existing.LastValue = lastValue
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(existing, AlarmEventRaised, "", occurredTime))
		return nil
	}
	// Already ACTIVE: track the current highest-satisfied severity and latest value.
	// Out-of-order guard (mirrors the reactivate/auto-clear guards): a measurement
	// predating this alarm cycle must not drive its severity or emit a spurious
	// escalate/de-escalate. A reorder *within* the active window (both readings after
	// the raise) still lands latest-processed-wins; a full fix needs a per-alarm
	// last-evaluated-time watermark (deferred, see 2.C notes).
	if occurredTime.Before(existing.RaisedTime) {
		return nil
	}
	prevSeverity := existing.Severity
	res := api.RDB.DB(ctx).Model(existing).Where("state = ?", string(AlarmStateActive)).
		Updates(map[string]interface{}{
			"last_value": lastValue,
			"severity":   severity,
		})
	if res.Error != nil {
		return res.Error
	}
	// A concurrent clear won the row: don't resurrect its severity or emit.
	if res.RowsAffected == 0 {
		return nil
	}
	existing.Severity = severity
	existing.LastValue = lastValue
	// Emit only when the severity actually moved: a value-only update is not a
	// state change a subscriber needs (and would flood the stream at ingest rate).
	if etype, changed := severityTransition(prevSeverity, severity); changed {
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(existing, etype, prevSeverity, occurredTime))
	}
	return nil
}

// autoClearAlarm moves an ACTIVE alarm for (device, alarmKey) to CLEARED because the
// condition no longer holds. A missing or already-CLEARED alarm is a no-op. This is
// the implicit clear of the SIMPLE evaluator (condition false ⇒ clear); an explicit
// clear rule / hysteresis to damp threshold flapping is a later additive refinement.
// Column-limited for the same concurrency reason as the raise path.
func (api *Api) autoClearAlarm(ctx context.Context, deviceId uint, alarmKey string,
	value float64, occurredTime time.Time) error {
	existing, err := api.alarmByOriginatorKey(ctx, string(entity.TypeDevice), deviceId, alarmKey)
	if err != nil {
		return err
	}
	if existing == nil || existing.State == string(AlarmStateCleared) {
		return nil
	}
	// Out-of-order guard: a measurement older than the raise it would undo must not
	// clear the alarm. Without this a stale low reading delivered after the reading
	// that raised the alarm would immediately clear a still-valid alarm.
	if occurredTime.Before(existing.RaisedTime) {
		return nil
	}
	clearedTime := sql.NullTime{Time: occurredTime, Valid: true}
	lastValue := sql.NullFloat64{Float64: value, Valid: true}
	// Predicate on ACTIVE + gate the emit on RowsAffected so a concurrent clear (a
	// manual ClearAlarm or another worker) doesn't produce a second CLEARED event.
	res := api.RDB.DB(ctx).Model(existing).Where("state = ?", string(AlarmStateActive)).
		Updates(map[string]interface{}{
			"state":        string(AlarmStateCleared),
			"cleared_time": clearedTime,
			"last_value":   lastValue,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	existing.State = string(AlarmStateCleared)
	existing.ClearedTime = clearedTime
	existing.LastValue = lastValue
	api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(existing, AlarmEventCleared, "", occurredTime))
	return nil
}
