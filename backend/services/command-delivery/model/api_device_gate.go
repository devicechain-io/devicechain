// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// gateChunk bounds how many device tokens one statement names, well inside every driver's
// bind-parameter limit.
const gateChunk = 1000

// DeviceGateReader is the read the dispatch path makes of command-delivery's record of disabled
// devices, plus the two reads a resume makes. It is a separate interface rather than part of
// CommandDeliveryApi so the processor can be assembled without it, and *Api is held to it below.
type DeviceGateReader interface {
	// DisabledAmong reports which of the named devices of the context's tenant are disabled.
	DisabledAmong(ctx context.Context, deviceTokens []string) (map[string]bool, error)
	// EnabledAmong reports which of the named devices the record holds as ENABLED: a row exists
	// and says not disabled. A device the record never heard of is in neither answer.
	EnabledAmong(ctx context.Context, deviceTokens []string) (map[string]bool, error)
	// ReleaseHeldForDevice returns the device's withheld commands to QUEUED.
	ReleaseHeldForDevice(ctx context.Context, deviceToken string) (int64, error)
	// QueuedCommandsForDevice reads the front of the device's QUEUED backlog, oldest first.
	QueuedCommandsForDevice(ctx context.Context, deviceToken string, limit int) ([]*Command, error)
}

var _ DeviceGateReader = (*Api)(nil)

// disabledDevices is the sub-select naming every (tenant, device) pair recorded as disabled. It
// is the ONE spelling of "this device is disabled" on the write paths: both claims fold it into
// their predicate, so a dispatch the pre-filter missed (a fact that landed between its read and
// the claim) still loses at the claim. The wake drain's read (DrainableCommands) carries it too,
// so it never offers a page its own claim must refuse.
//
// Written `disabled = true`, not a bound parameter: the partial index behind it is matched on
// the literal predicate, and a bound value would hide it from the planner.
func (api *Api) disabledDevices(ctx context.Context) *gorm.DB {
	return api.RDB.DB(ctx).Model(&DeviceGate{}).
		Select("tenant_id, device_token").Where("disabled = true")
}

// DatabaseNow is the database's clock, falling back to this process's where the database has no
// now() (the in-memory test database).
//
// 🔴 EVERY TIMESTAMP THE RECORD COMPARES COMES FROM HERE. The corrective walk changes a row only
// if it is older than the instant the walk began reading, and the rows are written by whichever
// pod heard the fact. Stamped from each pod's own clock, two pods a few seconds apart would let a
// fact written by the faster one look newer than a walk begun by the slower one, or the reverse.
// One clock, the database's, removes the question.
func (api *Api) DatabaseNow(ctx context.Context) (time.Time, error) {
	db := api.RDB.DB(ctx)
	if db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return time.Now().UTC(), nil
	}
	var now time.Time
	if err := db.Raw("SELECT now()").Scan(&now).Error; err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

// TenantGateSynced reports whether the context's tenant has ever had its record corrected.
func (api *Api) TenantGateSynced(ctx context.Context) (bool, error) {
	var count int64
	if err := api.RDB.DB(ctx).Model(&DeviceGateSync{}).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// MarkTenantGateSynced records that the context's tenant has had its record corrected.
func (api *Api) MarkTenantGateSynced(ctx context.Context) error {
	now, err := api.DatabaseNow(ctx)
	if err != nil {
		return err
	}
	return api.RDB.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"synced_at"}),
	}).Create(&DeviceGateSync{SyncedAt: now}).Error
}

// SetDeviceDisabled records, for the context's tenant, whether a device is disabled, and reports
// whether it was disabled until now. The write is an upsert on (tenant, device): the device-roster
// fact is the only caller that knows a state, and it names the whole state on every fact.
func (api *Api) SetDeviceDisabled(ctx context.Context, deviceToken string, disabled bool) (bool, error) {
	var prior []DeviceGate
	if err := api.RDB.DB(ctx).Where("device_token = ?", deviceToken).Limit(1).Find(&prior).Error; err != nil {
		return false, err
	}
	wasDisabled := len(prior) == 1 && prior[0].Disabled
	now, err := api.DatabaseNow(ctx)
	if err != nil {
		return false, err
	}
	row := &DeviceGate{DeviceToken: deviceToken, Disabled: disabled, UpdatedAt: now}
	err = api.RDB.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"disabled", "updated_at"}),
	}).Create(row).Error
	if err != nil {
		return false, err
	}
	return wasDisabled, nil
}

// DisabledAmong reports which of the named devices of the context's tenant are disabled. Devices
// the record has never heard of are absent from the answer: not disabled.
func (api *Api) DisabledAmong(ctx context.Context, deviceTokens []string) (map[string]bool, error) {
	out := make(map[string]bool)
	for start := 0; start < len(deviceTokens); start += gateChunk {
		end := min(start+gateChunk, len(deviceTokens))
		var found []string
		if err := api.RDB.DB(ctx).Model(&DeviceGate{}).
			Where("device_token IN ? AND disabled = true", deviceTokens[start:end]).
			Pluck("device_token", &found).Error; err != nil {
			return nil, err
		}
		for _, tok := range found {
			out[tok] = true
		}
	}
	return out, nil
}

// EnabledAmong reports which of the named devices of the context's tenant the record holds as
// enabled (a row that says not disabled), as opposed to never recorded.
func (api *Api) EnabledAmong(ctx context.Context, deviceTokens []string) (map[string]bool, error) {
	out := make(map[string]bool)
	for start := 0; start < len(deviceTokens); start += gateChunk {
		end := min(start+gateChunk, len(deviceTokens))
		var found []string
		if err := api.RDB.DB(ctx).Model(&DeviceGate{}).
			Where("device_token IN ? AND disabled = false", deviceTokens[start:end]).
			Pluck("device_token", &found).Error; err != nil {
			return nil, err
		}
		for _, tok := range found {
			out[tok] = true
		}
	}
	return out, nil
}

// DisabledDeviceTokens lists every device recorded as disabled for the context's tenant.
func (api *Api) DisabledDeviceTokens(ctx context.Context) ([]string, error) {
	var found []string
	if err := api.RDB.DB(ctx).Model(&DeviceGate{}).Where("disabled = true").
		Order("id").Pluck("device_token", &found).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// ReconcileDisabled corrects the context's tenant to device-management's list of disabled
// devices, which the caller began reading at readStarted. It returns the devices it found
// disabled in the record and enabled in the list, which is the enable the walk caught up with.
//
// 🔴 A ROW IS ONLY CHANGED IF IT IS OLDER THAN readStarted. A device-roster fact that landed
// while the walk was reading carries a newer truth than the list the walk is holding, and the
// walk must not overwrite it with the older one. That is also why a row is never deleted: the
// enabled row is the evidence that a fact already spoke.
func (api *Api) ReconcileDisabled(ctx context.Context, disabled []string, readStarted time.Time) ([]string, error) {
	now, err := api.DatabaseNow(ctx)
	if err != nil {
		return nil, err
	}
	readStarted = readStarted.UTC()
	listed := make(map[string]bool, len(disabled))
	for _, tok := range disabled {
		listed[tok] = true
	}
	for start := 0; start < len(disabled); start += gateChunk {
		chunk := disabled[start:min(start+gateChunk, len(disabled))]
		rows := make([]DeviceGate, 0, len(chunk))
		for _, tok := range chunk {
			rows = append(rows, DeviceGate{DeviceToken: tok, Disabled: true, UpdatedAt: now})
		}
		if err := api.RDB.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return nil, err
		}
		if err := api.RDB.DB(ctx).Model(&DeviceGate{}).
			Where("device_token IN ? AND disabled = false AND updated_at < ?", chunk, readStarted).
			Updates(map[string]any{"disabled": true, "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	recorded, err := api.DisabledDeviceTokens(ctx)
	if err != nil {
		return nil, err
	}
	var unlisted []string
	for _, tok := range recorded {
		if !listed[tok] {
			unlisted = append(unlisted, tok)
		}
	}
	// One statement per chunk, not one per device: the walk also runs on the roster consumer's
	// goroutine (a tenant's first fact), under that stream's ack deadline. RETURNING names the
	// rows THIS write moved, which is the answer: a device a newer fact already changed is left
	// alone by the predicate and so is not reported.
	var enabled []string
	for start := 0; start < len(unlisted); start += gateChunk {
		chunk := unlisted[start:min(start+gateChunk, len(unlisted))]
		var moved []DeviceGate
		if err := api.RDB.DB(ctx).Model(&moved).
			Clauses(clause.Returning{Columns: []clause.Column{{Name: "device_token"}}}).
			Where("device_token IN ? AND disabled = true AND updated_at < ?", chunk, readStarted).
			Updates(map[string]any{"disabled": false, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		for _, row := range moved {
			enabled = append(enabled, row.DeviceToken)
		}
	}
	return enabled, nil
}

// DeviceCommandHold reports whether a device is recorded as disabled and, if it is, how many
// commands the platform is holding for it: every command it still has that has not been sent
// (QUEUED, HELD or PARKED), which is exactly the set CancelDeviceCommands would cancel. An
// enabled device reports zero, because nothing is being held on its account.
//
// A member limited to part of the tenant (ADR-081) is answered only about a device it owns
// now; any other device — another customer's, or none at all — is NOT_FOUND. Once that is
// decided the hold is read for the whole device, not filtered to the member's own commands:
// it describes the device, which the member owns, and a count of only their own commands
// would under-report what is waiting on it.
func (api *Api) DeviceCommandHold(ctx context.Context, deviceToken string) (bool, int64, error) {
	if err := api.requireOwnedDevice(ctx, deviceToken); err != nil {
		return false, 0, err
	}
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		ctx = tenantWideReadContext(ctx)
	}
	disabled, err := api.DisabledAmong(ctx, []string{deviceToken})
	if err != nil {
		return false, 0, err
	}
	if !disabled[deviceToken] {
		return false, 0, nil
	}
	var held int64
	if err := api.RDB.DB(ctx).Model(&Command{}).
		Where("device_token = ? AND status IN ?", deviceToken, cancellableStatusStrings()).
		Count(&held).Error; err != nil {
		return false, 0, err
	}
	return true, held, nil
}

// CancelDeviceCommands cancels every command the platform still holds for one device (QUEUED,
// HELD or PARKED -> CANCELLED) and reports how many it cancelled. It is CancelCommand over a
// device: the same from-state-predicated UPDATE, so a command claimed a moment earlier is left
// alone, and a SENT command (at the device already) is never touched.
func (api *Api) CancelDeviceCommands(ctx context.Context, deviceToken string) (int64, error) {
	res := api.RDB.DB(ctx).Model(&Command{}).
		Where("device_token = ? AND status IN ?", deviceToken, cancellableStatusStrings()).
		Updates(terminalUpdate(CommandCancelled, OutcomeNotDelivered, nil))
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
