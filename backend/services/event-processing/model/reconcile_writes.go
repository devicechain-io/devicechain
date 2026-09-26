// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	dccore "github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This file holds the projection writes of the fact reconcile (processor/fact_reconcile.go): the
// repair of a rule, active-version, roster or attribute row whose fact never arrived, from what
// device-management says now.
//
// 🔴 EVERY ONE OF THEM IS CONDITIONAL ON THE EXACT ROW THE RECONCILE COMPARED, and that is what
// makes them safe to run beside the live fact consumers. They are the only projection writes that
// bypass the monotonic guards — they have to, because the reason a row needs repairing is that the
// fact that would have moved it forward was lost, and device-management's current answer can carry
// an instant the guard would refuse. So each one names the row it observed:
//
//   - an insert applies only if there is still NO row (ON CONFLICT DO NOTHING);
//   - a replace or a tombstone applies only if every column the reconcile read is unchanged.
//
// A live fact that lands between the reconcile's read and its write changes the row, the write
// affects nothing, and the fact wins. The next sweep compares again. Each returns whether it
// applied, so the reconcile counts only repairs that actually happened.

// LoadTenant returns every rule row of one tenant.
func (s *DetectRuleStore) LoadTenant(ctx context.Context, tenant string) ([]DetectRule, error) {
	var rows []DetectRule
	if err := s.rdb.DB(dccore.WithTenant(ctx, tenant)).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// InsertIfAbsent adds one rule row when no row with its id exists.
func (s *DetectRuleStore) InsertIfAbsent(ctx context.Context, want *DetectRule) (bool, error) {
	tx := s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).
		Clauses(clause.OnConflict{DoNothing: true}).Create(want)
	return tx.RowsAffected == 1, tx.Error
}

// ReplaceIf rewrites one rule row's definition and scope, only if they are still the observed ones.
func (s *DetectRuleStore) ReplaceIf(ctx context.Context, want *DetectRule, observed DetectRule) (bool, error) {
	tx := s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).Model(&DetectRule{}).
		Where("rule_id = ? AND definition = ? AND entity_group_token = ? AND entity_group_version = ?",
			observed.RuleId, observed.Definition, observed.EntityGroupToken, observed.EntityGroupVersion).
		Updates(map[string]any{
			"definition":           want.Definition,
			"entity_group_token":   want.EntityGroupToken,
			"entity_group_version": want.EntityGroupVersion,
			"updated_at":           time.Now().UTC(),
		})
	return tx.RowsAffected == 1, tx.Error
}

// DeleteIf removes one rule row, only if its definition and scope are still the observed ones.
func (s *DetectRuleStore) DeleteIf(ctx context.Context, observed DetectRule) (bool, error) {
	tx := s.rdb.DB(dccore.WithTenant(ctx, observed.Tenant)).
		Where("rule_id = ? AND definition = ? AND entity_group_token = ? AND entity_group_version = ?",
			observed.RuleId, observed.Definition, observed.EntityGroupToken, observed.EntityGroupVersion).
		Delete(&DetectRule{})
	return tx.RowsAffected == 1, tx.Error
}

// LoadTenant returns every active-version row of one tenant.
func (s *ProfileActiveStore) LoadTenant(ctx context.Context, tenant string) ([]ProfileActive, error) {
	var rows []ProfileActive
	if err := s.rdb.DB(dccore.WithTenant(ctx, tenant)).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// InsertIfAbsent records a profile's active version when no row exists for the profile.
func (s *ProfileActiveStore) InsertIfAbsent(ctx context.Context, want *ProfileActive) (bool, error) {
	tx := s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).
		Clauses(clause.OnConflict{DoNothing: true}).Create(want)
	return tx.RowsAffected == 1, tx.Error
}

// ReplaceIf moves a profile's active version to want, only if the row still names the observed
// version and publish time.
func (s *ProfileActiveStore) ReplaceIf(ctx context.Context, want *ProfileActive, observed ProfileActive) (bool, error) {
	tx := s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).Model(&ProfileActive{}).
		Where("profile_token = ? AND active_version_token = ? AND published_at = ?",
			observed.ProfileToken, observed.ActiveVersionToken, observed.PublishedAt).
		Updates(map[string]any{
			"active_version_token": want.ActiveVersionToken,
			"published_at":         want.PublishedAt,
			"updated_at":           time.Now().UTC(),
		})
	return tx.RowsAffected == 1, tx.Error
}

// LoadTenant returns every roster row of one tenant, tombstones included.
func (s *DeviceRosterStore) LoadTenant(ctx context.Context, tenant string) ([]DeviceRoster, error) {
	var rows []DeviceRoster
	if err := s.rdb.DB(dccore.WithTenant(ctx, tenant)).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// InsertIfAbsent rosters a device when no row — live or tombstone — exists for it. The row's
// lifecycle clock is its ExpectedSince, exactly as a delivered roster fact would set it.
func (s *DeviceRosterStore) InsertIfAbsent(ctx context.Context, want *DeviceRoster) (bool, error) {
	want.Deleted = false
	want.LastEventAt = want.ExpectedSince
	tx := s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).
		Clauses(clause.OnConflict{DoNothing: true}).Create(want)
	return tx.RowsAffected == 1, tx.Error
}

// rosterObserved is the predicate naming a roster row exactly as the reconcile read it.
func rosterObserved(db *gorm.DB, observed DeviceRoster) *gorm.DB {
	return db.Where("device_token = ? AND deleted = ? AND profile_token = ? AND expected_since = ? AND last_event_at = ?",
		observed.DeviceToken, observed.Deleted, observed.ProfileToken, observed.ExpectedSince, observed.LastEventAt)
}

// ReplaceIf re-homes a device onto want's profile and membership instant and marks it live, only
// if the row is still exactly the observed one. Its lifecycle clock becomes want's ExpectedSince,
// as a delivered roster fact would set it.
func (s *DeviceRosterStore) ReplaceIf(ctx context.Context, want *DeviceRoster, observed DeviceRoster) (bool, error) {
	tx := rosterObserved(s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).Model(&DeviceRoster{}), observed).
		Updates(map[string]any{
			"profile_token":  want.ProfileToken,
			"expected_since": want.ExpectedSince,
			"deleted":        false,
			"last_event_at":  want.ExpectedSince,
			"updated_at":     time.Now().UTC(),
		})
	return tx.RowsAffected == 1, tx.Error
}

// TombstoneIf marks a live device deleted, only if the row is still exactly the observed one.
//
// Its lifecycle clock is LEFT AS OBSERVED rather than stamped: the reconcile knows the device is
// gone but not when, and inventing an instant would decide the ordering against facts it has not
// seen. Left as it is, a stale create fact redelivered for the same membership (equal instant)
// still cannot resurrect the row under the strict guard, and a genuine re-create — whose instant
// is later — still applies.
func (s *DeviceRosterStore) TombstoneIf(ctx context.Context, observed DeviceRoster) (bool, error) {
	tx := rosterObserved(s.rdb.DB(dccore.WithTenant(ctx, observed.Tenant)).Model(&DeviceRoster{}), observed).
		Updates(map[string]any{"deleted": true, "updated_at": time.Now().UTC()})
	return tx.RowsAffected == 1, tx.Error
}

// LoadTenant returns every attribute row of one tenant, tombstones included.
func (s *DeviceAttributeStore) LoadTenant(ctx context.Context, tenant string) ([]DeviceAttribute, error) {
	var rows []DeviceAttribute
	if err := s.rdb.DB(dccore.WithTenant(ctx, tenant)).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// InsertIfAbsent records an attribute value when no row — live or tombstone — exists for its
// (device, scope, key). It honours the device-deletion fence exactly as Upsert does, including the
// verify-after-write that closes the race with a concurrent PurgeDevice: a value the fence refuses
// is not written and does not count as applied.
func (s *DeviceAttributeStore) InsertIfAbsent(ctx context.Context, want *DeviceAttribute) (bool, error) {
	ctx = dccore.WithTenant(ctx, want.Tenant)
	if _, fenced, err := s.deletionFence(ctx, want.Tenant, want.DeviceToken, want.LastEventAt); err != nil || fenced {
		return false, err
	}
	want.Deleted = false
	tx := s.rdb.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(want)
	if tx.Error != nil {
		return false, tx.Error
	}
	deletedAt, fenced, err := s.deletionFence(ctx, want.Tenant, want.DeviceToken, want.LastEventAt)
	if err != nil {
		return false, err
	}
	if fenced {
		return false, s.sweepStraggler(ctx, want, deletedAt)
	}
	return tx.RowsAffected == 1, nil
}

// attributeObserved is the predicate naming an attribute row exactly as the reconcile read it.
func attributeObserved(db *gorm.DB, observed DeviceAttribute) *gorm.DB {
	return db.Where("device_token = ? AND scope = ? AND attr_key = ? AND deleted = ? AND value = ? AND last_event_at = ?",
		observed.DeviceToken, observed.Scope, observed.AttrKey, observed.Deleted, observed.Value, observed.LastEventAt)
}

// ReplaceIf sets an attribute's value and write time to want's and marks it live, only if the row
// is still exactly the observed one.
func (s *DeviceAttributeStore) ReplaceIf(ctx context.Context, want *DeviceAttribute, observed DeviceAttribute) (bool, error) {
	tx := attributeObserved(s.rdb.DB(dccore.WithTenant(ctx, want.Tenant)).Model(&DeviceAttribute{}), observed).
		Updates(map[string]any{
			"value":         want.Value,
			"deleted":       false,
			"last_event_at": want.LastEventAt,
			"updated_at":    time.Now().UTC(),
		})
	return tx.RowsAffected == 1, tx.Error
}

// TombstoneIf marks a live attribute removed, only if the row is still exactly the observed one.
// Its clock is left as observed, for the reason DeviceRosterStore.TombstoneIf gives.
func (s *DeviceAttributeStore) TombstoneIf(ctx context.Context, observed DeviceAttribute) (bool, error) {
	tx := attributeObserved(s.rdb.DB(dccore.WithTenant(ctx, observed.Tenant)).Model(&DeviceAttribute{}), observed).
		Updates(map[string]any{"deleted": true, "updated_at": time.Now().UTC()})
	return tx.RowsAffected == 1, tx.Error
}
