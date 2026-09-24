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
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/messaging"
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

// Outcome is the classification's metric label (sent, retry, blocked, …). It is how code outside
// this package — the service's own wiring tests — reads what Execute decided.
func (o execOutcome) Outcome() string { return o.outcome }

// Executor performs the bounded outbound send for one connector-dispatch request (ADR-060 §4). It
// owns the credential resolution (fail-closed) and the shared SSRF hardening (core/httpsink); the
// consumer owns the ack/leave-unacked/dead-letter lifecycle around it. It executes both httpCall
// (core/httpsink) and publish (resolve the versioned Connector → build its typed target → bounded
// single-message send via the publish package). Both paths dial through ONE egress guard.
type Executor struct {
	secrets *SecretResolver
	// connectors resolves a publish action's ConnectorRef to its latest published version (the
	// dispatch-side read of the C4a entity). nil disables publish (a publish dispatch is then terminal
	// unsupported) — used by httpCall-only tests.
	connectors *model.Api
	// client is the HTTP client for delivery. NewExecutor builds it over the egress guard; it is a
	// field so a test can wrap its transport. The per-send context deadline bounds each call.
	client *http.Client
	// send performs the bounded single-message publish. NewExecutor builds it over the same egress
	// guard as client; it is a field so a test can inject a fake sink.
	send func(ctx context.Context, t connectorspec.Target, payload []byte, idempotencyKey string) error
	// defaultTimeout bounds a send whose action specified no timeout.
	defaultTimeout time.Duration
}

// NewExecutor builds the executor over a secret resolver, the connector store (for publish
// resolution), the egress guard every tenant delivery dials through, and the fallback send timeout.
// A nil connectors store disables publish execution.
//
// The one guard builds BOTH delivery paths — the webhook HTTP client and the publish sender — so
// the operator's allowed destinations reach every path or none. A nil guard is a guard with no
// allowances: a missed wiring narrows the boundary rather than removing it.
func NewExecutor(resolver *SecretResolver, connectors *model.Api, guard *egress.Guard, defaultTimeout time.Duration) *Executor {
	if guard == nil {
		guard = egress.NewGuard(nil)
	}
	return &Executor{
		secrets:        resolver,
		connectors:     connectors,
		client:         &http.Client{Transport: guard.Transport()},
		defaultTimeout: defaultTimeout,
		send:           publish.NewSender(guard).Send,
	}
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

	sendCtx, cancel := sendContext(ctx, e.effectiveSendTimeout(h.TimeoutMs))
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
		// A destination the egress boundary refused is TERMINAL, and separating it here is the whole
		// point of the sentinel. Left in the transient branch it would be retried to the redelivery
		// cap and then dead-lettered as an ordinary failure — five attempts at an address that
		// cannot become public, and an operator reading "dead" when the truth is "you pointed this
		// at 169.254.169.254".
		if errors.Is(err, egress.ErrBlocked) {
			return execOutcome{outcome: outcomeBlocked, retryable: false, err: err}
		}
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

// sendContext bounds one outbound send: its timeout, further capped to end sendMargin before the
// message's AckDeadline when ctx carries one (messaging.AckDeadlineFrom). A send still running when
// the broker redelivers its message is a send a second worker is about to make again; ending it
// with room to spare turns that duplicate into an ordinary retry.
//
// It caps only the send. The dead-letter writes the consumer makes after a failed send run on the
// caller's context, which carries the AckDeadline as a value and no deadline, so a letter is never
// cut short by the send's budget.
func sendContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(timeout)
	if ackDeadline, ok := messaging.AckDeadlineFrom(ctx); ok {
		if capped := ackDeadline.Add(-sendMargin); capped.Before(deadline) {
			deadline = capped
		}
	}
	return context.WithDeadline(ctx, deadline)
}

// executePublish delivers a Kind==publish dispatch through the versioned Connector its ConnectorRef
// names (ADR-060 Tier 2): resolve the connector's latest PUBLISHED version (the draft is never
// dispatched), resolve its optional credential, build its typed target, and perform a bounded
// single-message send. A dangling/unpublished ConnectorRef, an unsupported type, or a malformed
// stored config is TERMINAL (a redelivery cannot fix it) → dead-lettered visibly, and so is a
// destination the egress guard refused. A transient secret-store or send failure is RETRYABLE
// (bounded by the redelivery cap).
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

	// Build the typed target from the published {type, config} + secret. connectorspec re-validates
	// the stored config with the same parser the write path runs (defense in depth vs a forged or
	// pre-rule row): a unix socket, an unknown scheme or a second destination behind a comma is
	// refused here and never dialed. An unsupported type (a valid vocabulary member whose client has
	// not shipped) or a malformed config is terminal.
	target, err := connectorspec.Build(version.Type, version.Config, secret)
	if err != nil {
		// Both are terminal, but keep the ops-board metric vocabulary honest (slice 8): an
		// unsupported TYPE is outcomeUnsupported; a malformed/forged stored CONFIG of a supported
		// type is outcomeInvalid.
		outcome := outcomeInvalid
		if errors.Is(err, connectorspec.ErrUnsupportedType) {
			outcome = outcomeUnsupported
		}
		return execOutcome{outcome: outcome, retryable: false,
			err: fmt.Errorf("build target for connector %q (type %q): %w", p.ConnectorRef, version.Type, err)}
	}

	sendCtx, cancel := sendContext(ctx, e.effectiveSendTimeout(p.TimeoutMs))
	defer cancel()
	// The idempotency key is FORWARDED where the protocol carries metadata (a Kafka header, an
	// SNS/SQS attribute) so a downstream consumer can deduplicate on it; nothing here deduplicates.
	// At-least-once redelivery is otherwise the contract.
	if err := e.send(sendCtx, target, []byte(req.Payload), req.IdempotencyKey); err != nil {
		// A destination the egress guard refused is TERMINAL, and it is checked FIRST: the address
		// will not become public on redelivery, and an operator reading the letter needs "blocked",
		// not "invalid" or "dead".
		if errors.Is(err, egress.ErrBlocked) {
			return execOutcome{outcome: outcomeBlocked, retryable: false,
				err: fmt.Errorf("publish to connector %q: %w", p.ConnectorRef, err)}
		}
		// A terminal target error (publish.ErrPublishConfig — a target no client can be built for)
		// is dead-lettered, not retried: a redelivery cannot fix it. Any other error is a transient
		// DELIVERY failure (a broker briefly down) → retry, bounded by the redelivery cap. The error
		// is a delivery/connection error, not the payload or credential (which are never logged).
		if errors.Is(err, publish.ErrPublishConfig) {
			return execOutcome{outcome: outcomeInvalid, retryable: false,
				err: fmt.Errorf("publish to connector %q: %w", p.ConnectorRef, err)}
		}
		return execOutcome{outcome: outcomeRetry, retryable: true,
			err: fmt.Errorf("publish to connector %q: %w", p.ConnectorRef, err)}
	}
	return execOutcome{outcome: outcomeSent, retryable: false}
}
