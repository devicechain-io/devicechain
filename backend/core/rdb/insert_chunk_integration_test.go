// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// CreateChunked's all-or-nothing contract, asked of a real server.
//
// The contract rests on something gorm does, not on anything CreateChunked writes: it wraps a
// split insert in a transaction, or in a SAVEPOINT when the handle already is one. Only a
// refusal the SERVER raises can tell whether that happened. A refusal on the client side — the
// erasure fence, a value the driver cannot encode — sends nothing, so it neither aborts a
// transaction nor leaves a statement to roll back, and a test built on one passes with or
// without the savepoint. So the refusal here is the server's: a CHECK constraint violated by
// the last row, which a split insert sends in its SECOND statement, after the first has
// already been executed.
package rdb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// chunkItRow binds two parameters a row, so a split starts past 32767 rows.
type chunkItRow struct {
	ID int64 `gorm:"primaryKey;autoIncrement:false"`
	V  int
}

func (chunkItRow) TableName() string { return "chunk_it_rows" }

// chunkItDB is staleItDB's area with chunk_it_rows created in it and emptied: a column whose
// CHECK the server enforces, so a negative V is refused there and nowhere earlier.
func chunkItDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := staleItDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS chunk_it_rows (
		id bigint PRIMARY KEY, v integer NOT NULL CHECK (v >= 0))`).Error)
	require.NoError(t, db.Exec("TRUNCATE TABLE chunk_it_rows").Error)
	return db
}

// chunkItRows is enough rows to split in two, the last with badLast's V: a negative one is
// refused by the server, in the second statement.
func chunkItRows(t *testing.T, db *gorm.DB, badLast bool) []chunkItRow {
	t.Helper()
	per, err := RowsPerInsert(db, []chunkItRow{})
	require.NoError(t, err)
	rows := make([]chunkItRow, per+10)
	for i := range rows {
		rows[i] = chunkItRow{ID: int64(i + 1), V: 1}
	}
	if badLast {
		rows[len(rows)-1].V = -1
	}
	require.Greater(t, len(rows), per, "precondition: the insert must split")
	require.LessOrEqual(t, len(rows), 2*per, "precondition: the refused row must be in the second statement")
	return rows
}

func chunkItCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&chunkItRow{}).Count(&n).Error)
	return n
}

// On a handle that is not in a transaction, a refusal in the second statement leaves nothing:
// the first statement's rows are not committed on their own.
func TestCreateChunkedIsAllOrNothingOutsideATransaction(t *testing.T) {
	db := chunkItDB(t)

	err := CreateChunked(db, chunkItRows(t, db, true)).Error
	require.Error(t, err, "the server must refuse the last row")
	assert.Zero(t, chunkItCount(t, db), "the first statement's rows were committed without the second's")

	// The counterweight: the same rows, all valid, are stored whole.
	rows := chunkItRows(t, db, false)
	res := CreateChunked(db, rows)
	require.NoError(t, res.Error)
	assert.EqualValues(t, len(rows), res.RowsAffected)
	assert.EqualValues(t, len(rows), chunkItCount(t, db))
}

// Inside a transaction, a refusal in the second statement rolls back to the split's savepoint:
// its error is returned, the first statement's rows are gone, and the transaction goes on
// working — which it could not if the refusal had aborted it.
func TestCreateChunkedRollsBackToItsSavepointInsideATransaction(t *testing.T) {
	db := chunkItDB(t)
	rows := chunkItRows(t, db, true)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.Error(t, CreateChunked(tx, rows).Error, "the server must refuse the last row")
		var one int
		require.NoError(t, tx.Raw("SELECT 1").Scan(&one).Error,
			"the refusal aborted the transaction instead of rolling back to the savepoint")
		assert.Equal(t, 1, one)
		assert.Zero(t, chunkItCount(t, tx), "the first statement's rows survived the rollback")
		return tx.Create(&chunkItRow{ID: 1, V: 7}).Error
	}))

	var got []chunkItRow
	require.NoError(t, db.Find(&got).Error)
	assert.Equal(t, []chunkItRow{{ID: 1, V: 7}}, got, "only the row written after the rollback may be committed")
}
