// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/schema"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func strp(s string) *string { return &s }

// testRootKey is a fixed 32-byte instance root key for the secret store in unit tests
// (never a real key). It only has to be 256-bit and stable across the test.
var testRootKey = []byte("0123456789abcdef0123456789abcdef")

// newTestApi spins up an in-memory sqlite database with the tenant-scope + token-grammar
// callbacks registered, the ai_providers table + its partial unique indexes + the shared
// secrets table migrated, and an envelope-encrypting secret store over the same DB, so
// the CRUD path (including the API-key round-trip and the active-provider invariant) is
// exercised exactly as production does.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	// Run the REAL provider migration (table + both partial unique indexes), then the
	// tests below INSERT via the model type — so this also proves the migration and the
	// model agree on the table name ("ai_providers"), the exact mismatch gorm's
	// initialism handling would otherwise introduce. (schema imports no model type, so
	// there is no import cycle.)
	require.NoError(t, schema.NewAIProvidersSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	require.NoError(t, err)
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// secretValue resolves a provider's stored API key (looked up by token to get its
// immutable id, the secret's key), or "" when none.
func secretValue(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	found, err := api.AIProvidersByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	value, err := api.Secrets.Resolve(ctx, AIProviderSecretRef(found[0].ID))
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return ""
	}
	require.NoError(t, err)
	return string(value)
}

func claudeReq(token string, secret *string) *AIProviderCreateRequest {
	return &AIProviderCreateRequest{
		Token:   token,
		Name:    strp("Claude"),
		Kind:    string(AIProviderKindAnthropic),
		Model:   "claude-opus-4-8",
		Enabled: true,
		Secret:  secret,
	}
}

// TestProviderCrud exercises create -> read -> update -> delete, including the
// write-only API-key round-trip.
func TestProviderCrud(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()

	created, err := api.CreateAIProvider(ctx, claudeReq("primary", strp("sk-test-123")))
	require.NoError(t, err)
	assert.Equal(t, "primary", created.Token)
	assert.False(t, created.Active, "a new provider is never active")
	assert.Equal(t, "sk-test-123", secretValue(t, api, ctx, "primary"))

	// Read back.
	found, err := api.AIProvidersByToken(ctx, []string{"primary"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "claude-opus-4-8", found[0].ModelID)

	// Update the model; omit the secret (nil) → key preserved.
	upd := claudeReq("primary", nil)
	upd.Model = "claude-haiku-4-5-20251001"
	_, err = api.UpdateAIProvider(ctx, "primary", upd, nil)
	require.NoError(t, err)
	found, _ = api.AIProvidersByToken(ctx, []string{"primary"})
	assert.Equal(t, "claude-haiku-4-5-20251001", found[0].ModelID)
	assert.Equal(t, "sk-test-123", secretValue(t, api, ctx, "primary"), "nil secret preserves the stored key")

	// Replace the key.
	repl := claudeReq("primary", strp("sk-test-999"))
	_, err = api.UpdateAIProvider(ctx, "primary", repl, nil)
	require.NoError(t, err)
	assert.Equal(t, "sk-test-999", secretValue(t, api, ctx, "primary"))

	// Clear the key (explicit empty string).
	clr := claudeReq("primary", strp(""))
	_, err = api.UpdateAIProvider(ctx, "primary", clr, nil)
	require.NoError(t, err)
	assert.Equal(t, "", secretValue(t, api, ctx, "primary"))

	// Delete removes the row.
	deleted, err := api.DeleteAIProvider(ctx, "primary")
	require.NoError(t, err)
	assert.True(t, deleted)
	found, _ = api.AIProvidersByToken(ctx, []string{"primary"})
	assert.Empty(t, found)
}

// TestUnknownKindRejected pins the fail-closed vocabulary gate: a kind with no shipped
// Provider impl is refused at write.
func TestUnknownKindRejected(t *testing.T) {
	api := newTestApi(t)
	req := claudeReq("x", nil)
	req.Kind = "openai-compatible" // reserved, but no impl at GA
	_, err := api.CreateAIProvider(context.Background(), req)
	assert.ErrorIs(t, err, ErrUnknownProviderKind)
}

// TestBadEndpointRejected rejects a non-http(s) endpoint at write.
func TestBadEndpointRejected(t *testing.T) {
	api := newTestApi(t)
	req := claudeReq("x", nil)
	req.Endpoint = strp("file:///etc/passwd")
	_, err := api.CreateAIProvider(context.Background(), req)
	assert.ErrorIs(t, err, ErrInvalidEndpoint)
}

// TestActiveProviderInvariant pins "default of none" + at-most-one-active: promoting a
// second provider clears the first, and clearing returns to none.
func TestActiveProviderInvariant(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()

	_, err := api.CreateAIProvider(ctx, claudeReq("a", nil))
	require.NoError(t, err)
	_, err = api.CreateAIProvider(ctx, claudeReq("b", nil))
	require.NoError(t, err)

	// Default of none.
	active, err := api.ActiveProvider(ctx)
	require.NoError(t, err)
	assert.Nil(t, active, "no provider is active until one is promoted")

	// Promote a.
	_, err = api.SetActiveProvider(ctx, "a")
	require.NoError(t, err)
	active, _ = api.ActiveProvider(ctx)
	require.NotNil(t, active)
	assert.Equal(t, "a", active.Token)

	// Promote b → a is cleared (at most one active).
	_, err = api.SetActiveProvider(ctx, "b")
	require.NoError(t, err)
	active, _ = api.ActiveProvider(ctx)
	require.NotNil(t, active)
	assert.Equal(t, "b", active.Token)
	found, _ := api.AIProvidersByToken(ctx, []string{"a"})
	require.Len(t, found, 1)
	assert.False(t, found[0].Active, "promoting b must clear a")

	// Clear → back to none.
	require.NoError(t, api.ClearActiveProvider(ctx))
	active, _ = api.ActiveProvider(ctx)
	assert.Nil(t, active)
}

// TestSetActiveMissingProvider returns not-found for an unknown token.
func TestSetActiveMissingProvider(t *testing.T) {
	api := newTestApi(t)
	_, err := api.SetActiveProvider(context.Background(), "nope")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestUpdateConflict pins the optimistic-concurrency precondition.
func TestUpdateConflict(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()
	_, err := api.CreateAIProvider(ctx, claudeReq("p", nil))
	require.NoError(t, err)
	_, err = api.UpdateAIProvider(ctx, "p", claudeReq("p", nil), strp("1999-01-01T00:00:00Z"))
	assert.ErrorIs(t, err, ErrConflict)
}
