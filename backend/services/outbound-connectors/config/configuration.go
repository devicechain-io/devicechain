// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

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
	DefaultMaxConcurrentSends = 32
	// DefaultDispatchBacklog bounds the hand-off buffer between the single reader and the worker
	// pool. Kept SMALL on purpose — it is not a queue, just a smoothing buffer, and a large backlog
	// would let the read loop pull a second fetch batch ahead while the first is still in flight,
	// putting two batches' worth of messages under the AckWait clock at once (a slow-but-succeeding
	// send could then be redelivered underneath the worker). A small backlog paces the read loop to
	// worker availability, so roughly one fetch batch is exposed at a time; the durable stream is the
	// real durable buffer (unacked messages redeliver).
	DefaultDispatchBacklog = 8
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

	// MaxConcurrentSends is the outbound concurrency ceiling (worker-pool width). Unset (0) defaults
	// to DefaultMaxConcurrentSends; a non-positive value is rejected.
	MaxConcurrentSends int

	// DispatchBacklog is the reader→worker hand-off buffer size. Unset (0) defaults to
	// DefaultDispatchBacklog; a non-positive value is rejected.
	DispatchBacklog int
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
	if c.DispatchBacklog == 0 {
		c.DispatchBacklog = DefaultDispatchBacklog
	}
}

// Validate rejects non-positive tunables fail-closed (ADR-022 decision 1): a value that would make
// the consumer admit nothing (zero concurrency) or wait forever (negative timeout) is an operator
// error rejected at startup rather than silently degrading.
func (c *OutboundConnectorsConfiguration) Validate() error {
	if c.SendTimeoutMs < 0 {
		return fmt.Errorf("sendTimeoutMs must not be negative, got %d", c.SendTimeoutMs)
	}
	if c.MaxConcurrentSends <= 0 {
		return fmt.Errorf("maxConcurrentSends must be positive, got %d", c.MaxConcurrentSends)
	}
	if c.DispatchBacklog <= 0 {
		return fmt.Errorf("dispatchBacklog must be positive, got %d", c.DispatchBacklog)
	}
	return nil
}
