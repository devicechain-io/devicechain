// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// createCommandMutation enqueues a command in command-delivery (ADR-043). The token is a
// deterministic per-(detection, action) idempotency key, so a repeat with the same token is a no-op
// that returns the original command (command-delivery is idempotent on the token, ADR-051 slice
// 5b-1) — which is what lets the REACT dispatcher retry at-least-once without double-enqueuing.
//
// It selects BOTH arms of the result. createCommand answers with either the command or a typed
// rejection, and a mutation that asked only for the command would receive a successful response
// with a null command and no way to say why — which is precisely the blindness this replaced.
const createCommandMutation = `mutation($request: CommandCreateRequest!) {
  createCommand(request: $request) {
    command { token }
    rejection { code reason }
  }
}`

// permanentRejectionCodes are the createCommand rejection codes a REACT retry can NEVER turn into
// a success. Every one of them says the request itself is wrong, and the dispatcher re-derives a
// byte-identical request on every redelivery, so the same verdict is guaranteed.
//
// 🔴 IT IS AN OPT-IN LIST, AND THE DEFAULT IS RETRY. A code missing from here — one command-delivery
// added later, or the unclassified COMMAND_REJECTED — is retried exactly as everything was before,
// so falling behind the owner's vocabulary costs nothing worse than today's behaviour. The reverse
// default would DROP an actuation on any code this build did not recognize, and a dropped command
// has no second chance and no record beyond a log line.
//
// HELD_CEILING_EXCEEDED is deliberately ABSENT. It is the one rejection that is temporary: it says
// the tenant is at its limit of commands withheld for absent devices RIGHT NOW, and that limit
// frees as the fleet comes back. Retrying it is the correct behaviour, and treating it as permanent
// would drop exactly the commands an offline fleet is waiting for.
// Only half of this map is compiler-checked, and the split is not a style choice. The
// device-management codes are typed constants in a module this service already depends on, so a
// RENAME there breaks the build here — which matters, because the opt-in default above protects
// against a code being MISSING, not against one silently changing value. The command-delivery codes
// have no such anchor: event-processing does not depend on dc-command-delivery (and must not, to
// enqueue over its API rather than its package), so they stay string literals and a rename there
// would quietly demote those rejections back to retry-to-poison-cap.
var permanentRejectionCodes = map[string]bool{
	// Answered by device-management's enqueue gate, the owner of the device and its vocabulary.
	string(dmmodel.RejectDeviceNotFound):         true,
	string(dmmodel.RejectCommandNotInVocabulary): true,
	string(dmmodel.RejectPayloadSchemaViolation): true,
	// Answered by command-delivery itself, on the request as written.
	"PAYLOAD_NOT_JSON":   true,
	"METADATA_NOT_JSON":  true,
	"EXPIRES_AT_INVALID": true,
}

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
// idempotent replay of an already-enqueued token) and a non-nil error otherwise.
//
// It classifies exactly one thing: whether command-delivery REJECTED the request or FAILED TO
// ANSWER it. A rejection arrives as a typed payload carrying a stable code (the request was
// decided and found invalid); everything else — a transport blip, a non-200, a GraphQL error —
// arrives as an error from the client and is returned as one.
//
// A rejection whose code is in permanentRejectionCodes is returned as a *react.PermanentRejection,
// which the dispatcher DROPS instead of retrying. Everything else — a temporary rejection
// (the tenant's held-command ceiling), a code this build does not recognize, and every GraphQL
// error — is returned as a plain error and keeps the previous retry-everything behaviour: the
// event stays unacked, the deterministic token makes the retry safe, and the consumer's redelivery
// cap bounds it.
//
// This is what the sink used to lump together, and the lumping was live-visible: a command for a
// DELETED device was retried until the poison cap gave up, spending the whole redelivery budget on
// a question command-delivery had already answered correctly, and looking from the outside exactly
// like command-delivery being down.
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
	var out struct {
		CreateCommand struct {
			Command *struct {
				Token string `json:"token"`
			} `json:"command"`
			Rejection *struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"rejection"`
		} `json:"createCommand"`
	}
	if err := c.client.Query(ctx, c.url, req.Tenant, createCommandMutation, vars, &out); err != nil {
		return fmt.Errorf("react: enqueue command %q for device %q: %w", req.Command, req.DeviceToken, err)
	}
	if r := out.CreateCommand.Rejection; r != nil {
		if permanentRejectionCodes[r.Code] {
			return &react.PermanentRejection{Code: r.Code, Reason: r.Reason}
		}
		// A rejection that may yet succeed (the tenant's held-command ceiling) or one this build
		// cannot classify: a plain error, so the event redelivers as it always did.
		return fmt.Errorf("react: enqueue command %q for device %q rejected (%s): %s",
			req.Command, req.DeviceToken, r.Code, r.Reason)
	}
	if out.CreateCommand.Command == nil {
		// Neither arm answered. The schema says exactly one is non-null, so this is a broken or
		// intercepted response rather than a verdict — retryable, and emphatically NOT a silent
		// success: treating it as one would ack an event whose command was never enqueued.
		return fmt.Errorf("react: enqueue command %q for device %q: command-delivery returned neither "+
			"a command nor a rejection", req.Command, req.DeviceToken)
	}
	return nil
}

// compile-time assertion that the client satisfies the dispatcher's sink contract.
var _ react.CommandSink = (*commandClient)(nil)
