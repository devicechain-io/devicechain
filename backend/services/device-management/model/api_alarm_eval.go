// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

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
func (api *Api) EvaluateMeasurementAlarms(ctx context.Context, deviceId uint,
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
			v, err := strconv.ParseFloat(mx.Value, 64)
			if err != nil {
				continue
			}
			values[mx.Name] = v
		}
	}
	if len(values) == 0 {
		return nil
	}

	devices, err := api.DevicesById(ctx, []uint{deviceId})
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
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
		value, ok := values[tiers[0].MetricKey]
		if !ok {
			continue
		}
		severity, satisfied := highestSatisfiedSeverity(tiers, value, func(t *AlarmDefinition) (float64, bool) {
			return api.resolveThreshold(ctx, deviceId, t)
		})
		if satisfied {
			if err := api.raiseOrEscalateAlarm(ctx, deviceId, alarmKey, tiers[0].MetricKey,
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
// fallback. A missing or non-numeric attribute yields no threshold, so the tier is
// skipped rather than firing on a bogus bound.
func (api *Api) resolveThreshold(ctx context.Context, deviceId uint, rule *AlarmDefinition) (float64, bool) {
	if rule.Threshold.Valid {
		return rule.Threshold.Float64, true
	}
	if !rule.ThresholdAttr.Valid || rule.ThresholdAttr.String == "" {
		return 0, false
	}
	for _, scope := range []AttributeScope{AttributeScopeServer, AttributeScopeShared} {
		s := string(scope)
		attrs, err := api.EntityAttributesByEntity(ctx, string(entity.TypeDevice), deviceId, &s)
		if err != nil {
			return 0, false
		}
		for _, a := range attrs {
			if a.AttrKey == rule.ThresholdAttr.String && a.Value.Valid {
				if v, err := strconv.ParseFloat(a.Value.String, 64); err == nil {
					return v, true
				}
			}
		}
	}
	return 0, false
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
		return api.RDB.DB(ctx).Create(created).Error
	}

	updates := map[string]interface{}{
		"last_value": lastValue,
		"severity":   severity,
	}
	if existing.State == string(AlarmStateCleared) {
		updates["state"] = string(AlarmStateActive)
		updates["raised_time"] = occurredTime
		updates["cleared_time"] = sql.NullTime{}
		updates["acknowledged"] = false
		updates["acknowledged_time"] = sql.NullTime{}
		updates["acknowledged_by"] = sql.NullString{}
	}
	return api.RDB.DB(ctx).Model(existing).Updates(updates).Error
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
	return api.RDB.DB(ctx).Model(existing).Updates(map[string]interface{}{
		"state":        string(AlarmStateCleared),
		"cleared_time": sql.NullTime{Time: occurredTime, Valid: true},
		"last_value":   sql.NullFloat64{Float64: value, Valid: true},
	}).Error
}
