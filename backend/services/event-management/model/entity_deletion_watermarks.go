// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const EntityDeletionResweepDelay = 10 * time.Minute
const EntityDeletionResweepLimit = 100

// EntityDeletionWatermark retains the latest deletion even after its resweep has
// finished, so arbitrarily late deliveries still pass through the insert filter.
// The tenant purge discovers its tenant_id column without a table registration.
type EntityDeletionWatermark struct {
	rdb.TenantScoped
	EntityType   string    `gorm:"size:64;not null"`
	EntityToken  string    `gorm:"size:128;not null"`
	DeletedTime  time.Time `gorm:"not null"`
	ResweepAfter *time.Time
}

// recordEntityDeletion commits independently of cleanup. A crash or an exhausted
// reconciler delivery after this write leaves a durable, armed resweep.
func (api *Api) recordEntityDeletion(ctx context.Context, typ, token string, before time.Time) (time.Time, error) {
	// SQLite compares timestamp text in MAX; offsets must not change its ordering.
	before = before.UTC()
	db := api.RDB.DB(ctx)
	maxTime := "GREATEST(entity_deletion_watermarks.deleted_time, excluded.deleted_time)"
	if db.Dialector.Name() == "sqlite" {
		maxTime = "MAX(entity_deletion_watermarks.deleted_time, excluded.deleted_time)"
	}
	after := time.Now().UTC().Add(EntityDeletionResweepDelay)
	w := EntityDeletionWatermark{EntityType: typ, EntityToken: token, DeletedTime: before, ResweepAfter: &after}
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "entity_type"}, {Name: "entity_token"}},
		DoUpdates: clause.Assignments(map[string]any{
			"deleted_time": gorm.Expr(maxTime), "resweep_after": after,
		}),
	}).Create(&w).Error
	if err != nil {
		return time.Time{}, err
	}
	// Read the maximum, including a concurrent deletion that won the upsert race.
	err = api.RDB.DB(ctx).Where("entity_type = ? AND entity_token = ?", typ, token).Take(&w).Error
	return w.DeletedTime, err
}

// anchorCleanupQuery is the shared cleanup policy, not proof of entity incarnation.
// Use the STORED parent's receipt time: replays may carry a different processed_time
// and parent inserts deliberately preserve the original. Only a missing parent uses
// occurred_time (retention can drop the hypertables independently, with no FK).
func anchorCleanupQuery(db *gorm.DB, typ, token string, before time.Time) *gorm.DB {
	q := db.Model(&EventAnchor{})
	if typ == string(entity.TypeDevice) {
		q = q.Where("device_token = ? OR (anchor_type = ? AND anchor_token = ?)", token, typ, token)
	} else {
		q = q.Where("anchor_type = ? AND anchor_token = ?", typ, token)
	}
	if !before.IsZero() {
		parent := db.Session(&gorm.Session{NewDB: true}).Model(&Event{}).Select("1").
			Where("events.tenant_id = event_anchors.tenant_id AND events.occurred_time = event_anchors.occurred_time AND events.event_id = event_anchors.event_id")
		q = q.Where("EXISTS (?) OR (NOT EXISTS (?) AND event_anchors.occurred_time < ?)",
			parent.Session(&gorm.Session{}).Where("events.processed_time < ?", before), parent, before)
	}
	return q
}

// filterDeletedEntityAnchors uses the caller's transaction/savepoint, including on a
// grouped retry. The common case costs one tenant-scoped PK lookup and no DELETE.
func filterDeletedEntityAnchors(ctx context.Context, db *gorm.DB, anchors []*EventAnchor) error {
	keys := make([][]any, 0, len(anchors)*2)
	seen := make(map[AnchorRef]bool)
	for _, a := range anchors {
		for _, k := range []AnchorRef{{Type: "device", Token: a.DeviceToken}, {Type: a.AnchorType, Token: a.AnchorToken}} {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, []any{k.Type, k.Token})
			}
		}
	}
	var watermarks []EntityDeletionWatermark
	// Bind whole key/candidate sets as JSON arrays so even a large grouped batch uses
	// a constant number of parameters (like the column-array inserts themselves).
	keyJSON, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	keyQuery := "(entity_type, entity_token) IN (SELECT k->>0, k->>1 FROM jsonb_array_elements(?::jsonb) AS k)"
	if db.Dialector.Name() == "sqlite" {
		keyQuery = "(entity_type, entity_token) IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?))"
	}
	if err := db.WithContext(ctx).Where(keyQuery, string(keyJSON)).Find(&watermarks).Error; err != nil {
		return err
	}
	if len(watermarks) == 0 {
		return nil
	}
	for _, w := range watermarks {
		q, err := anchorCleanupCandidates(anchorCleanupQuery(db.WithContext(ctx), w.EntityType, w.EntityToken, w.DeletedTime), anchors)
		if err != nil {
			return err
		}
		if err := q.Delete(&EventAnchor{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// anchorCleanupCandidates bounds the hypertable scan before applying the shared
// correlated cleanup predicate. Identity includes time: an id alone can span chunks.
func anchorCleanupCandidates(q *gorm.DB, anchors []*EventAnchor) (*gorm.DB, error) {
	if len(anchors) == 0 {
		return q.Where("1 = 0"), nil
	}
	pairs := make([][2]string, 0, len(anchors))
	var min, max time.Time
	for i, a := range anchors {
		at := a.OccurredTime
		stamp := at.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		if q.Dialector.Name() == "sqlite" {
			// Match go-sqlite's bound time.Time representation, preserving precision.
			stamp = at.Format("2006-01-02 15:04:05.999999999-07:00")
		}
		pairs = append(pairs, [2]string{stamp, hex.EncodeToString(a.EventId)})
		if i == 0 || at.Before(min) {
			min = at
		}
		if i == 0 || at.After(max) {
			max = at
		}
	}
	pairJSON, err := json.Marshal(pairs)
	if err != nil {
		return nil, err
	}
	pairQuery := "(event_anchors.occurred_time, event_anchors.event_id) IN (SELECT (p->>0)::timestamptz, decode(p->>1, 'hex') FROM jsonb_array_elements(?::jsonb) AS p)"
	if q.Dialector.Name() == "sqlite" {
		pairQuery = "(event_anchors.occurred_time, lower(hex(event_anchors.event_id))) IN (SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?))"
	}
	return q.Where("event_anchors.occurred_time BETWEEN ? AND ?", min, max).
		Where(pairQuery, string(pairJSON)), nil
}

// resweepSchedule keeps checking until transactions that could have read no
// watermark have ended. It is pure so both decisions are covered without Postgres.
func resweepSchedule(olderTxnRunning bool, now time.Time) *time.Time {
	if olderTxnRunning {
		after := now.UTC().Add(time.Minute)
		return &after
	}
	return nil
}

// DueEntityDeletionWatermarks is a bounded cross-tenant read; every write below
// returns to a tenant context and uses a model so the erasure fence still applies.
func (api *Api) DueEntityDeletionWatermarks(ctx context.Context, now time.Time) ([]EntityDeletionWatermark, error) {
	var due []EntityDeletionWatermark
	err := api.RDB.DB(core.WithSystemContext(ctx)).Where("resweep_after <= ?", now.UTC()).
		Order("resweep_after, tenant_id, entity_type, entity_token").Limit(EntityDeletionResweepLimit).Find(&due).Error
	return due, err
}

// ResweepEntityDeletionWatermark is idempotent across replicas. During rolling
// upgrades an OLD reconciler records no watermark and an OLD writer does not filter.
// Late anchors from an old writer after a new cleanup are caught only within the
// resweep delay; fully upgraded writers filter even after the resweep has cleared.
func (api *Api) ResweepEntityDeletionWatermark(ctx context.Context, w EntityDeletionWatermark) error {
	if core.IsSystemContext(ctx) {
		return errors.New("entity deletion resweep refuses a system context for tenant writes")
	}
	ctx = core.WithTenant(ctx, w.TenantId)
	db := api.RDB.DB(ctx)
	var olderTxnRunning bool
	if db.Dialector.Name() == "postgres" && w.ResweepAfter != nil {
		// A persist transaction can read no watermark, wait on unrelated rows for
		// longer than the delay, then insert after this cleanup. Do not disarm while
		// any transaction in this database predates the arm time. Other roles' sessions
		// show NULL xact_start without pg_read_all_stats; persists use our service role.
		armedAt := w.ResweepAfter.Add(-EntityDeletionResweepDelay)
		if err := db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname =
			current_database() AND pid <> pg_backend_pid() AND xact_start IS NOT NULL AND xact_start < ?)`, armedAt).
			Scan(&olderTxnRunning).Error; err != nil {
			return err
		}
	}
	if err := anchorCleanupQuery(api.RDB.DB(ctx), w.EntityType, w.EntityToken, w.DeletedTime).
		Delete(&EventAnchor{}).Error; err != nil {
		return err
	}
	// A newer deletion may re-arm between the read, cleanup and update. Preserve it
	// whether this pass clears the schedule or postpones it for an in-flight writer.
	return api.RDB.DB(ctx).Model(&EntityDeletionWatermark{}).
		Where("entity_type = ? AND entity_token = ?", w.EntityType, w.EntityToken).
		Where("resweep_after = ?", w.ResweepAfter).
		Update("resweep_after", resweepSchedule(olderTxnRunning, time.Now())).Error
}
