// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
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
// that a struct update would skip. gorm adds updated_at itself, so any non-empty map
// moves the row's version. The caller should reload the row afterwards if it hands the
// version back: the value gorm leaves on record is the one it SENT, which a database
// that stores microseconds does not keep byte for byte.
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
	res := db.Model(record).Omit(clause.Associations).
		Where("updated_at = ?", readAt).
		Updates(assignments)
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
