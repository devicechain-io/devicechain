// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PendingAlarmEvent delivers immutable transition snapshots at least once. The broker
// honours alarm-event-<tenant>-<id> only within alarm-events' 120 s duplicate window.
// IDs order events per tenant within this outbox, with NO ordering across this outbox,
// agent effects (agent_effect_outbox.go), deletion cascades (deletion_outbox.go), or
// old pods publishing inline during a rolling upgrade. Keep an outbox-capable relay
// until pending rows drain when rolling back. Stored payloads use the additive event
// JSON contract; evolve readers before writers when introducing new event types.
type PendingAlarmEvent struct {
	ID            uint64         `gorm:"primaryKey;autoIncrement;index:idx_pending_alarm_events_tenant_id,priority:2"`
	TenantId      string         `gorm:"not null;size:128;index:idx_pending_alarm_events_tenant_id,priority:1"`
	Payload       datatypes.JSON `gorm:"not null"`
	CreatedAt     time.Time      `gorm:"not null"`
	QuarantinedAt *time.Time
	LastError     *string `gorm:"type:text"`
}

// AuditExempt excludes derived relay bookkeeping; the transition is already audited.
func (PendingAlarmEvent) AuditExempt() bool { return true }

var ErrNoAlarmEventPublisher = errors.New("no alarm event publisher is wired")

// ErrAlarmEventUnpublishable marks permanent encoding failures, never broker faults.
var ErrAlarmEventUnpublishable = errors.New("alarm event cannot be published")

const AlarmEventDrainPageSize = rdb.MaxLookupKeys

type AlarmEventDrainResult struct{ Tenants, Failed, Published, Quarantined int }

func alarmEventDedupID(tenant string, id uint64) string {
	return fmt.Sprintf("alarm-event-%s-%d", tenant, id)
}

// queueAlarmEventOnTx must use the transition's transaction, after create stamped tenancy.
// Failure is fatal to the transition: never commit a change without its announcement.
func queueAlarmEventOnTx(ctx context.Context, tx *gorm.DB, alarm *Alarm, event *AlarmStateChangeEvent) error {
	if alarm.TenantId == "" {
		return errors.New("alarm event requires a persisted tenant")
	}
	if tenant, ok := core.TenantFromContext(ctx); ok && !core.IsSystemContext(ctx) && tenant != alarm.TenantId {
		return errors.New("alarm event tenant differs from caller tenant")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encoding alarm event: %w", err)
	}
	// The row is relay bookkeeping, not data a caller reads: a member whose access is limited to
	// part of the tenant (a customer user acknowledging its own alarm) has already been checked
	// against the alarm itself, and the outbox declares no scope rule, so the insert drops the
	// caller's scope. It stays on the transition's transaction and under its tenant.
	bookkeeping := tx.WithContext(auth.WithClaims(ctx, nil))
	return bookkeeping.Create(&PendingAlarmEvent{TenantId: alarm.TenantId, Payload: payload, CreatedAt: time.Now().UTC()}).Error
}

// prepareAlarmEvent freezes enrichment BEFORE the transition transaction. Suppression
// is evaluated at the transition, not at a potentially much later publication/retry.
func (api *Api) prepareAlarmEvent(ctx context.Context, alarm *Alarm, kind AlarmEventType) *AlarmStateChangeEvent {
	ev := newAlarmStateChangeEvent(alarm, kind, "", time.Time{})
	api.describeAlarmDevice(ctx, ev)
	api.suppressAlarmEvent(ctx, ev)
	return ev
}

func copyAlarmEventPreparation(event, prepared *AlarmStateChangeEvent) {
	event.DeviceToken, event.DeviceName, event.DeviceTypeToken = prepared.DeviceToken, prepared.DeviceName, prepared.DeviceTypeToken
	event.SuppressedUntil, event.SuppressionWindowToken = prepared.SuppressedUntil, prepared.SuppressionWindowToken
}

// DrainAlarmEvents discovers tenant IDs only under system authority. WithTenant
// preserves that authority, so refuse system input and use a fresh, non-system base
// (without request claims/scope) for all tenant work, preserving only cancellation.
func (api *Api) DrainAlarmEvents(ctx context.Context, pageSize int) (AlarmEventDrainResult, error) {
	var res AlarmEventDrainResult
	if api.AlarmPublisher == nil {
		return res, ErrNoAlarmEventPublisher
	}
	if pageSize < 1 || pageSize > AlarmEventDrainPageSize {
		return res, fmt.Errorf("alarm event drain page size must be between 1 and %d", AlarmEventDrainPageSize)
	}
	if core.IsSystemContext(ctx) {
		return res, errors.New("alarm event drain must not inherit system authority")
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if ctx.Err() != nil {
		cancel()
	}
	var tenants []string
	// Intentional cross-tenant discovery of IDs only; no payload is read under system authority.
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingAlarmEvent{}).Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return res, err
	}
	res.Tenants = len(tenants)
	var lastErr error

	for _, tenant := range tenants {
		if tenant == "" {
			return res, errors.New("queued alarm event has no tenant")
		}
		tenantCtx := core.WithTenant(base, tenant)
		published, poison, err := api.drainTenantAlarmEvents(tenantCtx, pageSize)
		res.Published += published
		res.Quarantined += poison
		if err != nil {
			res.Failed++
			lastErr = err
			log.Warn().Err(err).Str("tenant", tenant).Msg("Alarm events remain queued for the next relay pass")
		}

		var shared *alarmEventPublishError
		if errors.As(err, &shared) {
			break
		}
	}
	if api.AlarmEventObserver != nil {
		var pending, quarantined int64
		var oldest time.Time
		var observeErr error
		for _, tenant := range tenants {
			db := api.RDB.DB(core.WithTenant(base, tenant))
			var n, q int64
			if err := db.Model(&PendingAlarmEvent{}).Where("quarantined_at IS NULL").Count(&n).Error; err != nil {
				observeErr = err
				break
			}
			if err := db.Model(&PendingAlarmEvent{}).Where("quarantined_at IS NOT NULL").Count(&q).Error; err != nil {
				observeErr = err
				break
			}
			var first []PendingAlarmEvent
			if n > 0 {
				if err := db.Select("created_at").Where("quarantined_at IS NULL").Order("created_at").Limit(1).Find(&first).Error; err != nil {
					observeErr = err
					break
				}
				if len(first) > 0 && (oldest.IsZero() || first[0].CreatedAt.Before(oldest)) {
					oldest = first[0].CreatedAt
				}
			}
			pending += n
			quarantined += q
		}
		if observeErr == nil {
			var age time.Duration
			if !oldest.IsZero() {
				age = max(0, time.Since(oldest))
			}
			api.AlarmEventObserver(int(pending), age, int(quarantined))
		} else {
			return res, errors.Join(lastErr, observeErr)
		}
	}

	var shared *alarmEventPublishError
	if errors.As(lastErr, &shared) {
		return res, lastErr
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

type alarmEventPublishError struct{ err error }

func (e *alarmEventPublishError) Error() string { return "publishing alarm event: " + e.err.Error() }
func (e *alarmEventPublishError) Unwrap() error { return e.err }

func (api *Api) drainTenantAlarmEvents(ctx context.Context, pageSize int) (int, int, error) {
	db := api.RDB.DB(ctx)
	var rows []PendingAlarmEvent
	if err := db.Where("quarantined_at IS NULL").Order("id").Limit(pageSize).Find(&rows).Error; err != nil {
		return 0, 0, err
	}
	done := make([]uint64, 0, len(rows))
	quarantined := 0
	var passErr error
	for _, row := range rows {
		var ev AlarmStateChangeEvent
		err := json.Unmarshal(row.Payload, &ev)
		if err == nil && (ev.AlarmToken == "" || !ev.EventType.Valid()) {
			err = errors.New("invalid alarm token or event type")
		}
		if err == nil {
			err = api.AlarmPublisher.PublishAlarmEventDedup(ctx, &ev, alarmEventDedupID(row.TenantId, row.ID))
			if err != nil && !errors.Is(err, ErrAlarmEventUnpublishable) {
				passErr = &alarmEventPublishError{err}
				break
			}
		}
		if err != nil {
			detail := err.Error()
			if len(detail) > 2048 {
				detail = string([]rune(detail)[:min(512, len([]rune(detail)))])
			}
			if e := db.Model(&PendingAlarmEvent{}).Where("id = ?", row.ID).Updates(map[string]any{"quarantined_at": time.Now().UTC(), "last_error": detail}).Error; e != nil {
				passErr = e
				break
			}
			quarantined++
			log.Error().Err(err).Uint64("id", row.ID).Str("tenant", row.TenantId).Msg("Quarantined alarm event; inspect stored payload and repair before replay")
			continue
		}
		done = append(done, row.ID)
	}
	// Immutable rows: remove only IDs whose publication was acknowledged.
	if len(done) > 0 {
		if err := db.Where("id IN ?", done).Delete(&PendingAlarmEvent{}).Error; err != nil {
			return len(done), quarantined, errors.Join(passErr, err)
		}
	}
	return len(done), quarantined, passErr
}

// Stable across rolling upgrades; do not change the name.
const alarmEventDrainLockName = "device-management-alarm-event-drain"

func (api *Api) DrainAlarmEventsLocked(ctx context.Context) error {
	pass := func() error { _, err := api.DrainAlarmEvents(ctx, AlarmEventDrainPageSize); return err }
	// SQLite unit tests have no pg_try_advisory_lock. Production Postgres uses the singleton.
	if api.RDB.DB(ctx).Dialector.Name() != "postgres" {
		return pass()
	}
	return api.RDB.SingletonPass(ctx, alarmEventDrainLockName, pass)
}
