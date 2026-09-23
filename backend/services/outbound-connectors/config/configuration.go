// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/config"
)

// Defaults for the outbound-connectors service (ADR-060 §4 / slice C3). The service is a durable
// consumer of the connector-dispatch stream that executes each fired httpCall/publish action; these
// bound its outbound behavior so a slow external target throttles the pull rather than growing an
// in-memory queue, and a hung endpoint cannot pin a worker forever (SD-2).
const (
	// DefaultSendTimeoutMs bounds a single outbound send when the action itself did not specify a
	// timeout (its TimeoutMs is 0). The publish gate already caps an authored timeout at
	// rules.MaxActionTimeoutMs; this is only the fallback for an unspecified one. An unbounded
	// outbound wait would pin a worker indefinitely on a hung endpoint.
	DefaultSendTimeoutMs = 10_000
	// DefaultMaxConcurrentSends bounds how many outbound sends run at once (the worker-pool width).
	// It is the in-flight concurrency ceiling that expresses SD-2's back-pressure: at most this many
	// slow targets are dialed concurrently, and the durable consumer stops pulling once the pool is
	// busy rather than buffering unboundedly — unpulled work stays durable on the stream (which is
	// itself per-tenant bounded, ADR-023 G.2). Per-tenant egress RATE limiting + cost-gate-at-source
	// is a follow-up (C3b); this concurrency bound is the C3a back-pressure primitive.
	//
	// It is also the dispatch reader's capacity: the reader fetches only as many dispatches as there
	// are free workers (messaging.ReaderWithCapacity), so no dispatch waits in the process while the
	// broker's redelivery clock runs. There is no separate hand-off buffer to size.
	DefaultMaxConcurrentSends = 32

	// DefaultOutboundMessagesPerSecond / DefaultOutboundBurst are the platform-default per-tenant
	// OUTBOUND egress rate ceiling (ADR-060 SD-3), applied to any tenant with no override in
	// user-management (fetched via governance) and as the whole ceiling when overrides are not wired.
	// Deliberately LOWER than the ingest default (1000/s): an outbound connector call is far more
	// expensive than an inbound event (a per-event webhook is a self-DoS / cost bomb against the
	// external target and our egress), so ADR-060 prices it strictly above an in-process action.
	// Fail-safe: a missing/zero override resolves to this default, never to unlimited.
	DefaultOutboundMessagesPerSecond = 100
	DefaultOutboundBurst             = 200

	// DefaultEgressWaitBudgetMs is how long a worker will BLOCK waiting for a token before a dispatch
	// is admitted, so a brief burst just over a tenant's rate is smoothed into pacing rather than
	// shed. A dispatch that cannot get a token within this budget is sustained over quota (a brief
	// burst would have been admitted) and is shed to the dead-letter subject — the wait never leaves a
	// rate-shed message unacked, so rate-limiting can never churn the redelivery/poison cap. See MaxEgressWaitBudgetMs
	// for the AckWait-safety bound this default sits comfortably inside.
	DefaultEgressWaitBudgetMs = 5_000

	// MaxEgressWaitBudgetMs caps the configurable wait budget at startup so ONE dispatch fits inside
	// the consumer AckWait, measured on that message's own clock. The dispatch reader fetches a
	// message only when a worker is free to start it, so the broker's clock and the worker's start
	// together, and everything the worker does must finish before the clock runs out:
	//
	//   waitBudget + secretResolve + maxSend + sendMargin < AckWait
	//   8s         + 5s            + 20s     + 5s         = 38s < 60s
	//
	// Every term is enforced in code: maxSend is the executor's hard clamp at
	// connectorwire.MaxTimeoutMs (Validate rejects a SendTimeoutMs above it), secretResolve and
	// sendMargin are the processor's constants, and the processor also caps the wait and the send at
	// the message's own AckDeadline, so a message that reached its worker late still cannot send past
	// it. It no longer depends on MaxConcurrentSends: the pool width sets how many dispatches are in
	// flight, not how long any one of them waits.
	MaxEgressWaitBudgetMs = 8_000
)

// OutboundConnectorsConfiguration is the typed, fail-closed configuration for the outbound-connectors
// service (ADR-060). It is loaded via core.LoadConfiguration (unknown keys rejected).
type OutboundConnectorsConfiguration struct {
	// RdbConfiguration is the per-service datastore configuration. The service keeps its own
	// envelope-encrypted secret store (ADR-059) in this database — each service seals its secrets
	// with the same instance KEK, so the crypto is uniform without a shared datastore.
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// SendTimeoutMs is the fallback per-send timeout when an action specifies none. Unset (0)
	// defaults to DefaultSendTimeoutMs; a negative value is rejected.
	SendTimeoutMs int

	// MaxConcurrentSends is the outbound concurrency ceiling (worker-pool width) and the dispatch
	// reader's capacity. Unset (0) defaults to DefaultMaxConcurrentSends; a non-positive value is
	// rejected.
	MaxConcurrentSends int

	// OutboundMessagesPerSecond / OutboundBurst are the platform-default per-tenant egress ceiling
	// (ADR-060 SD-3). Unset (0) defaults to DefaultOutboundMessagesPerSecond / DefaultOutboundBurst;
	// a non-positive value is rejected (never unlimited). A per-tenant override in user-management,
	// fetched via the governance resolver, raises or lowers this for that tenant; a tenant with no
	// override is metered at this default.
	OutboundMessagesPerSecond float64
	OutboundBurst             int

	// EgressWaitBudgetMs is the per-dispatch smoothing wait before a shed (see DefaultEgressWaitBudgetMs).
	// Unset (0) defaults to DefaultEgressWaitBudgetMs; a non-positive value or one above
	// MaxEgressWaitBudgetMs is rejected.
	EgressWaitBudgetMs int
}

// NewOutboundConnectorsConfiguration creates the default configuration.
func NewOutboundConnectorsConfiguration() *OutboundConnectorsConfiguration {
	cfg := &OutboundConnectorsConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset (0) fields with the platform defaults (ADR-022 decision 1).
func (c *OutboundConnectorsConfiguration) ApplyDefaults() {
	if c.SendTimeoutMs == 0 {
		c.SendTimeoutMs = DefaultSendTimeoutMs
	}
	if c.MaxConcurrentSends == 0 {
		c.MaxConcurrentSends = DefaultMaxConcurrentSends
	}
	if c.OutboundMessagesPerSecond == 0 {
		c.OutboundMessagesPerSecond = DefaultOutboundMessagesPerSecond
	}
	if c.OutboundBurst == 0 {
		c.OutboundBurst = DefaultOutboundBurst
	}
	if c.EgressWaitBudgetMs == 0 {
		c.EgressWaitBudgetMs = DefaultEgressWaitBudgetMs
	}
}

// Validate rejects non-positive tunables fail-closed (ADR-022 decision 1): a value that would make
// the consumer admit nothing (zero concurrency) or wait forever (negative timeout) is an operator
// error rejected at startup rather than silently degrading.
func (c *OutboundConnectorsConfiguration) Validate() error {
	if c.SendTimeoutMs < 0 {
		return fmt.Errorf("sendTimeoutMs must not be negative, got %d", c.SendTimeoutMs)
	}
	// The fallback send timeout must not exceed the shared per-send ceiling: it is the maxSend term in
	// the wait-budget/AckWait bound (see MaxEgressWaitBudgetMs), and the executor clamps to it anyway,
	// so an operator value above it is rejected loudly rather than silently clamped.
	if c.SendTimeoutMs > connectorwire.MaxTimeoutMs {
		return fmt.Errorf("sendTimeoutMs must not exceed %d (the shared per-send ceiling), got %d", connectorwire.MaxTimeoutMs, c.SendTimeoutMs)
	}
	if c.MaxConcurrentSends <= 0 {
		return fmt.Errorf("maxConcurrentSends must be positive, got %d", c.MaxConcurrentSends)
	}
	if c.OutboundMessagesPerSecond <= 0 {
		return fmt.Errorf("outboundMessagesPerSecond must be positive, got %v", c.OutboundMessagesPerSecond)
	}
	if c.OutboundBurst <= 0 {
		return fmt.Errorf("outboundBurst must be positive, got %d", c.OutboundBurst)
	}
	if c.EgressWaitBudgetMs <= 0 || c.EgressWaitBudgetMs > MaxEgressWaitBudgetMs {
		return fmt.Errorf("egressWaitBudgetMs must be in (0, %d], got %d", MaxEgressWaitBudgetMs, c.EgressWaitBudgetMs)
	}
	return nil
}
