// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The ADR-077 deletion record.
//
// A tenant's purge starts by marking its row (BeginTenantPurge) and ends by REMOVING
// that row, because the row's unique index on the token is what reserves the token and
// the reservation is meant to last exactly as long as the purge. So the row cannot be
// where the outcome is recorded — the outcome outlives it. These two entities are where
// it lives instead, and together they are the answer to "was this tenant's data actually
// erased, and what was left?".

// TenantPurge is the record of one purge of one tenant token.
//
// Identity is (Token, Epoch), not Token. Releasing the token means the same token can be
// purged more than once over an instance's life, so a record keyed by the token alone
// would be overwritten by the successor's purge and destroy the evidence that the
// predecessor was erased.
//
// 🔴 It deliberately does not carry the tenant's display name. A deletion record has to
// name what was deleted and the token is unavoidable for that — it is the identity every
// other area stored. The name is avoidable, so it is not kept: a record that retained
// "Acme Manufacturing GmbH" would leave the erasure's own evidence as the last place the
// customer's details still lived.
//
// No gorm.Model: soft deletion on a record whose entire purpose is to be durable would
// let a DELETE leave the row in place reporting success.
type TenantPurge struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Token string    `gorm:"not null;size:128;uniqueIndex:idx_tenant_purges_token_epoch,priority:1"`
	Epoch time.Time `gorm:"not null;uniqueIndex:idx_tenant_purges_token_epoch,priority:2"`

	// CompletedAt is nil until every store has reported complete. It is set in the same
	// transaction that removes the tenant row, so "the token was released" and "the
	// erasure was recorded complete" cannot come apart.
	CompletedAt *time.Time `gorm:"index"`
	// Rows is the total erased across every store, at completion.
	Rows int64 `gorm:"not null;default:0"`
}

func (TenantPurge) TableName() string { return "iam_tenant_purges" }

// TenantPurgeStore is one store's line in a purge's ledger: what the relational
// database, the event store, the broker or the object store reported on the latest pass.
//
// One row per (purge, store), REWRITTEN each pass rather than appended. The coordinator
// needs the current state of each store, not a history of attempts, and every pass
// re-runs every store — the erasures are idempotent and a repeat costs a delete that
// matches nothing. So a row is a standing claim that gets re-established, not a log entry
// that could quietly go stale while the coordinator kept reading it.
type TenantPurgeStore struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantPurgeID uint   `gorm:"not null;uniqueIndex:idx_tenant_purge_stores_purge_store,priority:1"`
	Store         string `gorm:"not null;size:32;uniqueIndex:idx_tenant_purge_stores_purge_store,priority:2"`

	// Complete is the only field completion consults. Rows and Deferred explain it.
	Complete bool  `gorm:"not null;default:false"`
	Rows     int64 `gorm:"not null;default:0"`
	// Deferred names what this store still holds and did not erase — the reason
	// Complete is false, in words. It is written for someone deciding whether an
	// erasure claim can be made, so it names the DATA left behind, not the work item.
	Deferred string
	// Failure is the last error, empty on success. Kept apart from Deferred because
	// the two mean opposite things about the future: a failure is expected to clear on
	// the next pass, a deferral is not going to clear until someone builds something.
	Failure string
	// Note qualifies a line without blocking it: what this store decided not to look at,
	// or the ground on which it reported clean while erasing nothing. It is the third
	// member of a set whose distinctions are the whole point — Deferred blocks completion
	// and names data still here, Failure is expected to clear on the next pass, and a Note
	// clears neither and blocks nothing. A reader who merges any two of them gets a
	// different answer to "can I claim this data was erased?".
	//
	// 🔴 Rendered to an operator it must read as a QUALIFIER on "clean", never as a
	// problem to act on. A store carrying a note is working as designed; a store carrying
	// a deferral is not.
	Note        string
	AttemptedAt time.Time
	// CleanSince is when this store FIRST reported complete and has reported complete on
	// every pass since; cleared the moment it reports anything else.
	//
	// It is the difference between "the delete returned no error" and "nothing came
	// back", and only the second supports an erasure record. A residual scan run right
	// after the sweep can only see writes that were already in flight — a straggler
	// admitted just before the ingest fence noticed the deletion lands after it. Holding
	// a store clean across a settle window turns a single observation into a sustained
	// one, and it is the coordinator's answer to the deleter grading its own work.
	CleanSince *time.Time

	TenantPurge *TenantPurge `gorm:"foreignKey:TenantPurgeID;constraint:OnDelete:CASCADE"`
}

func (TenantPurgeStore) TableName() string { return "iam_tenant_purge_stores" }

// TenantsPurging returns every tenant whose purge has begun, oldest cut first, which is
// the coordinator's work queue.
//
// Ordered by epoch so a backlog drains in the order the deletions were asked for: a
// tenant deleted an hour ago should not wait behind one deleted a minute ago just
// because its token sorts later.
func (s *Store) TenantsPurging(ctx context.Context) ([]Tenant, error) {
	var out []Tenant
	err := s.sys(ctx).Where("purge_state = ?", PurgePurging).Order("purge_epoch, id").Find(&out).Error
	return out, err
}

// PurgeRecordFor reads the record for (token, epoch), or gorm.ErrRecordNotFound.
func (s *Store) PurgeRecordFor(ctx context.Context, token string, epoch time.Time) (*TenantPurge, error) {
	var out TenantPurge
	if err := s.sys(ctx).Where("token = ? AND epoch = ?", token, epoch).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsurePurgeRecord returns the record for (token, epoch), creating it on first sight.
//
// It reads before it writes, and that ordering is the whole design of this function. The
// coordinator calls it on EVERY pass for the whole life of a purge, which on a purge that
// cannot yet complete is forever — so an unconditional insert-on-conflict-do-nothing would
// be a speculative write per tenant per minute, each conflict leaving a dead tuple behind.
// The steady state here is one indexed read and no write at all.
//
// The insert is still an upsert that does nothing on the unique (token, epoch) index,
// because the read-then-create is not atomic: two coordinator replicas racing on the same
// tenant can both miss, and both must converge on one record rather than one failing.
func (s *Store) EnsurePurgeRecord(ctx context.Context, token string, epoch time.Time) (*TenantPurge, error) {
	if token == "" {
		return nil, fmt.Errorf("refusing to open a purge record with no token")
	}
	switch existing, err := s.PurgeRecordFor(ctx, token, epoch); {
	case err == nil:
		return existing, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}
	rec := TenantPurge{Token: token, Epoch: epoch}
	if err := s.sys(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rec).Error; err != nil {
		return nil, err
	}
	return s.PurgeRecordFor(ctx, token, epoch)
}

// RecordPurgeStore writes one store's line of the ledger, replacing whatever the previous
// pass recorded for that store.
//
// Upsert on (purge, store) rather than insert: passes repeat, and a second row for the
// same store would make "is every store complete?" ambiguous — a stale incomplete line
// alongside a fresh complete one has no defined answer.
func (s *Store) RecordPurgeStore(ctx context.Context, line *TenantPurgeStore) error {
	if line.TenantPurgeID == 0 || line.Store == "" {
		return fmt.Errorf("refusing to record a ledger line with no purge (%d) or no store (%q)",
			line.TenantPurgeID, line.Store)
	}
	return s.sys(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_purge_id"}, {Name: "store"}},
		DoUpdates: clause.AssignmentColumns([]string{
			// Every writable column, and it has to STAY every writable column: a line is
			// rewritten on each pass rather than appended, so a column missing here keeps
			// the value the FIRST pass wrote forever, silently, while every other field
			// updates around it. TestRecordPurgeStoreReplacesTheLine is the guard.
			"complete", "rows", "deferred", "failure", "note", "attempted_at", "clean_since", "updated_at",
		}),
	}).Create(line).Error
}

// PurgeStores returns a purge's ledger lines, ordered by store name for a stable report.
func (s *Store) PurgeStores(ctx context.Context, purgeID uint) ([]TenantPurgeStore, error) {
	var out []TenantPurgeStore
	err := s.sys(ctx).Where("tenant_purge_id = ?", purgeID).Order("store").Find(&out).Error
	return out, err
}

// CompleteTenantPurge closes a purge: it stamps the record complete and REMOVES the
// tenant row, which is what releases the token for reuse. Both in one transaction,
// because a released token whose record does not say the data is gone is exactly the
// state ADR-077 exists to prevent.
//
// 🔴 THIS IS THE HARD DELETE THAT ADR-077 REMOVED, AND IT IS SAFE ONLY HERE. iam.Store
// has no DeleteTenant any more — hard-deleting a tenant freed its token while every
// other area still held rows keyed by it, so the next tenant created at that token
// inherited its predecessor's devices, telemetry and secrets. The defect was never the
// delete; it was the delete BEFORE reclamation. This method is the delete AFTER it, and
// three things keep it from becoming a general-purpose one again:
//
//   - it takes the purge RECORD, so there is no way to call it for a tenant that never
//     entered the lifecycle;
//   - the DELETE carries `purge_state = 'purging'` in its own WHERE clause and the row
//     count is checked, so a caller that reached here with an active tenant removes
//     nothing and gets an error rather than a silent success;
//   - it is Unscoped. Tenant embeds gorm.Model, so an ordinary Delete would SET
//     deleted_at and leave the row — and the unique index on token counts soft-deleted
//     rows, so the token would stay reserved forever while the code reported it
//     released. A soft delete here does not do a weaker version of the job, it does the
//     opposite of it.
//
// The caller is responsible for having established that every store reported complete;
// that judgement belongs to the coordinator, which is the only thing that knows the
// store set.
func (s *Store) CompleteTenantPurge(ctx context.Context, t *Tenant, rec *TenantPurge, rows int64, at time.Time) error {
	if rec == nil || rec.ID == 0 {
		return fmt.Errorf("refusing to complete a purge with no record")
	}
	if rec.Token != t.Token {
		return fmt.Errorf("purge record is for token %q but the tenant row is %q", rec.Token, t.Token)
	}
	// The token alone does not identify a purge. A token released by an earlier purge can
	// be taken again, so a PREDECESSOR's record carries the same token as this tenant and
	// would pass the check above — stamping the wrong purge complete and, worse, filing
	// this tenant's erasure under the earlier one's evidence.
	if t.PurgeEpoch == nil || !rec.Epoch.Equal(*t.PurgeEpoch) {
		return fmt.Errorf("purge record for %q is stamped at a different epoch (%v) from the tenant "+
			"row (%v) — a token can be purged more than once, so the epoch is what says WHICH purge",
			t.Token, rec.Epoch, t.PurgeEpoch)
	}
	// Refuse to rewrite a completion that already happened. This cannot arise from the
	// coordinator — completion removes the row in the same transaction — so a row still
	// present above a completed record means something restored or hand-inserted one, and
	// overwriting the original timestamp would destroy the evidence rather than repair it.
	if rec.CompletedAt != nil {
		return fmt.Errorf("purge record for %q at %v is already stamped complete (%v) while its "+
			"tenant row still exists — refusing to overwrite the original erasure record",
			t.Token, rec.Epoch, *rec.CompletedAt)
	}
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(rec).Updates(map[string]any{
			"completed_at": at,
			"rows":         rows,
		}).Error; err != nil {
			return err
		}
		out := tx.Unscoped().Where("id = ? AND purge_state = ?", t.ID, PurgePurging).Delete(&Tenant{})
		if out.Error != nil {
			return out.Error
		}
		if out.RowsAffected != 1 {
			return fmt.Errorf("refusing to complete: removing tenant %q at id %d affected %d rows, "+
				"expected 1 — the row is not in %s", t.Token, t.ID, out.RowsAffected, PurgePurging)
		}
		return nil
	})
}
