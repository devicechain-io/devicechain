// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Exercise the read/write gap without scheduler-dependent goroutines.
func TestProviderUpdateDoesNotRecreateDeletedRow(t *testing.T) {
	for _, reuseToken := range []bool{false, true} {
		t.Run(fmt.Sprint("reuse token: ", reuseToken), func(t *testing.T) {
			api := newTestApi(t)
			created := mustProvider(t, api, "primary")
			var replacement *AIProvider
			fired := false
			require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register("test:delete_before_update", func(tx *gorm.DB) {
				if fired || tx.Statement.Table != "ai_providers" {
					return
				}
				fired = true
				other := tx.Session(&gorm.Session{NewDB: true})
				require.NoError(t, other.Exec("DELETE FROM ai_providers WHERE id = ?", created.ID).Error)
				if reuseToken {
					replacement = &AIProvider{TokenReference: rdb.TokenReference{Token: "primary"}, Kind: created.Kind, ModelID: created.ModelID, Enabled: true}
					require.NoError(t, other.Create(replacement).Error)
				}
			}))

			got, err := api.UpdateAIProvider(adminCtx(), "primary", disableProvider(), nil)
			assert.True(t, fired)
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
			assert.Nil(t, got)
			var count int64
			require.NoError(t, api.sys(adminCtx()).Model(&AIProvider{}).Where("id = ?", created.ID).Count(&count).Error)
			assert.Zero(t, count, "the deleted provider must not reappear")
			if reuseToken {
				_, stored := providerOf(t, api, adminCtx(), "primary")
				assert.Equal(t, replacement.ID, stored.ID)
				assert.True(t, stored.Enabled, "the replacement must not receive the old row's update")
			}
		})
	}
}

func TestProviderUpdatePreservesConcurrentRename(t *testing.T) {
	api := newTestApi(t)
	created := mustProvider(t, api, "primary")
	fired := false
	require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register("test:rename_before_update", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "ai_providers" {
			return
		}
		fired = true
		require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE ai_providers SET token = ? WHERE id = ?", "renamed", created.ID).Error)
	}))

	// The response reload may report not-found for the old token; the committed
	// update must still preserve the rename and apply its folded fields.
	_, err := api.UpdateAIProvider(adminCtx(), "primary", disableProvider(), nil)
	if err != nil {
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
	assert.True(t, fired)
	found, err := api.AIProvidersByToken(adminCtx(), []string{"renamed"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, created.ID, found[0].ID)
	assert.False(t, found[0].Enabled)
}

func TestProviderDeleteIsAttributableInJournal(t *testing.T) {
	api, db := auditedTestApi(t)
	created := mustProvider(t, api, "primary")
	removed, err := api.DeleteAIProvider(adminCtx(), "primary")
	require.NoError(t, err)
	require.True(t, removed)
	var events []rdb.AuditEvent
	require.NoError(t, db.WithContext(api.sys(adminCtx()).Statement.Context).
		Where("table_name = ? AND operation = ?", "ai_providers", "delete").Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, fmt.Sprint(created.ID), events[0].EntityPK)
	assert.Equal(t, created.AuditLabel(), events[0].EntityLabel)
}

func TestProviderDeleteTargetsCheckedRowAfterTokenReuse(t *testing.T) {
	api := newTestApi(t)
	created := mustProvider(t, api, "primary")
	var replacement AIProvider
	fired := false
	require.NoError(t, api.RDB.Database.Callback().Delete().Before("gorm:delete").Register("test:reuse_before_delete", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "ai_providers" {
			return
		}
		fired = true
		other := tx.Session(&gorm.Session{NewDB: true})
		require.NoError(t, other.Exec("UPDATE ai_providers SET token = ? WHERE id = ?", "renamed", created.ID).Error)
		replacement = AIProvider{TokenReference: rdb.TokenReference{Token: "primary"}, Kind: created.Kind, ModelID: created.ModelID, Enabled: true}
		require.NoError(t, other.Create(&replacement).Error)
	}))
	removed, err := api.DeleteAIProvider(adminCtx(), "primary")
	require.NoError(t, err)
	assert.True(t, removed)
	assert.True(t, fired)
	var rows []AIProvider
	require.NoError(t, api.sys(adminCtx()).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, replacement.ID, rows[0].ID)
	assert.Equal(t, "primary", rows[0].Token)
}
