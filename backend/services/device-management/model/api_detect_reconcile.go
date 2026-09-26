// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"gorm.io/gorm"
)

// This file implements the three read-only doors event-processing reconciles its detection
// projections against — the published rules and active versions, the device roster, and the
// threshold attributes (shapes in model_detect_reconcile.go). Each door is built from the SAME
// code as the fact it stands in for, so a repair reproduces what the lost fact would have said:
// the rules from enabledSnapshotRulesStrict and resolveActiveSince, the roster from
// rosterEntries, the attributes from thresholdAttributeScopes/numericAttributeValue.
//
// Each is a keyset page over row ids and refuses a page size outside 1..its Max (loudly, rather
// than clamping to something the caller did not ask for).

// checkReconcilePageSize refuses a page size outside 1..max.
func checkReconcilePageSize(what string, limit, max int) error {
	if limit < 1 || limit > max {
		return fmt.Errorf("%s page size must be between 1 and %d, got %d", what, max, limit)
	}
	return nil
}

// fullPageCursor is the cursor a keyset page returns: the last row id it scanned when it scanned a
// full page, and nil when it scanned less — the walk is complete.
func fullPageCursor(scanned, limit int, lastId uint) *uint64 {
	if scanned < limit {
		return nil
	}
	next := uint64(lastId)
	return &next
}

// ActiveProfileRules returns one page of the tenant's PUBLISHED profiles, each with its active
// version token, the instant that version became active and the version's enabled rules.
//
// 🔴 A SNAPSHOT THAT DOES NOT PARSE, OR AN ACTIVE VERSION WITH NO VERSION ROW, IS AN ERROR FOR THE
// WHOLE PAGE — never a profile with no rules. The caller treats a rule it holds that is absent
// from this answer as deleted, so "no rules" would be an instruction to delete every rule the
// engine runs for that version. The fact emit logs and carries on over the same condition because
// its caller has already committed; this caller can simply try again.
func (api *Api) ActiveProfileRules(ctx context.Context, afterId uint64, limit int) (*ActiveProfileRulesPage, error) {
	if err := checkReconcilePageSize("active profile", limit, MaxActiveProfileRulesPageSize); err != nil {
		return nil, err
	}
	db := api.RDB.DB(ctx)
	profiles := make([]DeviceProfile, 0, limit)
	q := db.Model(&DeviceProfile{}).Select("id", "token", "active_version", "active_since").
		Where("device_profiles.active_version IS NOT NULL")
	if afterId > 0 {
		q = q.Where("device_profiles.id > ?", afterId)
	}
	if err := q.Order("device_profiles.id").Limit(limit).Find(&profiles).Error; err != nil {
		return nil, err
	}
	page := &ActiveProfileRulesPage{Entries: make([]ActiveProfileRules, 0, len(profiles))}
	for _, p := range profiles {
		latest, found, err := latestProfileVersion(db, p.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("device profile %q names active version %d but has no version rows",
				p.Token, p.ActiveVersion.Int32)
		}
		var active DeviceProfileVersion
		if err := db.Where("device_profile_id = ? AND version = ?", p.ID, p.ActiveVersion.Int32).
			First(&active).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("device profile %q names active version %d, which has no version row",
					p.Token, p.ActiveVersion.Int32)
			}
			return nil, err
		}
		rules, err := enabledSnapshotRulesStrict(active.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("device profile %q version %d: its frozen snapshot does not parse: %w",
				p.Token, p.ActiveVersion.Int32, err)
		}
		page.Entries = append(page.Entries, ActiveProfileRules{
			ProfileId:    p.ID,
			ProfileToken: p.Token,
			VersionToken: fmt.Sprintf("%s@%d", p.Token, p.ActiveVersion.Int32),
			ActiveSince:  resolveActiveSince(p.ActiveSince, p.ActiveVersion, latest),
			Rules:        rules,
		})
	}
	if len(profiles) > 0 {
		page.NextCursor = fullPageCursor(len(profiles), limit, profiles[len(profiles)-1].ID)
	}
	return page, nil
}

// DeviceRosterPage returns one page of the device roster: every device, the stable token of the
// profile its type adopts ("" when none) and when that membership began — rosterEntries, the same
// query the roster facts are built from.
func (api *Api) DeviceRosterPage(ctx context.Context, afterId uint64, limit int) (*DeviceRosterPage, error) {
	if err := checkReconcilePageSize("device roster", limit, MaxRosterPageSize); err != nil {
		return nil, err
	}
	entries, err := api.rosterEntries(ctx, func(q *gorm.DB) *gorm.DB {
		if afterId > 0 {
			q = q.Where("devices.id > ?", afterId)
		}
		return q.Limit(limit)
	})
	if err != nil {
		return nil, err
	}
	page := &DeviceRosterPage{Entries: entries}
	if len(entries) > 0 {
		page.NextCursor = fullPageCursor(len(entries), limit, entries[len(entries)-1].DeviceId)
	}
	return page, nil
}

// rosterRow is the scan shape of rosterEntries. ExpectedSince and CreatedAt are read separately
// and resolved in Go rather than COALESCEd in SQL, because a COALESCE of two timestamps comes back
// from SQLite as text the driver will not parse into a time.
type rosterRow struct {
	DeviceId      uint
	DeviceToken   string
	ExpectedSince sql.NullTime
	CreatedAt     time.Time
	ProfileToken  sql.NullString
}

// rosterEntries is the ONE query that says what a device's roster entry is: its token, the STABLE
// token of the profile its type adopts ("" when the type adopts none, or when the type or the
// profile is gone), and the instant its membership began — the stored expected_since, or the
// device's creation when none was stored. The roster facts this service emits and the roster page
// the detection engine reconciles from are both built from it, so the two cannot disagree.
//
// The profile token is the profile's identity, not a "{profileToken}@{version}" token, and the
// profile need NOT be published: a device is rostered under its profile the moment its type adopts
// one, so a later first publish can arm absence for it. An empty token (no profile) is still an
// entry, retained so a later re-type re-homes it.
//
// where narrows the statement (a device id, a type, a keyset page). Rows come back in id order.
func (api *Api) rosterEntries(ctx context.Context, where func(*gorm.DB) *gorm.DB) ([]DeviceRosterEntry, error) {
	rows := make([]rosterRow, 0)
	q := api.RDB.DB(ctx).Model(&Device{}).
		Select("devices.id AS device_id, devices.token AS device_token, " +
			"devices.expected_since AS expected_since, devices.created_at AS created_at, " +
			"device_profiles.token AS profile_token").
		Joins("LEFT JOIN device_types ON device_types.id = devices.device_type_id AND device_types.deleted_at IS NULL").
		Joins("LEFT JOIN device_profiles ON device_profiles.id = device_types.profile_id AND device_profiles.deleted_at IS NULL")
	if err := where(q).Order("devices.id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]DeviceRosterEntry, 0, len(rows))
	for _, r := range rows {
		since := r.CreatedAt
		if r.ExpectedSince.Valid {
			since = r.ExpectedSince.Time
		}
		out = append(out, DeviceRosterEntry{
			DeviceId:      r.DeviceId,
			DeviceToken:   r.DeviceToken,
			ProfileToken:  r.ProfileToken.String,
			ExpectedSince: since.UTC(),
		})
	}
	return out, nil
}

// thresholdRow is the scan shape of DeviceThresholdAttributePage.
type thresholdRow struct {
	Id          uint
	DeviceToken string
	Scope       string
	AttrKey     string
	ValueType   string
	Value       sql.NullString
	LastUpdated time.Time
}

// DeviceThresholdAttributePage returns one page of the device attributes a dynamic detection
// threshold can read: a DEVICE's attribute in a platform-set scope (thresholdAttributeScopes)
// whose value is numeric (numericAttributeValue) — the same two predicates that decide whether an
// attribute write emits a value fact rather than a removal. The scope and value type narrow the
// statement in SQL so a fleet's device-reported CLIENT attributes are never scanned; the value
// itself is parsed here, by the function the emit uses.
func (api *Api) DeviceThresholdAttributePage(ctx context.Context, afterId uint64, limit int) (*DeviceThresholdAttributePage, error) {
	if err := checkReconcilePageSize("threshold attribute", limit, MaxThresholdAttributePageSize); err != nil {
		return nil, err
	}
	rows := make([]thresholdRow, 0, limit)
	q := api.RDB.DB(ctx).Model(&EntityAttribute{}).
		Select("entity_attributes.id AS id, devices.token AS device_token, "+
			"entity_attributes.scope AS scope, entity_attributes.attr_key AS attr_key, "+
			"entity_attributes.value_type AS value_type, entity_attributes.value AS value, "+
			"entity_attributes.last_updated AS last_updated").
		Joins("JOIN devices ON devices.id = entity_attributes.entity_id AND devices.deleted_at IS NULL").
		Where("entity_attributes.entity_type = ?", string(entity.TypeDevice)).
		Where("entity_attributes.scope IN ?", thresholdAttributeScopes).
		Where("entity_attributes.value_type IN ?", numericAttributeValueTypes)
	if afterId > 0 {
		q = q.Where("entity_attributes.id > ?", afterId)
	}
	if err := q.Order("entity_attributes.id").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	page := &DeviceThresholdAttributePage{Entries: make([]DeviceThresholdAttribute, 0, len(rows))}
	for _, r := range rows {
		var raw *string
		if r.Value.Valid {
			raw = &r.Value.String
		}
		v, ok := numericAttributeValue(r.ValueType, raw)
		if !ok {
			continue // not a threshold value: the emit would have sent a removal for it
		}
		page.Entries = append(page.Entries, DeviceThresholdAttribute{
			Id:          r.Id,
			DeviceToken: r.DeviceToken,
			Scope:       r.Scope,
			AttrKey:     r.AttrKey,
			Value:       v,
			UpdatedAt:   r.LastUpdated.UTC(),
		})
	}
	if len(rows) > 0 {
		page.NextCursor = fullPageCursor(len(rows), limit, rows[len(rows)-1].Id)
	}
	return page, nil
}
