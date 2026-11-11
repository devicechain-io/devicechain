// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 🔴 DELIVERY CONTRACT. The relay is at-least-once. A row's stable id is the Nats-Msg-Id, which the broker
// recognises only inside alarm-events' duplicate window (the broker default, 120 s): a republish after a
// crash between the broker's acknowledgement and the row's delete, followed by an outage longer than that
// window, is stored twice. Rows are claimed oldest-first per tenant but a failing row is rotated behind
// untouched ones, so two events for ONE alarm (for example an acknowledgement and a clear from one
// execution) can reach the stream in either order; subscribers must key on the alarm's state, not on arrival
// order. A row that has failed MaxAgentEffectAttempts times is left in place for an operator and not retried.
type AgentEffectOutbox struct {
	Id            string         `gorm:"primaryKey;size:32"`
	TenantId      string         `gorm:"not null;size:128"`
	ProposalToken string         `gorm:"not null;size:128"`
	Kind          string         `gorm:"not null;size:32"`
	Payload       datatypes.JSON `gorm:"not null"`
	CreatedAt     time.Time      `gorm:"not null;index"`
	Attempts      int            `gorm:"not null"`
	LastError     *string        `gorm:"type:text"`
}

// Queue rows derive from already audited alarm/proposal writes. Auditing their insertion,
// retries and removal would duplicate that history without recording another user action.
func (AgentEffectOutbox) AuditExempt() bool { return true }

func queueAgentEffect(tx *gorm.DB, proposal string, event *AlarmStateChangeEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	return tx.Create(&AgentEffectOutbox{Id: hex.EncodeToString(id[:]), ProposalToken: proposal,
		Kind: "alarm-event", Payload: payload, CreatedAt: time.Now().UTC()}).Error
}

const AgentEffectsDrainPageSize = rdb.MaxLookupKeys

// MaxAgentEffectAttempts leaves exhausted rows in place for operator inspection.
const MaxAgentEffectAttempts = 100

var ErrNoAgentEffectPublisher = errors.New("no alarm publisher is wired for agent effects")

type AgentEffectsDrainResult struct{ Tenants, Failed, Published, Removed int }

// DrainAgentEffects discovers only tenant identifiers with system authority. Every queue
// read, retry update and delete runs under the tenant that owns it.
func (api *Api) DrainAgentEffects(ctx context.Context, pageSize int) (AgentEffectsDrainResult, error) {
	var res AgentEffectsDrainResult
	if api.AlarmPublisher == nil {
		return res, ErrNoAgentEffectPublisher
	}
	if pageSize < 1 || pageSize > AgentEffectsDrainPageSize {
		return res, fmt.Errorf("agent effects page size must be between 1 and %d", AgentEffectsDrainPageSize)
	}
	var tenants []string
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&AgentEffectOutbox{}).Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return res, err
	}
	res.Tenants = len(tenants)
	var lastErr error
	for _, tenant := range tenants {
		published, removed, err := api.drainTenantAgentEffects(core.WithTenant(ctx, tenant), pageSize)
		res.Published += published
		res.Removed += removed
		if err != nil {
			res.Failed++
			lastErr = err
		}
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

func (api *Api) drainTenantAgentEffects(ctx context.Context, pageSize int) (published, removed int, err error) {
	var publishErr error
	var exhausted []AgentEffectOutbox
	// Claims and deletes share a transaction. Execution only inserts new random IDs, so
	// the relay never waits on an execution while holding an alarm or proposal lock.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []AgentEffectOutbox
		q := tx.Where("attempts < ?", MaxAgentEffectAttempts).Order("created_at").Order("id").Limit(pageSize)
		if tx.Dialector.Name() == "postgres" {
			q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var event AlarmStateChangeEvent
			pubErr := json.Unmarshal(row.Payload, &event)
			if pubErr == nil && (event.AlarmToken == "" || !event.EventType.Valid()) {
				pubErr = errors.New("invalid alarm event in agent effect outbox")
			}
			if row.Kind != "alarm-event" {
				pubErr = fmt.Errorf("unknown agent effect kind %q", row.Kind)
			}
			if pubErr == nil {
				api.describeAlarmDevice(ctx, &event)
				pubErr = api.AlarmPublisher.PublishAlarmEventDedup(ctx, &event, row.Id)
			}
			if pubErr != nil {
				publishErr = pubErr
				// Rotate failed rows behind untouched work even when a tenant's backlog
				// exceeds one page; a poison page must not starve every later effect.
				detail := []rune(pubErr.Error())
				msg := string(detail[:min(len(detail), 2048)])
				if err := tx.Model(&AgentEffectOutbox{}).Where("id = ?", row.Id).Updates(map[string]any{
					"attempts": gorm.Expr("attempts + 1"), "last_error": msg, "created_at": time.Now().UTC(),
				}).Error; err != nil {
					return err
				}
				if row.Attempts+1 == MaxAgentEffectAttempts {
					exhausted = append(exhausted, row)
				}
				continue
			}
			published++
			result := tx.Where("id = ?", row.Id).Delete(&AgentEffectOutbox{})
			if result.Error != nil {
				return result.Error
			}
			removed += int(result.RowsAffected)
		}
		return nil
	})
	if err != nil {
		removed = 0
	} else {
		for _, row := range exhausted {
			log.Error().Str("tenant", row.TenantId).Str("effect", row.Id).
				Int("attempts", MaxAgentEffectAttempts).Msg("Agent effect exhausted its attempts; retained for operator inspection.")
			if api.AgentEffectsExhausted != nil {
				api.AgentEffectsExhausted.Inc()
			}
		}
		err = publishErr
	}
	return
}

func (api *Api) DrainAgentEffectsLocked(ctx context.Context) error {
	ran, err := api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey("device-management-agent-effects-drain"), func() error {
		_, err := api.DrainAgentEffects(ctx, AgentEffectsDrainPageSize)
		return err
	})
	if !ran && err == nil {
		return core.ErrPassSkipped
	}
	return err
}
