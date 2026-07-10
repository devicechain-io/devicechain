// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm/clause"
)

// attributeMonotonicGuard gates every per-row DeviceAttribute mutation on the fact clock: apply the
// DO UPDATE only when the incoming fact is STRICTLY newer than the row's recorded LastEventAt. This
// is what makes the projection converge to the latest fact under any delivery order — a redelivered
// or lagging older set/removal for the same (device, scope, key) is a no-op. Strict "<" (not "<=")
// means a stale fact tying the recorded time does not apply, so a redelivered set can never
// resurrect a same-instant removal tombstone; real set/removal times never tie.
var attributeMonotonicGuard = clause.Where{Exprs: []clause.Expression{
	clause.Expr{SQL: "device_attributes.last_event_at < excluded.last_event_at"},
}}

// DeviceAttributeStore is the durable projection of numeric, platform-set device attributes (ADR-051
// slice 4c-3) — the dynamic-threshold source a detection rule reads via the CEL "attr" var (slice
// 4c-3b-2). The attribute consumer upserts/removes each device-attribute fact here BEFORE acking it
// (persist-before-ack), and the entity-deleted consumer purges a deleted device's rows and drops a
// per-device fence. Every mutation is monotonic on the fact clock (LastEventAt), so redelivery and
// cross-stream reordering can neither regress a value nor resurrect a removed/deleted one.
type DeviceAttributeStore struct {
	rdb *rdb.RdbManager
	// beforeInsertForTest, when set, runs inside Upsert AFTER the deletion-fence pre-check passes but
	// BEFORE the row insert. It exists solely to let a test deterministically inject the concurrent
	// PurgeDevice window (fence-write + row-sweep committing between the pre-check and the insert)
	// that the verify-after-write step closes. It is nil in production.
	beforeInsertForTest func()
}

// NewDeviceAttributeStore wraps the rdb manager.
func NewDeviceAttributeStore(r *rdb.RdbManager) *DeviceAttributeStore {
	return &DeviceAttributeStore{rdb: r}
}

// Upsert applies one numeric SET fact: it records the attribute's current value and (re)marks it
// live, with LastEventAt set to the fact's write time. It inserts when absent and, on conflict,
// overwrites value/deleted — but ONLY when strictly newer than the recorded LastEventAt
// (attributeMonotonicGuard), so a stale redelivered/lagging set is a no-op and a set older than a
// removal tombstone cannot resurrect the value. Idempotent for an exact redelivery (equal LastEventAt
// is not strictly newer). LastEventAt MUST be non-zero (the caller drops a zero-timed fact) or the
// guard could never advance.
//
// FENCE (verify-after-write, not just check-before). A device deletion arrives on a DIFFERENT stream
// through a DIFFERENT consumer goroutine (PurgeDevice), so a check-then-insert would be TOCTOU-racy:
// a straggler set could read "no fence", then PurgeDevice could commit its fence + row-sweep, then
// the straggler's insert could land a PERMANENT phantom row for the dead device (neither fact
// redelivers, and a reused token inherits it). So the fast pre-check is only an optimization; the
// guarantee comes from re-reading the fence AFTER the insert and, if the device is now fenced at or
// after this fact's time, sweeping this straggler row with the same monotonic delete PurgeDevice
// uses. This is race-free under READ COMMITTED: for a phantom to survive, this re-read would have to
// miss the fence AND PurgeDevice's own sweep would have to miss this row — which forces PurgeDevice's
// sweep to commit before its own fence write, contradicting its fixed order. A newer token-reuse
// write to the same key (LastEventAt > the fence) is preserved by the delete's `<=` bound.
func (s *DeviceAttributeStore) Upsert(ctx context.Context, attr *DeviceAttribute) error {
	deletedAt, fenced, err := s.deletionFence(ctx, attr.Tenant, attr.DeviceToken, attr.LastEventAt)
	if err != nil {
		return err
	}
	if fenced {
		// The deletion is already visible: this fact is a straggler, so do not project it — and SWEEP
		// (don't just return), because a PRIOR attempt of this same Upsert may have inserted the row
		// and then errored on its post-insert verify/sweep; persistBeforeAck retries the whole call,
		// and this retry's pre-check would otherwise ack the fact while the phantom lingered. The
		// sweep is idempotent and, in the common case (no phantom), deletes nothing.
		return s.sweepStraggler(ctx, attr, deletedAt)
	}
	if s.beforeInsertForTest != nil {
		s.beforeInsertForTest() // test seam: inject a concurrent PurgeDevice into the check→insert gap
	}
	attr.Deleted = false
	if err := s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "device_token"}, {Name: "scope"}, {Name: "attr_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "deleted", "last_event_at", "updated_at"}),
		Where:     attributeMonotonicGuard,
	}).Create(attr).Error; err != nil {
		return err
	}
	// Verify: a PurgeDevice may have committed its fence + sweep in the window between our pre-check
	// and our insert. Re-read; if now fenced, our row is a straggler phantom — sweep it.
	deletedAt, fenced, err = s.deletionFence(ctx, attr.Tenant, attr.DeviceToken, attr.LastEventAt)
	if err != nil {
		return err // transient: the row stays, but the retry's pre-check re-runs the sweep (idempotent)
	}
	if fenced {
		return s.sweepStraggler(ctx, attr, deletedAt)
	}
	return nil
}

// sweepStraggler removes a straggler set's row for a device deleted at deletedAt, scoped to the exact
// (scope, key) and bounded `last_event_at <= deletedAt` so a concurrent token-reuse NEWER write to
// the same key (strictly after the deletion) is spared. It is idempotent — deleting an absent or
// already-swept row is a no-op — so both Upsert's fenced pre-check (retry safety) and its post-insert
// verify can call it freely.
func (s *DeviceAttributeStore) sweepStraggler(ctx context.Context, attr *DeviceAttribute, deletedAt time.Time) error {
	return s.rdb.DB(ctx).Where(
		"tenant = ? AND device_token = ? AND scope = ? AND attr_key = ? AND last_event_at <= ?",
		attr.Tenant, attr.DeviceToken, attr.Scope, attr.AttrKey, deletedAt).
		Delete(&DeviceAttribute{}).Error
}

// Remove applies one per-attribute REMOVAL fact (the attribute was deleted, or overwritten with a
// non-numeric value/type). It tombstones the (device, scope, key) row rather than dropping it, so a
// stale older set cannot resurrect the value. Like Upsert it is fenced by a device deletion (a
// removal for a dead device is redundant — the purge already dropped the rows) and monotonic: it
// upserts a fresh tombstone if the row is absent (a removal that races ahead of the set — cross-
// consumer/redelivery reordering), and applies only when strictly newer, so a stale redelivered
// removal cannot tombstone a legitimately re-set (newer) value. removedAt MUST be non-zero.
//
// Unlike Upsert, Remove keeps only the pre-check (no verify-after-write): the same PurgeDevice race
// can leave a straggler tombstone row, but a phantom TOMBSTONE is benign — it is excluded from
// LoadAll and a newer token-reuse set overwrites it via the monotonic guard, so it never surfaces as
// a live value (only a live phantom, which Upsert's verify closes, would).
func (s *DeviceAttributeStore) Remove(ctx context.Context, tenant, deviceToken, scope, attrKey string, removedAt time.Time) error {
	_, fenced, err := s.deletionFence(ctx, tenant, deviceToken, removedAt)
	if err != nil || fenced {
		return err
	}
	tombstone := &DeviceAttribute{
		Tenant:      tenant,
		DeviceToken: deviceToken,
		Scope:       scope,
		AttrKey:     attrKey,
		Deleted:     true,
		LastEventAt: removedAt,
	}
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "device_token"}, {Name: "scope"}, {Name: "attr_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"deleted", "last_event_at", "updated_at"}),
		Where:     attributeMonotonicGuard,
	}).Create(tombstone).Error
}

// PurgeDevice tears down a DELETED device's whole attribute projection (ADR-044 entity-deleted): it
// records/advances the per-device deletion fence and hard-deletes every attribute row for the device
// whose clock is at or before the deletion. Both steps are monotonic on the deletion time so a
// redelivered deletion is idempotent and, critically, a stale redelivered deletion cannot erase a
// token-reused device's NEWER attributes (their LastEventAt is strictly after the fence). The fence
// is written FIRST so a set fact racing the purge sees it and is dropped by Upsert's fence check;
// the row delete then removes what already landed. Both key columns are matched so a repeated token
// across tenants is not over-purged.
func (s *DeviceAttributeStore) PurgeDevice(ctx context.Context, tenant, deviceToken string, deletedTime time.Time) error {
	fence := &DeviceAttributeDeletion{Tenant: tenant, DeviceToken: deviceToken, DeletedAt: deletedTime}
	if err := s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"deleted_at", "updated_at"}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "device_attribute_deletions.deleted_at < excluded.deleted_at"},
		}},
	}).Create(fence).Error; err != nil {
		return err
	}
	return s.rdb.DB(ctx).Where(
		"tenant = ? AND device_token = ? AND last_event_at <= ?", tenant, deviceToken, deletedTime).
		Delete(&DeviceAttribute{}).Error
}

// deletionFence reads the per-device deletion fence and reports the recorded deletion time plus
// whether a fact stamped `at` is a straggler for that deletion — its write time is at or before the
// deletion. Such a fact must not mutate the projection (it would resurrect a phantom value for a
// token a new device may already have reused). A genuine post-reuse fact (write time strictly after
// the fence) is not fenced. An absent fence returns a zero time and not-fenced. The returned time is
// the sweep bound Upsert's verify step uses.
func (s *DeviceAttributeStore) deletionFence(ctx context.Context, tenant, deviceToken string, at time.Time) (time.Time, bool, error) {
	var fence DeviceAttributeDeletion
	tx := s.rdb.DB(ctx).Where("tenant = ? AND device_token = ?", tenant, deviceToken).Limit(1).Find(&fence)
	if tx.Error != nil {
		return time.Time{}, false, tx.Error
	}
	if tx.RowsAffected == 0 {
		return time.Time{}, false, nil
	}
	// A fact at or before the deletion is stale w.r.t. it; strictly-later facts (token reuse) pass.
	return fence.DeletedAt, !at.After(fence.DeletedAt), nil
}

// LoadAll returns every LIVE device attribute (tombstones excluded) — the full set the engine's
// dynamic-threshold view is rebuilt from at startup (slice 4c-3b-2). Not tenant-scoped (the
// projection spans every tenant, tenant on the row), so it reads the whole table.
func (s *DeviceAttributeStore) LoadAll(ctx context.Context) ([]DeviceAttribute, error) {
	var attrs []DeviceAttribute
	if err := s.rdb.DB(ctx).Where("deleted = ?", false).Find(&attrs).Error; err != nil {
		return nil, err
	}
	return attrs, nil
}
