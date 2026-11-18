// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInferenceBucketMigrationIsRegisteredAndPreservesBudgetOnReplay(t *testing.T) {
	db := newMigratedDB(t)
	// Insert through literal SQL: the shipped chain must create the table, and
	// replay must preserve its data without relying on the runtime model's shape.
	require.NoError(t, db.Exec(`INSERT INTO ai_inference_buckets (tenant_id, tokens, updated_at, revision)
		VALUES ('acme', 0.25, 1234567890, 7)`).Error)
	require.NoError(t, NewInferenceBucketsMigration().Migrate(db))
	var row struct {
		Tokens    float64
		UpdatedAt float64
		Revision  int64
	}
	result := db.Raw("SELECT tokens, updated_at, revision FROM ai_inference_buckets WHERE tenant_id = ?", "acme").Scan(&row)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
	require.Equal(t, 0.25, row.Tokens)
	require.Equal(t, float64(1234567890), row.UpdatedAt)
	require.Equal(t, int64(7), row.Revision)
}
