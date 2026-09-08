// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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
// callbacks registered, the connector tables + the shared secrets table migrated, and an
// envelope-encrypting secret store over the same DB, so the CRUD path (including the
// credential round-trip) is exercised exactly as production does. It also creates the
// per-tenant partial unique token index the real migration creates (ADR-042 P1), so the
// token-uniqueness behavior runs against the real constraint, not a bare table.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&Connector{}, &ConnectorVersion{}))
	require.NoError(t, rdb.CreateTenantTokenIndex(db, &Connector{}))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	require.NoError(t, err)
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// secretValue resolves a connector's stored credential (looked up by token to get its
// immutable id, the secret's key) in the test's tenant context, or "" when none.
func secretValue(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	found, err := api.ConnectorsByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	ref, err := ConnectorSecretRef(ctx, found[0].ID)
	require.NoError(t, err)
	value, err := api.Secrets.Resolve(ctx, ref)
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return ""
	}
	require.NoError(t, err)
	return string(value)
}

const mqttConfig = `{"urls":["tcp://broker:1883"],"topic":"alerts"}`

// connectorEdit builds an update naming a new type and config and NOTHING else. Every
// other field — name, description, the write-only secret — is ABSENT, which under the
// three-state semantic means "leave it alone".
//
// 🔴 IT EXISTS TO KEEP WHAT A TEST LEAVES ABSENT VISIBLE. Under the full-replace input
// these call sites restated `token` and every field they were not exercising, because
// omitting one CLEARED it. The two readings now differ, and a helper naming the one
// shape that dominates this file means a call site doing anything more has to spell the
// literal out — so the extra field is the thing that stands out.
func connectorEdit(connectorType ConnectorType, config string) *ConnectorUpdateRequest {
	return &ConnectorUpdateRequest{
		Type:   dcgraphql.OptionalStringOf(string(connectorType)),
		Config: dcgraphql.OptionalStringOf(config),
	}
}

// TestConnectorCrud exercises create -> read -> update -> delete.
func TestConnectorCrud(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token:  "pager",
		Name:   strp("Ops pager"),
		Type:   string(ConnectorTypeMQTT),
		Config: mqttConfig,
	})
	require.NoError(t, err)
	assert.Equal(t, "pager", created.Token)
	assert.Equal(t, "mqtt", created.Type)

	found, err := api.ConnectorsByToken(ctx, []string{"pager"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.JSONEq(t, mqttConfig, string(found[0].Config))

	// A partial update naming type + config re-points the draft.
	_, err = api.UpdateConnector(ctx, "pager",
		connectorEdit(ConnectorTypeKafka, `{"addresses":["k:9092"],"topic":"t"}`), nil)
	require.NoError(t, err)
	found, err = api.ConnectorsByToken(ctx, []string{"pager"})
	require.NoError(t, err)
	assert.Equal(t, "kafka", found[0].Type)

	deleted, err := api.DeleteConnector(ctx, "pager")
	require.NoError(t, err)
	assert.True(t, deleted)
	found, err = api.ConnectorsByToken(ctx, []string{"pager"})
	require.NoError(t, err)
	assert.Len(t, found, 0)
}

// TestCreateConnectorRejectsUnknownType fails closed on a type outside the vocabulary.
func TestCreateConnectorRejectsUnknownType(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "bad", Type: "carrier_pigeon", Config: `{}`,
	})
	require.ErrorIs(t, err, ErrUnknownConnectorType)
}

// TestCreateConnectorRejectsBadConfig rejects non-object and oversized configs.
func TestCreateConnectorRejectsBadConfig(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, cfg := range []string{`42`, `true`, `[1,2]`, `not json`, ``} {
		_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
			Token: "c", Type: string(ConnectorTypeMQTT), Config: cfg,
		})
		require.ErrorIs(t, err, ErrInvalidConfig, "config %q should be rejected", cfg)
	}

	big := `{"x":"` + strings.Repeat("a", maxConfigBytes) + `"}`
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: big,
	})
	require.ErrorIs(t, err, ErrConfigTooLarge)
}

// TestDuplicateTokenRejected verifies the per-tenant token unique index (ADR-042 P1)
// the migration creates: a second live connector with the same token in one tenant
// fails. Without the index, by-token ops would act on an arbitrary duplicate.
func TestDuplicateTokenRejected(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "dup", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	_, err = api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "dup", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.Error(t, err, "a duplicate token in the same tenant must be rejected")
}

// TestTokenReusableAfterDelete verifies the hard delete frees the token immediately —
// the partial unique index must exclude deleted rows, or a re-create would collide.
func TestTokenReusableAfterDelete(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "reuse", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	_, err = api.DeleteConnector(ctx, "reuse")
	require.NoError(t, err)
	_, err = api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "reuse", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err, "the token should be reusable after a hard delete")
}

// TestConnectorSecretRoundTrip covers the write-only credential lifecycle: sealed on
// create, preserved on omit, replaced on send, cleared on empty, removed on delete.
func TestConnectorSecretRoundTrip(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	// Create with a credential -> sealed + resolvable.
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig, Secret: strp("s3cret"),
	})
	require.NoError(t, err)
	assert.Equal(t, "s3cret", secretValue(t, api, ctx, "c"))

	// An update that never mentions the secret PRESERVES it (write-only; the caller
	// cannot read it back to resend it).
	_, err = api.UpdateConnector(ctx, "c", &ConnectorUpdateRequest{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", secretValue(t, api, ctx, "c"))

	// A value ROTATES it.
	_, err = api.UpdateConnector(ctx, "c", &ConnectorUpdateRequest{
		Secret: dcgraphql.OptionalStringOf("rotated"),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "rotated", secretValue(t, api, ctx, "c"))

	// 🔴 AN EXPLICIT NULL CLEARS IT, and that spelling is NEW. Under the old pointer the
	// clear was the empty STRING, because a pointer had no third state to give it; null
	// now means what it means on every other field on the platform.
	_, err = api.UpdateConnector(ctx, "c", &ConnectorUpdateRequest{
		Secret: dcgraphql.ClearedString(),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", secretValue(t, api, ctx, "c"))

	// …and the empty string still clears, so a caller who spells it the old way is not
	// silently left with a live credential. Re-seal first, or this asserts nothing.
	_, err = api.UpdateConnector(ctx, "c", &ConnectorUpdateRequest{
		Secret: dcgraphql.OptionalStringOf("re-sealed"),
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "re-sealed", secretValue(t, api, ctx, "c"),
		"precondition: the empty-string clear below proves nothing against an absent secret")
	_, err = api.UpdateConnector(ctx, "c", &ConnectorUpdateRequest{
		Secret: dcgraphql.OptionalStringOf(""),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", secretValue(t, api, ctx, "c"))
}

// TestSecretSurvivesTokenRename verifies the credential is keyed by immutable id, so a
// rename keeps it bound (no orphaning).
//
// 🔴 IT IS RE-POINTED AT renameConnector RATHER THAN DELETED. The rename used to live
// in updateConnector's payload token, and this test is the only evidence that moving it
// to its own mutation did not orphan the credential — deleting it would have removed
// the proof while leaving the claim standing.
func TestSecretSurvivesTokenRename(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "old", Type: string(ConnectorTypeMQTT), Config: mqttConfig, Secret: strp("keep"),
	})
	require.NoError(t, err)
	_, err = api.RenameConnector(ctx, "old", "new")
	require.NoError(t, err)
	assert.Equal(t, "keep", secretValue(t, api, ctx, "new"))
}

// failOnPutStore wraps a real store but forces Put to fail, to exercise the
// create-rollback-on-seal-failure path without a live store outage.
type failOnPutStore struct {
	secrets.SecretStore
}

func (s failOnPutStore) Put(ctx context.Context, ref secrets.SecretRef, value []byte) error {
	return errors.New("simulated seal failure")
}

// TestCreateRollsBackOnSecretFailure verifies that when sealing the credential fails,
// the connector row is rolled back so the create is atomic (no row without its secret,
// and the token is freed for a retry).
func TestCreateRollsBackOnSecretFailure(t *testing.T) {
	api := newTestApi(t)
	api.Secrets = failOnPutStore{api.Secrets}
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig, Secret: strp("s"),
	})
	require.Error(t, err)

	found, err := api.ConnectorsByToken(ctx, []string{"c"})
	require.NoError(t, err)
	assert.Len(t, found, 0, "the connector row must be rolled back when the seal fails")
}

// TestDeleteRemovesSecret verifies delete removes the stored credential.
func TestDeleteRemovesSecret(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	created, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig, Secret: strp("s"),
	})
	require.NoError(t, err)
	ref, err := ConnectorSecretRef(ctx, created.ID)
	require.NoError(t, err)

	_, err = api.DeleteConnector(ctx, "c")
	require.NoError(t, err)
	exists, err := api.Secrets.Exists(ctx, ref)
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestDeleteRemovesVersions verifies delete cascades the version history (there is no
// FK cascade — the api deletes the version rows in the same transaction).
func TestDeleteRemovesVersions(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	created, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, "c", nil, nil, "alice", nil)
	require.NoError(t, err)

	_, err = api.DeleteConnector(ctx, "c")
	require.NoError(t, err)

	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&ConnectorVersion{}).
		Where("connector_id = ?", created.ID).Count(&count).Error)
	assert.Equal(t, int64(0), count, "version history must be cascaded on delete")
}

// TestConnectorVersioning covers publish -> version snapshot -> rollback, and that a
// version snapshots {type, config} while rollback restores both.
func TestConnectorVersioning(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)

	v1, err := api.PublishConnector(ctx, "c", strp("v1"), nil, "alice", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), v1.Version)
	assert.Equal(t, "alice", v1.PublishedBy)

	// Edit the draft, then publish again -> version 2.
	kafkaCfg := `{"addresses":["k:9092"],"topic":"t"}`
	_, err = api.UpdateConnector(ctx, "c", connectorEdit(ConnectorTypeKafka, kafkaCfg), nil)
	require.NoError(t, err)
	v2, err := api.PublishConnector(ctx, "c", nil, nil, "bob", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), v2.Version)

	// Versions list newest-first.
	versions, err := api.ConnectorVersions(ctx, "c")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, int32(2), versions[0].Version)
	assert.Equal(t, int32(1), versions[1].Version)

	// Rollback to v1 restores the mqtt type + config into the draft.
	rolled, err := api.RollbackConnector(ctx, "c", 1)
	require.NoError(t, err)
	assert.Equal(t, "mqtt", rolled.Type)
	assert.JSONEq(t, mqttConfig, string(rolled.Config))

	// Rollback preserves history (nothing deleted).
	versions, err = api.ConnectorVersions(ctx, "c")
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}

// TestOptimisticConcurrency covers the expectedUpdatedAt precondition on update +
// publish: a stale precondition is rejected with ErrConflict; a matching one succeeds.
func TestOptimisticConcurrency(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)

	stale := "2000-01-01T00:00:00Z"
	_, err = api.UpdateConnector(ctx, "c", connectorEdit(ConnectorTypeMQTT, mqttConfig), &stale)
	require.ErrorIs(t, err, ErrConflict)
	_, err = api.PublishConnector(ctx, "c", nil, nil, "alice", &stale)
	require.ErrorIs(t, err, ErrConflict)

	// A matching precondition succeeds. Build the precondition with the SAME function the
	// GraphQL layer hands the client (core/graphql.FormatTime) rather than re-spelling a
	// layout here: the precondition compare and the read formatter are one contract, and
	// hardcoding the layout in the test is exactly what let them drift apart — the read
	// published whole seconds while the compare accepted whole seconds, so a client stale
	// by under a second passed a check that looked airtight from either side alone.
	reloaded, err := api.ConnectorsByToken(ctx, []string{"c"})
	require.NoError(t, err)
	current := *dcgraphql.FormatTime(reloaded[0].UpdatedAt)
	_, err = api.UpdateConnector(ctx, "c", connectorEdit(ConnectorTypeKafka, `{"addresses":["k:9092"],"topic":"t"}`), &current)
	require.NoError(t, err)
	found, err := api.ConnectorsByToken(ctx, []string{"c"})
	require.NoError(t, err)
	assert.Equal(t, "kafka", found[0].Type)
}

// TestOptimisticConcurrencyIsSubSecond pins the precision of the precondition itself.
//
// The guarded write re-reads updated_at, so the string comparison in UpdateConnector is
// the ONLY thing enforcing the CALLER's stated version. While both the read formatter and
// that comparison truncated to whole seconds, a client whose view was stale by less than a
// second produced a byte-identical precondition and overwrote a change it had never seen —
// a lost update that no test could see, because both sides of the contract were wrong in
// the same direction.
//
// The require.Equal below is the negative control and is load-bearing: it asserts the two
// instants really were indistinguishable under the old layout. Without it this test would
// still pass if someone made the precondition reject everything.
func TestOptimisticConcurrencyIsSubSecond(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	reloaded, err := api.ConnectorsByToken(ctx, []string{"c"})
	require.NoError(t, err)
	actual := reloaded[0].UpdatedAt

	// A DIFFERENT instant inside the same wall-clock second as the real updated_at.
	stale := actual.Truncate(time.Second).Add(250 * time.Millisecond)
	if stale.Equal(actual) {
		stale = actual.Truncate(time.Second).Add(750 * time.Millisecond)
	}
	require.Equal(t, actual.Format(time.RFC3339), stale.Format(time.RFC3339),
		"negative control: under the old whole-second layout these must be the same string, "+
			"otherwise this test is not exercising the lost-update window at all")

	_, err = api.UpdateConnector(ctx, "c", connectorEdit(ConnectorTypeKafka, `{"addresses":["k:9092"],"topic":"t"}`), dcgraphql.FormatTime(stale))
	require.ErrorIs(t, err, ErrConflict,
		"a precondition stale by less than a second must be refused")

	// And the real value still succeeds, so the check rejects staleness rather than everything.
	_, err = api.UpdateConnector(ctx, "c", connectorEdit(ConnectorTypeKafka, `{"addresses":["k:9092"],"topic":"t"}`), dcgraphql.FormatTime(actual))
	require.NoError(t, err)
}

// TestLatestPublishedConnector verifies the dispatch-side read returns the MAX published
// version (not the first/last inserted) and distinguishes not-found from not-published.
func TestLatestPublishedConnector(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	// Nonexistent connector.
	_, err := api.LatestPublishedConnector(ctx, "ghost")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Draft-only connector: exists but never published.
	_, err = api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "c", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	_, err = api.LatestPublishedConnector(ctx, "c")
	require.ErrorIs(t, err, ErrNotPublished)

	// Publish v1 (mqtt), edit to kafka, publish v2 → latest must be v2 (kafka).
	_, err = api.PublishConnector(ctx, "c", nil, nil, "alice", nil)
	require.NoError(t, err)
	_, err = api.UpdateConnector(ctx, "c", connectorEdit(ConnectorTypeKafka, `{"addresses":["k:9092"],"topic":"t"}`), nil)
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, "c", nil, nil, "bob", nil)
	require.NoError(t, err)

	latest, err := api.LatestPublishedConnector(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, int32(2), latest.Version)
	assert.Equal(t, "kafka", latest.Type)
}

// TestTenantIsolation confirms a connector in one tenant is invisible to another —
// draft lookup, version listing, and rollback all stay tenant-confined.
func TestTenantIsolation(t *testing.T) {
	api := newTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	globex := core.WithTenant(context.Background(), "globex")

	_, err := api.CreateConnector(acme, &ConnectorCreateRequest{
		Token: "shared", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	_, err = api.PublishConnector(acme, "shared", nil, nil, "alice", nil)
	require.NoError(t, err)

	found, err := api.ConnectorsByToken(globex, []string{"shared"})
	require.NoError(t, err)
	assert.Len(t, found, 0)

	// Versions + rollback are keyed off a tenant-scoped parent lookup, so the other
	// tenant sees the connector as nonexistent.
	_, err = api.ConnectorVersions(globex, "shared")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.RollbackConnector(globex, "shared", 1)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
