// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ApplyDeviceDeleted removes only the deleted incarnation's live projection.
func (api *Api) ApplyDeviceDeleted(ctx context.Context, deviceToken string, incarnation uint64, deletedTime time.Time) (bool, error) {
	if deviceToken == "" || deletedTime.IsZero() {
		return false, nil
	}
	var deleted bool
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var row DeviceState
		err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_token = ?", deviceToken).First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		parent := err == nil
		if parent {
			if row.Incarnation.Valid {
				if incarnation == 0 || row.Incarnation.Int64 < 0 || uint64(row.Incarnation.Int64) > incarnation {
					return nil
				}
			} else if row.CreatedAt.After(deletedTime) {
				return nil
			}
		}
		deleted, err = deleteLiveProjection(tx, deviceToken, parent, deletedTime)
		return err
	})
	return deleted && err == nil, err
}

// deleteLiveProjection is called after locking the parent, in the same order as batch writers.
// Without a parent, only children last written before the fact may be removed.
func deleteLiveProjection(tx *gorm.DB, token string, parent bool, cutoff time.Time) (bool, error) {
	var n int64
	if parent {
		res := tx.Unscoped().Where("device_token = ?", token).Delete(&DeviceState{})
		if res.Error != nil {
			return false, res.Error
		}
		n += res.RowsAffected
	}
	for _, child := range []any{&LatestMeasurement{}, &LatestLocation{}, &DroppedMeasurement{}} {
		q := tx.Unscoped().Where("device_token = ?", token)
		if !parent {
			q = q.Where("created_at <= ? AND updated_at <= ?", cutoff, cutoff)
		}
		res := q.Delete(child)
		if res.Error != nil {
			return false, res.Error
		}
		n += res.RowsAffected
	}
	return n > 0, nil
}

// OrphanPage walks all projections, including soft-deleted rows, within the context's tenant.
func (api *Api) OrphanPage(ctx context.Context, after uint, limit int) ([]DeviceState, error) {
	var rows []DeviceState
	err := api.RDB.DB(ctx).Unscoped().Where("id > ?", after).Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ReconcileDeviceState rechecks the scanned row under lock before deleting or stamping it.
// The current row's stamp and timestamps decide the action, never stale page contents.
func (api *Api) ReconcileDeviceState(ctx context.Context, scanned DeviceState, liveID int64, createdAt time.Time) (deleted, stamped bool, err error) {
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var row DeviceState
		e := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_token = ?", scanned.DeviceToken).First(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if row.ID != scanned.ID {
			return nil
		}
		if liveID == 0 || (row.Incarnation.Valid && row.Incarnation.Int64 != liveID) || (!row.Incarnation.Valid && row.CreatedAt.Before(createdAt)) {
			deleted, e = deleteLiveProjection(tx, row.DeviceToken, true, time.Time{})
			return e
		}
		if !row.Incarnation.Valid {
			res := tx.Unscoped().Model(&DeviceState{}).Where("id = ? AND incarnation IS NULL", row.ID).UpdateColumn("incarnation", liveID)
			stamped = res.RowsAffected > 0
			return res.Error
		}
		return nil
	})
	if err != nil {
		deleted, stamped = false, false
	}
	return
}
