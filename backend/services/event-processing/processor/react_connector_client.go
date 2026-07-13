// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// connectorClient is the REACT dispatcher's outbound-connector sink (ADR-060 §4): it publishes a
// rendered connector-dispatch request onto the per-tenant connector-dispatch subject, which the
// dedicated outbound-connectors service (slice C3) consumes and executes (the HTTP call / broker
// publish, secret resolution, egress rate-limiting). It is dependency-inverted behind
// react.ConnectorSink so the dispatcher never depends on the transport, and it deliberately does NOT
// execute the action here — REACT's job ends at a durable publish, keeping the heavy connector
// dep-tree and any credential handling out of this replay-correct binary.
//
// It carries the deterministic idempotency token (react.ConnectorRequest.Token) on the wire so the
// consumer dedups an at-least-once redelivery; this sink itself is a thin marshal-and-publish, mirroring
// alarmClient.
type connectorClient struct {
	writer messaging.MessageWriter
}

// NewConnectorClient builds an outbound-connector sink over a tenant-scoped writer on the
// connector-dispatch subject.
func NewConnectorClient(writer messaging.MessageWriter) react.ConnectorSink {
	return &connectorClient{writer: writer}
}

// wireConnectorKind maps the dispatcher's internal action type onto the SHARED wire vocabulary
// (connectorwire.ConnectorKind*), so the outbound-connectors wire binds only to connectorwire's constants on BOTH
// ends. Making the translation explicit (rather than passing rules.ActionType through as a string)
// means a change to either module's spelling is a compile-visible mapping edit, not a silent misroute.
// It returns ok=false for any non-connector action — unreachable, since react.dispatchAction routes
// only httpCall/publish here, but fail-closed so a future action kind cannot leak through unmapped.
func wireConnectorKind(t rules.ActionType) (string, bool) {
	switch t {
	case rules.ActionHTTPCall:
		return connectorwire.ConnectorKindHTTPCall, true
	case rules.ActionPublish:
		return connectorwire.ConnectorKindPublish, true
	default:
		return "", false
	}
}

// Dispatch flattens the resolved action onto the wire and publishes one connector-dispatch request on
// its tenant's subject. The writer derives the subject from the tenant in context (fail-closed on
// none), so the request lands on exactly "{instance}.{tenant}.connector-dispatch". A marshal or write
// failure is returned so the dispatcher retries (the event redelivers; the idempotency token makes the
// re-run safe). An unmappable/malformed action is a programming error (the dispatcher only routes
// httpCall/publish here); it is rejected fail-closed rather than published as a malformed request.
func (c *connectorClient) Dispatch(ctx context.Context, req react.ConnectorRequest) error {
	kind, ok := wireConnectorKind(req.Action.Type)
	if !ok {
		return fmt.Errorf("react: connector dispatch for rule %q has non-connector action type %q", req.RuleID, req.Action.Type)
	}
	out := &connectorwire.ConnectorDispatchRequest{
		Kind:           kind,
		Tenant:         req.Tenant,
		DeviceToken:    req.DeviceToken,
		RuleID:         req.RuleID,
		Edge:           req.Edge,
		OccurredTime:   req.OccurredTime,
		IdempotencyKey: req.Token,
		Payload:        req.Payload,
	}
	switch req.Action.Type {
	case rules.ActionHTTPCall:
		h := req.Action.HTTPCall
		if h == nil {
			return fmt.Errorf("react: httpCall dispatch for rule %q has no httpCall config", req.RuleID)
		}
		out.HTTPCall = &connectorwire.HTTPCallDispatch{
			URL:       h.URL,
			Method:    h.Method,
			Headers:   h.Headers,
			SecretRef: h.SecretRef,
			TimeoutMs: h.TimeoutMs,
		}
	case rules.ActionPublish:
		p := req.Action.Publish
		if p == nil {
			return fmt.Errorf("react: publish dispatch for rule %q has no publish config", req.RuleID)
		}
		out.Publish = &connectorwire.PublishDispatch{
			ConnectorRef: p.ConnectorRef,
			TimeoutMs:    p.TimeoutMs,
		}
	}
	payload, err := connectorwire.MarshalConnectorDispatchRequest(out)
	if err != nil {
		return fmt.Errorf("react: marshal connector dispatch for rule %q: %w", req.RuleID, err)
	}
	tctx := dccore.WithTenant(ctx, req.Tenant)
	if err := c.writer.WriteMessages(tctx, messaging.Message{Value: payload}); err != nil {
		return fmt.Errorf("react: publish connector dispatch for rule %q: %w", req.RuleID, err)
	}
	return nil
}
