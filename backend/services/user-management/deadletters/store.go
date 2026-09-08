// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package deadletters is the read side of ADR-024: a durable consumer of the platform's
// dead-letter stream, the table it fills, and the queries an operator asks of it.
//
// # Why it lives in user-management
//
// The letters come from four other services, and none of them is the natural home for the
// answer to "what has the platform given up on". The stronger reason is what the surface
// has to be: a per-entry failure reason at full fidelity on a TENANT-readable surface
// hands back the status-class oracle the egress boundary closed — a tenant could learn
// whether a destination it named refused, timed out or answered, one dead letter at a
// time. Serving this from the operator plane, where the caller is an identity-tier
// principal rather than a tenant, means the question does not arise instead of being
// answered carefully in a resolver.
//
// The cost of that choice, stated rather than left implicit: a tenant cannot see its own
// dead letters. An operator can, and the letters are per-tenant so they can be told what
// theirs are. A tenant-facing surface would need the collapse rule first.
package deadletters

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeadLetter is one stored record. It is deliberately NOT rdb.TenantScoped: the reader is
// an operator asking across tenants, and the embed's callbacks would inject a predicate
// for a tenant that is never in context here. The column is still named tenant_id, which
// is what the ADR-077 purge classifies on — so a deleted tenant's letters are swept with
// everything else of theirs.
type DeadLetter struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId     string    `gorm:"not null;size:128;index:idx_dead_letters_tenant_time,priority:1"`
	OccurredTime time.Time `gorm:"not null;index:idx_dead_letters_tenant_time,priority:2,sort:desc;index:idx_dead_letters_time,sort:desc;index:idx_dead_letters_kind_time,priority:2,sort:desc"`

	Kind   string `gorm:"not null;size:64;index:idx_dead_letters_kind_time,priority:1"`
	Reason string `gorm:"not null;size:32"`
	Source string `gorm:"not null;size:64"`

	Summary string `gorm:"not null"`
	Detail  string

	Attempts    int
	Subject     string `gorm:"size:512"`
	Sequence    uint64
	Correlation string `gorm:"size:128"`
	Reference   string `gorm:"size:256"`

	// 🔴 THE UNIQUE INDEX IS WHAT MAKES Record IDEMPOTENT, so it is declared here as well
	// as in the migration that creates it. The tags on a live model are documentation
	// everywhere else in this codebase; on this one field the code DEPENDS on the
	// constraint — the ON CONFLICT below names it — and a model that did not declare it
	// worked in production (where the migration made it) while quietly storing duplicates
	// anywhere the table was derived from the model instead.
	StreamSeq uint64 `gorm:"not null;uniqueIndex:idx_dead_letters_stream_seq,priority:1"`

	// AppendTime is the broker's own write time for the message, and it is HALF THE
	// IDENTITY rather than decoration.
	//
	// 🔴 A STREAM SEQUENCE IS UNIQUE WITHIN ONE INCARNATION OF A STREAM, NOT ACROSS
	// STREAMS. Rebuild the broker — the ADR-028 restore drill, or a lost JetStream volume
	// — and sequences restart at 1 while this table still holds 1..N from before. Keyed on
	// the sequence alone, every letter after such a restore would collide with an older
	// row, be discarded by ON CONFLICT, ACKED, and counted as stored: silence that reads
	// exactly like success, on the one table whose job is to not lose things. The append
	// time is assigned by the broker at publish and is stable across redeliveries of the
	// same message, so it separates incarnations without separating a message from itself.
	AppendTime time.Time `gorm:"not null;uniqueIndex:idx_dead_letters_stream_seq,priority:2"`

	Payload []byte
}

func (DeadLetter) TableName() string { return "dead_letters" }

// FenceExempt implements rdb.FenceExempt: a dead letter records that work did NOT happen,
// so writing one for a purged tenant cannot resurrect anything a successor could inherit.
//
// 🔴 WITHOUT IT THE FENCE REFUSES THIS TABLE'S OWN WRITES DURING A PURGE. Deleting a tenant
// refuses its upstream writes, those consumers dead-letter, and the letter's own row would
// then be refused too — deleting the evidence that a tenant's last alarms went nowhere, and
// reporting it to an operator as a database outage. The rows are still swept by the purge
// like any other tenant-bearing table; only the write-time refusal is lifted.
func (DeadLetter) FenceExempt() bool { return true }

// SearchCriteria narrows a list. Every filter is optional; pagination is not.
type SearchCriteria struct {
	rdb.Pagination
	// Tenant, Kind and Source are exact matches — all three are closed or
	// operator-known vocabularies, so a LIKE would only invite a scan.
	Tenant string
	Kind   string
	Source string
	// Since and Until bound the occurred time, which is what an operator has when they
	// are working backwards from an incident.
	Since *time.Time
	Until *time.Time
}

// SearchResults is one page.
type SearchResults struct {
	Results    []DeadLetter
	Pagination rdb.SearchResultsPagination
}

// Store reads and writes the dead-letter table.
type Store struct {
	rdbm *rdb.RdbManager
}

// NewStore builds the store over this service's database.
func NewStore(rdbm *rdb.RdbManager) *Store { return &Store{rdbm: rdbm} }

// db returns a handle in a system context, which is what this table needs: it is read
// across tenants by an operator and written by a consumer that has no ambient tenant, and
// the rows carry their tenant as data rather than as scope.
func (s *Store) db(ctx context.Context) *gorm.DB { return s.rdbm.DB(systemCtx(ctx)) }

// Record stores one letter, keyed by the stream sequence it arrived on.
//
// 🔑 IDEMPOTENT BY (SEQUENCE, APPEND TIME) — the pair that survives a redelivery unchanged
// AND separates one incarnation of the stream from the next. Without any key, a consumer
// that stored a row and then failed to ack would store it again on every redelivery, and
// the redelivery cap would decide how many copies of one failure an operator sees. Without
// the append time, a rebuilt broker would collide every new letter with an old row and
// discard it silently — see the field.
func (s *Store) Record(ctx context.Context, tenant string, streamSeq uint64,
	appendTime time.Time, e deadletter.Envelope) error {
	row := &DeadLetter{
		TenantId: tenant, OccurredTime: e.OccurredAt, AppendTime: appendTime,
		Kind: string(e.Kind), Reason: string(e.Reason), Source: e.Source,
		Summary: e.Summary, Detail: e.Detail, Attempts: e.Attempts,
		Subject: e.Subject, Sequence: e.Sequence, Correlation: e.Correlation,
		Reference: e.Reference, StreamSeq: streamSeq, Payload: e.Payload,
	}
	return s.db(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "stream_seq"}, {Name: "append_time"}},
		DoNothing: true,
	}).Create(row).Error
}

// DefaultOrder implements rdb.Sortable: newest first, and TOTAL.
//
// 🔴 THE ID IS NOT DECORATION. occurred_time alone is not unique — a burst of failures
// shares an instant — so ordering by it leaves the rows within a tie group free to move
// between pages, which is the unstable-pagination defect this project has been bitten by
// before wearing an ORDER BY.
func (DeadLetter) DefaultOrder() string { return "occurred_time DESC, id DESC" }

// List returns one page, newest first.
func (s *Store) List(ctx context.Context, criteria SearchCriteria) (*SearchResults, error) {
	results := make([]DeadLetter, 0)
	db, pag := s.rdbm.ListOf(systemCtx(ctx), &DeadLetter{}, func(q *gorm.DB) *gorm.DB {
		if criteria.Tenant != "" {
			q = q.Where("tenant_id = ?", criteria.Tenant)
		}
		if criteria.Kind != "" {
			q = q.Where("kind = ?", criteria.Kind)
		}
		if criteria.Source != "" {
			q = q.Where("source = ?", criteria.Source)
		}
		if criteria.Since != nil {
			q = q.Where("occurred_time >= ?", *criteria.Since)
		}
		if criteria.Until != nil {
			q = q.Where("occurred_time <= ?", *criteria.Until)
		}
		return q
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &SearchResults{Results: results, Pagination: pag}, nil
}

// ByID returns one letter, or (nil, nil) when there is none.
func (s *Store) ByID(ctx context.Context, id uint) (*DeadLetter, error) {
	found := &DeadLetter{}
	if err := s.db(ctx).First(found, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return found, nil
}

// Prune deletes letters older than before, and reports how many it removed.
//
// 🔴 THE TABLE HAS TO BE BOUNDED SOMEWHERE, and the stream is not that place. The stream
// ages out in seven days, which is the window the store exists to outlive — ADR-024 asks
// for a record that survives longer than the messages it describes. So the retention is
// this sweep's, and it is configurable, and a value of zero would mean "keep forever",
// which the configuration refuses rather than accepts.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	res := s.db(ctx).Where("occurred_time < ?", before).Delete(&DeadLetter{})
	return res.RowsAffected, res.Error
}
