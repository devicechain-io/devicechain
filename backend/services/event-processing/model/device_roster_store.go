// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm/clause"
)

// DeviceRosterStore is the durable projection of the devices expected to report (ADR-051 slice
// 4c-2b). The roster consumer upserts each device-roster fact here BEFORE it acks the fact, and
// the engine's dead-man arming is rebuilt from LoadAll at startup — so a never-reported device's
// absence arming survives a restart independent of the finite-retention roster fact stream.
// Unlike the rule projection it is live-mutable: Delete removes a decommissioned device's entry
// (driven by the entity-deleted fact) so its arming is cancelled rather than firing forever.
type DeviceRosterStore struct {
	rdb *rdb.RdbManager
}

// NewDeviceRosterStore wraps the rdb manager.
func NewDeviceRosterStore(r *rdb.RdbManager) *DeviceRosterStore {
	return &DeviceRosterStore{rdb: r}
}

// Upsert persists one device-roster fact, overwriting any existing row for the same
// (tenant, deviceToken) — a re-type fact re-homes the device onto its new profile in place. It
// is idempotent, so a redelivered fact is a no-op replay. On conflict the profile token, the
// expected-since base, and updated_at are refreshed (a re-type legitimately moves the membership
// clock forward and can change the profile), while the primary key is fixed.
func (s *DeviceRosterStore) Upsert(ctx context.Context, roster *DeviceRoster) error {
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"profile_token", "expected_since", "updated_at"}),
	}).Create(roster).Error
}

// Delete removes a device's roster entry (its device was deleted, ADR-044 entity-deleted). It is
// idempotent — deleting an absent row is a no-op — so a redelivered entity-deleted fact is
// harmless. Both key columns are matched so a device token that repeats across tenants is not
// over-deleted.
func (s *DeviceRosterStore) Delete(ctx context.Context, tenant, deviceToken string) error {
	return s.rdb.DB(ctx).
		Where("tenant = ? AND device_token = ?", tenant, deviceToken).
		Delete(&DeviceRoster{}).Error
}

// LoadAll returns every rostered device — the full set the engine's dead-man arming is rebuilt
// from at startup. Like the rule projection it is not tenant-scoped (the projection spans every
// tenant, tenant on the row), so it reads the whole table.
func (s *DeviceRosterStore) LoadAll(ctx context.Context) ([]DeviceRoster, error) {
	var rosters []DeviceRoster
	if err := s.rdb.DB(ctx).Find(&rosters).Error; err != nil {
		return nil, err
	}
	return rosters, nil
}
