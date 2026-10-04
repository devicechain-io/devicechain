// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// # Optimistic concurrency: one shape for a save made from a stale copy
//
// An update that accepts `expectedUpdatedAt` is saying "apply this only if the record is
// still the version I edited". Two steps enforce that, and both are needed:
//
//  1. RefuseIfMoved compares the CALLER's version with the one just read. It is the only
//     step that sees the caller's string at all.
//  2. UpdateIfUnmoved writes with `WHERE updated_at = <the value just read>`, so a writer
//     who lands between that read and this write moves the row and the write matches
//     nothing, instead of silently overwriting what they saved.
//
// Both steps rely on every write moving the version, so the writes that carry no
// precondition move it the same way, through AdvancingFrom.
//
// Every service that offers the precondition calls these two rather than spelling the
// comparison and the guarded write out itself. They used to be written out in each
// service, which is how one of them came to document a layout the code no longer used.

// StaleWriteError refuses a save made from a copy of a record that has changed since the
// caller read it. Each service declares ONE as a package variable (conventionally
// ErrConflict) so errors.Is identifies it.
//
// 🔴 IT IS NOT A UNIQUENESS CONFLICT AND MUST NEVER BECOME ONE: it has no Extensions()
// and is never a conflict.Error. CONFLICT tells a client "the value is already taken",
// which a client may read as "already exists, carry on" — and for a lost update that
// would report a save that never happened as done.
type StaleWriteError struct {
	subject string
}

// NewStaleWriteError returns the refusal for one kind of record. subject is the noun its
// sentence starts with ("dashboard", "connector", "provider", "notification policy").
// An empty subject panics: the sentence would begin with a space, and a service that
// forgets to name its record should fail at start-up, not in a client's error dialog.
func NewStaleWriteError(subject string) *StaleWriteError {
	if subject == "" {
		panic("rdb.NewStaleWriteError: a stale-write refusal needs the name of the record it refuses")
	}
	return &StaleWriteError{subject: subject}
}

// Error is "<subject> was modified by another writer; reload and try again".
//
// 🔴 THE WORDING IS A WIRE CONTRACT. The refusal carries no extensions.code, so the
// console recognises it by the phrase "modified by another writer". Change the sentence
// and a stale save stops being recognised as one.
func (e *StaleWriteError) Error() string {
	return e.subject + " was modified by another writer; reload and try again"
}

// staleWriteLayout MUST equal the layout core/graphql.FormatTime serves `updatedAt` in,
// because the string a caller sends back as its precondition is the one FormatTime
// produced. It is restated here rather than imported because core/graphql links the
// GraphQL runtime and this package must not; a test pins the two together.
//
// Sub-second precision is the point: at whole seconds, a caller whose copy was stale by
// less than a second passed the check and overwrote a change it had never seen.
const staleWriteLayout = time.RFC3339Nano

// RefuseIfMoved is the check against the caller's stated version. It returns nil when
// expectedUpdatedAt is nil (no precondition: the last write wins) or when it equals
// readAt as served; otherwise it returns stale.
//
// readAt is the record's UpdatedAt as just READ from the database, not a value computed
// in memory.
func RefuseIfMoved(readAt time.Time, expectedUpdatedAt *string, stale *StaleWriteError) error {
	mustHaveStale(stale)
	if expectedUpdatedAt == nil {
		return nil
	}
	if readAt.Format(staleWriteLayout) != *expectedUpdatedAt {
		return stale
	}
	return nil
}

// versionColumn is the column the stale-write guard versions a row by.
const versionColumn = "updated_at"

// nextVersion is the version a write made from a row read at readAt must store: the
// session's clock, or the first whole microsecond after readAt, whichever is later.
//
// The clock alone is not enough. Two writes can read the same instant from it (a coarse
// clock, a frozen test clock), and a second replica's clock can be behind the first's; either
// way the write would store the version it read, and a writer holding that version would
// still be told the row had not moved. The microsecond is the precision PostgreSQL keeps:
// a value less than a microsecond past readAt is stored AS readAt, so the floor is a whole
// microsecond strictly past it, which no truncation or rounding can bring back down.
func nextVersion(db *gorm.DB, readAt time.Time) time.Time {
	floor := readAt.Truncate(time.Microsecond).Add(time.Microsecond)
	if now := db.NowFunc(); now.After(floor) {
		return now
	}
	return floor
}

// AdvancingFrom returns a session on db whose clock reads the version a write from a row
// read at readAt must store (see nextVersion), for the writes to a versioned row that carry
// no precondition: a last-write-wins update, a rollback, a rename. They are not guarded, but
// they must still move the version past the one they read, or a writer who read it before
// them is not refused.
//
// The clock is pinned once, so every timestamp gorm stamps in that session is the same value.
// Use the session for the one write and nothing else.
func AdvancingFrom(db *gorm.DB, readAt time.Time) *gorm.DB {
	v := nextVersion(db, readAt)
	return db.Session(&gorm.Session{NowFunc: func() time.Time { return v }})
}

// UpdateIfUnmoved is the guarded write: it updates record's row with assignments only if
// that row's updated_at still equals readAt, and returns stale when it matched nothing —
// another writer moved the row after it was read, or deleted it.
//
// record must be the row as LOADED, with its primary key set. That is enforced, not
// documented: with a zero key, gorm adds no key condition and `updated_at = ?` alone
// would update every row that happens to share the timestamp. It also keeps the audit
// journal honest, which records the identity of the value handed to Model.
//
// The record's associations are never saved. A map Updates on a model otherwise upserts
// every preloaded has-many and belongs-to row it carries — and does so even when the
// UPDATE itself matched nothing.
//
// assignments is a map on purpose: it writes the zero values (false, 0, a cleared null)
// that a struct update would skip. It must not name updated_at: the version is this
// write's to set, and it is set to a value strictly past readAt at the precision the
// database keeps (the clock, or one microsecond past readAt when the clock has not moved
// that far), so a successful write always moves the version even when two writes read the
// same instant from the clock. The caller's map is not modified. The caller should reload
// the row afterwards if it hands the version back: the value left on record is the one
// SENT, which a database that stores microseconds does not keep byte for byte.
func UpdateIfUnmoved(db *gorm.DB, record any, readAt time.Time, assignments map[string]any,
	stale *StaleWriteError) error {
	mustHaveStale(stale)
	if len(assignments) == 0 {
		return errors.New("rdb.UpdateIfUnmoved: no columns to write")
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(record); err != nil {
		return fmt.Errorf("rdb.UpdateIfUnmoved: %w", err)
	}
	pk := stmt.Schema.PrioritizedPrimaryField
	if pk == nil {
		return fmt.Errorf("rdb.UpdateIfUnmoved: %s has no primary key", stmt.Schema.Name)
	}
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if _, zero := pk.ValueOf(ctx, reflect.Indirect(reflect.ValueOf(record))); zero {
		return fmt.Errorf("rdb.UpdateIfUnmoved: the %s passed has no primary key; it needs the loaded row",
			stmt.Schema.Name)
	}
	version := stmt.Schema.LookUpField(versionColumn)
	if version == nil {
		return fmt.Errorf("rdb.UpdateIfUnmoved: %s has no %s column to version it by",
			stmt.Schema.Name, versionColumn)
	}
	for _, key := range []string{version.DBName, version.Name} {
		if _, set := assignments[key]; set {
			return fmt.Errorf("rdb.UpdateIfUnmoved: %s is the version this write sets; do not assign it", key)
		}
	}
	write := maps.Clone(assignments) // the caller's map is theirs; it is never written to
	write[version.DBName] = nextVersion(db, readAt)
	res := db.Model(record).Omit(clause.Associations).
		Where(versionColumn+" = ?", readAt).
		Updates(write)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return stale
	}
	return nil
}

func mustHaveStale(stale *StaleWriteError) {
	if stale == nil {
		panic("rdb: a stale-write check needs the StaleWriteError it returns")
	}
}
