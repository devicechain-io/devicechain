// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The chain must not be empty — gormigrate refuses to run with no migrations, which would
// leave the area with no schema at all — and the baseline must be first.
func TestTheChainStartsAtTheBaseline(t *testing.T) {
	require.NotEmpty(t, Migrations)
	require.Equal(t, BaselineID, Migrations[0].ID)
}

// The baseline creates no tables of the area's own (see NewBaselineSchema), and it is
// re-runnable: migrations run with UseTransaction:false and replay from the top after a
// failure.
func TestTheBaselineCreatesNothingAndReplays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)

	for range 2 {
		require.NoError(t, NewBaselineSchema().Migrate(db))
	}
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "the scaffold baseline must create no tables of the area's own")
}
