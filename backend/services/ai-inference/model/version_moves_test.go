// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A save that lands in the same clock instant as the read must still move the provider's
// version, or an editor holding the version they read is not refused and overwrites it.
// Every row freezes the clock, so the version can only move because the write moves it.

// frozenAt is an instant with sub-microsecond digits. It is in the past on purpose.
var frozenAt = time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC)

// setClock replaces the clock every later call of api sees. Each api.* call takes a fresh
// copy of the database config, so the clock in effect is the one set before that call.
func setClock(api *Api, now *time.Time) {
	api.RDB.Database.Config.NowFunc = func() time.Time { return *now }
}

func providerOf(t *testing.T, api *Api, ctx context.Context, token string) (string, *AIProvider) {
	t.Helper()
	found, err := api.AIProvidersByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return *dcgraphql.FormatTime(found[0].UpdatedAt), found[0]
}

func describeProvider(d string) *AIProviderUpdateRequest {
	return &AIProviderUpdateRequest{Description: dcgraphql.OptionalStringOf(d)}
}

func TestEveryProviderWriteMovesTheVersion(t *testing.T) {
	rows := []struct {
		name  string
		write func(t *testing.T, api *Api, ctx context.Context, read string) string
		check func(t *testing.T, p *AIProvider)
	}{
		{"guarded update", func(t *testing.T, api *Api, ctx context.Context, read string) string {
			_, err := api.UpdateAIProvider(ctx, "primary", describeProvider("writer"), &read)
			require.NoError(t, err)
			return "primary"
		}, func(t *testing.T, p *AIProvider) { assert.Equal(t, "writer", p.Description.String) }},
		{"update without a precondition", func(t *testing.T, api *Api, ctx context.Context, _ string) string {
			_, err := api.UpdateAIProvider(ctx, "primary", describeProvider("writer"), nil)
			require.NoError(t, err)
			return "primary"
		}, func(t *testing.T, p *AIProvider) { assert.Equal(t, "writer", p.Description.String) }},
		{"rename", func(t *testing.T, api *Api, ctx context.Context, _ string) string {
			_, err := api.RenameAIProvider(ctx, "primary", "primary-2")
			require.NoError(t, err)
			return "primary-2"
		}, func(t *testing.T, p *AIProvider) { assert.Equal(t, "primary-2", p.Token) }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			api := newTestApi(t)
			now := frozenAt
			setClock(api, &now)
			ctx := context.Background()
			_, err := api.CreateAIProvider(ctx, claudeReq("primary", nil))
			require.NoError(t, err)
			v0, read := providerOf(t, api, ctx, "primary")

			token := row.write(t, api, ctx, v0)

			_, stored := providerOf(t, api, ctx, token)
			assert.True(t, stored.UpdatedAt.After(read.UpdatedAt), "the write did not move the version forward: read %v stored %v", read.UpdatedAt, stored.UpdatedAt)
			row.check(t, stored)

			// The editor who read the version before the write is refused and changes nothing.
			_, err = api.UpdateAIProvider(ctx, token, describeProvider("late"), &v0)
			assert.ErrorIs(t, err, ErrConflict)
			_, after := providerOf(t, api, ctx, token)
			assert.NotEqual(t, "late", after.Description.String)
		})
	}
}

// The clock moving on is what is stored: the floor never invents a version when time has moved.
func TestProviderWriteStoresTheClockWhenItHasMoved(t *testing.T) {
	api := newTestApi(t)
	now := frozenAt
	setClock(api, &now)
	ctx := context.Background()
	_, err := api.CreateAIProvider(ctx, claudeReq("primary", nil))
	require.NoError(t, err)

	now = frozenAt.Add(time.Hour)
	_, err = api.UpdateAIProvider(ctx, "primary", describeProvider("writer"), nil)
	require.NoError(t, err)
	_, stored := providerOf(t, api, ctx, "primary")
	assert.True(t, stored.UpdatedAt.Equal(now), "stored %v, want the clock %v", stored.UpdatedAt, now)
}

// A rename hands back the version the database stored, which is what the console sends as its
// next precondition. The fixture keeps updated_at to the microsecond, as PostgreSQL does, so a
// response carrying the value that was sent is refused by the very next save.
func TestRenameProviderReturnsTheStoredVersion(t *testing.T) {
	api := newTestApi(t)
	now := frozenAt
	setClock(api, &now)
	const hook = "test:store_microseconds"
	require.NoError(t, api.RDB.Database.Callback().Update().After("gorm:update").Register(hook, func(tx *gorm.DB) {
		p, ok := tx.Statement.Model.(*AIProvider)
		if !ok || tx.Error != nil || tx.Statement.RowsAffected == 0 {
			return
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).
			Exec("UPDATE ai_providers SET updated_at = ? WHERE id = ?", p.UpdatedAt.Truncate(time.Microsecond), p.ID).Error; err != nil {
			tx.AddError(err)
		}
	}))
	ctx := context.Background()
	_, err := api.CreateAIProvider(ctx, claudeReq("primary", nil))
	require.NoError(t, err)
	now = frozenAt.Add(time.Hour) // past the floor, so the clock itself (with its nanoseconds) is sent

	got, err := api.RenameAIProvider(ctx, "primary", "primary-2")
	require.NoError(t, err)

	_, stored := providerOf(t, api, ctx, "primary-2")
	assert.True(t, got.UpdatedAt.Equal(stored.UpdatedAt), "response %v, stored %v", got.UpdatedAt, stored.UpdatedAt)
	_, err = api.UpdateAIProvider(ctx, "primary-2", describeProvider("next"), dcgraphql.FormatTime(got.UpdatedAt))
	assert.NoError(t, err)
}
