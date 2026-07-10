// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm/clause"
)

// rosterMonotonicGuard gates every DeviceRoster mutation on the lifecycle clock: apply the DO
// UPDATE only when the incoming fact is STRICTLY newer than the row's recorded LastEventAt. This
// is what makes the projection converge to the latest lifecycle event under any delivery order —
// a redelivered or lagging older upsert/delete for the same device is a no-op. "excluded" is the
// standard upsert alias for the proposed row on both postgres and sqlite. Strict "<" (not "<=")
// means a stale fact that ties the recorded time does not apply, so a redelivered create can
// never resurrect a same-instant tombstone; real create/re-type/delete times never tie.
var rosterMonotonicGuard = clause.Where{Exprs: []clause.Expression{
	clause.Expr{SQL: "device_rosters.last_event_at < excluded.last_event_at"},
}}

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

// Upsert applies one device-roster (create/re-type) fact: it re-homes the device onto its profile
// and (re)marks it live, with LastEventAt set to the fact's ExpectedSince (the lifecycle time of
// this create/re-type). It inserts when absent and, on conflict, overwrites profile/expected-since
// and clears any tombstone — but ONLY when strictly newer than the recorded LastEventAt
// (rosterMonotonicGuard), so a stale redelivered/lagging fact is a no-op and a fact older than a
// tombstone cannot resurrect the device. Idempotent for an exact redelivery (equal LastEventAt is
// not "strictly newer", so the row is left as-is). ExpectedSince MUST be non-zero (the caller
// drops a zero-timed fact) or the guard could never advance.
func (s *DeviceRosterStore) Upsert(ctx context.Context, roster *DeviceRoster) error {
	roster.Deleted = false
	roster.LastEventAt = roster.ExpectedSince
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"profile_token", "expected_since", "deleted", "last_event_at", "updated_at"}),
		Where:     rosterMonotonicGuard,
	}).Create(roster).Error
}

// Delete tombstones a device's roster entry (its device was deleted, ADR-044 entity-deleted),
// carrying the entity-deleted fact's DeletedTime as the lifecycle clock. It does NOT hard-delete:
// the row is retained as a tombstone (Deleted=true, LastEventAt=deletedTime) so a stale
// redelivered create/re-type older than the deletion cannot resurrect it. It upserts (a delete
// that races ahead of the device's own create fact — cross-stream reordering — lands a fresh
// tombstone), and applies only when strictly newer than the recorded LastEventAt: a stale
// redelivered delete therefore cannot erase a legitimately re-created (token-reused) device whose
// newer create already advanced the clock. Both key columns are matched so a repeated token across
// tenants is not over-deleted.
func (s *DeviceRosterStore) Delete(ctx context.Context, tenant, deviceToken string, deletedTime time.Time) error {
	tombstone := &DeviceRoster{
		Tenant:      tenant,
		DeviceToken: deviceToken,
		// ProfileToken/ExpectedSince are placeholders for the insert-a-fresh-tombstone case (a
		// delete applied before the device's create fact); they are never read on a tombstone
		// (LoadAll excludes it) and are not overwritten on the update branch.
		ExpectedSince: deletedTime,
		Deleted:       true,
		LastEventAt:   deletedTime,
	}
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"deleted", "last_event_at", "updated_at"}),
		Where:     rosterMonotonicGuard,
	}).Create(tombstone).Error
}

// LoadAll returns every LIVE rostered device (tombstones excluded) — the full set the engine's
// dead-man arming is rebuilt from at startup. Like the rule projection it is not tenant-scoped
// (the projection spans every tenant, tenant on the row), so it reads the whole table.
func (s *DeviceRosterStore) LoadAll(ctx context.Context) ([]DeviceRoster, error) {
	var rosters []DeviceRoster
	if err := s.rdb.DB(ctx).Where("deleted = ?", false).Find(&rosters).Error; err != nil {
		return nil, err
	}
	return rosters, nil
}
