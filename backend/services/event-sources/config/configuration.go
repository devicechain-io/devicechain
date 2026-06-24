// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

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
	return &EventSourcesConfiguration{
		EventSources: []EventSource{
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
		},
		InboundEventBatching: KafkaEventBatching{
			MaxBatchSize:   100,
			BatchTimeoutMs: 100,
		},
	}
}
