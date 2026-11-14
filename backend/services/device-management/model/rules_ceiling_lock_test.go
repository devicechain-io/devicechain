// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Keep SQLite's execution machinery while selecting the PostgreSQL lock path.
// The raw callback observes and refuses the lock before SQL reaches SQLite.
type ruleCeilingPostgres struct{ gorm.Dialector }

func (ruleCeilingPostgres) Name() string { return "postgres" }

func TestRuleActivationsRequireTheTenantLock(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "rollback"}[rollback], func(t *testing.T) {
			api := newPublishEmitTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			seedProfileWithRule(t, api, ctx, "a", "ra", true)
			_, err := api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
			require.NoError(t, err)
			db := api.RDB.Database
			db.Dialector = ruleCeilingPostgres{db.Dialector}
			refused := errors.New("lock unavailable")
			calls := 0
			require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test_rule_lock", func(tx *gorm.DB) {
				calls++
				require.Equal(t, "SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tx.Statement.SQL.String())
				require.Equal(t, []any{"acme:active_rules"}, tx.Statement.Vars)
				_ = tx.AddError(refused)
			}))
			if rollback {
				_, err = api.RollbackDeviceProfile(ctx, "a", 1)
			} else {
				_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
			}
			require.ErrorIs(t, err, refused)
			require.Equal(t, 1, calls)
		})
	}
}
