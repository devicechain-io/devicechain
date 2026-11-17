// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Shared with the Postgres PID probe: preserve the reserved connection while
// giving each statement its own error state.
func claimsConnectionSession(pinned *gorm.DB) *gorm.DB {
	return pinned.Session(&gorm.Session{})
}

func TestClaimsConnectionRecoversAfterStatementError(t *testing.T) {
	db := sqlitetest.OpenMemory(t, &gorm.Config{})
	require.NoError(t, db.Connection(func(pinned *gorm.DB) error {
		conn := claimsConnectionSession(pinned)
		require.Same(t, pinned.Statement.ConnPool, conn.Statement.ConnPool)
		require.NoError(t, conn.Exec("CREATE TEMP TABLE claims_connection_probe (id integer)").Error)
		require.Error(t, conn.Exec("INSERT INTO claims_connection_missing VALUES (1)").Error)
		// A session created AFTER the failure would copy the sticky error too.
		mgr := &rdb.RdbManager{Database: conn}
		require.NoError(t, mgr.DB(context.Background()).Exec("INSERT INTO claims_connection_probe VALUES (1)").Error)
		var count int64
		require.NoError(t, conn.Raw("SELECT count(*) FROM claims_connection_probe").Scan(&count).Error)
		require.EqualValues(t, 1, count)
		return nil
	}))
}
