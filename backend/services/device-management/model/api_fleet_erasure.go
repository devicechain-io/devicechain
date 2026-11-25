// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const fleetMaintenanceChunk = 10000

// redactFleetDeviceOnTx runs after K_dev, never K_op: taking the fence here
// would invert the posting lock order. The row UPDATE is the last line lock.
// Erasure deliberately also covers records from newer driver protocols.
func redactFleetDeviceOnTx(tx *gorm.DB, deviceID uint) error {
	return redactFleetDeviceLinesOnTx(tx, deviceID, false)
}

func redactFleetDeviceLinesOnTx(tx *gorm.DB, deviceID uint, repair bool) error {
	for {
		var lines []FleetOperationLine
		query := tx.Select("id").Where("target_id = ? AND redacted_at IS NULL", deviceID)
		if repair {
			query = query.Where("operation_id IN (?)", fleetMaintenanceProtocol(tx.Model(&FleetOperation{}).Select("id")))
		}
		if err := query.Order("id").Limit(fleetMaintenanceChunk).Find(&lines).Error; err != nil {
			return err
		}
		if len(lines) == 0 {
			return nil
		}
		ids := make([]uint, len(lines))
		for i := range lines {
			ids[i] = lines[i].ID
		}
		now, err := fleetMaintenanceNow(tx)
		if err != nil {
			return err
		}
		// Every expression uses the locked row's current state. A resolution or
		// settlement which committed after the hint read must not be overwritten.
		changes := map[string]any{
			"target_key": "", "prior": nil, "posted_value": nil, "detail": "", "redacted_at": now,
			"posting":    gorm.Expr("CASE WHEN posting = 'PLANNED' THEN 'SKIPPED' ELSE posting END"),
			"code":       gorm.Expr("CASE WHEN posting = 'PLANNED' OR (posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS')) THEN 'TARGET_ERASED' ELSE code END"),
			"settlement": gorm.Expr("CASE WHEN posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS') THEN 'AMBIGUOUS' ELSE settlement END"),
			"settled_at": gorm.Expr("CASE WHEN posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS') THEN ? ELSE settled_at END", now),
		}
		var updated []FleetOperationLine
		update := tx.Model(&updated).Clauses(clause.Returning{Columns: []clause.Column{
			{Name: "id"}, {Name: "tenant_id"}, {Name: "operation_id"}, {Name: "resolution_id"},
		}}).Where("id IN ? AND redacted_at IS NULL", ids)
		if repair {
			update = update.Where("operation_id IN (?)", fleetMaintenanceProtocol(tx.Model(&FleetOperation{}).Select("id")))
		}
		if err := update.Updates(changes).Error; err != nil {
			return err
		}
		// RETURNING captures the resolution reference after any line-lock wait.
		// Clearing one shared note also removes it from surviving targets' views.
		seen := map[int64]bool{}
		var notes []int64
		events := make([]FleetOperationEvent, len(updated))
		for i, current := range updated {
			if current.ResolutionId.Valid && !seen[current.ResolutionId.Int64] {
				notes = append(notes, current.ResolutionId.Int64)
				seen[current.ResolutionId.Int64] = true
			}
			events[i] = FleetOperationEvent{TenantId: current.TenantId, OperationId: current.OperationId,
				LineId: sql.NullInt64{Int64: int64(current.ID), Valid: true}, At: now, Kind: "REDACTED"}
		}
		if len(notes) != 0 {
			slices.Sort(notes)
			if err := tx.Model(&FleetOperationEvent{}).Where("id IN ? AND kind = 'RESOLUTION'", notes).
				Updates(map[string]any{"note": "", "note_redacted_at": now}).Error; err != nil {
				return err
			}
		}
		if len(events) != 0 {
			if err := tx.CreateInBatches(&events, 500).Error; err != nil {
				return err
			}
		}
		// A delete must erase every line atomically. Repair commits one bounded
		// chunk; remaining lines are eligible again on the next maintenance pass.
		if repair {
			return nil
		}
	}
}

func fleetMaintenanceNow(tx *gorm.DB) (time.Time, error) {
	if tx.Dialector.Name() != "postgres" {
		return time.Now().UTC(), nil
	}
	var now time.Time
	err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error
	return now, err
}

func fleetMaintenanceProtocol(db *gorm.DB) *gorm.DB {
	return db.Where("driver_protocol >= ? AND driver_protocol <= ?", 1, ledger.SupportedDriverProtocol)
}

// RepairFleetErasure covers deletes by previous-minor pods. The correlated
// NOT EXISTS is an anti-join, including the tenant and soft-deletion predicate;
// a nullable target_id is an unresolved input, not a deleted device.
func (api *Api) RepairFleetErasure(ctx context.Context) error {
	if _, ok := core.TenantFromContext(ctx); !ok || core.IsSystemContext(ctx) {
		return errors.New("bulk-change erasure repair requires a tenant context")
	}
	db := api.RDB.DB(ctx)
	q := func(model string) string { return db.Statement.Quote(db.NamingStrategy.TableName(model)) }
	lines := q("FleetOperationLine")
	devices := db.Model(&Device{}).Select("1").Where(q("Device") + ".id = " + lines + ".target_id").
		Where(q("Device") + ".tenant_id = " + lines + ".tenant_id")
	ops := fleetMaintenanceProtocol(db.Model(&FleetOperation{}).Select("1")).
		Where(q("FleetOperation") + ".id = " + lines + ".operation_id").Where(q("FleetOperation") + ".tenant_id = " + lines + ".tenant_id")
	var candidates []FleetOperationLine
	if err := db.Select("id", "target_id").Where("redacted_at IS NULL AND target_id IS NOT NULL").
		Where("NOT EXISTS (?)", devices).Where("EXISTS (?)", ops).Order("id").Limit(fleetMaintenanceChunk).Find(&candidates).Error; err != nil {
		return err
	}
	var failures []error
	seen := map[int64]bool{}
	for _, line := range candidates {
		if seen[line.TargetId.Int64] {
			continue
		}
		seen[line.TargetId.Int64] = true
		err := api.runDeviceAttributeTx(ctx, "bulk-change-erasure-repair", func(tx *gorm.DB) error {
			id := uint(line.TargetId.Int64)
			if err := lockDeviceAttributes(ctx, tx, id); err != nil {
				return err
			}
			var n int64
			if err := tx.Model(&Device{}).Where("id = ?", id).Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return nil
			}
			return redactFleetDeviceLinesOnTx(tx, id, true)
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	// Report rows a previous-minor delete leaves behind are not this pass's:
	// MaintainDeviceConfigurationReports already removes orphans on every replica.
	return errors.Join(failures...)
}
