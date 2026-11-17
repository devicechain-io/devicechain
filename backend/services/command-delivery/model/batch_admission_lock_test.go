// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"hash/fnv"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Only the dialect name is changed: SQLite still executes the batch. The raw
// callback records the Postgres admission protocol, not actual lock contention.
type admissionDialect struct{ gorm.Dialector }

func (admissionDialect) Name() string { return "postgres" }

func TestBatchAdmissionLocksBeforeCountingAndFailsClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "acquired", true: "lock-error"}[fail], func(t *testing.T) {
			api := newBatchTestApi(t)
			db := api.RDB.Database
			db.Dialector = admissionDialect{db.Dialector}
			ctx := machineryCtx(core.WithTenant(context.Background(), "acme"))
			sentinel := errors.New("admission lock unavailable")
			locked, counted := false, false
			require.NoError(t, db.Callback().Raw().Replace("gorm:raw", func(tx *gorm.DB) {
				require.Equal(t, "SELECT pg_advisory_xact_lock(?)", tx.Statement.SQL.String())
				hash := fnv.New64a()
				_, _ = hash.Write([]byte("command-batch:acme"))
				require.Equal(t, []any{int64(hash.Sum64())}, tx.Statement.Vars)
				locked = true
				if fail {
					tx.AddError(sentinel)
				}
			}))
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:admission-order", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*int64); ok {
					require.True(t, locked, "headroom was counted without the tenant admission lock")
					counted = true
				}
			}))
			batch, err := api.CreateCommandBatch(ctx, batchRequest("locked-batch", deviceTokens(2)))
			require.True(t, locked, "batch bypassed admission locking")
			if fail {
				require.ErrorIs(t, err, sentinel)
				require.Nil(t, batch)
				require.False(t, counted, "a failed lock must stop admission before counting")
				require.Empty(t, commandsOf(t, api, ctx, "locked-batch"))
				var rows []CommandBatch
				require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
				require.Empty(t, rows)
			} else {
				require.NoError(t, err)
				require.True(t, counted)
				require.Equal(t, 2, batch.Accepted)
			}
		})
	}
}
