// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The ownership outbox (ADR-081): how a committed change of a device's owners is made to
// REACH device-state, rather than merely be announced to it.
//
// 🔴 WHY THE FACT CANNOT BE PUBLISHED FROM THE REQUEST. A device that LEAVES an owner — an
// edge removed, a group disabled, a claim reopened — keeps its old owner's members on its
// live view until device-state hears about it. When the fact was published after the commit
// and a publish failure was only logged, the device's next event was the only repair, and
// for a device that reports nothing there is no next event: the old owner kept the device
// for as long as it stayed quiet. That is the leak direction, and an unbounded one. A
// process that crashed between the commit and the publish lost the fact the same way, with
// nothing logged at all.
//
// So the change and the REQUEST to announce it commit together: every writer that may move
// a device's owners upserts one row per device into pending_ownership_facts inside its own
// transaction (queueOwnershipOnTx), and the drain (DrainOwnershipFacts), run on a timer by
// one replica at a time, reads each device's CURRENT anchors, publishes them, and deletes
// the row only once the broker has acknowledged the publish. A row is never deleted on a
// failure, so a committed change is announced as many times as it takes — a broker outage
// delays the fact by the length of the outage, not by the length of the device's silence.
// A rolled-back write leaves no row, so nothing is announced for a change that never was.
//
// What a row holds is "this device's owners may have changed", not what they changed to: the
// drain reads the anchors when it publishes, so a row queued twice, or queued by a write
// that changed nothing, publishes the same statement of state once. Over-announcing is
// harmless — device-state overwrites what it holds with the fact — which is what lets a
// writer queue every device it touched without first working out whether its owners moved.
//
// It also takes the fan-out off the request path: disabling an access group of 10 000
// devices used to publish 10 000 facts inline before the mutation could answer. It now
// writes 10 000 rows in one INSERT (rdb.CreateChunked splits only past the bind-parameter
// limit), and the drain publishes them in the background.

// OwnershipDrainPageSize is how many queued devices of one tenant one drain pass announces.
// It is a page, not a ceiling: the rest are announced on the next pass.
const OwnershipDrainPageSize = rdb.MaxLookupKeys

// PendingOwnershipFact records that a device's owners may have changed and device-state has
// not yet been told: one row per device, written in the transaction of the change, deleted
// by the drain once the fact is published.
//
// Generation identifies the write that queued the row last. The drain deletes a row only
// where Generation is still what it read, so a change committed while a fact was being
// published — after the drain read the device's anchors, before it deleted the row — leaves
// the row for the next pass instead of being swallowed by the delete. It is a random value
// rather than a counter because the upsert then needs no read of the row it replaces.
type PendingOwnershipFact struct {
	TenantId    string    `gorm:"primaryKey;not null;size:128"`
	DeviceId    uint      `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation  int64     `gorm:"not null"`
	RequestedAt time.Time `gorm:"not null"`
}

// TableName is NOT declared: a TableName() bypasses this area's TablePrefix (see
// schema/migration_list_order_indexes.go), and the type name derives pending_ownership_facts.

// AuditExempt opts the queue out of the audit journal. A row is bookkeeping derived from a
// write the journal already records — the edge, the group, the attribute — and lives only
// until it is published; journalling it would add a second row to every ownership change and
// one more to every drain, recording nothing an operator could act on.
func (PendingOwnershipFact) AuditExempt() bool { return true }

// ownershipQueued is the proof that a write queued its devices' ownership facts INSIDE its
// own transaction. The post-commit funnel (evictRelationshipSources, evictEntityDelete)
// takes one instead of a bare id list, and the only thing that makes one is
// queueOwnershipOnTx, so a new edge writer cannot reach the funnel it already has to call —
// or its devices' cached tracked sets go stale — without having queued their facts first.
type ownershipQueued struct {
	ids []uint
}

// queueOwnershipOnTx queues an ownership fact for each given device, in tx — the
// transaction of the write that may have moved the devices' owners. It is one statement for
// any number of devices up to the bind-parameter limit.
//
// 🔴 CALL IT LAST IN THE TRANSACTION. The upsert locks one row per device until the commit,
// and two writers queueing the same device serialize on it; a writer that queued and then
// went on to take another lock (a family advisory lock, a device's configuration lock) could
// close a cycle with one that holds that lock and is about to queue. Taken last, it only
// ever waits on a writer that is itself about to commit. The ids are queued in ascending
// order, so two writers queueing overlapping sets take the rows in the same order.
//
// A failure is the write's failure: the transaction rolls back rather than commit a change
// device-state will never hear of.
func queueOwnershipOnTx(tx *gorm.DB, deviceIds []uint) (ownershipQueued, error) {
	ids := make([]uint, 0, len(deviceIds))
	for _, id := range deviceIds {
		if id != 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ownershipQueued{}, nil
	}
	slices.Sort(ids)
	now := time.Now().UTC()
	rows := make([]PendingOwnershipFact, len(ids))
	for i, id := range ids {
		rows[i] = PendingOwnershipFact{DeviceId: id, Generation: rand.Int64(), RequestedAt: now}
	}
	err := rdb.CreateChunked(tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "device_id"}},
		// requested_at is deliberately NOT refreshed: the drain pages oldest-first, so a
		// device re-queued over and over would keep moving to the back of a backlog larger
		// than a page and a cold device behind it would be passed over for ever. The row
		// keeps the time of the request that first queued it, which is the one waiting longest.
		DoUpdates: clause.AssignmentColumns([]string{"generation"}),
	}), &rows).Error
	if err != nil {
		return ownershipQueued{}, fmt.Errorf("queueing the ownership facts of %d device(s): %w", len(ids), err)
	}
	return ownershipQueued{ids: ids}, nil
}

// sourceDevicesOnTx is relationshipSourceDevices inside a transaction, and with its error:
// the devices whose ownership facts a write queues must be read where the write is, and a
// read that fails must fail the write rather than queue a partial set.
func sourceDevicesOnTx(tx *gorm.DB, cond string, args ...any) ([]uint, error) {
	ids := make([]uint, 0)
	err := tx.Model(&EntityRelationship{}).
		Where("source_type = ?", string(entity.TypeDevice)).Where(cond, args...).
		Distinct().Pluck("source_id", &ids).Error
	return ids, err
}

// ErrNoOwnershipPublisher is the drain's refusal to run with nothing to publish through. It
// is an error, never an empty pass: a drain that deleted rows it could not publish would
// lose every queued fact, and one that reported success would hide that none is sent.
var ErrNoOwnershipPublisher = errors.New("no device ownership publisher is wired; queued ownership facts cannot be published")

// OwnershipDrainResult is what one drain pass did.
type OwnershipDrainResult struct {
	// Tenants is how many tenants had queued facts; Failed how many of them the pass could
	// not complete.
	Tenants, Failed int
	// Published is how many facts the broker acknowledged; Removed how many queued rows the
	// pass deleted (published, or naming a device that no longer exists).
	Published, Removed int
}

// DrainOwnershipFacts publishes one page of every tenant's queued ownership facts and
// deletes the rows it published. It is the outbox's only reader; see the file comment.
//
// 🔴 THE CROSS-TENANT READ IS THE LIST OF TENANTS WITH QUEUED ROWS, AND NOTHING ELSE. Every
// statement that reads or deletes a row runs under the row's own tenant, so the tenant
// predicate is on each of them; a system context is used only to learn which tenants to
// visit, which no tenant context can answer.
//
// Tenants are visited one at a time and a failed one does not stop the others — except for
// a failed PUBLISH, which ends the pass: the broker is shared, and a broker that has just
// refused one fact would make every remaining tenant wait out the same timeout. Each tenant
// gets at most a page, so one tenant's large backlog — a big access group toggled — is
// announced over several passes without holding up a small change in another tenant.
func (api *Api) DrainOwnershipFacts(ctx context.Context, pageSize int) (OwnershipDrainResult, error) {
	var res OwnershipDrainResult
	if api.DeviceOwnershipPublisher == nil {
		return res, ErrNoOwnershipPublisher
	}
	if pageSize < 1 || pageSize > OwnershipDrainPageSize {
		return res, fmt.Errorf("ownership drain page size must be between 1 and %d, got %d", OwnershipDrainPageSize, pageSize)
	}
	tenants := make([]string, 0)
	// Cross-tenant by necessity: which tenants have queued facts. No row is read here.
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingOwnershipFact{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return res, fmt.Errorf("listing tenants with queued ownership facts: %w", err)
	}
	res.Tenants = len(tenants)
	var lastErr error
	for _, tenant := range tenants {
		published, removed, err := api.drainTenantOwnership(core.WithTenant(ctx, tenant), pageSize)
		res.Published += published
		res.Removed += removed
		if err == nil {
			continue
		}
		res.Failed++
		lastErr = err
		var pubErr *ownershipPublishError
		if errors.As(err, &pubErr) {
			return res, err
		}
		log.Warn().Err(err).Str("tenant", tenant).Msg("Unable to drain a tenant's queued device ownership facts; they stay queued for the next pass")
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

// ownershipPublishError marks a drain failure as the broker's, which ends the pass.
type ownershipPublishError struct{ err error }

func (e *ownershipPublishError) Error() string {
	return "publishing a device ownership fact: " + e.err.Error()
}
func (e *ownershipPublishError) Unwrap() error { return e.err }

// drainTenantOwnership announces one page of one tenant's queued facts, oldest first.
//
// The anchors are read AFTER the queued rows, so every change whose row this page holds has
// committed (the row commits with it) and is in what is published. The fact's ChangedTime is
// taken just before that read: it is the moment the published state is known to be current
// as of, which is never earlier than the change, so device-state's precedence rule still
// keeps an event received before the change from overriding it (see device-state
// model/ownership.go).
//
// 🔴 THE TIME IS THE DATABASE'S, NOT THIS REPLICA'S. The drain's advisory lock passes between
// replicas, and device-state applies a fact only if it is strictly later than what it holds.
// Stamped from each holder's own wall clock, a skew between two consecutive holders larger
// than the gap between their passes would give the LATER fact the EARLIER time and have it
// refused. Every replica reads the one clock the database keeps, so the facts are ordered in
// the order they were read.
//
// A row is deleted only where its Generation is still what this pass read; a row re-queued
// meanwhile stays, and is announced again with the newer state. A device that no longer
// exists is deleted with no fact — the entity-deleted fact is its last word.
func (api *Api) drainTenantOwnership(ctx context.Context, pageSize int) (published, removed int, err error) {
	db := api.RDB.DB(ctx)
	rows := make([]PendingOwnershipFact, 0, pageSize)
	if err := db.Order("requested_at").Order("device_id").Limit(pageSize).Find(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("reading queued ownership facts: %w", err)
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	ids := make([]uint, len(rows))
	for i, r := range rows {
		ids[i] = r.DeviceId
	}
	changed, err := api.ownershipChangedTime(db)
	if err != nil {
		return 0, 0, fmt.Errorf("reading the database clock: %w", err)
	}
	facts, err := api.ownershipFacts(ctx, ids, changed)
	if err != nil {
		return 0, 0, fmt.Errorf("reading the anchors of %d queued device(s): %w", len(ids), err)
	}
	byId := make(map[uint]*DeviceOwnershipChangedEvent, len(facts))
	for _, f := range facts {
		byId[f.deviceId] = f
	}
	done := make([]PendingOwnershipFact, 0, len(rows))
	var pubErr error
	for _, r := range rows {
		f, exists := byId[r.DeviceId]
		if exists {
			if err := api.DeviceOwnershipPublisher.PublishDeviceOwnershipChanged(ctx, f); err != nil {
				pubErr = &ownershipPublishError{err: err}
				break
			}
			published++
		}
		done = append(done, r)
	}
	removed, err = deletePublishedOwnership(db, done)
	if err != nil {
		// Published but not deleted: the next pass announces the same state again, which
		// device-state applies as a no-op. Nothing is lost.
		return published, removed, fmt.Errorf("removing %d published ownership fact(s): %w", len(done), err)
	}
	return published, removed, pubErr
}

// ownershipChangedTime is the time a fact is stamped with: the database server's clock, read
// before the anchors. On an engine with no such clock (the SQLite the unit tests run on, which
// has a single process and so no skew to order) it is the process clock, which api.ownershipNow
// lets a test replace.
func (api *Api) ownershipChangedTime(db *gorm.DB) (time.Time, error) {
	if db.Dialector.Name() != "postgres" {
		if api.ownershipNow != nil {
			return api.ownershipNow().UTC(), nil
		}
		return time.Now().UTC(), nil
	}
	var now time.Time
	if err := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

// deletePublishedOwnership deletes the given rows where each still holds the Generation the
// drain read.
//
// 🔴 IT NEVER WAITS ON A WRITER. The rows are first locked with SKIP LOCKED, and only the rows
// this transaction then holds are deleted. A row a writer has locked is being re-queued by a
// transaction that has not committed yet — its Generation is about to change, so it would
// survive the delete anyway — and skipping it means the drain holds no row lock while waiting
// for another. A plain multi-row DELETE would wait on that writer while holding the rows it
// had already reached, in whatever order the plan visits them, and a writer queueing two of
// those rows in ascending order could close a cycle with it: the deadlock detector would then
// pick one of the two, and it could pick the request.
func deletePublishedOwnership(db *gorm.DB, rows []PendingOwnershipFact) (int, error) {
	removed := 0
	for start := 0; start < len(rows); start += rdb.MaxLookupKeys {
		chunk := rows[start:min(start+rdb.MaxLookupKeys, len(rows))]
		keys := make([][]any, len(chunk))
		for i, r := range chunk {
			keys[i] = []any{r.DeviceId, r.Generation}
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			held := make([]uint, 0, len(chunk))
			if err := tx.Model(&PendingOwnershipFact{}).
				Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
				Where("(device_id, generation) IN ?", keys).
				Pluck("device_id", &held).Error; err != nil {
				return err
			}
			if len(held) == 0 {
				return nil
			}
			res := tx.Where("device_id IN ?", held).Delete(&PendingOwnershipFact{})
			removed += int(res.RowsAffected)
			return res.Error
		})
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// ownershipDrainLockName names the advisory lock one drain pass runs under.
const ownershipDrainLockName = "device-management-ownership-drain"

// DrainOwnershipFactsLocked is one drain pass as the service runs it: under an advisory lock,
// so one replica drains at a time and the others skip (core.ErrPassSkipped). Two replicas
// draining at once would be harmless — a fact is a statement of state, published twice it
// is applied once — but would publish every queued fact twice.
func (api *Api) DrainOwnershipFactsLocked(ctx context.Context) error {
	var res OwnershipDrainResult
	ran, err := api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(ownershipDrainLockName), func() error {
		var err error
		res, err = api.DrainOwnershipFacts(ctx, OwnershipDrainPageSize)
		return err
	})
	if !ran {
		if err != nil {
			return fmt.Errorf("acquiring the ownership-drain lock: %w", err)
		}
		return core.ErrPassSkipped
	}
	if res.Published > 0 || res.Removed > 0 {
		log.Debug().Int("tenants", res.Tenants).Int("published", res.Published).Int("removed", res.Removed).
			Msg("Published queued device ownership facts")
	}
	return err
}
