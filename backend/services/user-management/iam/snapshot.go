// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

type snapshotKey struct{}

// ErrOutsideSnapshot means a store operation cannot honor the caller's snapshot.
var ErrOutsideSnapshot = errors.New("store operation cannot run in a read snapshot")

// InSnapshot reports whether ctx carries a database read snapshot.
func InSnapshot(ctx context.Context) bool {
	_, ok := ctx.Value(snapshotKey{}).(*gorm.DB)
	return ok
}

// ReadSnapshot answers a decision from one state of this service's database.
// On Postgres the snapshot is taken at the FIRST statement, not at BEGIN. It is
// READ ONLY, so a write fails rather than commits. Facts outside Postgres, such as
// NATS KV and process configuration, are not part of this snapshot. Nested calls
// reuse the outer snapshot; fn's error rolls the transaction back and is returned.
func (s *Store) ReadSnapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	if InSnapshot(ctx) {
		return fn(ctx)
	}
	// sys, not a second system-context call site: ctx carries no snapshot here, so this is the
	// instance-global pool handle every store read already uses.
	db := s.sys(ctx)
	var opts []*sql.TxOptions
	if db.Dialector.Name() == "postgres" {
		opts = append(opts, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	// SQLite already serializes a transaction; glebarez/modernc may refuse an
	// explicit isolation level, so use its default transaction options.
	return db.Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, snapshotKey{}, tx))
	}, opts...)
}
