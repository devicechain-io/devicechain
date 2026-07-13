// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/httpsink"
)

// secretResolveTimeout bounds the credential resolve so a hung secret store cannot pin a dispatch
// past the consumer AckWait (see the wait+resolve+send budget in config.MaxEgressWaitBudgetMs).
// Generous, since a resolve is normally a cached/fast envelope-decrypt.
const secretResolveTimeout = 5 * time.Second

// execOutcome is the executor's classification of one dispatch, driving the consumer's
// ack/nak/dead-letter decision and the bounded metric. err is nil only for a successful send.
type execOutcome struct {
	// outcome is one of the outcome* metric enum values.
	outcome string
	// retryable is true only for a TRANSIENT failure worth redelivering (a send error / non-2xx, or
	// a secret-resolve error that may be a DB blip). A terminal failure (unsupported kind, a malformed
	// stored config) is not retryable — the consumer dead-letters it rather than churning the cap.
	retryable bool
	// err carries the failure for logging (never a secret); nil on success.
	err error
}

// Executor performs the bounded outbound send for one connector-dispatch request (ADR-060 §4). It
// owns the credential resolution (fail-closed) and the shared SSRF hardening (core/httpsink); the
// consumer owns the ack/nak/dead-letter lifecycle around it. In this slice (C3) it executes httpCall
// only; publish (the Bento tier + Connector entity, slice C4) is recognized but returns a terminal
// unsupported outcome so a publish dispatch is dead-lettered visibly, never silently dropped.
type Executor struct {
	secrets *SecretResolver
	// client is the HTTP client for delivery; nil uses httpsink.DefaultClient. A field so a test can
	// inject a client pointed at an httptest server. The per-send context deadline bounds each call.
	client *http.Client
	// defaultTimeout bounds a send whose action specified no timeout.
	defaultTimeout time.Duration
}

// NewExecutor builds the executor over a secret resolver and the fallback send timeout.
func NewExecutor(resolver *SecretResolver, defaultTimeout time.Duration) *Executor {
	return &Executor{secrets: resolver, defaultTimeout: defaultTimeout}
}

// Execute performs the dispatch and classifies the result. The request has already passed
// connectorwire.Validate (structural), so the variant matching the kind is present; Execute re-checks
// the fields it actually uses at the point of use (defense-in-depth: a forged/corrupt stored rule
// could carry a kind-valid but semantically-invalid config that bypassed the publish gate — the
// executor is the enforcement point, ADR-060 slice-C3 guardrail).
func (e *Executor) Execute(ctx context.Context, req *connectorwire.ConnectorDispatchRequest) execOutcome {
	switch req.Kind {
	case connectorwire.ConnectorKindHTTPCall:
		return e.executeHTTPCall(ctx, req)
	case connectorwire.ConnectorKindPublish:
		// Recognized but not executable until the Bento tier + Connector entity land (slice C4). This
		// is terminal for this build (a redelivery cannot make it executable) — dead-letter it so an
		// operator sees a publish dispatched at a version this service cannot run, never silently drop.
		return execOutcome{outcome: outcomeUnsupported, retryable: false,
			err: fmt.Errorf("publish is not executable in this build (Bento tier is slice C4)")}
	default:
		// connectorwire.Validate already rejected unknown kinds; this is unreachable defense-in-depth.
		return execOutcome{outcome: outcomeInvalid, retryable: false,
			err: fmt.Errorf("unknown connector kind %q", req.Kind)}
	}
}

// executeHTTPCall issues the hardened outbound HTTP request for a Kind==httpCall dispatch. It
// re-validates the URL and method at execution (the publish gate is authoring-time; a stored rule
// could have been forged past it), resolves the SecretRef fail-closed (a resolve error never sends
// unauthenticated), renders nothing (REACT already rendered the CEL payload to req.Payload), and
// sends via core/httpsink (no-redirect, reserved-header drop, response-body suppression on secret).
func (e *Executor) executeHTTPCall(ctx context.Context, req *connectorwire.ConnectorDispatchRequest) execOutcome {
	h := req.HTTPCall // present: connectorwire.Validate required it for this kind

	// Re-validate the URL at the point of use (defense-in-depth vs a config that bypassed the publish
	// gate). A bad URL is terminal — a redelivery cannot fix a stored value.
	if _, err := httpsink.ValidateURL(h.URL); err != nil {
		return execOutcome{outcome: outcomeInvalid, retryable: false, err: fmt.Errorf("httpCall url: %w", err)}
	}
	// POST-only, matching the publish gate and the notification webhook policy (an unbounded method
	// widens the SSRF surface). Empty defaults to POST in httpsink; a non-empty non-POST is terminal.
	if h.Method != "" && h.Method != http.MethodPost {
		return execOutcome{outcome: outcomeInvalid, retryable: false,
			err: fmt.Errorf("httpCall method %q is not supported (POST only)", h.Method)}
	}

	// Resolve the credential fail-closed: if a handle was authored, a resolve failure (missing or a
	// transient store error) must NOT fall through to an unauthenticated send. It is classified
	// retryable so a transient DB blip recovers on redelivery and a permanently-missing secret
	// dead-letters after the cap — either way, never sent without the intended auth.
	var secret string
	if h.SecretRef != "" {
		// Bound the resolve with its own timeout: it is the one otherwise-unbounded term inside the
		// wait+resolve+send in-flight budget the egress limiter sizes against AckWait, so an indefinitely
		// hung secret store (DB) must not pin the message past AckWait into a redelivery. Normally the
		// resolve is a cached/fast envelope-decrypt, so this ceiling never trips in practice.
		resolveCtx, cancelResolve := context.WithTimeout(ctx, secretResolveTimeout)
		resolved, err := e.secrets.Resolve(resolveCtx, req.Tenant, h.SecretRef)
		cancelResolve()
		if err != nil {
			// Never log the handle's value; the ref name is a non-sensitive handle.
			return execOutcome{outcome: outcomeRetry, retryable: true,
				err: fmt.Errorf("resolve secret %q: %w", h.SecretRef, err)}
		}
		secret = resolved
	}

	// Clamp the EFFECTIVE per-send timeout at the SHARED ceiling (connectorwire.MaxTimeoutMs),
	// whether it came from the action (h.TimeoutMs) OR the configured fallback (e.defaultTimeout).
	// Clamping BOTH — not just the authored value — is what makes the egress wait-budget/AckWait
	// invariant hold: the config validates `waitBudget + MaxTimeoutMs < AckWait`, so an operator's
	// over-large sendTimeoutMs (or a forged authored value) must not put a send in flight longer than
	// the ceiling that bound was sized against, or the message could be redelivered underneath the
	// worker (a duplicate call + a spurious dead-letter). Working in integer ms and clamping before the
	// time.Duration conversion also avoids an out-of-range value overflowing Duration into a negative
	// (instantly-expired) deadline that would misclassify a never-attempted send as a transient retry.
	maxMs := int64(connectorwire.MaxTimeoutMs)
	effMs := e.defaultTimeout.Milliseconds()
	if h.TimeoutMs > 0 {
		effMs = int64(h.TimeoutMs)
	}
	if effMs <= 0 || effMs > maxMs {
		effMs = maxMs
	}
	sendCtx, cancel := context.WithTimeout(ctx, time.Duration(effMs)*time.Millisecond)
	defer cancel()

	err := httpsink.Send(sendCtx, e.client, httpsink.Request{
		URL:            h.URL,
		Method:         h.Method,
		Headers:        h.Headers,
		Body:           []byte(req.Payload),
		Secret:         secret,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		// A send error or non-2xx is transient (bounded by the redelivery cap): the endpoint may be
		// briefly down. httpsink already suppresses the response body when a secret is presented, so
		// this error text cannot leak the credential.
		return execOutcome{outcome: outcomeRetry, retryable: true, err: err}
	}
	return execOutcome{outcome: outcomeSent, retryable: false}
}
