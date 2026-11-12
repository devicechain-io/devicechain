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
	var baseline DeviceProfileVersion
	// A missing historical version cannot establish an exact boundary either.
	if err := db.Where("device_profile_id = ? AND version = ?", profile.ID, baselineVersion).Limit(1).Find(&baseline).Error; err != nil {
		return nil, err
	}
	changed := make([][]string, 4)
	if baseline.AutomationReviewToken == nil {
		// Only reviewed publishes hold the journal lock: otherwise an in-flight edit
		// can get an id below the publish boundary without being in its snapshot.
		// For differing rows, include all historical writers. This over-includes
		// authors, which only excludes more people from approving.
		var err error
		changed, err = changedReviewAuthorRows(profile.ID, baselineSnap, draftSnap)
		if err != nil {
			return nil, err
		}
	}
	// A rollback leaves the draft intact. Use the publish that froze this version
	// for the boundary; the latest activation still binds review staleness.
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
		query := db.Where("category = ? AND table_name = ?", rdb.AuditCategoryMutation, table).
			Where("entity_pk IN ?", keys)
		if len(changed[i]) == 0 {
			query = query.Where("id > ?", after)
		} else {
			// Unknown keys may have affected a differing row, so include their history too.
			query = query.Where("(id > ? OR entity_pk IN ?)", after, append(changed[i], ""))
		}
		if err := query.Find(&rows).Error; err != nil {
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

// Compare row content with the same canonicalization used by snapshot digests;
// timestamps, tenant scope and other bookkeeping must not make a row differ.
func changedReviewAuthorRows(profileID uint, baseline, draft *ProfileSnapshot) ([][]string, error) {
	versions := make([][]map[string]string, 0, 2)
	for _, snap := range []*ProfileSnapshot{baseline, draft} {
		rows := []map[string]string{{}, {}, {}, {}}
		add := func(kind int, id uint, value any) error {
			canonical, err := reviewElement(value)
			if err != nil {
				return err
			}
			rows[kind][strconv.FormatUint(uint64(id), 10)] = string(canonical)
			return nil
		}
		for _, metric := range snap.Metrics {
			if err := add(0, metric.ID, metric); err != nil {
				return nil, err
			}
		}
		for _, command := range snap.Commands {
			if err := add(1, command.ID, command); err != nil {
				return nil, err
			}
		}
		for _, rule := range snap.Rules {
			if err := add(2, rule.ID, rule); err != nil {
				return nil, err
			}
		}
		declarations := &ProfileSnapshot{Location: snap.Location, Configuration: snap.Configuration, Decoder: snap.Decoder}
		if err := add(3, profileID, declarations); err != nil {
			return nil, err
		}
		versions = append(versions, rows)
	}
	changed := make([][]string, 4)
	for i, before := range versions[0] {
		after := versions[1][i]
		for id, value := range before {
			if other, ok := after[id]; !ok || other != value {
				changed[i] = append(changed[i], id)
			}
		}
		for id := range after {
			if _, ok := before[id]; !ok {
				changed[i] = append(changed[i], id)
			}
		}
	}
	return changed, nil
}
