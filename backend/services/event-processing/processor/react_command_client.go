// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// createCommandMutation enqueues a command in command-delivery (ADR-043). The token is a
// deterministic per-(detection, action) idempotency key, so a repeat with the same token is a no-op
// that returns the original command (command-delivery is idempotent on the token, ADR-051 slice
// 5b-1) — which is what lets the REACT dispatcher retry at-least-once without double-enqueuing.
const createCommandMutation = `mutation($request: CommandCreateRequest!) {
  createCommand(request: $request) { token }
}`

// commandClient is the REACT dispatcher's send-command sink (ADR-051 slice 5b): it calls
// command-delivery's createCommand mutation over the ADR-044 service-token client. It is
// dependency-inverted behind react.CommandSink so the dispatcher never depends on the transport.
type commandClient struct {
	client *svcclient.Client
	url    string
}

// NewCommandClient builds a send-command sink over a service-token client and command-delivery's
// GraphQL URL (the REACT dispatcher's command seam, wired in main). It returns the dispatcher's
// sink interface so the concrete adapter stays package-private.
func NewCommandClient(client *svcclient.Client, url string) react.CommandSink {
	return &commandClient{client: client, url: url}
}

// Send enqueues one command. It returns nil on success (a fresh enqueue OR command-delivery's
// idempotent replay of an already-enqueued token) and a non-nil error on ANY failure — a transport
// blip, a non-200, or a GraphQL rejection alike. The dispatcher retries every error (the event
// stays unacked) and the consumer's redelivery cap bounds a permanently-failing event, so the sink
// does not classify: a device-deleted rejection and a command-delivery outage are both retried, the
// former until the poison cap gives up, the latter until it recovers. The deterministic token makes
// every such retry safe.
func (c *commandClient) Send(ctx context.Context, req react.CommandRequest) error {
	// The command's Name carries the rule's command key (ADR-043); command-delivery matches the
	// device's handler on it. Payload is the frozen JSON-object argument set (empty ⇒ omitted).
	request := map[string]any{
		"token":       req.Token,
		"deviceToken": req.DeviceToken,
		"name":        req.Command,
	}
	if req.Payload != "" {
		request["payload"] = req.Payload
	}
	vars := map[string]any{"request": request}
	if err := c.client.Query(ctx, c.url, req.Tenant, createCommandMutation, vars, nil); err != nil {
		return fmt.Errorf("react: enqueue command %q for device %q: %w", req.Command, req.DeviceToken, err)
	}
	return nil
}

// compile-time assertion that the client satisfies the dispatcher's sink contract.
var _ react.CommandSink = (*commandClient)(nil)
