// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

type plainInsertKey struct{}

// WithPlainInsert marks ctx so that a ColumnTable.Insert issued under it (Postgres only) is
// a PLAIN INSERT, without the ON CONFLICT … DO NOTHING arbiter.
//
// 🔴 A PLAIN INSERT DOES NOT ABSORB A DUPLICATE: IT FAILS WITH A UNIQUE VIOLATION
// (IsUniqueViolation), AND THE TRANSACTION IS ABORTED. It exists for a caller that expects
// no duplicates, wants to skip the arbiter (an ON CONFLICT insert writes a speculative
// heap record and a confirmation record for every row), and can recover the rare duplicate
// itself: take a SAVEPOINT, write under this context, and on IsUniqueViolation roll back to
// the savepoint and write again under the ORIGINAL context, where the arbiter absorbs the
// duplicate exactly as before. What is stored is then the same either way; only the cost
// of the common case differs. A caller with no such recovery must not use this.
//
// It changes nothing for an upsert (NewColumnUpsert), whose arbiter is the statement, or on
// the VALUES rendering sqlite gets, which keeps its arbiter.
func WithPlainInsert(ctx context.Context) context.Context {
	return context.WithValue(ctx, plainInsertKey{}, true)
}

func plainInsert(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(plainInsertKey{}).(bool)
	return on
}

// IsUniqueViolation reports whether err is Postgres refusing a row for a unique index or
// constraint (SQLSTATE 23505).
func IsUniqueViolation(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "23505"
}
