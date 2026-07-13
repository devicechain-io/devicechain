// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/json"
	"fmt"
	"strings"
)

// kafkaSASL is the optional SASL auth block. The password is NEVER in the config — it is
// the connector's resolved SecretRef, injected at BuildOutput time.
type kafkaSASL struct {
	// Mechanism is the SASL mechanism: PLAIN, SCRAM-SHA-256, or SCRAM-SHA-512.
	Mechanism string `json:"mechanism"`
	// Username is the SASL user (the password is the connector's secret).
	Username string `json:"username"`
}

// kafkaConfig is the DeviceChain-facing Kafka connector config (ADR-060 slice C4c), a
// curated subset of the Bento `kafka` (sarama) output. The credential (SASL password) is
// resolved from the connector's SecretRef, never stored here.
type kafkaConfig struct {
	// Addresses is the broker address list ("host:9092"). Required, ≥1 non-empty.
	Addresses []string `json:"addresses"`
	// Topic is the publish topic. Required.
	Topic string `json:"topic"`
	// ClientID is the Kafka client identifier. Optional.
	ClientID string `json:"clientId,omitempty"`
	// TLS enables TLS to the brokers. Optional; defaults false.
	TLS bool `json:"tls,omitempty"`
	// SASL is the optional SASL auth block (password comes from the secret).
	SASL *kafkaSASL `json:"sasl,omitempty"`
}

var kafkaSASLMechanisms = map[string]struct{}{
	"PLAIN": {}, "SCRAM-SHA-256": {}, "SCRAM-SHA-512": {},
}

func decodeKafka(config []byte) (*kafkaConfig, error) {
	dec := json.NewDecoder(strings.NewReader(string(config)))
	dec.DisallowUnknownFields()
	var c kafkaConfig
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("kafka config: %w", err)
	}
	return &c, nil
}

func validateKafka(config []byte) error {
	c, err := decodeKafka(config)
	if err != nil {
		return err
	}
	if len(c.Addresses) == 0 {
		return fmt.Errorf("kafka config: at least one broker address is required")
	}
	for i, a := range c.Addresses {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("kafka config: addresses[%d] is empty", i)
		}
	}
	if strings.TrimSpace(c.Topic) == "" {
		return fmt.Errorf("kafka config: topic is required")
	}
	// topic is a Bento interpolated field — a tenant topic must be a literal.
	if err := rejectInterpolation("kafka config: topic", c.Topic); err != nil {
		return err
	}
	if c.SASL != nil {
		if _, ok := kafkaSASLMechanisms[c.SASL.Mechanism]; !ok {
			return fmt.Errorf("kafka config: sasl.mechanism must be one of PLAIN, SCRAM-SHA-256, SCRAM-SHA-512, got %q", c.SASL.Mechanism)
		}
		if strings.TrimSpace(c.SASL.Username) == "" {
			return fmt.Errorf("kafka config: sasl.username is required when sasl is set")
		}
	}
	return nil
}

func buildKafka(config []byte, secret string) (map[string]any, error) {
	c, err := decodeKafka(config)
	if err != nil {
		return nil, err
	}
	kafka := map[string]any{
		"addresses": c.Addresses,
		"topic":     c.Topic,
	}
	if c.ClientID != "" {
		kafka["client_id"] = c.ClientID
	}
	if c.TLS {
		kafka["tls"] = map[string]any{"enabled": true}
	}
	if c.SASL != nil {
		// A SASL block with no sealed secret is a terminal misconfiguration — sarama rejects an
		// empty SASL password at connect, so sending it would just burn the redelivery cap. Fail
		// here (BuildOutput errors are classified terminal) rather than dispatch a doomed send.
		if secret == "" {
			return nil, fmt.Errorf("kafka config: sasl is configured but no credential is sealed for this connector")
		}
		kafka["sasl"] = map[string]any{
			"mechanism": c.SASL.Mechanism,
			"user":      c.SASL.Username,
			"password":  secret,
		}
	}
	return map[string]any{"kafka": kafka}, nil
}
