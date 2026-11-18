// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentActuationFence struct {
	TenantId           string    `gorm:"primaryKey;not null;size:128"`
	Enabled            bool      `gorm:"not null"`
	KilledAtGeneration int       `gorm:"not null"`
	Epoch              int64     `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

// Kill fences are safety bookkeeping; the policy change is already audited by its author.
func (AgentActuationFence) AuditExempt() bool { return true }

// ApplyKillEvent only stops execution. Replays bump the epoch and never lower the kill generation.
func (api *Api) ApplyKillEvent(ctx context.Context, ev actiongate.KillEvent) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant != ev.Tenant {
		return fmt.Errorf("kill event tenant does not match context")
	}
	return applyKillEventDB(api.RDB.DB(ctx), tenant, ev)
}

// applyKillEventDB is ApplyKillEvent on a handle already scoped to the tenant: the consumer's
// own connection, or (in tests) the execution transaction itself.
func applyKillEventDB(db *gorm.DB, tenant string, ev actiongate.KillEvent) error {
	maxExpr := "GREATEST(?, excluded.killed_at_generation)"
	if db.Dialector.Name() == "sqlite" {
		maxExpr = "MAX(?, excluded.killed_at_generation)"
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"enabled": false, "epoch": gorm.Expr("? + 1", clause.Column{Table: clause.CurrentTable, Name: "epoch"}),
			"killed_at_generation": gorm.Expr(maxExpr, clause.Column{Table: clause.CurrentTable, Name: "killed_at_generation"}), "updated_at": time.Now().UTC(),
		}),
	}).Create(&[]AgentActuationFence{{KilledAtGeneration: ev.ActuationGeneration, Epoch: 1, UpdatedAt: time.Now().UTC()}}).Error
}

var errFenceNeedsReenable = errors.New("agent fence needs reenable")

// fenceReadTx seeds and reads the tenant's fence row, taking FOR SHARE on Postgres.
//
// The row is SEEDED first (insert, do nothing on conflict; Epoch 0 means no kill has ever landed).
// Without a row to lock, a kill arriving between this read and the commit would insert a fresh
// row, conflict with nothing, and commit ahead of an execution that saw "no row": exactly the
// window the fence exists to close. With the row present, the kill's upsert waits on the share lock.
func fenceReadTx(tx *gorm.DB) (actiongate.FenceState, error) {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&[]AgentActuationFence{{Enabled: true, UpdatedAt: time.Now().UTC()}}).Error; err != nil {
		return actiongate.FenceState{}, err
	}
	var row AgentActuationFence
	q := tx
	if tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := q.First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return actiongate.FenceState{}, err
	}
	return actiongate.FenceState{Present: err == nil && row.Epoch > 0, Enabled: row.Enabled, KilledAtGeneration: row.KilledAtGeneration}, nil
}

// fenceVerdictErr judges a previously read fence without touching the transaction.
func fenceVerdictErr(state actiongate.FenceState, decisionGeneration int) error {
	switch actiongate.CheckFence(state, decisionGeneration) {
	case actiongate.FenceClosed:
		return actiongate.ErrFenceClosed
	case actiongate.FenceReenable:
		return errFenceNeedsReenable
	}
	return nil
}

func (api *Api) reenableFence(ctx context.Context, decisionGeneration int) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Model(&AgentActuationFence{}).Where("enabled = ? AND killed_at_generation < ?", false, decisionGeneration).
			Updates(map[string]any{"enabled": true, "epoch": gorm.Expr("epoch + 1"), "updated_at": time.Now().UTC()}).Error
	})
}

const DefaultKillFeedTTL = 30 * time.Second

// KillFeed is process-local evidence that the kill consumer can still reach the broker.
type KillFeed struct {
	mu   sync.RWMutex
	last time.Time
	ttl  time.Duration
	now  func() time.Time
}

func NewKillFeed(ttl time.Duration, now func() time.Time) *KillFeed {
	if ttl <= 0 {
		ttl = DefaultKillFeedTTL
	}
	if now == nil {
		now = time.Now
	}
	return &KillFeed{ttl: ttl, now: now}
}

func (f *KillFeed) Now() time.Time {
	if f.now == nil {
		return time.Now()
	}
	return f.now()
}
func (f *KillFeed) Beat(now time.Time) { f.mu.Lock(); defer f.mu.Unlock(); f.last = now }
func (f *KillFeed) Fresh(now time.Time) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	ttl := f.ttl
	if ttl <= 0 {
		ttl = DefaultKillFeedTTL
	}
	return !f.last.IsZero() && !now.Before(f.last) && now.Sub(f.last) < ttl
}
