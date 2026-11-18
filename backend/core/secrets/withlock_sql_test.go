// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postgresName struct{ gorm.Dialector }

func (postgresName) Name() string { return "postgres" }

// Exercise the production Postgres branch without pretending SQLite implements
// advisory locks. Only the lock statement is intercepted; transactions are real.
func TestWithLockPostgresStatementAndFailure(t *testing.T) {
	db := newStoreDB(t)
	db.Dialector = postgresName{db.Dialector}
	var names []string
	var lockErr error
	require.NoError(t, db.Callback().Raw().Replace("gorm:raw", func(tx *gorm.DB) {
		require.Equal(t, "SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tx.Statement.SQL.String())
		require.Len(t, tx.Statement.Vars, 1)
		names = append(names, tx.Statement.Vars[0].(string))
		tx.AddError(lockErr)
	}))
	st := NewStore(db, newTestKP(t))
	ctx := context.Background()
	for _, pair := range [][2]string{{"a", "b:c"}, {"a:b", "c"}, {"other", "b:c"}} {
		called := false
		ok, err := WithLock(ctx, st, pair[0], pair[1], func(view SecretStore) error {
			called = true
			require.NotSame(t, st, view, "callback needs a transaction-bound store")
			return nil
		})
		require.True(t, ok)
		require.NoError(t, err)
		require.True(t, called)
	}
	require.Equal(t, []string{"secrets:1:a:b:c", "secrets:3:a:b:c", "secrets:5:other:b:c"}, names)
	lockErr = errors.New("lock refused")
	called := false
	ok, err := WithLock(ctx, st, "a", "b:c", func(SecretStore) error { called = true; return nil })
	require.True(t, ok)
	require.ErrorIs(t, err, lockErr)
	require.False(t, called, "lock acquisition failure must fail closed")
}
