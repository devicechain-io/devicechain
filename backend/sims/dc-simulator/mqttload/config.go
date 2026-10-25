// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package mqttload drives device telemetry into the platform over MQTT, as a fleet of real
// devices would: one authenticated session per device, QoS 1 (or 0) publishes to the device's
// own events topic, every publish accounted for by what the BROKER said about it.
//
// It is the transport half of the load harness's MQTT mode. The storage verdict (what was
// PUBACKed against what the platform persisted) belongs to package loadtest, which reads the
// platform back over the tenant GraphQL API; this package only produces load and tells the
// truth about what the broker acknowledged.
//
// Three rules shape it, each learned from a measurement that was wrong without it:
//
//   - A COMPLETED PUBLISH TOKEN IS NOT A PUBACK. A publish counts as acknowledged only when the
//     session store saw its PUBACK (core/test/mqttack), never because paho completed a token.
//   - A DEVICE IS ONE SESSION. The broker confines a device credential to its own client id and
//     its own topic, so the harness cannot multiplex devices over a shared connection; the
//     connection count IS the device count.
//   - THE TRANSPORT FLAG MUST TELL THE TRUTH. Whether TLS was used is observed on the dialled
//     connection and reported, not inferred from a flag (an earlier round's --no-tls turned off
//     the ingress's HTTPS and left the broker's TLS on, and two rounds ran as if it were off).
package mqttload

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// Defaults for the knobs a Config leaves zero.
const (
	DefaultConnectTimeout     = 15 * time.Second
	DefaultAckTimeout         = 30 * time.Second
	DefaultKeepAlive          = 30 * time.Second
	DefaultConnectConcurrency = 64
	DefaultBackoffBase        = 500 * time.Millisecond
	DefaultBackoffMax         = 30 * time.Second
	DefaultMaxInflight        = 64
	// MaxMaxInflight is paho's outbound channel depth: more unacknowledged publishes per
	// session than this would block the sender inside Publish instead of being counted.
	MaxMaxInflight = 100
)

// Config is how a fleet connects and publishes.
type Config struct {
	// Broker is the gateway address, in the handshake's form: "ssl://host:1883",
	// "tcp://host:1883", "wss://host/mqtt". TLS decides the scheme actually dialled (see
	// ResolveBroker), so the address and the flag cannot disagree.
	Broker string
	// TLS is whether to dial TLS. The harness's default is true.
	TLS bool
	// TLSInsecure skips verification of the broker's certificate (a local bring-up's
	// self-signed gateway). It is only meaningful with TLS.
	TLSInsecure bool

	InstanceId string
	Tenant     string

	// QoS is 1 (PUBACKed delivery) or 0 (fire and forget).
	QoS byte
	// CleanSession starts every connection with no session state. False keeps the session
	// (and its unacknowledged publishes) across a reconnect, as a device that resumes would.
	CleanSession bool

	ConnectTimeout time.Duration
	// AckTimeout is how long a publish may wait for its PUBACK before it is counted
	// unconfirmed (ambiguous: it may still have been stored).
	AckTimeout time.Duration
	KeepAlive  time.Duration
	// ConnectConcurrency bounds simultaneous CONNECTs, so a fleet of thousands is ramped
	// rather than thrown at the broker (and a reconnect storm after a broker restart is
	// spread out by the same bound).
	ConnectConcurrency int
	// BackoffBase/BackoffMax bound the jittered exponential delay between reconnect attempts.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// MaxInflight bounds one session's unacknowledged publishes. A publish that would exceed
	// it is not sent and is counted (SkippedInflight), so a slow broker shows up as a shortfall
	// in offered load rather than as unbounded memory.
	MaxInflight int
}

// withDefaults fills zero knobs.
func (c Config) withDefaults() Config {
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = DefaultAckTimeout
	}
	if c.KeepAlive <= 0 {
		c.KeepAlive = DefaultKeepAlive
	}
	if c.ConnectConcurrency <= 0 {
		c.ConnectConcurrency = DefaultConnectConcurrency
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = DefaultBackoffBase
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = DefaultBackoffMax
	}
	if c.MaxInflight <= 0 {
		c.MaxInflight = DefaultMaxInflight
	}
	return c
}

// Validate rejects a configuration that cannot be run as asked.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Broker) == "" {
		return fmt.Errorf("no MQTT broker address: the handshake has no endpoints.mqttBroker and --mqtt-broker was not given")
	}
	resolved, err := ResolveBroker(c.Broker, c.TLS)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resolved, "tcp://") && !strings.HasPrefix(resolved, "ssl://") {
		return fmt.Errorf("MQTT broker %q is a websocket address: the harness dials tcp:// and ssl:// so that it can observe what it connected over", c.Broker)
	}
	if c.QoS > 1 {
		return fmt.Errorf("QoS %d is not supported: use 0 or 1", c.QoS)
	}
	if c.TLSInsecure && !c.TLS {
		return fmt.Errorf("--tls-insecure skips certificate verification and means nothing without TLS")
	}
	if err := core.ValidateToken(c.InstanceId); err != nil {
		return fmt.Errorf("instance id: %w", err)
	}
	if err := core.ValidateToken(c.Tenant); err != nil {
		return fmt.Errorf("tenant: %w", err)
	}
	if c.MaxInflight > MaxMaxInflight {
		return fmt.Errorf("max in-flight %d exceeds %d, the client's outbound queue depth", c.MaxInflight, MaxMaxInflight)
	}
	if c.ConnectTimeout < 0 || c.AckTimeout < 0 || c.KeepAlive < 0 || c.BackoffBase < 0 || c.BackoffMax < 0 ||
		c.ConnectConcurrency < 0 || c.MaxInflight < 0 {
		return fmt.Errorf("negative timeout, backoff, concurrency or in-flight bound")
	}
	if c.BackoffMax > 0 && c.BackoffBase > c.BackoffMax {
		return fmt.Errorf("backoff base %s exceeds backoff max %s", c.BackoffBase, c.BackoffMax)
	}
	return nil
}

// ResolveBroker returns the address to dial: the broker's host and port with the scheme TLS
// calls for. A "tcp://" (or "mqtt://") address with TLS on becomes "ssl://", an "ssl://" one
// with TLS off becomes "tcp://", and websocket addresses move between ws:// and wss://. The
// flag decides, so a handshake written for a TLS broker does not silently run plaintext (or
// the reverse) when the operator said otherwise; if the broker disagrees, the connect fails,
// which is the honest result.
func ResolveBroker(broker string, useTLS bool) (string, error) {
	raw := strings.TrimSpace(broker)
	if !strings.Contains(raw, "://") {
		raw = "tcp://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse MQTT broker %q: %w", broker, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("MQTT broker %q has no host", broker)
	}
	switch strings.ToLower(u.Scheme) {
	case "tcp", "mqtt", "ssl", "tls", "mqtts", "tcps", "mqtt+ssl":
		if useTLS {
			u.Scheme = "ssl"
		} else {
			u.Scheme = "tcp"
		}
	case "ws", "wss":
		if useTLS {
			u.Scheme = "wss"
		} else {
			u.Scheme = "ws"
		}
	default:
		return "", fmt.Errorf("MQTT broker %q has unsupported scheme %q (use tcp, ssl, ws or wss)", broker, u.Scheme)
	}
	return u.String(), nil
}
