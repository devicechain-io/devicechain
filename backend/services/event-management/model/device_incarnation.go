// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// incarnationBound is the lower bound a read by device token puts on RECEIPT time: the
// latest recorded deletion of a device with that token. A zero value (no deletion recorded)
// bounds nothing.
//
// A token can be deleted and created again. Both devices' events stay stored (deletion keeps
// history), so a read by token would otherwise return the deleted device's events as the new
// one's. Receipt time (events.processed_time, the STORED parent's) is what separates them: a
// deleted device resolves no new events, so everything received at or after its deletion is
// the new device's, including store-and-forward readings dated before the deletion, while an
// old device whose clock ran ahead has readings dated after the deletion but received before
// it. It is the same cut the deletion's anchor cleanup makes (anchorCleanupQuery).
type incarnationBound struct {
	token   string
	deleted time.Time
}

func (b incarnationBound) active() bool { return !b.deleted.IsZero() }

// deletedDeviceBound is entity_deletion_watermarks as deviceIncarnationBound reads it: the
// deletion time of a token, nothing else. It is a model of its own, rather than a read of
// EntityDeletionWatermark, because the bound applies to EVERY caller, a scoped one too, and
// the watermark model declares no scope rule (a scoped read of it is refused). A deletion
// time is not device data and is never returned to the caller: it only removes rows from a
// read the caller's own scope rule already limits. Writes are never made through it.
type deletedDeviceBound struct {
	rdb.TenantScoped
	EntityType  string
	EntityToken string
	DeletedTime time.Time
}

func (deletedDeviceBound) TableName(namer schema.Namer) string {
	return namer.TableName("EntityDeletionWatermark")
}

// ScopedBy admits every member, scoped or not, to read the deletion bound of a token.
func (deletedDeviceBound) ScopedBy() rdb.ScopeRule { return rdb.Catalogue() }

// deviceIncarnationBound looks up the bound for a read naming token, one primary-key read of
// entity_deletion_watermarks. A failed lookup fails the read: answering without the bound
// would return another device's events.
//
// A scoped caller gets the bound as well. Its reads are also limited to events whose anchors
// are in its scope, and the deletion removes the deleted device's anchors with this same cut,
// but that cleanup runs after the watermark commits and can fail; the read does not depend on
// it having finished.
func (api *Api) deviceIncarnationBound(ctx context.Context, token *string) (incarnationBound, error) {
	if token == nil {
		return incarnationBound{}, nil
	}
	var found []deletedDeviceBound
	if err := api.RDB.DB(ctx).Model(&deletedDeviceBound{}).
		Where("entity_type = ? AND entity_token = ?", string(entity.TypeDevice), *token).
		Limit(1).Find(&found).Error; err != nil {
		return incarnationBound{}, err
	}
	if len(found) == 0 {
		return incarnationBound{}, nil
	}
	return incarnationBound{token: *token, deleted: found[0].DeletedTime}, nil
}

// baseEvents bounds a read of the base event table, which carries its own receipt time.
func (b incarnationBound) baseEvents(db *gorm.DB) *gorm.DB {
	if !b.active() {
		return db
	}
	return db.Where("events.processed_time >= ?", b.deleted)
}

// payloadRows bounds a read of a payload table (or anything keyed by event_id) to the rows
// whose STORED parent event was received at or after the deletion. table qualifies event_id.
// A row whose parent is not stored (retention drops each hypertable's chunks independently,
// with no FK) cannot be shown to be the current device's and is left out.
//
// It is a top-level IN on an uncorrelated subquery, so Postgres plans it as one semi-join
// over the token's base events (idx_events_tenant_device_time), not a probe per row.
func (api *Api) payloadRows(ctx context.Context, b incarnationBound, table string, db *gorm.DB) *gorm.DB {
	if !b.active() {
		return db
	}
	return db.Where(table+".event_id IN (?)", api.RDB.DB(ctx).Model(&eventReceiptKey{}).Select("event_id").
		Where("device_token = ? AND processed_time >= ?", b.token, b.deleted))
}

// eventReceiptKey is the base event table as the inside of payloadRows' key subquery, for
// the reason eventAnchorKey is the anchor table's: the subquery only selects event ids, and
// what the caller may see is decided by the outer read's own rule on the same statement.
type eventReceiptKey struct {
	rdb.TenantScoped
	EventId       []byte
	DeviceToken   string
	ProcessedTime time.Time
}

func (eventReceiptKey) TableName(namer schema.Namer) string { return namer.TableName("Event") }

func (eventReceiptKey) ScopedBy() rdb.ScopeRule { return rdb.KeyFilterOnly() }
