// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The statement-size limit, asked of the real driver and a real server.
//
// MaxBindParameters and IsStatementTooLarge are both claims about pgx that no unit test can
// check: the first that a statement binding exactly that many parameters is accepted and one
// more is refused, the second that the refusal still reads the way it is matched. pgx gives
// the refusal no type, so a release that rewords it would turn IsStatementTooLarge false with
// nothing else noticing; this is where that fails instead.
package rdb

import (
	"context"
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
