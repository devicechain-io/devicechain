// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
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
	string(dmcontract.RejectDeviceNotFound):         true,
	string(dmcontract.RejectCommandNotInVocabulary): true,
	string(dmcontract.RejectPayloadSchemaViolation): true,
	// A disabled device accepts no command while it stays disabled, and a rule firing at a
	// quarantined device would otherwise spend its whole retry budget on each detection.
	string(dmcontract.RejectDeviceDisabled): true,
	// The device type's next maintenance window opens only after the command's expiresAt. A REACT
	// command carries an expiresAt only when its rule sets expiresAfter, and the author's bound
	// is measured from the enqueue, so a later retry could only enqueue a command past the wait
	// the rule allowed. Dropping it is the honest verdict; retrying spends the redelivery budget
	// and then poison-drops it as if command-delivery were down.
	string(dmcontract.RejectWindowAfterExpiry): true,
	// A raw Factory Reset is refused identically every time; the rule would otherwise retry it.
	string(dmcontract.RejectCommandDestructiveRefused): true,
	// Answered by command-delivery itself, on the request as written.
	"PAYLOAD_NOT_JSON":   true,
	"METADATA_NOT_JSON":  true,
	"EXPIRES_AT_INVALID": true,
	// The deterministic command token already names a row this caller does not own. The token is a
	// function of the detection and action, so a retry re-sends the same colliding token and can
	// only collide again.
	"TOKEN_IN_USE": true,
	// The request exceeds a size cap command-delivery enforces on the request as written (a rule
	// may carry a larger payload than a command may). The same rule re-sends the same oversized
	// request on every redelivery, so retrying only spends the budget and then poison-drops it as
	// if it were an infrastructure fault.
	"PAYLOAD_TOO_LARGE":     true,
	"METADATA_TOO_LARGE":    true,
	"COMMAND_NAME_TOO_LONG": true,
}

// commandClient is the REACT dispatcher's send-command sink (ADR-051 slice 5b): it calls
// command-delivery's createCommand mutation over the ADR-044 service-token client. It is
// dependency-inverted behind react.CommandSink so the dispatcher never depends on the transport.
type commandClient struct {
	client *svcclient.Client
	url    string
	// onCauseOmitted is told the reason each time a command is enqueued WITHOUT its cause.
	onCauseOmitted func(reason string)
}

// NewCommandClient builds a send-command sink over a service-token client and command-delivery's
// GraphQL URL (the REACT dispatcher's command seam, wired in main). It returns the dispatcher's
// sink interface so the concrete adapter stays package-private. onCauseOmitted (may be nil) is
// told the reason each time a command has to be enqueued without its cause.
func NewCommandClient(client *svcclient.Client, url string, onCauseOmitted func(reason string)) react.CommandSink {
	return &commandClient{client: client, url: url, onCauseOmitted: onCauseOmitted}
}

// The reasons a command was enqueued without its cause (a metric label, so a fixed, small set).
const (
	// CauseOmittedPeerLacksCause: command-delivery runs a build from before the field.
	CauseOmittedPeerLacksCause = "peer_lacks_cause"
	// CauseOmittedCauseInvalid: command-delivery refused the cause itself.
	CauseOmittedCauseInvalid = "cause_invalid"
)

// rejectCauseInvalid is command-delivery's code for a cause it refuses. A string literal for the
// reason permanentRejectionCodes documents: this service does not import command-delivery.
const rejectCauseInvalid = "CAUSE_INVALID"

// errCauseInvalid marks command-delivery's CAUSE_INVALID rejection inside this client.
var errCauseInvalid = errors.New("command-delivery refused the command's cause")

// causeInput is the CommandCauseInput a cause is sent as. Absent optional fields are not sent.
func causeInput(cause *react.CommandCause) map[string]any {
	in := map[string]any{
		"kind":            cause.Kind,
		"ruleId":          cause.RuleID,
		"detectionKind":   cause.DetectionKind,
		"detectedAt":      cause.DetectedAt.UTC().Format(time.RFC3339Nano),
		"positionDerived": cause.PositionDerived,
	}
	if cause.Value != nil {
		in["value"] = *cause.Value
	}
	if cause.AlarmKey != "" {
		in["alarmKey"] = cause.AlarmKey
	}
	return in
}

// peerLacksCommandCause reports whether err is command-delivery refusing the request because its
// schema does not declare CommandCreateRequest.cause: a build from before the field, which a
// rolling upgrade runs briefly. graphql-go validates variables before it executes anything, so such
// a refusal created no command. Each line of each message is compared whole against the exact text
// graphql-go writes (VariablesOfCorrectType), so another error's text, a suggestion or a transport
// error never reads as version skew.
func peerLacksCommandCause(err error) bool {
	var gqlErr *svcclient.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return false
	}
	const want = `Field "cause" is not defined by type "CommandCreateRequest".`
	for _, message := range gqlErr.Messages {
		for _, line := range strings.Split(message, "\n") {
			if line == want {
				return true
			}
		}
	}
	return false
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
	if !req.ExpiresAt.IsZero() {
		request["expiresAt"] = req.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if req.Cause == nil {
		return c.create(ctx, req, request)
	}
	request["cause"] = causeInput(req.Cause)
	err := c.create(ctx, req, request)
	var reason string
	switch {
	case err == nil:
		return nil
	case peerLacksCommandCause(err):
		reason = CauseOmittedPeerLacksCause
	case errors.Is(err, errCauseInvalid):
		reason = CauseOmittedCauseInvalid
	default:
		return err
	}
	// The cause could not be recorded. Either command-delivery runs a build from before the field
	// (a rolling upgrade; its validation refused the whole request, so nothing was created), or it
	// refused this cause, which it would do again on every redelivery since the cause is a function
	// of the detection. Neither is a reason to withhold the actuation, so the request is sent once
	// more WITHOUT the cause, under the same token, and the omission is counted.
	if c.onCauseOmitted != nil {
		c.onCauseOmitted(reason)
	}
	delete(request, "cause")
	return c.create(ctx, req, request)
}

// create sends one createCommand request and classifies the answer (see Send).
func (c *commandClient) create(ctx context.Context, req react.CommandRequest, request map[string]any) error {
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
		if r.Code == rejectCauseInvalid {
			// Send resends without the cause. Wrapped so a request that carried no cause, and so
			// cannot have been refused for one, still surfaces as an ordinary retryable error.
			return fmt.Errorf("react: enqueue command %q for device %q: %w: %s",
				req.Command, req.DeviceToken, errCauseInvalid, r.Reason)
		}
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
