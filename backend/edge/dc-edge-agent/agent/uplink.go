// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Uplink is the cloud-facing MQTT connection. The agent is a GATEWAY: it authenticates
// to the cloud broker as its own device (the gateway device's credential, through the
// broker's auth callout, with that device's plain MQTT client id) and publishes every
// local device's events in the gateway's own namespace,
// {instance}/{tenant}/gateways/{gateway}/devices/{child}/events, so the cloud's
// MQTT-gateway capture (ADR-030) ingests them with no platform-side publish path and the
// platform can check each child against the gateway's assignments.
//
// The only way to build an uplink topic is EventTopic, which takes a child token and
// nothing else: the agent cannot express a topic outside its own namespace, so the one
// failure the broker would hide (a publish outside the grant is PUBACKed and dropped,
// MQTT 3.1.1 having no way to refuse it) is not reachable from this code.
//
// Reconnect is hand-rolled with bounded backoff and paho auto-reconnect is OFF —
// the same discipline as sparkplug-ingest: the agent owns the connection lifecycle
// so a flapping WAN is not hammered and reconnect behaviour is observable rather
// than hidden inside the library.
type Uplink struct {
	broker         string // paho-normalised broker URL (tls:// → ssl://)
	instanceID     string
	tenant         string
	gateway        string // the gateway device's token (this agent's identity)
	clientID       string
	username       string
	password       string
	tlsConfig      *tls.Config
	connectTimeout time.Duration
	backoffMin     time.Duration
	backoffMax     time.Duration
	log            *slog.Logger

	// noticesTopic is the MQTT topic the platform tells this gateway about refused
	// publishes on; onNotice receives each one. Both are set before Run.
	noticesTopic string
	onNotice     func(messaging.GatewayNotice)

	mu     sync.RWMutex
	client mqtt.Client
}

// uplinkSubscribeTimeout bounds the notices SUBSCRIBE at connect.
const uplinkSubscribeTimeout = 10 * time.Second

// NewUplink resolves an Uplink from config: TLS is selected by URL scheme, the
// password is read once from the named environment variable (a projected Secret,
// never cleartext — the edge box has no ADR-059 store), and the client id is the
// gateway device's plain client id, so it is stable across restarts.
func NewUplink(cfg config.UplinkConfiguration, instanceId string, log *slog.Logger) (*Uplink, error) {
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

	// The plain client id of the gateway DEVICE. The broker's callout admits only the
	// authenticating device's own id, and the id is the same one that device's presence is
	// asserted under, so it is deliberately NOT discriminated per agent: two agents
	// authenticating as one gateway device would take each other's session over, which is
	// the right failure for a duplicated gateway identity.
	clientID, err := messaging.DeviceClientID(instanceId, cfg.Tenant, cfg.GatewayToken)
	if err != nil {
		return nil, fmt.Errorf("uplink identity: %w", err)
	}

	return &Uplink{
		broker:         broker,
		instanceID:     instanceId,
		tenant:         cfg.Tenant,
		gateway:        cfg.GatewayToken,
		clientID:       clientID,
		username:       cfg.Username,
		password:       password,
		tlsConfig:      tlsConfig,
		connectTimeout: time.Duration(cfg.ConnectTimeoutSeconds) * time.Second,
		backoffMin:     time.Duration(cfg.BackoffMinSeconds) * time.Second,
		backoffMax:     time.Duration(cfg.BackoffMaxSeconds) * time.Second,
		log:            log,
		noticesTopic:   messaging.SubjectToMqttTopic(messaging.GatewayNoticesSubject(instanceId, cfg.Tenant, cfg.GatewayToken)),
	}, nil
}

// EventTopic is the ONE constructor of an uplink topic. It accepts a child device token
// and builds that child's events topic inside this gateway's own namespace. It refuses
// anything that is not a plain child token (a metacharacter would escape the namespace)
// and the gateway's own token (a gateway is not its own child; the platform refuses it
// too, and refusing it here keeps it out of the cloud and lets the caller keep it).
func (up *Uplink) EventTopic(child string) (string, error) {
	if err := core.ValidateToken(child); err != nil {
		return "", fmt.Errorf("not a child device token: %w", err)
	}
	if child == up.gateway {
		return "", ErrGatewayAddressedItself
	}
	return messaging.SubjectToMqttTopic(messaging.GatewayEventsSubject(up.instanceID, up.tenant, up.gateway, child)), nil
}

// ErrGatewayAddressedItself is returned by EventTopic for the gateway's own token.
var ErrGatewayAddressedItself = errors.New("the gateway's own token is not a child of itself")

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
	// The session is clean, so the notices subscription is made on EVERY connect. It is
	// confirmed (a refused subscription leaves paho's error nil): a gateway that cannot
	// hear its refusals is a gateway whose silent loss is back, so a refusal here fails
	// the connect, loudly, and the backoff retries.
	if err := messaging.SubscribeMqttConfirmed(client, up.noticesTopic, 0, up.handleNotice, uplinkSubscribeTimeout); err != nil {
		client.Disconnect(0)
		return fmt.Errorf("subscribe to gateway notices: %w (does the platform grant this gateway device its notices subject? the broker and the platform must both be upgraded before an agent uses the gateway uplink)", err)
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

// Begin starts one QoS 1 publish and returns a function that waits for its PUBACK (or the
// timeout) and reports the outcome. Several can be in flight at once: the caller bounds the
// window, and acks its spool message on that message's own PUBACK. It returns an error at
// once when the uplink is not connected.
//
// A PUBACK says the broker took the message, not that the platform accepted the event
// (MQTT 3.1.1 cannot refuse a publish). A refusal by the platform comes back as a notice.
func (up *Uplink) Begin(topic string, payload []byte) (wait func() error, err error) {
	up.mu.RLock()
	c := up.client
	up.mu.RUnlock()
	if c == nil || !c.IsConnectionOpen() {
		return nil, fmt.Errorf("uplink not connected")
	}
	tok := c.Publish(topic, 1 /* QoS 1 */, false /* not retained */, payload)
	return func() error {
		if !tok.WaitTimeout(up.connectTimeout) {
			return fmt.Errorf("publish to %q timed out", topic)
		}
		return tok.Error()
	}, nil
}

// handleNotice decodes one notice and hands it to the agent. A notice that does not decode
// is logged and dropped: it is best-effort by design and carries no payload to lose.
func (up *Uplink) handleNotice(_ mqtt.Client, m mqtt.Message) {
	var n messaging.GatewayNotice
	if err := json.Unmarshal(m.Payload(), &n); err != nil {
		up.log.Warn("gateway notice did not decode", "err", err, "topic", m.Topic())
		return
	}
	if up.onNotice != nil {
		up.onNotice(n)
	}
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
