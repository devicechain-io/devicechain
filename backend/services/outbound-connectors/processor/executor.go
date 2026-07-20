// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/devicechain-io/dc-outbound-connectors/publish"
	"gorm.io/gorm"
)

// secretResolveTimeout bounds the credential resolve so a hung secret store cannot pin a dispatch
// past the consumer AckWait (see the wait+resolve+send budget in config.MaxEgressWaitBudgetMs).
// Generous, since a resolve is normally a cached/fast envelope-decrypt.
const secretResolveTimeout = 5 * time.Second

// execOutcome is the executor's classification of one dispatch, driving the consumer's
// ack/leave-unacked/dead-letter decision and the bounded metric. err is nil only for a successful send.
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
// consumer owns the ack/leave-unacked/dead-letter lifecycle around it. It executes both httpCall (core/httpsink)
// and publish (resolve the versioned Connector → generate the Bento output → bounded single-message
// send via the publish sink, slice C4b).
type Executor struct {
	secrets *SecretResolver
	// connectors resolves a publish action's ConnectorRef to its latest published version (the
	// dispatch-side read of the C4a entity). nil disables publish (a publish dispatch is then terminal
	// unsupported) — used by httpCall-only tests.
	connectors *model.Api
	// client is the HTTP client for delivery; nil uses httpsink.DefaultClient. A field so a test can
	// inject a client pointed at an httptest server. The per-send context deadline bounds each call.
	client *http.Client
	// send performs the bounded Bento single-message publish. A field so a test can inject a fake sink
	// (the real one dials a broker); NewExecutor always sets it to publish.Send.
	send func(ctx context.Context, outputYAML string, payload []byte, metadata map[string]string) error
	// defaultTimeout bounds a send whose action specified no timeout.
	defaultTimeout time.Duration
}

// NewExecutor builds the executor over a secret resolver, the connector store (for publish
// resolution), and the fallback send timeout. A nil connectors store disables publish execution.
func NewExecutor(resolver *SecretResolver, connectors *model.Api, defaultTimeout time.Duration) *Executor {
	return &Executor{secrets: resolver, connectors: connectors, defaultTimeout: defaultTimeout, send: publish.Send}
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
		return e.executePublish(ctx, req)
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

	sendCtx, cancel := context.WithTimeout(ctx, e.effectiveSendTimeout(h.TimeoutMs))
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

// effectiveSendTimeout clamps the EFFECTIVE per-send timeout at the SHARED ceiling
// (connectorwire.MaxTimeoutMs), whether it came from the action (authoredMs) OR the configured
// fallback (e.defaultTimeout). Clamping BOTH — not just the authored value — is what makes the
// egress wait-budget/AckWait invariant hold: the config validates `waitBudget + MaxTimeoutMs <
// AckWait`, so an operator's over-large sendTimeoutMs (or a forged authored value) must not put a
// send in flight longer than the ceiling that bound was sized against, or the message could be
// redelivered underneath the worker (a duplicate call + a spurious dead-letter). Working in integer
// ms and clamping before the time.Duration conversion also avoids an out-of-range value overflowing
// Duration into a negative (instantly-expired) deadline that would misclassify a never-attempted
// send as a transient retry.
func (e *Executor) effectiveSendTimeout(authoredMs int) time.Duration {
	maxMs := int64(connectorwire.MaxTimeoutMs)
	effMs := e.defaultTimeout.Milliseconds()
	if authoredMs > 0 {
		effMs = int64(authoredMs)
	}
	if effMs <= 0 || effMs > maxMs {
		effMs = maxMs
	}
	return time.Duration(effMs) * time.Millisecond
}

// executePublish delivers a Kind==publish dispatch through the versioned Connector its ConnectorRef
// names (ADR-060 Tier 2): resolve the connector's latest PUBLISHED version (the draft is never
// dispatched), resolve its optional credential, generate the Bento output config, and perform a
// bounded single-message send. A dangling/unpublished ConnectorRef, an unsupported type, or a
// malformed stored config is TERMINAL (a redelivery cannot fix it) → dead-lettered visibly. A
// transient secret-store or send failure is RETRYABLE (bounded by the redelivery cap).
func (e *Executor) executePublish(ctx context.Context, req *connectorwire.ConnectorDispatchRequest) execOutcome {
	p := req.Publish // present: connectorwire.Validate required it for this kind
	if e.connectors == nil {
		// No connector store wired (httpCall-only deployment/test): publish is not executable. Terminal.
		return execOutcome{outcome: outcomeUnsupported, retryable: false,
			err: fmt.Errorf("publish is not executable: no connector store configured")}
	}

	// Resolve the ConnectorRef to its latest published version (tenant-confined via ctx). A
	// since-deleted connector or a draft-only (never-published) one is terminal — a redelivery cannot
	// make it resolvable/published; the author must fix the rule or publish the connector.
	version, err := e.connectors.LatestPublishedConnector(ctx, p.ConnectorRef)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return execOutcome{outcome: outcomeInvalid, retryable: false,
			err: fmt.Errorf("connector %q not found", p.ConnectorRef)}
	case errors.Is(err, model.ErrNotPublished):
		return execOutcome{outcome: outcomeInvalid, retryable: false,
			err: fmt.Errorf("connector %q has no published version", p.ConnectorRef)}
	case err != nil:
		// An unexpected store error (DB blip) is transient — retry rather than dead-letter a connector
		// that may resolve on redelivery.
		return execOutcome{outcome: outcomeRetry, retryable: true,
			err: fmt.Errorf("resolve connector %q: %w", p.ConnectorRef, err)}
	}

	// Resolve the connector's credential fail-closed. The secret is OPTIONAL (an anonymous broker has
	// none), so a "not found" means no credential — proceed unauthenticated by design. A transient
	// store error is retryable (never proceed on an unknown credential state). The handle is keyed by
	// the parent connector's immutable id (rename-safe), matching the write path.
	var secret string
	resolveCtx, cancelResolve := context.WithTimeout(ctx, secretResolveTimeout)
	resolved, err := e.secrets.Resolve(resolveCtx, req.Tenant, model.ConnectorSecretName(version.ConnectorID))
	cancelResolve()
	switch {
	case errors.Is(err, secrets.ErrSecretNotFound):
		secret = "" // anonymous connector — no credential configured
	case err != nil:
		return execOutcome{outcome: outcomeRetry, retryable: true,
			err: fmt.Errorf("resolve connector %q credential: %w", p.ConnectorRef, err)}
	default:
		secret = resolved
	}

	// Generate the Bento output config from the published {type, config} + secret. connectorspec
	// re-validates the stored config (defense in depth vs a forged/corrupt row). An unsupported type
	// (a valid vocabulary member whose generator has not shipped) or a malformed config is terminal.
	outputYAML, err := connectorspec.BuildOutput(version.Type, version.Config, secret)
	if err != nil {
		// Both are terminal, but keep the ops-board metric vocabulary honest (slice 8): an
		// unsupported TYPE (a valid vocabulary member whose generator has not shipped) is
		// outcomeUnsupported; a malformed/forged stored CONFIG of a supported type is outcomeInvalid.
		outcome := outcomeInvalid
		if errors.Is(err, connectorspec.ErrUnsupportedType) {
			outcome = outcomeUnsupported
		}
		return execOutcome{outcome: outcome, retryable: false,
			err: fmt.Errorf("build output for connector %q (type %q): %w", p.ConnectorRef, version.Type, err)}
	}

	sendCtx, cancel := context.WithTimeout(ctx, e.effectiveSendTimeout(p.TimeoutMs))
	defer cancel()
	// The idempotency key rides as message metadata for outputs that can dedup on it; at-least-once
	// redelivery is otherwise the contract (content-addressed idempotency upstream).
	if err := e.send(sendCtx, outputYAML, []byte(req.Payload), map[string]string{"idempotency_key": req.IdempotencyKey}); err != nil {
		// A terminal config/stream error (publish.ErrPublishConfig — a config that generated but Bento
		// cannot build/run) is dead-lettered, not retried: a redelivery cannot fix it. Any other error
		// is a transient DELIVERY failure (broker briefly down) → retry, bounded by the redelivery cap.
		// The error is a delivery/connection error, not the payload or config (which are never logged).
		if errors.Is(err, publish.ErrPublishConfig) {
			return execOutcome{outcome: outcomeInvalid, retryable: false,
				err: fmt.Errorf("publish to connector %q: %w", p.ConnectorRef, err)}
		}
		return execOutcome{outcome: outcomeRetry, retryable: true,
			err: fmt.Errorf("publish to connector %q: %w", p.ConnectorRef, err)}
	}
	return execOutcome{outcome: outcomeSent, retryable: false}
}
