// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-microservice/config"
)

// The policy as values, including the rows database/sql would hide: it clamps an idle
// count above the open count itself, so only the policy's own answer shows whether the
// clamp is ours.
func TestPoolSizing(t *testing.T) {
	for _, tc := range []struct {
		cfgOpen, cfgIdle int
		open, idle       int
	}{
		{0, 0, 20, 20},     // nothing configured: the default pool, all of it kept
		{30, 0, 30, 30},    // a configured pool, all of it kept
		{1, 0, 1, 1},       // the smallest pool
		{12, 4, 12, 4},     // an explicit idle count is honoured
		{12, 40, 12, 12},   // ...up to the open count
		{0, 25, 20, 20},    // an idle count above the DEFAULT open count is clamped too
		{20, 20, 20, 20},   // explicit and equal
		{48, 0, 48, 48},    // a raised pool keeps all of it
		{100, 99, 100, 99}, // explicit, just below
	} {
		t.Run(fmt.Sprintf("open=%d,idle=%d", tc.cfgOpen, tc.cfgIdle), func(t *testing.T) {
			open, idle := poolSizing(tc.cfgOpen, tc.cfgIdle)
			assert.Equal(t, [2]int{tc.open, tc.idle}, [2]int{open, idle})
		})
	}
}

func TestApplyPoolSizing(t *testing.T) {
	for _, tc := range []struct {
		cfgOpen, cfgIdle int
		open, idle       int
		errKey           string
	}{
		{-1, 0, 0, 0, "maxOpenConnections"},
		{0, -1, 0, 0, "maxIdleConnections"},
		{0, 0, 20, 20, ""},
		{10, 5, 10, 5, ""},
		{5, 10, 5, 5, ""},
	} {
		t.Run(fmt.Sprintf("open=%d,idle=%d", tc.cfgOpen, tc.cfgIdle), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)
			sqldb, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqldb.Close() })
			before := sqldb.Stats()
			cfg := config.MicroserviceDatastoreConfiguration{
				MaxOpenConnections: tc.cfgOpen,
				MaxIdleConnections: tc.cfgIdle,
			}
			nop := zerolog.Nop()
			err = applyPoolSizing(db, cfg, nop.Info())
			if tc.errKey != "" {
				require.ErrorContains(t, err, tc.errKey)
				assert.Equal(t, before, sqldb.Stats(), "invalid config must not change the pool")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.open, sqldb.Stats().MaxOpenConnections)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conns := make([]*sql.Conn, tc.open)
			for i := range conns {
				conns[i], err = sqldb.Conn(ctx)
				require.NoError(t, err)
			}
			for _, conn := range conns {
				require.NoError(t, conn.Close())
			}
			assert.Equal(t, tc.idle, sqldb.Stats().Idle)
			open, idle := poolSizing(tc.cfgOpen, tc.cfgIdle)
			assert.Equal(t, [2]int{tc.open, tc.idle}, [2]int{open, idle})
		})
	}
}
