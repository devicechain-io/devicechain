// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

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
