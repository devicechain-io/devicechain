// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/devicechain-io/dc-edge-agent/config"
)

// Uplink is the cloud-facing MQTT connection: a paho client that publishes the
// golden path to the cloud broker exactly as a device would, so the cloud's own
// MQTT-gateway capture (ADR-030) ingests it with no platform-side publish path.
//
// Reconnect is hand-rolled with bounded backoff and paho auto-reconnect is OFF —
// the same discipline as sparkplug-ingest: the agent owns the connection lifecycle
// so a flapping WAN is not hammered and reconnect behaviour is observable rather
// than hidden inside the library.
type Uplink struct {
	broker         string // paho-normalised broker URL (tls:// → ssl://)
	clientID       string
	username       string
	password       string
	tlsConfig      *tls.Config
	connectTimeout time.Duration
	backoffMin     time.Duration
	backoffMax     time.Duration
	log            *slog.Logger

	mu     sync.RWMutex
	client mqtt.Client
}

// NewUplink resolves an Uplink from config: TLS is selected by URL scheme, the
// password is read once from the named environment variable (a projected Secret,
// never cleartext — the edge box has no ADR-059 store), and the client id is
// derived from the instance so it is stable across restarts.
func NewUplink(cfg config.UplinkConfiguration, instanceId, agentId string, log *slog.Logger) (*Uplink, error) {
	u, err := url.Parse(cfg.BrokerURL)
	if err != nil {
		return nil, fmt.Errorf("uplink.brokerUrl %q is not a valid URL: %w", cfg.BrokerURL, err)
	}

	// paho understands tcp:// and ssl://; normalise tls:// (which config.Validate
	// also accepts) onto ssl:// so the two schemes cannot drift.
	broker := cfg.BrokerURL
	var tlsConfig *tls.Config
	switch u.Scheme {
	case "ssl", "tls":
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		broker = "ssl://" + u.Host + u.Path
	case "tcp":
		// plaintext; no TLS config.
	default:
		return nil, fmt.Errorf("uplink.brokerUrl scheme %q unsupported (want tcp://, ssl://, or tls://)", u.Scheme)
	}

	var password string
	if cfg.PasswordEnv != "" {
		password = os.Getenv(cfg.PasswordEnv)
		if password == "" {
			return nil, fmt.Errorf("uplink.passwordEnv %q is set but that environment variable is empty (the uplink Secret was not projected)", cfg.PasswordEnv)
		}
	}

	return &Uplink{
		broker: broker,
		// Unique per edge box: two agents feeding one Instance must not share an
		// MQTT client id (takeover would boot each other in a loop). agentId is the
		// required, operator-set discriminator.
		clientID:       fmt.Sprintf("dc-edge-agent-%s-%s", instanceId, agentId),
		username:       cfg.Username,
		password:       password,
		tlsConfig:      tlsConfig,
		connectTimeout: time.Duration(cfg.ConnectTimeoutSeconds) * time.Second,
		backoffMin:     time.Duration(cfg.BackoffMinSeconds) * time.Second,
		backoffMax:     time.Duration(cfg.BackoffMaxSeconds) * time.Second,
		log:            log,
	}, nil
}

// clientOptions builds a fresh paho option set for one connection attempt. Auto-
// reconnect and connect-retry are OFF: this Uplink owns the reconnect loop.
func (up *Uplink) clientOptions() *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(up.broker)
	opts.SetClientID(up.clientID)
	if up.username != "" {
		opts.SetUsername(up.username)
	}
	if up.password != "" {
		opts.SetPassword(up.password)
	}
	if up.tlsConfig != nil {
		opts.SetTLSConfig(up.tlsConfig)
	}
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetConnectTimeout(up.connectTimeout)
	opts.SetCleanSession(true)
	return opts
}

// Run keeps the uplink connected until ctx is cancelled, reconnecting with bounded
// exponential backoff. It returns when ctx is done.
func (up *Uplink) Run(ctx context.Context) {
	backoff := up.backoffMin
	for {
		if err := up.connectOnce(ctx); err != nil {
			up.log.Warn("uplink connect failed", "broker", up.broker, "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > up.backoffMax {
				backoff = up.backoffMax
			}
			continue
		}
		up.log.Info("uplink connected", "broker", up.broker, "client_id", up.clientID)
		backoff = up.backoffMin

		// Hold the connection until it drops or ctx is cancelled.
		if up.waitUntilDisconnected(ctx) {
			return // ctx cancelled
		}
		up.log.Warn("uplink disconnected; will reconnect")
	}
}

// connectOnce attempts a single connection and, on success, records the client.
func (up *Uplink) connectOnce(ctx context.Context) error {
	client := mqtt.NewClient(up.clientOptions())
	tok := client.Connect()
	if !tok.WaitTimeout(up.connectTimeout) {
		client.Disconnect(0)
		return fmt.Errorf("connect timed out after %s", up.connectTimeout)
	}
	if err := tok.Error(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		client.Disconnect(0)
		return ctx.Err()
	}
	up.mu.Lock()
	up.client = client
	up.mu.Unlock()
	return nil
}

// waitUntilDisconnected blocks until the current client reports not-connected or
// ctx is cancelled. Returns true iff ctx was cancelled (a clean shutdown).
func (up *Uplink) waitUntilDisconnected(ctx context.Context) bool {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			up.disconnect()
			return true
		case <-ticker.C:
			up.mu.RLock()
			c := up.client
			up.mu.RUnlock()
			if c == nil || !c.IsConnectionOpen() {
				up.clearClient(c)
				return false
			}
		}
	}
}

// Publish forwards one event to the cloud on its golden-path topic at QoS 1,
// blocking until the broker PUBACKs or the timeout elapses. It returns an error
// when the uplink is not connected or the publish is not acked — the caller
// decides what to do with that (E1 forwards best-effort; E2 will withhold the
// local ack so the durable consumer redelivers).
func (up *Uplink) Publish(topic string, payload []byte) error {
	up.mu.RLock()
	c := up.client
	up.mu.RUnlock()
	if c == nil || !c.IsConnectionOpen() {
		return fmt.Errorf("uplink not connected")
	}
	tok := c.Publish(topic, 1 /* QoS 1 */, false /* not retained */, payload)
	if !tok.WaitTimeout(up.connectTimeout) {
		return fmt.Errorf("publish to %q timed out", topic)
	}
	return tok.Error()
}

// Connected reports whether the uplink currently has an open connection.
func (up *Uplink) Connected() bool {
	up.mu.RLock()
	c := up.client
	up.mu.RUnlock()
	return c != nil && c.IsConnectionOpen()
}

func (up *Uplink) disconnect() {
	up.mu.Lock()
	c := up.client
	up.client = nil
	up.mu.Unlock()
	if c != nil {
		c.Disconnect(250)
	}
}

// clearClient drops a client reference that has been observed disconnected.
func (up *Uplink) clearClient(c mqtt.Client) {
	up.mu.Lock()
	if up.client == c {
		up.client = nil
	}
	up.mu.Unlock()
}
