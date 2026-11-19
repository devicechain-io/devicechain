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
	ID            uint64         `gorm:"primaryKey;autoIncrement;index:idx_pending_alarm_events_tenant_id,priority:2,where:quarantined_at IS NULL"`
	TenantId      string         `gorm:"not null;size:128;index:idx_pending_alarm_events_tenant_id,priority:1,where:quarantined_at IS NULL;check:chk_pending_alarm_events_tenant_id,tenant_id <> ''"`
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

// ErrAlarmEventBrokerUnavailable marks transport failures shared by all tenants.
var ErrAlarmEventBrokerUnavailable = errors.New("alarm event broker unavailable")

const alarmEventLastErrorRunes = 2048

const AlarmEventDrainPageSize = rdb.MaxLookupKeys

type AlarmEventDrainResult struct{ Tenants, Failed, Published, Quarantined, Skipped int }

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

// DrainAlarmEvents runs one relay pass over every tenant and reports the backlog to
// AlarmEventObserver. See drainAlarmEvents.
func (api *Api) DrainAlarmEvents(ctx context.Context, pageSize int) (AlarmEventDrainResult, error) {
	return api.drainAlarmEvents(ctx, pageSize, true)
}

// drainAlarmEvents publishes a page of each tenant's queued events, in id order. Its only
// system-context reads are the tenant discovery (tenant IDs with a row ready to publish) and,
// when observe is set, the backlog counts (numbers, no row content). WithTenant preserves system
// authority, so a system ctx is refused, and every tenant's reads, publishes and deletes run on a
// fresh context with no system authority and no request claims, keeping only ctx's cancellation.
// The publisher is read once, so a broker restart that replaces it mid-pass does not race the pass.
func (api *Api) drainAlarmEvents(ctx context.Context, pageSize int, observe bool) (AlarmEventDrainResult, error) {
	var res AlarmEventDrainResult
	if observe {
		defer api.observeAlarmEvents(ctx)
	}
	pub := api.alarmPublisher()
	if pub == nil {
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
	sys := api.RDB.DB(core.WithSystemContext(base))
	var tenants []string
	// Cross-tenant discovery of tenant IDs only, and only of tenants with a row to publish: a
	// tenant whose rows are all quarantined is not visited again.
	if err := sys.Model(&PendingAlarmEvent{}).Where("quarantined_at IS NULL").Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return res, err
	}
	res.Tenants = len(tenants)
	var lastErr error
	for _, tenant := range tenants {
		if tenant == "" {
			log.Error().Msg("Skipping queued alarm events with no tenant")
			continue
		}
		published, poison, skipped, err := api.drainTenantAlarmEvents(core.WithTenant(base, tenant), pub, pageSize)
		res.Published += published
		res.Quarantined += poison
		res.Skipped += skipped
		if err != nil {
			res.Failed++
			lastErr = errors.Join(lastErr, err)
			log.Warn().Err(err).Str("tenant", tenant).Msg("Alarm events stay queued for the next relay pass")
		}
		if errors.Is(err, ErrAlarmEventBrokerUnavailable) {
			break // a transport failure would hold every later tenant too
		}
	}
	if res.Skipped > 0 {
		log.Warn().Int("skipped", res.Skipped).Msg("Unknown alarm event types stay queued for a newer relay")
	}
	var publishErr *alarmEventPublishError
	if errors.As(lastErr, &publishErr) {
		return res, lastErr
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

type alarmEventPublishError struct{ err error }

func (e *alarmEventPublishError) Error() string { return "publishing alarm event: " + e.err.Error() }
func (e *alarmEventPublishError) Unwrap() error { return e.err }

func (api *Api) drainTenantAlarmEvents(ctx context.Context, pub AlarmEventPublisher, pageSize int) (int, int, int, error) {
	db := api.RDB.DB(ctx)
	var rows []PendingAlarmEvent
	if err := db.Where("quarantined_at IS NULL").Order("id").Limit(pageSize).Find(&rows).Error; err != nil {
		return 0, 0, 0, err
	}
	done := make([]uint64, 0, len(rows))
	quarantined, skipped := 0, 0
	var passErr error
	for _, row := range rows {
		var ev AlarmStateChangeEvent
		err := json.Unmarshal(row.Payload, &ev)
		decoded := err == nil
		if err == nil && ev.AlarmToken == "" {
			err = errors.New("empty alarm token")
		}
		if err == nil && !ev.EventType.Valid() {
			skipped++
			continue // version skew: a newer binary can relay this unchanged row
		}
		if err == nil {
			err = pub.PublishAlarmEventDedup(ctx, &ev, alarmEventDedupID(row.TenantId, row.ID))
			if err != nil && !errors.Is(err, ErrAlarmEventUnpublishable) {
				passErr = &alarmEventPublishError{err}
				break
			}
		}
		if err != nil {
			runes := []rune(err.Error())
			if len(runes) > alarmEventLastErrorRunes {
				runes = runes[:alarmEventLastErrorRunes]
			}
			detail := string(runes)
			updated := db.Model(&PendingAlarmEvent{}).Where("id = ? AND quarantined_at IS NULL", row.ID).
				Updates(map[string]any{"quarantined_at": time.Now().UTC(), "last_error": detail})
			if updated.Error != nil {
				passErr = updated.Error
				break
			}
			if updated.RowsAffected == 0 {
				continue
			}
			// The UPDATE has committed before this best-effort operator index write.
			api.alarmEventMu.Lock()
			letter := api.AlarmEventDeadLetter
			api.alarmEventMu.Unlock()
			if letter != nil {
				reference := ev.AlarmToken
				if reference == "" || !decoded {
					reference = fmt.Sprintf("pending-alarm-event-%s-%d", row.TenantId, row.ID)
				}
				if e := letter(ctx, reference, row.Payload, errors.New(detail)); e != nil {
					log.Warn().Err(e).Uint64("id", row.ID).Msg("Unable to dead-letter quarantined alarm event")
				}
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
			return len(done), quarantined, skipped, errors.Join(passErr, err)
		}
	}
	return len(done), quarantined, skipped, passErr
}

// Stable across rolling upgrades; do not change the name.
const alarmEventDrainLockName = "device-management-alarm-event-drain"

// DrainAlarmEventsLocked is the periodic pass: one replica at a time, under the advisory lock,
// reporting the backlog.
func (api *Api) DrainAlarmEventsLocked(ctx context.Context) error {
	return api.drainAlarmEventsLocked(ctx, true)
}

// drainAlarmEventsLocked runs a pass under the advisory lock. The lock is held on its own
// session while the pass works on pooled connections, as the deletion outbox's is: if that
// session is lost mid-pass (a terminated backend, a failover), another replica can take the lock
// and publish while this pass finishes its page. Delivery is at least once either way, and in
// that window two passes can publish one tenant's events out of id order.
func (api *Api) drainAlarmEventsLocked(ctx context.Context, observe bool) error {
	if observe {
		api.observeAlarmEvents(ctx)
	}
	pass := func() error { _, err := api.drainAlarmEvents(ctx, AlarmEventDrainPageSize, false); return err }
	// SQLite, the unit-test engine, has no pg_try_advisory_lock and runs in one process.
	if api.RDB.DB(ctx).Dialector.Name() != "postgres" {
		return pass()
	}
	return api.RDB.SingletonPass(ctx, alarmEventDrainLockName, pass)
}

// observeAlarmEvents runs on every replica, outside the singleton pass.
func (api *Api) observeAlarmEvents(ctx context.Context) {
	if api.AlarmEventObserver == nil {
		return
	}
	sys := api.RDB.DB(core.WithSystemContext(ctx))
	// Three reads per pass, whatever the number of tenants: two counts and the oldest ready
	// row by primary key (ids follow insertion, closely enough to date the backlog).
	var pending, quarantined int64
	var oldest []PendingAlarmEvent
	err := sys.Model(&PendingAlarmEvent{}).Where("quarantined_at IS NULL").Count(&pending).Error
	if err == nil {
		err = sys.Model(&PendingAlarmEvent{}).Where("quarantined_at IS NOT NULL").Count(&quarantined).Error
	}
	if err == nil && pending > 0 {
		err = sys.Select("created_at").Where("quarantined_at IS NULL").Order("id").Limit(1).Find(&oldest).Error
	}
	if err != nil {
		log.Warn().Err(err).Msg("Unable to observe pending alarm events")
		return
	}
	var age time.Duration
	if len(oldest) > 0 {
		age = max(0, time.Since(oldest[0].CreatedAt))
	}
	api.AlarmEventObserver(int(pending), age, int(quarantined))
}
