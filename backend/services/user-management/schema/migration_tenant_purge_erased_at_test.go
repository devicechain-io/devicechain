// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// purgeErasedAtRow is an independent read-back type, deliberately not the migration's
// snapshot, so a wrong TableName cannot pass by being wrong in both places.
type purgeErasedAtRow struct {
	ID       uint
	ErasedAt *time.Time
}

func (purgeErasedAtRow) TableName() string { return "iam_tenant_purges" }

func TestTheErasedAtMigrationAddsTheColumnToTheDeletionRecord(t *testing.T) {
	db := newMigratedDB(t)
	require.True(t, db.Migrator().HasColumn(&purgeErasedAtRow{}, "erased_at"))

	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(
		`INSERT INTO iam_tenant_purges (token, epoch, erased_at) VALUES (?, ?, ?)`,
		"acme", at, at).Error)
	var back purgeErasedAtRow
	require.NoError(t, db.First(&back).Error)
	require.NotNil(t, back.ErasedAt)
	require.True(t, at.Equal(*back.ErasedAt))
}

func TestTheErasedAtMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	require.False(t, db.Migrator().HasTable("tenant_purge_erased_at_snapshots"))
}
