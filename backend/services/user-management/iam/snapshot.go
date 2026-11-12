// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

type snapshotKey struct{}

// ReadSnapshotTimeout bounds one snapshot, whatever deadline its caller brought (a caller with
// none would otherwise hold the transaction, and its pooled connection, for as long as it
// liked). A decision's reads take milliseconds; one that has not finished in this long is
// refused, and the caller treats that error as a refusal. A variable only so a test can
// shorten it.
var ReadSnapshotTimeout = 5 * time.Second

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
	// The outer snapshot alone sets the bound; a nested call reuses its context, deadline included.
	ctx, cancel := context.WithTimeout(ctx, ReadSnapshotTimeout)
	defer cancel()
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
