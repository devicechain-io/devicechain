// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"strings"
)

// MqttStoreWriterMode names the event writer's deduplication protocol.
type MqttStoreWriterMode string

const (
	MqttStoreWriterPerMessage MqttStoreWriterMode = "per-message"
	MqttStoreWriterEnvelopes  MqttStoreWriterMode = "envelopes"
)

// SetMqttMessageStoreWriterMode fixes the WAL event writer's protocol before its
// readers and watches start. Different modes cannot share in-flight
// WAL deliveries because their deduplication ids belong to different namespaces.
func (nmgr *NatsManager) SetMqttMessageStoreWriterMode(mode MqttStoreWriterMode) error {
	if mode != MqttStoreWriterPerMessage && mode != MqttStoreWriterEnvelopes {
		return fmt.Errorf("messaging: unknown MQTT message-store writer mode %q", mode)
	}
	nmgr.mqttStoreWriterMode = mode
	return nil
}

func (nmgr *NatsManager) mqttWriterMode() MqttStoreWriterMode {
	if nmgr.mqttStoreWriterMode == "" {
		return MqttStoreWriterPerMessage
	}
	return nmgr.mqttStoreWriterMode
}

// A two-component signature predates WAL envelopes and means per-message.
// Unknown signatures never grant permission to read the WAL.
func mqttFailClosedWriterMode(signature string) MqttStoreWriterMode {
	parts := strings.Split(signature, "/")
	if len(parts) == 2 {
		return MqttStoreWriterPerMessage
	}
	if len(parts) == 3 {
		mode := MqttStoreWriterMode(parts[2])
		if mode == MqttStoreWriterPerMessage || mode == MqttStoreWriterEnvelopes {
			return mode
		}
	}
	return ""
}
