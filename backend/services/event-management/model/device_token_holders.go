// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceTokenHolder records that a LIVE device held a token at HeldSince or later. It is what
// tells a token that was deleted and created again apart from one that was only deleted: a read
// by a deleted token is bounded to its current device only once HeldSince reaches the token's
// latest deletion (deviceIncarnationBound). A deleted token nobody holds again reads its history
// as it always did.
//
// HeldSince only moves forward (GREATEST), so the order its two sources arrive in does not
// matter, and neither does a duplicate:
//   - the device-roster fact device-management publishes when a device is created, re-typed,
//     disabled or enabled (its ExpectedSince is the creation or a later instant), and
//   - the anchor sweep, which asks device-management whether a deleted token resolves again and,
//     if it does, records the DELETION TIME it asked about (the roster fact is best-effort).
//
// A deletion of the holder moves the watermark past HeldSince, which turns the bound off again
// until a newer holder is recorded.
type DeviceTokenHolder struct {
	rdb.TenantScoped
	DeviceToken string    `gorm:"size:128;not null"`
	HeldSince   time.Time `gorm:"not null"`
}

// RecordDeviceTokenHeld moves the token's HeldSince forward to since, never back. Idempotent.
func (api *Api) RecordDeviceTokenHeld(ctx context.Context, token string, since time.Time) error {
	// SQLite compares timestamp text in MAX; offsets must not change its ordering.
	since = since.UTC()
	db := api.RDB.DB(ctx)
	maxTime := "GREATEST(device_token_holders.held_since, excluded.held_since)"
	if db.Dialector.Name() == "sqlite" {
		maxTime = "MAX(device_token_holders.held_since, excluded.held_since)"
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}},
		DoUpdates: clause.Assignments(map[string]any{"held_since": gorm.Expr(maxTime)}),
	}).Create(&DeviceTokenHolder{DeviceToken: token, HeldSince: since}).Error
}

// DeletedDeviceTokenTenants lists every tenant with a recorded entity deletion (cross-tenant,
// through the sanctioned RdbManager.TenantsWith): the tenants the sweep's holder repair visits.
func (api *Api) DeletedDeviceTokenTenants(ctx context.Context) ([]string, error) {
	return api.RDB.TenantsWith(ctx, &EntityDeletionWatermark{})
}

// DeletedDeviceToken is a deleted device token and the latest deletion recorded for it.
type DeletedDeviceToken struct {
	Token             string
	DeletedTime       time.Time
	CheckAfter        *time.Time
	CheckDelaySeconds int
}

// UnheldDeletedDeviceTokensAfter returns one page of the current tenant's deleted device tokens
// that no live device is yet recorded as holding since their latest deletion, each with that
// deletion, ordered by token and starting strictly after the cursor. Only checks due at now
// are selected, before the page limit; newer deletions bypass an older check's schedule.
// An empty page is the end.
func (api *Api) UnheldDeletedDeviceTokensAfter(ctx context.Context, after string, now time.Time) ([]DeletedDeviceToken, error) {
	db := api.RDB.DB(ctx)
	held := db.Session(&gorm.Session{NewDB: true}).Model(&DeviceTokenHolder{}).Select("1").
		Where("device_token_holders.tenant_id = entity_deletion_watermarks.tenant_id").
		Where("device_token_holders.device_token = entity_deletion_watermarks.entity_token").
		Where("device_token_holders.held_since >= entity_deletion_watermarks.deleted_time")
	var rows []EntityDeletionWatermark
	err := db.Model(&EntityDeletionWatermark{}).Select("entity_token, deleted_time, holder_checked_deletion, holder_check_after, holder_check_delay_seconds").
		Where("entity_type = ? AND entity_token > ?", string(entity.TypeDevice), after).
		Where("holder_checked_deletion IS NULL OR holder_checked_deletion < deleted_time OR holder_check_after IS NULL OR holder_check_after <= ?", now.UTC()).
		Where("NOT EXISTS (?)", held).
		Order("entity_token").Limit(anchorRefPageSize).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]DeletedDeviceToken, len(rows))
	for i, r := range rows {
		out[i] = DeletedDeviceToken{Token: r.EntityToken, DeletedTime: r.DeletedTime}
		out[i].CheckAfter = r.HolderCheckAfter
		if r.HolderCheckedDeletion != nil && r.HolderCheckedDeletion.Equal(r.DeletedTime) {
			out[i].CheckDelaySeconds = r.HolderCheckDelaySeconds
		}
	}
	return out, nil
}

// RecordDeviceTokenAbsent backs off a successful negative answer: 1h, 2h, 4h,
// ... 128h, then 168h forever. With hourly passes a missed roster fact is repaired
// within seven days plus one sweep interval once device-management is reachable.
// No deletion ages out permanently. CAS prevents overlapping replicas advancing
// the same observation twice or a stale answer postponing a newer deletion.
func (api *Api) RecordDeviceTokenAbsent(ctx context.Context, d DeletedDeviceToken, now time.Time) error {
	delay := time.Duration(d.CheckDelaySeconds) * time.Second * 2
	if delay < time.Hour {
		delay = time.Hour
	}
	if delay > 7*24*time.Hour {
		delay = 7 * 24 * time.Hour
	}
	q := api.RDB.DB(ctx).Model(&EntityDeletionWatermark{}).
		Where("entity_type = ? AND entity_token = ? AND deleted_time = ?", string(entity.TypeDevice), d.Token, d.DeletedTime.UTC())
	if d.CheckAfter == nil {
		q = q.Where("holder_check_after IS NULL")
	} else {
		q = q.Where("holder_check_after = ?", d.CheckAfter.UTC())
	}
	return q.Updates(map[string]any{
		"holder_checked_deletion":    d.DeletedTime.UTC(),
		"holder_check_after":         now.UTC().Add(delay),
		"holder_check_delay_seconds": int(delay / time.Second),
	}).Error
}
