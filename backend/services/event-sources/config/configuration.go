/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
