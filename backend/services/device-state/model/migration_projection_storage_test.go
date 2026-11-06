// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The three tables the StateProcessor updates per event each get the tuning, qualified by
// the area schema, and the statement is a SET that can be replayed.
func TestProjectionStorageCoversEveryHotTable(t *testing.T) {
	stmts := projectionStorageStatements()
	require.Len(t, stmts, 3)
	for i, table := range []string{"device_states", "latest_measurements", "latest_locations"} {
		require.True(t, strings.HasPrefix(stmts[i], `ALTER TABLE "device-state".`+table+` SET (`), stmts[i])
		require.Contains(t, stmts[i], "fillfactor = 75")
		require.Contains(t, stmts[i], "autovacuum_vacuum_scale_factor = 0")
		require.Contains(t, stmts[i], "autovacuum_vacuum_threshold = 10000")
		require.Contains(t, stmts[i], "autovacuum_vacuum_cost_limit = 2000")
	}
}

// The migration is appended after the existing chain and never edits it: the inactivity alarm
// time restore (20261106120000) and its final pass (20261107120000) follow it.
func TestProjectionStorageIsAppendedLast(t *testing.T) {
	require.Equal(t, "20261104120000", Migrations[len(Migrations)-3].ID)
	require.Equal(t, "20261106120000", Migrations[len(Migrations)-2].ID)
	require.Equal(t, "20261107120000", Migrations[len(Migrations)-1].ID)
}
