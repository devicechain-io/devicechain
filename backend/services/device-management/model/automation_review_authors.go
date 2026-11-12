// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sort"
	"strconv"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// journalTable uses the same parsed statement table as the journal callback,
// including the configured dialect's schema/table prefix.
func journalTable(db *gorm.DB, model any) (string, error) {
	s := &gorm.Statement{DB: db}
	if err := s.Parse(model); err != nil {
		return "", err
	}
	return s.Table, nil
}

func (api *Api) draftAuthors(ctx context.Context, profile *DeviceProfile, baselineVersion int32, baselineSnap, draftSnap *ProfileSnapshot) ([]string, error) {
	db := api.RDB.DB(ctx)
	// A rollback leaves the draft intact. Only the publish that froze this version
	// is a safe author boundary; the latest activation still binds review staleness.
	var publishes []DeviceProfileActivation
	if err := db.Where("device_profile_id = ? AND version = ? AND kind = ?", profile.ID, baselineVersion, ActivationKindPublish).
		Order("id ASC").Limit(1).Find(&publishes).Error; err != nil {
		return nil, err
	}
	activationTable, err := journalTable(db, &DeviceProfileActivation{})
	if err != nil {
		return nil, err
	}
	var boundary []rdb.AuditEvent
	if len(publishes) != 0 {
		if err := db.Where("category = ? AND table_name = ? AND entity_pk = ? AND operation = ?",
			rdb.AuditCategoryMutation, activationTable, strconv.FormatUint(uint64(publishes[0].ID), 10), "create").
			Order("id DESC").Limit(1).Find(&boundary).Error; err != nil {
			return nil, err
		}
	}
	// Older activations were audit-exempt. With no journal boundary, no lower
	// bound is safe: over-including authors excludes more people from approval.
	var after uint
	if len(boundary) != 0 {
		after = boundary[0].ID
	}
	ids := []map[string]bool{{}, {}, {}, {strconv.FormatUint(uint64(profile.ID), 10): true}}
	for _, snap := range []*ProfileSnapshot{baselineSnap, draftSnap} {
		for _, m := range snap.Metrics {
			ids[0][strconv.FormatUint(uint64(m.ID), 10)] = true
		}
		for _, c := range snap.Commands {
			ids[1][strconv.FormatUint(uint64(c.ID), 10)] = true
		}
		for _, r := range snap.Rules {
			ids[2][strconv.FormatUint(uint64(r.ID), 10)] = true
		}
	}
	actors := map[string]bool{}
	for i, model := range []any{&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DeviceProfile{}} {
		table, err := journalTable(db, model)
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(ids[i]))
		for k := range ids[i] {
			if k != "0" {
				keys = append(keys, k)
			}
		}
		// A statement that matched by condition rather than by one key (a delete by token, a bulk
		// update) is journaled with no entity_pk, so it cannot be attributed to this profile or
		// ruled out. It counts: over-including excludes more people from approval, never fewer.
		keys = append(keys, "")
		var rows []rdb.AuditEvent
		if err := db.Where("category = ? AND table_name = ? AND id > ?", rdb.AuditCategoryMutation, table, after).
			Where("entity_pk IN ?", keys).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Actor != "" && row.Actor != "system" {
				actors[row.Actor] = true
			}
		}
	}
	out := make([]string, 0, len(actors))
	for actor := range actors {
		out = append(out, actor)
	}
	sort.Strings(out)
	return out, nil
}
