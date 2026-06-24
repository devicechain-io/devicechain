// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

const (
	KAFKA_TOPIC_INBOUND_EVENTS = "inbound-events"
	KAFKA_TOPIC_FAILED_DECODE  = "failed-decode"
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
					"host":  "dc-mosquitto.dc-system",
					"port":  "1883",
					"topic": "devicechain/events",
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
