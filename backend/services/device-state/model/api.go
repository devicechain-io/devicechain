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
	MergeDeviceState(ctx context.Context, deviceToken string, occurredAt time.Time, pt *PresenceTransition) (*DeviceState, error)
	DeviceStatesByDeviceToken(ctx context.Context, deviceTokens []string) ([]*DeviceState, error)
	DeviceStates(ctx context.Context, criteria DeviceStateSearchCriteria) (*DeviceStateSearchResults, error)
	SweepInactive(ctx context.Context, now time.Time) (int64, error)
	MergeLatestMeasurements(ctx context.Context, deviceToken string, inputs []LatestMeasurementInput) error
	LatestMeasurementsByDeviceToken(ctx context.Context, deviceToken string) ([]*LatestMeasurement, error)
}

// PresenceTransition is an authoritative connectivity edge (ADR-067) carried by a
// StateChange event from a presence-asserting transport. It is nil for a plain data
// event. Connected true = CONNECTED, false = DISCONNECTED. SessionId is the
// producer's monotone per-session id (a host-observed connect epoch, not e.g. a raw
// Sparkplug bdSeq). OccurredAt is the transition's event time.
type PresenceTransition struct {
	Connected  bool
	SessionId  uint64
	OccurredAt time.Time
}

// presenceApplies reports whether an incoming transition is not older than the
// last-applied one, mirroring the alarm integrator's monotonic guard
// (device-management alarm_contributor.apply): a newer session always supersedes;
// within the same session the newer OccurredAt wins; at an equal (session, time)
// DISCONNECTED wins over CONNECTED (a session that births and dies at one instant
// is net-dead) and a same-state re-apply is a no-op. curConnected is the device's
// current Active state (for an ASSERTED device this is exactly its last presence).
// It is a pure function so it is order-independent and unit-testable off the DB.
func presenceApplies(curSession uint64, curTime sql.NullTime, curConnected bool, pt PresenceTransition) bool {
	if pt.SessionId != curSession {
		return pt.SessionId > curSession
	}
	if !curTime.Valid {
		return true // first transition applied in this session
	}
	if pt.OccurredAt.After(curTime.Time) {
		return true
	}
	if pt.OccurredAt.Equal(curTime.Time) {
		// Equal stamp: only a DISCONNECTED over a currently-CONNECTED device applies;
		// CONNECTED-over-DISCONNECTED and any same-state re-apply are no-ops.
		return !pt.Connected && curConnected
	}
	return false // strictly older within the session
}

// newDeviceState builds the row for the first event ever seen for a device. A plain
// data event creates it active (today's behavior); an authoritative presence
// transition creates it ASSERTED with Active set to exactly the transition — so a
// first-ever DISCONNECT records a dead device, never a connected one (ADR-067).
func newDeviceState(deviceToken string, occurredAt time.Time, pt *PresenceTransition) *DeviceState {
	ds := &DeviceState{
		DeviceToken:       deviceToken,
		PresenceSource:    PresenceSourceInferred,
		InactivityTimeout: config.DefaultInactivityTimeout,
	}
	if pt != nil {
		ds.PresenceSource = PresenceSourceAsserted
		ds.SessionId = pt.SessionId
		ds.PresenceTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
		ds.Active = pt.Connected
		if pt.Connected {
			ds.LastConnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
			ds.LastActivityTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
		} else {
			ds.LastDisconnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
		}
		return ds
	}
	ds.Active = true
	ds.LastActivityTime = sql.NullTime{Time: occurredAt, Valid: true}
	ds.LastConnectTime = sql.NullTime{Time: occurredAt, Valid: true}
	return ds
}

// MergeDeviceState updates (or creates) the live state projection for a device in
// response to a resolved event. It is the write path of the projection. A non-nil
// pt is an authoritative presence transition (ADR-067): it promotes the device to
// ASSERTED and drives Active under the monotonic (SessionId, OccurredTime) guard. A
// nil pt is a plain data event: for an INFERRED device it is an implicit heartbeat
// (unchanged); for an ASSERTED device it advances activity but NEVER flips Active —
// a stray data event can't resurrect a device the platform knows is dead.
func (api *Api) MergeDeviceState(ctx context.Context, deviceToken string, occurredAt time.Time, pt *PresenceTransition) (*DeviceState, error) {
	// The 5 decode workers can process two events for the same device
	// concurrently. Read-modify-write the row inside a transaction that takes a
	// row lock (SELECT … FOR UPDATE), so same-device merges serialize and a later
	// write can't regress LastActivityTime or clobber a reconnect.
	found := &DeviceState{}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_token = ?", deviceToken).First(found)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// First event seen for this device: create a new row. A concurrent
				// first event loses the (tenant_id, device_token) unique index race
				// and errors out (redelivered), rather than producing a duplicate.
				found = newDeviceState(deviceToken, occurredAt, pt)
				return tx.Create(found).Error
			}
			return result.Error
		}

		if pt != nil {
			// Authoritative presence transition: promote to ASSERTED (first-sight) and
			// apply the connectivity edge under the monotonic guard.
			found.PresenceSource = PresenceSourceAsserted
			if presenceApplies(found.SessionId, found.PresenceTime, found.Active, *pt) {
				found.SessionId = pt.SessionId
				found.PresenceTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
				if pt.Connected {
					found.Active = true
					found.LastConnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
					found.InactivityAlarmTime = sql.NullTime{}
				} else {
					found.Active = false
					found.LastDisconnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
				}
			}
			// A CONNECTED is also activity; a DISCONNECTED is the opposite of activity,
			// so it must not advance LastActivityTime.
			if pt.Connected && (!found.LastActivityTime.Valid || pt.OccurredAt.After(found.LastActivityTime.Time)) {
				found.LastActivityTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
			}
			return tx.Save(found).Error
		}

		// Plain data event. An ASSERTED device takes Active ONLY from a StateChange, so
		// a data event must not flip it (it still advances activity below); an INFERRED
		// device treats every event as an implicit heartbeat (unchanged behavior).
		if found.PresenceSource != PresenceSourceAsserted && !found.Active {
			found.Active = true
			found.LastConnectTime = sql.NullTime{Time: occurredAt, Valid: true}
			found.InactivityAlarmTime = sql.NullTime{}
		}
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

// MergeLatestMeasurements upserts the current value of each named measurement for
// a device from a resolved measurement event. Like MergeDeviceState it is a
// read-modify-write under a row lock so the concurrent decode workers serialize
// per key, and it only advances a key when the incoming reading is newer
// (out-of-order safe): a delayed old value never clobbers a newer stored one.
// All entries in the event commit together in one transaction.
func (api *Api) MergeLatestMeasurements(ctx context.Context, deviceToken string, inputs []LatestMeasurementInput) error {
	if len(inputs) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, in := range inputs {
			found := &LatestMeasurement{}
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("device_token = ? AND name = ?", deviceToken, in.Name).First(found)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					// First value for this (device, name): create it. A concurrent
					// first create loses the unique-index race and errors out
					// (redelivered), rather than producing a duplicate row.
					created := &LatestMeasurement{
						DeviceToken:  deviceToken,
						Name:         in.Name,
						Value:        in.Value,
						Classifier:   in.Classifier,
						Unit:         in.Unit,
						DataType:     in.DataType,
						OccurredTime: in.OccurredTime,
					}
					if err := tx.Create(created).Error; err != nil {
						return err
					}
					continue
				}
				return result.Error
			}
			// Existing row: overwrite only when this reading is strictly newer, so a
			// late-arriving old value (or a redelivered duplicate) is ignored.
			if in.OccurredTime.After(found.OccurredTime) {
				found.Value = in.Value
				found.Classifier = in.Classifier
				found.Unit = in.Unit
				found.DataType = in.DataType
				found.OccurredTime = in.OccurredTime
				if err := tx.Save(found).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// LatestMeasurementsByDeviceToken returns the current value of every measurement
// name for a device (name-ordered) — the live "current readings" surface.
func (api *Api) LatestMeasurementsByDeviceToken(ctx context.Context, deviceToken string) ([]*LatestMeasurement, error) {
	found := make([]*LatestMeasurement, 0)
	result := api.RDB.DB(ctx).Where("device_token = ?", deviceToken).Order("name asc").Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device states by originating device token.
func (api *Api) DeviceStatesByDeviceToken(ctx context.Context, deviceTokens []string) ([]*DeviceState, error) {
	found := make([]*DeviceState, 0)
	result := api.RDB.DB(ctx).Find(&found, "device_token in ?", deviceTokens)
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
	// Only INFERRED devices are swept: an ASSERTED device's offline is an
	// authoritative DEATH/LWT, never a data-silence timeout (ADR-067 decision 6), so
	// the sweep must not flip it. The predicate is applied at the scan so an asserted
	// device is never even considered.
	active := make([]DeviceState, 0)
	result := api.RDB.DB(ctx).Where("active = ? AND presence_source <> ?", true, PresenceSourceAsserted).Find(&active)
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
