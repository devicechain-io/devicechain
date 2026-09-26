// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"fmt"
	"net/url"
	"strings"
)

// mqttConfig is the DeviceChain-facing MQTT connector config (ADR-060 slice C4b). The
// credential (the MQTT password) is NEVER in this config; it is resolved from the
// connector's SecretRef and attached by Build.
type mqttConfig struct {
	// URLs is the broker URL list, one broker per entry ("tcp://broker:1883",
	// "ssl://broker:8883", "wss://broker:443/mqtt"). Required, at least one.
	URLs []string `json:"urls"`
	// Topic is the publish topic. Required.
	Topic string `json:"topic"`
	// QoS is the publish quality-of-service (0, 1, or 2). Optional; defaults to 1
	// (at-least-once).
	QoS *int `json:"qos,omitempty"`
	// ClientID is the MQTT client identifier prefix. Optional.
	ClientID string `json:"clientId,omitempty"`
	// Username authenticates to the broker (the password is the connector's secret).
	// Optional (anonymous brokers omit it).
	Username string `json:"username,omitempty"`
}

// MQTTTarget is a validated MQTT destination.
type MQTTTarget struct {
	// Brokers are the parsed broker URLs, each with a scheme in MQTTSchemes, a host and an
	// explicit port.
	Brokers []*url.URL
	Topic   string
	QoS     byte
	// ClientID is the authored client-id PREFIX; the publish client appends a random
	// suffix so two concurrent sends through one connector are two sessions.
	ClientID string
	Username string
	Password string
}

func (MQTTTarget) isTarget() {}

// mqttTCPSchemes are the schemes that name a raw TCP (optionally TLS) broker. They carry
// no path, query or fragment: a client would drop them, so accepting them would store a
// destination that is not the one reached.
var mqttTCPSchemes = map[string]bool{"tcp": false, "mqtt": false, "ssl": true, "tls": true, "mqtts": true}

// mqttWSSchemes are the WebSocket schemes; the value is whether the scheme is TLS. A path
// is part of the destination here (the broker's WebSocket endpoint), so it is allowed.
var mqttWSSchemes = map[string]bool{"ws": false, "wss": true}

// MQTTSchemeUsesTLS reports whether a validated broker URL's scheme is a TLS scheme, and
// whether it is a WebSocket one. It is the single place the scheme vocabulary is read
// after parsing, so the publish client cannot drift from the parser.
func MQTTSchemeUsesTLS(scheme string) (tls, websocket, ok bool) {
	if t, found := mqttTCPSchemes[scheme]; found {
		return t, false, true
	}
	if t, found := mqttWSSchemes[scheme]; found {
		return t, true, true
	}
	return false, false, false
}

// parseMQTTURL validates one broker entry. The scheme is compared exactly (lower case):
// a client library that lower-cases or aliases schemes itself would otherwise reach a
// transport this check never saw.
func parseMQTTURL(raw string) (*url.URL, error) {
	if strings.Contains(raw, ",") {
		return nil, fmt.Errorf("%q: one broker per entry (no commas)", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	// url.Parse lower-cases the scheme, so the exact comparison is against the text as
	// authored: what is stored is what a reader of the connector sees.
	_, ws, ok := MQTTSchemeUsesTLS(u.Scheme)
	if !ok || !strings.HasPrefix(raw, u.Scheme+"://") {
		return nil, fmt.Errorf("%q: the scheme must be one of tcp, mqtt, ssl, tls, mqtts, ws or wss", raw)
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("%q must be scheme://host:port", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%q must not carry userinfo; set username on the connector", raw)
	}
	if err := validHost(u.Hostname()); err != nil {
		return nil, fmt.Errorf("%q: %w", raw, err)
	}
	if err := validPort(u.Port()); err != nil {
		return nil, fmt.Errorf("%q: %w", raw, err)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("%q must not carry a query or fragment", raw)
	}
	if !ws && (u.Path != "" || u.RawPath != "") {
		return nil, fmt.Errorf("%q: a %s URL carries no path", raw, u.Scheme)
	}
	return u, nil
}

func parseMQTT(config []byte) (Target, error) {
	var c mqttConfig
	if err := decodeStrict("mqtt", config, &c); err != nil {
		return nil, err
	}
	if len(c.URLs) == 0 {
		return nil, fmt.Errorf("mqtt config: at least one broker url is required")
	}
	t := MQTTTarget{Topic: c.Topic, QoS: 1, ClientID: c.ClientID, Username: c.Username}
	for i, raw := range c.URLs {
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("mqtt config: url[%d] is empty", i)
		}
		u, err := parseMQTTURL(raw)
		if err != nil {
			return nil, fmt.Errorf("mqtt config: url[%d]: %w", i, err)
		}
		t.Brokers = append(t.Brokers, u)
	}
	if strings.TrimSpace(c.Topic) == "" {
		return nil, fmt.Errorf("mqtt config: topic is required")
	}
	if c.QoS != nil {
		if *c.QoS < 0 || *c.QoS > 2 {
			return nil, fmt.Errorf("mqtt config: qos must be 0, 1, or 2, got %d", *c.QoS)
		}
		t.QoS = byte(*c.QoS)
	}
	return t, nil
}

// withMQTTSecret attaches the broker password. It is optional: an anonymous broker has
// none. A username with no password is a SUPPORTED shape too — brokers that take a token as
// the username — and it is deliberately not refused the way Kafka SASL (kafka.go) and AWS
// (aws.go) refuse a missing secret. TestMQTTUsernameWithoutPasswordIsAccepted pins it, so a
// later "consistency" change has to fail a test to reverse it.
func withMQTTSecret(t Target, secret string) (Target, error) {
	m := t.(MQTTTarget)
	m.Password = secret
	return m, nil
}
