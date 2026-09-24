// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"fmt"
	"strings"
)

// kafkaSASLConfig is the optional SASL auth block. The password is NEVER in the config — it
// is the connector's resolved SecretRef, attached by Build.
type kafkaSASLConfig struct {
	// Mechanism is the SASL mechanism: PLAIN, SCRAM-SHA-256, or SCRAM-SHA-512.
	Mechanism string `json:"mechanism"`
	// Username is the SASL user (the password is the connector's secret).
	Username string `json:"username"`
}

// kafkaConfig is the DeviceChain-facing Kafka connector config (ADR-060 slice C4c). The
// credential (SASL password) is resolved from the connector's SecretRef, never stored
// here.
type kafkaConfig struct {
	// Addresses is the seed broker list, one host:port per entry. Required, at least one.
	Addresses []string `json:"addresses"`
	// Topic is the publish topic. Required.
	Topic string `json:"topic"`
	// ClientID is the Kafka client identifier. Optional; the client defaults it.
	ClientID string `json:"clientId,omitempty"`
	// TLS enables TLS to every broker. Optional; defaults false.
	TLS bool `json:"tls,omitempty"`
	// SASL is the optional SASL auth block (password comes from the secret).
	SASL *kafkaSASLConfig `json:"sasl,omitempty"`
}

// KafkaTarget is a validated Kafka destination.
type KafkaTarget struct {
	// Seeds are the authored bootstrap brokers, each host:port. The brokers the cluster
	// advertises in metadata are dialed too, through the same guarded dial.
	Seeds    []string
	Topic    string
	ClientID string
	TLS      bool
	SASL     *KafkaSASL
}

// KafkaSASL is SASL authentication for a KafkaTarget.
type KafkaSASL struct {
	// Mechanism is PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512.
	Mechanism string
	User      string
	Password  string
}

func (KafkaTarget) isTarget() {}

// KafkaSASLMechanisms is the accepted SASL mechanism vocabulary.
var KafkaSASLMechanisms = []string{"PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"}

func parseKafka(config []byte) (Target, error) {
	var c kafkaConfig
	if err := decodeStrict("kafka", config, &c); err != nil {
		return nil, err
	}
	if len(c.Addresses) == 0 {
		return nil, fmt.Errorf("kafka config: at least one broker address is required")
	}
	t := KafkaTarget{Topic: c.Topic, ClientID: c.ClientID, TLS: c.TLS}
	for i, a := range c.Addresses {
		if strings.TrimSpace(a) == "" {
			return nil, fmt.Errorf("kafka config: addresses[%d] is empty", i)
		}
		if err := validHostPort(a); err != nil {
			return nil, fmt.Errorf("kafka config: addresses[%d]: %w", i, err)
		}
		t.Seeds = append(t.Seeds, a)
	}
	if strings.TrimSpace(c.Topic) == "" {
		return nil, fmt.Errorf("kafka config: topic is required")
	}
	if c.SASL != nil {
		known := false
		for _, m := range KafkaSASLMechanisms {
			known = known || c.SASL.Mechanism == m
		}
		if !known {
			return nil, fmt.Errorf("kafka config: sasl.mechanism must be one of %s, got %q",
				strings.Join(KafkaSASLMechanisms, ", "), c.SASL.Mechanism)
		}
		if strings.TrimSpace(c.SASL.Username) == "" {
			return nil, fmt.Errorf("kafka config: sasl.username is required when sasl is set")
		}
		t.SASL = &KafkaSASL{Mechanism: c.SASL.Mechanism, User: c.SASL.Username}
	}
	return t, nil
}

// withKafkaSecret attaches the SASL password. A SASL block with no sealed secret is a
// terminal misconfiguration — the broker would refuse an empty password at connect, so
// sending it would only burn the redelivery cap. It fails here, where a Build error is
// classified terminal, rather than dispatching a doomed send.
func withKafkaSecret(t Target, secret string) (Target, error) {
	k := t.(KafkaTarget)
	if k.SASL == nil {
		return k, nil
	}
	if secret == "" {
		return nil, fmt.Errorf("kafka config: sasl is configured but no credential is sealed for this connector")
	}
	sasl := *k.SASL
	sasl.Password = secret
	k.SASL = &sasl
	return k, nil
}
