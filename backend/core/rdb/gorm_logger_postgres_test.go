// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// unreachablePool is a connection that must never be used: the session below is DryRun, so
// gorm builds and logs each statement without executing it. A call would nil-panic.
type unreachablePool struct{ gorm.ConnPool }

// Every other test in this file's package runs on sqlite, whose placeholder is `?`.
// Production runs on Postgres, whose placeholders are numbered, and gorm's postgres Explain
// rewrites each `$N` to `$N$` before substituting the bound values — so with ParamsFilter
// returning none, a placeholder would reach the sql field as `$1$`. This renders real
// statements through the real postgres dialector and gorm's own logging path, and pins
// that the sql field shows the placeholder exactly as Postgres numbers it.
func TestPostgresPlaceholdersAreLoggedAsPostgresWritesThem(t *testing.T) {
	l, buf := captureGormLogger(zerolog.TraceLevel)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: unreachablePool{}}), &gorm.Config{Logger: l})
	require.NoError(t, err)
	dry := db.Session(&gorm.Session{DryRun: true}).Debug()

	const secret = "tenant-secret-value"
	buf.Reset()
	var got []widget
	require.NoError(t, dry.Where("name = ? AND id IN ?", secret, []uint{7, 8}).Limit(3).Find(&got).Error)

	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	sql, _ := lines[0]["sql"].(string)
	require.Equal(t,
		`SELECT * FROM "widgets" WHERE name = $1 AND id IN ($2,$3) LIMIT $4`,
		sql)
	require.NotContains(t, sql, secret)
	require.NotContains(t, sql, "$1$", "gorm's intermediate placeholder form must not leak")
}
