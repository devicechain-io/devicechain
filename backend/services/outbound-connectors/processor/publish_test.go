// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/devicechain-io/dc-outbound-connectors/publish"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var publishTestRootKey = []byte("0123456789abcdef0123456789abcdef")

// newPublishTestExecutor builds an executor over a real connector store (sqlite) + secret
// store, with an injected fake sink that records what would be sent. Returns the executor,
// the api (to seed connectors), and a pointer to the last captured send.
func newPublishTestExecutor(t *testing.T) (*Executor, *model.Api, *capturedSend) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&model.Connector{}, &model.ConnectorVersion{}))
	require.NoError(t, rdb.CreateTenantTokenIndex(db, &model.Connector{}))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider(publishTestRootKey)
	require.NoError(t, err)
	store := secrets.NewStore(db, kek)
	api := model.NewApi(&rdb.RdbManager{Database: db}, store)

	cap := &capturedSend{}
	e := NewExecutor(NewSecretResolver(store), api, 10*time.Second)
	e.send = cap.fn
	return e, api, cap
}

type capturedSend struct {
	called     bool
	outputYAML string
	payload    string
	meta       map[string]string
	err        error // when set, the fake send returns it (a transient sink failure)
}

func (c *capturedSend) fn(_ context.Context, outputYAML string, payload []byte, meta map[string]string) error {
	c.called = true
	c.outputYAML = outputYAML
	c.payload = string(payload)
	c.meta = meta
	return c.err
}

func publishReq(ref string) *connectorwire.ConnectorDispatchRequest {
	return &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindPublish, Tenant: "acme", IdempotencyKey: "idem-1",
		Payload: `{"temp":72}`, Publish: &connectorwire.PublishDispatch{ConnectorRef: ref},
	}
}

// seedMQTT creates + publishes an mqtt connector (optionally with a credential) and
// returns its token.
func seedMQTT(t *testing.T, api *model.Api, ctx context.Context, token, secret string) {
	t.Helper()
	var sec *string
	if secret != "" {
		sec = &secret
	}
	_, err := api.CreateConnector(ctx, &model.ConnectorCreateRequest{
		Token: token, Type: "mqtt", Config: `{"urls":["tcp://b:1883"],"topic":"alerts","username":"u"}`, Secret: sec,
	})
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, token, nil, nil, "alice", nil)
	require.NoError(t, err)
}

// TestExecutePublishSuccess resolves the published connector, injects the credential as
// the password, and sends the rendered payload.
func TestExecutePublishSuccess(t *testing.T) {
	e, api, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedMQTT(t, api, ctx, "pager", "p4ss")

	res := e.Execute(ctx, publishReq("pager"))
	require.NoError(t, res.err)
	assert.Equal(t, outcomeSent, res.outcome)
	require.True(t, cap.called)
	assert.Equal(t, `{"temp":72}`, cap.payload)
	assert.Equal(t, "idem-1", cap.meta["idempotency_key"])

	// The generated output carries the mqtt mapping + the resolved secret as the password.
	var parsed map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(cap.outputYAML), &parsed))
	assert.Equal(t, "alerts", parsed["mqtt"]["topic"])
	assert.Equal(t, "u", parsed["mqtt"]["user"])
	assert.Equal(t, "p4ss", parsed["mqtt"]["password"])
}

// TestExecutePublishAnonymous sends with no password when the connector has no credential.
func TestExecutePublishAnonymous(t *testing.T) {
	e, api, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedMQTT(t, api, ctx, "anon", "")

	res := e.Execute(ctx, publishReq("anon"))
	require.NoError(t, res.err)
	assert.Equal(t, outcomeSent, res.outcome)
	var parsed map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(cap.outputYAML), &parsed))
	_, hasPass := parsed["mqtt"]["password"]
	assert.False(t, hasPass, "an anonymous connector must send no password")
}

// TestExecutePublishConnectorNotFound is terminal (a dangling ConnectorRef).
func TestExecutePublishConnectorNotFound(t *testing.T) {
	e, _, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	res := e.Execute(ctx, publishReq("ghost"))
	assert.False(t, res.retryable)
	assert.Equal(t, outcomeInvalid, res.outcome)
	assert.False(t, cap.called)
}

// TestExecutePublishNotPublished is terminal (a draft-only connector).
func TestExecutePublishNotPublished(t *testing.T) {
	e, api, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &model.ConnectorCreateRequest{
		Token: "draft", Type: "mqtt", Config: `{"urls":["tcp://b:1883"],"topic":"t"}`,
	})
	require.NoError(t, err)
	res := e.Execute(ctx, publishReq("draft"))
	assert.False(t, res.retryable)
	assert.Equal(t, outcomeInvalid, res.outcome)
	assert.False(t, cap.called)
}

// TestExecutePublishUnsupportedType is terminal: a connector whose type has no generator
// (a future vocabulary member) is recognized but dead-lettered, never silently dropped.
func TestExecutePublishUnsupportedType(t *testing.T) {
	e, api, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	// Create a kafka connector directly (bypassing the write-time vocabulary — model allows the
	// 5-type vocab; only mqtt has a generator in C4b), publish it, and dispatch.
	_, err := api.CreateConnector(ctx, &model.ConnectorCreateRequest{
		Token: "k", Type: "kafka", Config: `{"brokers":["b:9092"],"topic":"t"}`,
	})
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, "k", nil, nil, "alice", nil)
	require.NoError(t, err)

	res := e.Execute(ctx, publishReq("k"))
	assert.False(t, res.retryable)
	assert.Equal(t, outcomeUnsupported, res.outcome)
	assert.False(t, cap.called)
}

// TestExecutePublishSendFailureRetryable classifies a sink failure as transient.
func TestExecutePublishSendFailureRetryable(t *testing.T) {
	e, api, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedMQTT(t, api, ctx, "pager", "p4ss")
	cap.err = errors.New("broker unreachable")

	res := e.Execute(ctx, publishReq("pager"))
	assert.True(t, res.retryable)
	assert.Equal(t, outcomeRetry, res.outcome)
	assert.True(t, cap.called)
}

// TestExecutePublishNoStoreUnsupported: an executor with no connector store treats publish
// as terminal unsupported (httpCall-only deployment).
func TestExecutePublishNoStoreUnsupported(t *testing.T) {
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, 10*time.Second)
	res := e.Execute(context.Background(), publishReq("x"))
	assert.False(t, res.retryable)
	assert.Equal(t, outcomeUnsupported, res.outcome)
}

// TestExecutePublishConfigErrorTerminal: a terminal config/stream error from the sink
// (publish.ErrPublishConfig — a config that generated but Bento cannot run) is dead-lettered
// as invalid, not retried.
func TestExecutePublishConfigErrorTerminal(t *testing.T) {
	e, api, cap := newPublishTestExecutor(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedMQTT(t, api, ctx, "pager", "p4ss")
	cap.err = fmt.Errorf("wrapped: %w", publish.ErrPublishConfig)

	res := e.Execute(ctx, publishReq("pager"))
	assert.False(t, res.retryable)
	assert.Equal(t, outcomeInvalid, res.outcome)
	assert.True(t, cap.called)
}
