// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var suppressionEvaluationFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "devicechain_devicemanagement_suppression_evaluation_failures_total",
	Help: "Suppression evaluation errors, failing toward paging.",
}, []string{"phase"})

func init() {
	for _, phase := range []string{"cycle_start", "live"} {
		suppressionEvaluationFailures.WithLabelValues(phase).Add(0)
	}
}

// Test seams are replaced only by serial package tests.
var cycleStampInsertHookForTest func(*gorm.DB) error
var suppressionEvaluationHookForTest func(context.Context, uint, string, string, time.Time) (*SuppressionMatch, error)

func (api *Api) evaluateSuppression(ctx context.Context, deviceId uint, key, severity string, at time.Time) (*SuppressionMatch, error) {
	if suppressionEvaluationHookForTest != nil {
		return suppressionEvaluationHookForTest(ctx, deviceId, key, severity, at)
	}
	return api.SuppressionFor(ctx, deviceId, key, severity, at)
}

func suppressionEvaluationFailed(err error, phase string) {
	suppressionEvaluationFailures.WithLabelValues(phase).Inc()
	log.Warn().Err(err).Str("phase", phase).Msg("Could not evaluate alarm suppression; continuing toward paging")
}

func (api *Api) cycleStartSuppression(ctx context.Context, deviceId uint, key, severity string, at time.Time) *SuppressionMatch {
	match, err := api.evaluateSuppression(ctx, deviceId, key, severity, at)
	if err != nil {
		suppressionEvaluationFailed(err, "cycle_start")
		return nil
	}
	return match
}

func insertCycleSuppression(tx *gorm.DB, alarm *Alarm, match *SuppressionMatch) error {
	if match == nil {
		return nil
	}
	if cycleStampInsertHookForTest != nil {
		if err := cycleStampInsertHookForTest(tx); err != nil {
			return err
		}
	}
	// Tenant scoping also fills TenantId on create; copy it explicitly for the immutable history.
	// Two cycles of one alarm raised at the same instant share one disposition
	// and one suppression stamp. Other insert failures still roll back the edge.
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "alarm_id"}, {Name: "raised_time"}},
		DoNothing: true,
	}).Create(&AlarmCycleSuppression{
		TenantId: alarm.TenantId, AlarmId: alarm.ID, RaisedTime: alarm.RaisedTime,
		AlarmToken: alarm.Token, WindowToken: match.WindowToken,
		WindowName:   sql.NullString{String: match.WindowName, Valid: match.WindowName != ""},
		ScopeAnchors: alarm.ScopeAnchors,
	}).Error
}

func (api *Api) suppressAlarmEvent(ctx context.Context, event *AlarmStateChangeEvent) {
	if event.OriginatorType != string(entity.TypeDevice) ||
		(event.EventType != AlarmEventRaised && event.EventType != AlarmEventEscalated) {
		return
	}
	event.SuppressedUntil, event.SuppressionWindowToken = nil, ""
	rctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	match, err := api.evaluateSuppression(rctx, event.OriginatorId, event.AlarmKey, event.Severity, time.Now())
	if err != nil {
		suppressionEvaluationFailed(err, "live")
		return
	}
	if match != nil {
		until := match.Until.UTC()
		event.SuppressedUntil, event.SuppressionWindowToken = &until, match.WindowToken
	}
}
