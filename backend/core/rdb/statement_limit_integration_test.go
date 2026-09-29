// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The driver's client-side refusals, asked of the real driver and a real server.
//
// MaxBindParameters, IsStatementTooLarge and IsEncodeRefusal are claims about pgx that no unit
// test can check: the first that a statement binding exactly that many parameters is accepted
// and one more is refused, the others that each refusal still reads the way it is matched. pgx
// gives neither refusal a type, so a release that rewords one would turn its matcher false
// with nothing else noticing; this is where that fails instead.
package rdb

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// arrayOf is `SELECT cardinality(ARRAY[$1, …, $n]::int[])` and its n arguments.
func arrayOf(n int) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT cardinality(ARRAY[")
	args := make([]any, n)
	for i := range args {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(i + 1))
		args[i] = 1
	}
	b.WriteString("]::int[])")
	return b.String(), args
}

func TestIsStatementTooLargeAtTheDriverBoundary(t *testing.T) {
	db := staleItDB(t)
	sqldb, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()

	// The same path gorm takes: pgx's database/sql driver, which prepares the statement
	// before binding it.
	q, args := arrayOf(MaxBindParameters)
	var got int
	require.NoError(t, sqldb.QueryRowContext(ctx, q, args...).Scan(&got),
		"a statement binding exactly MaxBindParameters must be accepted")
	assert.Equal(t, MaxBindParameters, got)

	q, args = arrayOf(MaxBindParameters + 1)
	err = sqldb.QueryRowContext(ctx, q, args...).Scan(&got)
	require.Error(t, err, "a statement binding one parameter more must be refused")
	assert.True(t, IsStatementTooLarge(err), "the driver's refusal no longer reads as matched: %v", err)
}

// encodeItRow puts a uint64 in a bigint: gorm binds it as it is, and pgx cannot encode a value
// above the int64 range for the column the server described.
type encodeItRow struct {
	ID int64 `gorm:"primaryKey;autoIncrement:false"`
	V  int
	S  uint64
}

func (encodeItRow) TableName() string { return "encode_it_rows" }

func TestIsEncodeRefusalAtTheDriver(t *testing.T) {
	db := staleItDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS encode_it_rows (
		id bigint PRIMARY KEY, v integer NOT NULL, s bigint NOT NULL)`).Error)
	require.NoError(t, db.Exec("TRUNCATE TABLE encode_it_rows").Error)

	require.NoError(t, db.Create(&[]encodeItRow{{ID: 1, V: 1, S: math.MaxInt64}}).Error,
		"a value at the top of the int64 range must be accepted")
	err := db.Create(&[]encodeItRow{{ID: 2, V: 1, S: math.MaxInt64 + 1}}).Error
	require.Error(t, err, "a value one above the int64 range must be refused")
	assert.True(t, IsEncodeRefusal(err), "the driver's refusal no longer reads as matched: %v", err)
	assert.False(t, IsStatementTooLarge(err))
}
