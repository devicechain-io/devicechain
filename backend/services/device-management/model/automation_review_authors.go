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
	// Journal ids are assigned at insert, not commit, so no boundary is exact.
	// Include every historical writer of differing rows for every baseline.
	// Over-including authors only excludes more approvers, which fails closed.
	changed, err := changedReviewAuthorRows(profile.ID, baselineSnap, draftSnap)
	if err != nil {
		return nil, err
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
	removed := []map[string]bool{{}, {}, {}, {}}
	ids := []map[string]bool{{}, {}, {}, {strconv.FormatUint(uint64(profile.ID), 10): true}}
	for which, snap := range []*ProfileSnapshot{baselineSnap, draftSnap} {
		track := func(kind int, id uint) {
			key := strconv.FormatUint(uint64(id), 10)
			ids[kind][key] = true
			if which == 0 {
				removed[kind][key] = true
			} else {
				delete(removed[kind], key)
			}
		}
		for _, m := range snap.Metrics {
			track(0, m.ID)
		}
		for _, c := range snap.Commands {
			track(1, c.ID)
		}
		for _, r := range snap.Rules {
			track(2, r.ID)
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
		var rows []string
		query := db.Model(&rdb.AuditEvent{}).Where("category = ? AND table_name = ?", rdb.AuditCategoryMutation, table).
			Where("entity_pk IN ?", keys)
		if len(changed[i]) == 0 {
			query = query.Where("id > ?", after)
		} else {
			// Include all historical writers of differing keyed rows.
			query = query.Where("(id > ? OR entity_pk IN ?)", after, changed[i])
		}
		if err := query.Distinct("actor").Pluck("actor", &rows).Error; err != nil {
			return nil, err
		}
		legacy, err := legacyReviewDeleteActors(db, model, table, removed[i])
		if err != nil {
			return nil, err
		}
		for _, actor := range append(rows, legacy...) {
			if actor != "" && actor != "system" {
				actors[actor] = true
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

// legacyReviewDeleteActors must stay indefinitely: pre-upgrade journal rows and
// deletes by not-yet-upgraded pods during a rolling upgrade have no key. Only an
// unexplained removal needs this conservative fallback. A moved row is still live;
// a soft-deleted row needs attribution just like a physically removed row.
// A delete takes the row lock after its last write, so its journal id exceeds
// that write's id. Publication is not a safe floor: a delete can race publication.
func legacyReviewDeleteActors(db *gorm.DB, model any, table string, removed map[string]bool) ([]string, error) {
	journal := func() *gorm.DB {
		return db.Model(&rdb.AuditEvent{}).Where("category = ? AND table_name = ?", rdb.AuditCategoryMutation, table)
	}
	var floor uint
	unresolved := false
	for id := range removed {
		if id == "0" {
			continue
		}
		var deleted int64
		if err := journal().Where("entity_pk = ? AND operation = ?", id, "delete").Count(&deleted).Error; err != nil {
			return nil, err
		}
		if deleted != 0 {
			continue
		}
		var present int64
		if err := db.Model(model).Where("id = ?", id).Count(&present).Error; err != nil {
			return nil, err
		}
		if present != 0 {
			continue
		}
		var latest uint
		if err := journal().Where("entity_pk = ?", id).Select("COALESCE(MAX(id), 0)").Scan(&latest).Error; err != nil {
			return nil, err
		}
		if !unresolved || latest < floor {
			floor = latest
		}
		unresolved = true
	}
	if !unresolved {
		return nil, nil
	}
	var actors []string
	err := journal().Where("entity_pk = ? AND operation = ? AND id > ?", "", "delete", floor).
		Distinct("actor").Pluck("actor", &actors).Error
	return actors, err
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
