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
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func strp(s string) *string { return &s }

// testRootKey is a fixed 32-byte instance root key for the secret store in unit tests
// (never a real key). It only has to be 256-bit and stable across the test.
var testRootKey = []byte("0123456789abcdef0123456789abcdef")

// newTestApi spins up an in-memory sqlite database with the tenant-scope + token-grammar
// callbacks registered, the ai_providers + grant tables and their indexes + the shared
// secrets table migrated, and an envelope-encrypting secret store over the same DB, so
// the CRUD path (including the API-key round-trip and the grant invariants) is
// exercised exactly as production does.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	// foreign_keys=1: SQLite ignores FOREIGN KEY constraints unless asked, so without
	// this the grant tables' ON DELETE RESTRICT would be declared by the migration and
	// enforced by nothing in these tests — leaving the claim that the database backs up
	// ErrProviderInUse resting on the Postgres golden alone. Postgres enforces it
	// always; turning it on here makes the two engines agree and makes the backstop a
	// thing the suite can actually prove.
	db, err := gorm.Open(sqlite.Open("file::memory:?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	// Run the REAL migrations (tables + every index + the grant FKs), then the tests
	// below INSERT via the model types — so this also proves the migrations and the
	// models agree on the table names ("ai_providers", "ai_provider_tier_grants",
	// "ai_provider_tenant_grants", "ai_function_assignments"), the exact mismatch gorm's
	// initialism handling would otherwise introduce on all four. (schema imports no model
	// type, so there is no import cycle.)
	require.NoError(t, schemaMigrateAll(t, db))
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	require.NoError(t, err)
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// schemaMigrateAll runs ai-inference's real migrations, in order, against db. Shared so
// that a harness variant (see auditedTestApi) cannot drift from this one by forgetting a
// migration — a divergence that would show up as a missing table in one suite only.
func schemaMigrateAll(t *testing.T, db *gorm.DB) error {
	t.Helper()
	for _, m := range []func() *gormigrate.Migration{
		schema.NewAIProvidersSchema,
		schema.NewAIProviderGrantsSchema,
		schema.NewAIFunctionAssignmentsSchema,
	} {
		if err := m().Migrate(db); err != nil {
			return err
		}
	}
	return nil
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

// TestEndpointQueryRejected rejects an endpoint carrying a query or fragment — the
// provider impl splices its API path onto this base, so a query/fragment would build a
// broken URL.
func TestEndpointQueryRejected(t *testing.T) {
	api := newTestApi(t)
	for _, ep := range []string{"https://host/base?x=1", "https://host/base#frag"} {
		req := claudeReq("x", nil)
		req.Endpoint = strp(ep)
		_, err := api.CreateAIProvider(context.Background(), req)
		assert.ErrorIs(t, err, ErrInvalidEndpoint, ep)
	}
}

// TestGrantToUnknownProvider rejects a grant naming a provider that does not exist.
// The FK would reject the insert regardless; this pins the friendlier error, since the
// grant surface addresses providers by token and a constraint violation names an id.
func TestGrantToUnknownProvider(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()

	err := api.GrantProviderToTier(ctx, "gold", "nope")
	assert.ErrorIs(t, err, ErrUnknownProvider)

	err = api.GrantProviderToTenant(ctx, "acme", "nope")
	assert.ErrorIs(t, err, ErrUnknownProvider)
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
