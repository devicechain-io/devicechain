// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
)

const (
	SUBJECT_INBOUND_EVENTS = "inbound-events"
	SUBJECT_FAILED_DECODE  = "failed-decode"
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

type EventSourcesConfiguration struct {
	EventSources         []EventSource
	InboundEventBatching KafkaEventBatching
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
					// Tenant-bearing topic (ADR-006): "dc/{tenant}/...". The
					// second level carries the tenant the producer scopes on.
					"topic": "dc/+/#",
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
				// "/dc/{tenant}/events"; the tenant is taken from the path, mirroring
				// the MQTT topic convention (ADR-006).
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
