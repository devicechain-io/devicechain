// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/json"
	"fmt"
	"strings"
)

// mqttConfig is the DeviceChain-facing MQTT connector config (ADR-060 slice C4b). It is
// a curated subset of the Bento mqtt output fields — the tenant authors this shape, not
// Bento YAML. The credential (the MQTT password) is NEVER in this config; it is resolved
// from the connector's SecretRef and injected at BuildOutput time.
type mqttConfig struct {
	// URLs is the broker URL list (e.g. "tcp://broker:1883", "ssl://broker:8883").
	// Required, at least one non-empty entry.
	URLs []string `json:"urls"`
	// Topic is the publish topic. Required.
	Topic string `json:"topic"`
	// QoS is the publish quality-of-service (0, 1, or 2). Optional; defaults to 1
	// (at-least-once), which is emitted explicitly so behavior does not depend on Bento's default.
	QoS *int `json:"qos,omitempty"`
	// ClientID is the MQTT client identifier. Optional.
	ClientID string `json:"clientId,omitempty"`
	// Username authenticates to the broker (the password is the connector's secret).
	// Optional (anonymous brokers omit it).
	Username string `json:"username,omitempty"`
}

// decodeMQTT strictly decodes the MQTT config (unknown fields rejected, fail-closed).
func decodeMQTT(config []byte) (*mqttConfig, error) {
	dec := json.NewDecoder(strings.NewReader(string(config)))
	dec.DisallowUnknownFields()
	var c mqttConfig
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("mqtt config: %w", err)
	}
	return &c, nil
}

// validateMQTT rejects a structurally-invalid MQTT config.
func validateMQTT(config []byte) error {
	c, err := decodeMQTT(config)
	if err != nil {
		return err
	}
	if len(c.URLs) == 0 {
		return fmt.Errorf("mqtt config: at least one broker url is required")
	}
	for i, u := range c.URLs {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("mqtt config: url[%d] is empty", i)
		}
	}
	if strings.TrimSpace(c.Topic) == "" {
		return fmt.Errorf("mqtt config: topic is required")
	}
	// The Bento mqtt `topic` is a Bloblang-INTERPOLATED field: a "${!...}" fragment is evaluated
	// per message. Reject it so a tenant topic is a literal string, not executable Bloblang.
	if err := rejectInterpolation("mqtt config: topic", c.Topic); err != nil {
		return err
	}
	if c.QoS != nil && (*c.QoS < 0 || *c.QoS > 2) {
		return fmt.Errorf("mqtt config: qos must be 0, 1, or 2, got %d", *c.QoS)
	}
	return nil
}

// buildMQTT builds the Bento mqtt output config map from the connector config + resolved
// secret (the broker password). Only set fields are emitted so Bento's own defaults apply
// to the rest.
func buildMQTT(config []byte, secret string) (map[string]any, error) {
	c, err := decodeMQTT(config)
	if err != nil {
		return nil, err
	}
	// qos defaults to 1 (at-least-once) and is emitted explicitly so delivery semantics do not
	// depend on Bento's default (which is also 1, but making it explicit is a stable contract).
	qos := 1
	if c.QoS != nil {
		qos = *c.QoS
	}
	mqtt := map[string]any{
		"urls":  c.URLs,
		"topic": c.Topic,
		"qos":   qos,
		// Bound the one otherwise-uncancellable wait a broker-down send can be in (the client's
		// connect handshake). Without this Bento defaults to 30s (> the 20s send ceiling), so a
		// stuck connect would outlive the send deadline and prolong the async stream teardown. Kept
		// at the send ceiling so a genuinely slow-but-reachable broker still connects.
		"connect_timeout": "20s",
	}
	if c.ClientID != "" {
		mqtt["client_id"] = c.ClientID
		// A fixed client_id shared by two concurrent sends causes an MQTT session TAKEOVER (the broker
		// disconnects the earlier session), turning overlapping dispatches through one connector into
		// spurious failures + retry churn. A per-connection nanoid suffix makes each ephemeral send a
		// distinct session while preserving the authored client_id prefix (e.g. for ACL matching).
		mqtt["dynamic_client_id_suffix"] = "nanoid"
	}
	if c.Username != "" {
		mqtt["user"] = c.Username
	}
	if secret != "" {
		mqtt["password"] = secret
	}
	return map[string]any{"mqtt": mqtt}, nil
}
