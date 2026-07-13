// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// captureConnectorWriter records the tenant the sink scoped the write to and the marshaled payload.
type captureConnectorWriter struct {
	tenant  string
	payload []byte
	err     error
}

func (w *captureConnectorWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if w.err != nil {
		return w.err
	}
	w.tenant, _ = dccore.TenantFromContext(ctx)
	if len(msgs) > 0 {
		w.payload = msgs[0].Value
	}
	return nil
}
func (w *captureConnectorWriter) HandleResponse(error) {}

func connReq(a rules.Action) react.ConnectorRequest {
	return react.ConnectorRequest{
		Tenant:       "acme",
		DeviceToken:  "device-1",
		RuleID:       "acme/p@1/r1",
		Edge:         "raised",
		OccurredTime: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
		Token:        "abc123",
		Payload:      `{"hello":"world"}`,
		Action:       a,
	}
}

// TestWireConnectorKindMapping pins the internal action-type→wire-kind translation (ADR-060): the sink
// must map onto the SHARED wire constants, and reject any non-connector type fail-closed rather than
// misroute it. Asserting the mapping here makes a future spelling divergence a test failure.
func TestWireConnectorKindMapping(t *testing.T) {
	if k, ok := wireConnectorKind(rules.ActionHTTPCall); !ok || k != connectorwire.ConnectorKindHTTPCall {
		t.Fatalf("httpCall must map to the shared wire constant; got %q ok=%v", k, ok)
	}
	if k, ok := wireConnectorKind(rules.ActionPublish); !ok || k != connectorwire.ConnectorKindPublish {
		t.Fatalf("publish must map to the shared wire constant; got %q ok=%v", k, ok)
	}
	if _, ok := wireConnectorKind(rules.ActionSendCommand); ok {
		t.Fatal("a non-connector action type must not map (fail closed)")
	}
	// The wire constants must equal the rules action-type strings so the two vocabularies cannot drift.
	if connectorwire.ConnectorKindHTTPCall != string(rules.ActionHTTPCall) || connectorwire.ConnectorKindPublish != string(rules.ActionPublish) {
		t.Fatal("wire kind constants must equal the rules action-type strings")
	}
}

// TestConnectorClientDispatchHTTPCall proves the sink flattens an httpCall action onto the wire,
// scopes the write to the request's tenant, and carries the rendered payload + idempotency token.
func TestConnectorClientDispatchHTTPCall(t *testing.T) {
	w := &captureConnectorWriter{}
	c := NewConnectorClient(w)
	a := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{
		URL: "https://hooks.example/x", Headers: map[string]string{"X-Env": "prod"},
		SecretRef: "httpcall/acme/auth", TimeoutMs: 5000,
	}}
	if err := c.Dispatch(context.Background(), connReq(a)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if w.tenant != "acme" {
		t.Fatalf("write must be scoped to the request tenant, got %q", w.tenant)
	}
	got, err := connectorwire.UnmarshalConnectorDispatchRequest(w.payload)
	if err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if got.Kind != connectorwire.ConnectorKindHTTPCall || got.HTTPCall == nil || got.Publish != nil {
		t.Fatalf("wire discriminator/variant wrong: %+v", got)
	}
	if got.IdempotencyKey != "abc123" || got.Payload != `{"hello":"world"}` || got.RuleID != "acme/p@1/r1" {
		t.Fatalf("wire context wrong: %+v", got)
	}
	if got.HTTPCall.URL != "https://hooks.example/x" || got.HTTPCall.SecretRef != "httpcall/acme/auth" ||
		got.HTTPCall.Headers["X-Env"] != "prod" || got.HTTPCall.TimeoutMs != 5000 {
		t.Fatalf("httpCall config not carried: %+v", got.HTTPCall)
	}
}

// TestConnectorClientDispatchPublish proves the sink flattens a publish action's connectorRef onto the
// wire under the publish discriminator.
func TestConnectorClientDispatchPublish(t *testing.T) {
	w := &captureConnectorWriter{}
	c := NewConnectorClient(w)
	a := rules.Action{Type: rules.ActionPublish, Publish: &rules.PublishAction{ConnectorRef: "kafka-main", TimeoutMs: 2000}}
	if err := c.Dispatch(context.Background(), connReq(a)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, err := connectorwire.UnmarshalConnectorDispatchRequest(w.payload)
	if err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if got.Kind != connectorwire.ConnectorKindPublish || got.Publish == nil || got.HTTPCall != nil {
		t.Fatalf("wire discriminator/variant wrong: %+v", got)
	}
	if got.Publish.ConnectorRef != "kafka-main" || got.Publish.TimeoutMs != 2000 {
		t.Fatalf("publish config not carried: %+v", got.Publish)
	}
}

// TestConnectorClientRejectsNonConnectorAction proves a non-connector action type is rejected
// fail-closed (unreachable via the dispatcher, but never marshaled as a malformed request).
func TestConnectorClientRejectsNonConnectorAction(t *testing.T) {
	w := &captureConnectorWriter{}
	c := NewConnectorClient(w)
	a := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "x"}}
	if err := c.Dispatch(context.Background(), connReq(a)); err == nil {
		t.Fatal("a non-connector action must be rejected")
	}
	if w.payload != nil {
		t.Fatal("a rejected action must not be published")
	}
}

// TestConnectorClientPropagatesWriteError proves a broker-write failure surfaces so the dispatcher
// retries (the event redelivers, dedup makes it safe).
func TestConnectorClientPropagatesWriteError(t *testing.T) {
	w := &captureConnectorWriter{err: errors.New("broker down")}
	c := NewConnectorClient(w)
	a := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y"}}
	if err := c.Dispatch(context.Background(), connReq(a)); err == nil {
		t.Fatal("a write failure must be returned so the dispatcher retries")
	}
}
