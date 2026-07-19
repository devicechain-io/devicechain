// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
)

const (
	// DefaultIngestMessagesPerSecond and DefaultIngestBurst are the platform
	// per-tenant ingest ceiling applied when none is configured. They are a
	// generous safety ceiling — high enough not to shed a normally busy fleet,
	// low enough that a single runaway tenant cannot saturate the pipeline. A
	// genuinely high-volume tenant is raised past this by a per-tenant override
	// (a later slice); the platform default is deliberately never unlimited.
	DefaultIngestMessagesPerSecond = 1000
	DefaultIngestBurst             = 2000
)

// Decodes event payloads into standardized format.
type EventDecoder struct {
	Type          string
	Configuration map[string]string
}

// Source that reads events from a protocol and decodes them.
type EventSource struct {
	Id            string
	Type          string
	Configuration map[string]string
	Decoder       EventDecoder
	Debug         bool
}

type KafkaEventBatching struct {
	MaxBatchSize   int
	BatchTimeoutMs int
}

// IngestRateLimit is the platform-default, per-tenant ingest ceiling. Every
// tenant is metered by an independent token bucket at these rates and events over
// the ceiling are shed at the receive point, before decode, so a noisy tenant
// spends no pipeline CPU past its allowance. It is fail-safe: an unset or
// non-positive value falls back to the platform default rather than to unlimited,
// so a misconfiguration cannot silently remove the protection. Per-tenant
// overrides that raise the ceiling for a legitimately high-volume tenant are a
// later slice.
type IngestRateLimit struct {
	// MessagesPerSecond is the sustained per-tenant event rate.
	MessagesPerSecond float64
	// Burst is the largest instantaneous batch a tenant may send before the
	// sustained rate applies — it absorbs bursty devices without raising the
	// sustained ceiling.
	Burst int
}

type EventSourcesConfiguration struct {
	EventSources         []EventSource
	InboundEventBatching KafkaEventBatching
	IngestRateLimit      IngestRateLimit
}

// Creates the default event sources configuration
func NewEventSourcesConfiguration() *EventSourcesConfiguration {
	cfg := &EventSourcesConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with their defaults so configuration loaded
// from a document that omits them is still well-formed (ADR-022 decision 1). It
// runs on both the constructor and the load path so there is one source of
// defaults. An empty event-source list is load-bearing: without a source the
// service ingests nothing, so it is populated with the default MQTT source.
func (c *EventSourcesConfiguration) ApplyDefaults() {
	if len(c.EventSources) == 0 {
		c.EventSources = []EventSource{
			{
				Id:   "mqtt1",
				Type: "mqtt",
				Configuration: map[string]string{
					"host": "dc-nats.dc-system",
					"port": "1883",
					// Device-plane topic (ADR-006): "{instanceId}/{tenant}/...". The
					// first level is the instance id and the second carries the tenant
					// the producer scopes on. For the gateway source this value is
					// replaced at bind time with the instance-scoped wildcard
					// "{instanceId}/+/#" (ADR-048); the placeholder here documents the
					// shape and applies only to an external-broker source.
					"topic": "+/#",
				},
				Decoder: EventDecoder{
					Type:          "json",
					Configuration: map[string]string{},
				},
				Debug: false,
			},
			{
				// HTTP ingest is on by default alongside MQTT (TB §2.9, the most
				// common integration after MQTT). Devices POST events to
				// "/{instanceId}/{tenant}/events"; the instance and tenant are taken
				// from the path, mirroring the MQTT topic convention (ADR-006/ADR-048).
				Id:   "http1",
				Type: "http",
				Configuration: map[string]string{
					"port": "8081",
				},
				Decoder: EventDecoder{
					Type:          "json",
					Configuration: map[string]string{},
				},
				Debug: false,
			},
		}
	}
	if c.InboundEventBatching.MaxBatchSize == 0 {
		c.InboundEventBatching.MaxBatchSize = 100
	}
	if c.InboundEventBatching.BatchTimeoutMs == 0 {
		c.InboundEventBatching.BatchTimeoutMs = 100
	}
	// Fail-safe defaulting: a non-positive rate or burst falls back to the
	// platform ceiling, never to unlimited, so an omitted or zeroed limit still
	// meters every tenant.
	if c.IngestRateLimit.MessagesPerSecond <= 0 {
		c.IngestRateLimit.MessagesPerSecond = DefaultIngestMessagesPerSecond
	}
	if c.IngestRateLimit.Burst <= 0 {
		c.IngestRateLimit.Burst = DefaultIngestBurst
	}
}

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1). Batching
// bounds must be positive; the source list is left to the source loaders.
func (c *EventSourcesConfiguration) Validate() error {
	if c.InboundEventBatching.MaxBatchSize <= 0 {
		return fmt.Errorf("inboundEventBatching.maxBatchSize must be positive (got %d)", c.InboundEventBatching.MaxBatchSize)
	}
	if c.InboundEventBatching.BatchTimeoutMs <= 0 {
		return fmt.Errorf("inboundEventBatching.batchTimeoutMs must be positive (got %d)", c.InboundEventBatching.BatchTimeoutMs)
	}
	return nil
}
