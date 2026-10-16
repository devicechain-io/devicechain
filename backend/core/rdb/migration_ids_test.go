// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb_test

import (
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/rdb"
)

func chain(ids ...string) []*gormigrate.Migration {
	out := make([]*gormigrate.Migration, 0, len(ids))
	for _, id := range ids {
		out = append(out, &gormigrate.Migration{ID: id})
	}
	return out
}

// The control: a well-formed chain passes. Without it, a helper that refused everything
// would satisfy every negative case below.
func TestCheckMigrationIDsAcceptsADistinctChain(t *testing.T) {
	require.NoError(t, rdb.CheckMigrationIDs(chain("20260101000000", "20260102000000", "20260103000000")))
	require.NoError(t, rdb.CheckMigrationIDs(chain("20260101000000")))
}

func TestCheckMigrationIDsRefusesADuplicate(t *testing.T) {
	err := rdb.CheckMigrationIDs(chain("a", "b", "c", "b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `migrations[3] reuses the ID "b" already taken by migrations[1]`)
}

// A duplicate of the FIRST entry and of the LAST: the positions a lookup built
// off-by-one would miss.
func TestCheckMigrationIDsRefusesADuplicateAtEitherEnd(t *testing.T) {
	err := rdb.CheckMigrationIDs(chain("a", "a"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `migrations[1] reuses the ID "a" already taken by migrations[0]`)

	err = rdb.CheckMigrationIDs(chain("a", "b", "z", "z"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `migrations[3] reuses the ID "z" already taken by migrations[2]`)
}

// Every problem is reported, not just the first.
func TestCheckMigrationIDsReportsEveryDuplicate(t *testing.T) {
	err := rdb.CheckMigrationIDs(chain("a", "b", "a", "b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `migrations[2] reuses the ID "a"`)
	assert.Contains(t, err.Error(), `migrations[3] reuses the ID "b"`)
}

func TestCheckMigrationIDsRefusesWhatGormigrateRefuses(t *testing.T) {
	require.ErrorIs(t, rdb.CheckMigrationIDs(nil), rdb.ErrEmptyMigrationChain)

	err := rdb.CheckMigrationIDs(chain("a", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migrations[1] has an empty ID")

	err = rdb.CheckMigrationIDs([]*gormigrate.Migration{{ID: "a"}, nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migrations[1] is nil")

	err = rdb.CheckMigrationIDs(chain("a", "SCHEMA_INIT"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `migrations[1] uses the ID "SCHEMA_INIT"`)
}
