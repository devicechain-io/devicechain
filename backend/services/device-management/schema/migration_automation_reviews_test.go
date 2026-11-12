// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestAutomationReviewsMigrationIsReplayable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	m := NewAutomationReviewsSchema()
	require.Equal(t, "20261111150000", m.ID)
	require.Equal(t, m.ID, Migrations[len(Migrations)-2].ID)
	require.NoError(t, m.Migrate(db))
	require.NoError(t, m.Migrate(db))
	require.False(t, db.Migrator().HasColumn(&automationReview{}, "deleted_at"))
	row := automationReview{TenantId: "a", Token: "acr_one", RequestId: "request", DraftAuthors: datatypes.JSON("[]"),
		ProfileChanges: datatypes.JSON("[]"), Scope: datatypes.JSON("{}"), StreamBounds: datatypes.JSON("[]"), Expectations: datatypes.JSON("[]"), Approvals: datatypes.JSON("[]"), Result: `{ "n": 1.0 }`}
	require.NoError(t, db.Create(&row).Error)
	var stored automationReview
	require.NoError(t, db.First(&stored, row.ID).Error)
	require.Equal(t, `{ "n": 1.0 }`, stored.Result)
	duplicate := row
	duplicate.ID, duplicate.Token = 0, "acr_two"
	require.True(t, conflict.Is(db.Create(&duplicate).Error), "requestId is unique within a tenant")
	duplicate = row
	duplicate.ID, duplicate.RequestId = 0, "different"
	require.True(t, conflict.Is(db.Create(&duplicate).Error), "token is unique within a tenant")
	duplicate = row
	duplicate.ID, duplicate.TenantId = 0, "b"
	require.NoError(t, db.Create(&duplicate).Error)
	require.NoError(t, m.Rollback(db))
	require.False(t, db.Migrator().HasTable(&automationReview{}))
}
