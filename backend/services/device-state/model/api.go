// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Api struct {
	RDB *rdb.RdbManager
}

// Create a new API instance.
func NewApi(rdb *rdb.RdbManager) *Api {
	api := &Api{}
	api.RDB = rdb
	return api
}

// Interface for device state API (used for mocking)
type DeviceStateApi interface {
	MergeDeviceState(ctx context.Context, deviceId uint, occurredAt time.Time) (*DeviceState, error)
	DeviceStatesByDeviceId(ctx context.Context, deviceIds []uint) ([]*DeviceState, error)
	DeviceStates(ctx context.Context, criteria DeviceStateSearchCriteria) (*DeviceStateSearchResults, error)
	SweepInactive(ctx context.Context, now time.Time) (int64, error)
}

// MergeDeviceState updates (or creates) the live state projection for a device
// in response to a resolved event. It is the write path of the projection.
func (api *Api) MergeDeviceState(ctx context.Context, deviceId uint, occurredAt time.Time) (*DeviceState, error) {
	// The 5 decode workers can process two events for the same device
	// concurrently. Read-modify-write the row inside a transaction that takes a
	// row lock (SELECT … FOR UPDATE), so same-device merges serialize and a later
	// write can't regress LastActivityTime or clobber a reconnect.
	found := &DeviceState{}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_id = ?", deviceId).First(found)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// First event seen for this device: create a new active row. A
				// concurrent first event loses the device_id unique index race and
				// errors out (redelivered), rather than producing a duplicate.
				found = &DeviceState{
					DeviceId:          deviceId,
					Active:            true,
					LastActivityTime:  sql.NullTime{Time: occurredAt, Valid: true},
					LastConnectTime:   sql.NullTime{Time: occurredAt, Valid: true},
					InactivityTimeout: config.DefaultInactivityTimeout,
				}
				return tx.Create(found).Error
			}
			return result.Error
		}

		// Existing row: a previously-inactive device reconnecting.
		if !found.Active {
			found.Active = true
			found.LastConnectTime = sql.NullTime{Time: occurredAt, Valid: true}
			found.InactivityAlarmTime = sql.NullTime{}
		}

		// Advance last-activity time only when this event is newer than what we have.
		if !found.LastActivityTime.Valid || occurredAt.After(found.LastActivityTime.Time) {
			found.LastActivityTime = sql.NullTime{Time: occurredAt, Valid: true}
		}

		return tx.Save(found).Error
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Get device states by originating device id.
func (api *Api) DeviceStatesByDeviceId(ctx context.Context, deviceIds []uint) ([]*DeviceState, error) {
	found := make([]*DeviceState, 0)
	result := api.RDB.DB(ctx).Find(&found, "device_id in ?", deviceIds)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device states that meet criteria.
func (api *Api) DeviceStates(ctx context.Context, criteria DeviceStateSearchCriteria) (*DeviceStateSearchResults, error) {
	results := make([]DeviceState, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceState{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Active != nil {
			result = result.Where("active = ?", *criteria.Active)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceStateSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// SweepInactive is the core of the background inactivity monitor. The caller
// passes a system context so the scan spans all tenants; each row keeps its own
// tenant_id on Save. It marks every active device whose last activity is older
// than its inactivity timeout as inactive and returns how many were flipped.
func (api *Api) SweepInactive(ctx context.Context, now time.Time) (int64, error) {
	active := make([]DeviceState, 0)
	result := api.RDB.DB(ctx).Where("active = ?", true).Find(&active)
	if result.Error != nil {
		return 0, result.Error
	}

	var flipped int64
	for i := range active {
		row := active[i]
		if !isInactive(row.LastActivityTime, row.InactivityTimeout, now) {
			continue
		}
		// Flip with a conditional update keyed to the snapshot's activity time, not
		// a full-row Save of the stale snapshot: if a MergeDeviceState landed new
		// activity since the scan, last_activity_time no longer matches and the row
		// is left active (no flip), so the sweep can't clobber a just-active device.
		res := api.RDB.DB(ctx).Model(&DeviceState{}).
			Where("id = ? AND active = ? AND last_activity_time = ?", row.ID, true, row.LastActivityTime).
			Updates(map[string]any{
				"active":                false,
				"last_disconnect_time":  sql.NullTime{Time: now, Valid: true},
				"inactivity_alarm_time": sql.NullTime{Time: now, Valid: true},
			})
		if res.Error != nil {
			return flipped, res.Error
		}
		flipped += res.RowsAffected
	}
	return flipped, nil
}

// isInactive reports whether a device whose last activity was at last (with the
// given per-device timeout in seconds) should be considered inactive at now. A
// device with no recorded activity is never flipped. A non-positive timeout is
// treated as the configured default.
func isInactive(last sql.NullTime, timeoutSecs int, now time.Time) bool {
	if !last.Valid {
		return false
	}
	if timeoutSecs <= 0 {
		timeoutSecs = config.DefaultInactivityTimeout
	}
	return now.Sub(last.Time) > time.Duration(timeoutSecs)*time.Second
}
