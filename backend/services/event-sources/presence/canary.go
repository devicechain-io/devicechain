// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// CanaryTenant is the reserved tenant token the canary's MQTT client id is built
// under. It exists so the canary presents a client id of exactly the shape a real
// device presents — which is what lets it prove the whole classification chain,
// including that the running instance id is the one devices actually connect with —
// while still being identifiable as not-a-device.
//
// 🔴 IT IS RESERVED BY CONVENTION, NOT BY THE GRAMMAR. A tenant token is any
// [A-Za-z0-9][A-Za-z0-9_-]* string, so nothing prevents an operator creating a tenant
// with this token; if one did, that tenant's devices would be skipped by the tap and
// their presence would stay inferred. The device-token prefix below is required as
// well, which narrows the collision to a tenant of this name owning devices whose
// tokens also start with dc-canary-.
const CanaryTenant = "dc-presence-canary"

// canaryDevicePrefix is the second half of the canary's identity. Both must match for
// an advisory to be treated as the canary's own.
const canaryDevicePrefix = "dc-canary-"

// IsCanary reports whether a parsed client id is the presence canary rather than a
// device. Checked AFTER the parse, deliberately: a canary that failed to parse would
// prove only that an advisory arrived, and the failure this instrument exists to catch
// includes a misconfigured instance id, which makes every real device's id fail the
// parse while the tap looks perfectly healthy.
func IsCanary(parts messaging.DeviceClientIDParts) bool {
	return parts.Tenant == CanaryTenant && len(parts.DeviceToken) > len(canaryDevicePrefix) &&
		parts.DeviceToken[:len(canaryDevicePrefix)] == canaryDevicePrefix
}

// CanaryClientID is the MQTT client id this replica's canary connects with. The
// replica name makes it unique, so N replicas' canaries do not take each other's
// connections over (a duplicate client id is a takeover — measured).
func CanaryClientID(instanceId, replica string) (string, error) {
	return messaging.DeviceClientID(instanceId, CanaryTenant, canaryDevicePrefix+replica)
}

// CanaryMetrics are the negative control's signals.
type CanaryMetrics struct {
	// Observed counts canary connects seen on the tap's own subscription.
	Observed prometheus.Counter
	// Missed counts canary connects that did NOT arrive within the deadline. This is
	// the alarmable one: it means this replica opened an MQTT connection to the broker
	// and never saw the broker announce it.
	Missed prometheus.Counter
}

// Canary is the tap's negative control.
//
// 🔴 THE OBVIOUS INSTRUMENT CANNOT FAIL, WHICH IS WHY THIS EXISTS. A long-lived MQTT
// fleet legitimately produces zero connect advisories for days, so a "presence events
// emitted" counter reads identically whether the subscription is healthy or silently
// dead — the reading is the same in both worlds, so it measures nothing. The canary
// makes the healthy and broken cases differ: it OPENS a connection, so an advisory is
// owed, and its absence is a fact rather than an absence of traffic.
//
// 🔴 IT SUBSCRIBES ON ITS OWN, NOT THROUGH THE TAP'S QUEUE GROUP. The queue group
// delivers each advisory to exactly ONE replica (measured), so a replica's own canary
// connect is usually handled by a different replica — and "alarm if I do not observe
// my own connect" would then page continuously on a perfectly healthy tap. A plain
// subscription receives every advisory, so each replica can observe its own.
//
// What it proves, end to end: the MQTT gateway is reachable, the SYS credential
// authenticates, the subject name is right, the broker still publishes an advisory for
// an MQTT connect, that advisory carries the client id under the `client_id` key, and
// the parse admits a real device-shaped id.
//
// What it does NOT prove, stated because the tempting claim is false: that the running
// instance id is the one devices connect under. The canary builds its client id from
// the SAME instance id it later parses against, so the two agree by construction and a
// wrong value is self-consistently wrong. Nor does it say anything about queue-group
// behaviour under a broker leader change. Both stay open questions rather than ones
// this instrument pretends to answer.
type Canary struct {
	instanceId string
	replica    string
	brokerURL  string
	user       string
	password   string
	// tls is the broker's client TLS configuration, or nil for a plaintext broker.
	//
	// 🔴 ITS ABSENCE IS A STANDING FALSE ALARM, NOT A MISSING FEATURE. The broker
	// terminates TLS on the MQTT gateway by default, with a certificate signed by a
	// PRIVATE per-instance CA that only the instance config carries. A probe that dials
	// ssl:// without this verifies against the system roots, fails
	// x509-unknown-authority every time, and increments the one metric an operator is
	// told to alarm on — so the negative control reports the tap dead on every healthy
	// instance, which is worse than having no canary at all.
	tls      *tls.Config
	metrics  CanaryMetrics
	deadline time.Duration

	mu   sync.Mutex
	want string
	seen chan struct{}
}

// NewCanary builds the negative control. brokerURL is the MQTT gateway URL and
// user/password the static service credential — which is in the callout's auth_users,
// so it bypasses the device callout and may present a client id of its own choosing.
func NewCanary(instanceId, replica, brokerURL, user, password string, tlsCfg *tls.Config,
	metrics CanaryMetrics, deadline time.Duration) *Canary {
	return &Canary{instanceId: instanceId, replica: replica, brokerURL: brokerURL,
		user: user, password: password, tls: tlsCfg, metrics: metrics, deadline: deadline}
}

// Watch binds the canary's own plain subscription to the connect advisories.
func (c *Canary) Watch(nc *nats.Conn) (func(), error) {
	sub, err := nc.Subscribe(natsauth.SysConnectSubject, func(m *nats.Msg) { c.observe(m.Data) })
	if err != nil {
		return nil, fmt.Errorf("subscribing the presence canary: %w", err)
	}
	if err := nc.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("flushing the presence canary subscription: %w", err)
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// observe records a canary connect if it is the one this replica is waiting for.
func (c *Canary) observe(raw []byte) {
	adv := advisory{}
	if err := json.Unmarshal(raw, &adv); err != nil || adv.Client.MQTTClient == "" {
		return
	}
	// Parsed with the SAME function the tap uses. That is the point of routing the
	// canary through a real device-shaped id rather than an obviously-not-a-device
	// one: the parse is on the path every real advisory takes, so a change that broke
	// it would break the canary too. (It cannot detect a WRONG instance id — the
	// canary presents and parses the same value — see the type's doc.)
	parts, err := messaging.ParseDeviceClientIDFor(c.instanceId, adv.Client.MQTTClient)
	if err != nil || !IsCanary(parts) {
		return
	}
	c.mu.Lock()
	want, ch := c.want, c.seen
	c.mu.Unlock()
	if want == "" || adv.Client.MQTTClient != want {
		return // another replica's canary
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Probe opens an MQTT connection under this replica's canary id and waits to observe
// the broker's own announcement of it. It returns nil when the chain is proven.
func (c *Canary) Probe(ctx context.Context) error {
	clientID, err := CanaryClientID(c.instanceId, c.replica)
	if err != nil {
		return fmt.Errorf("building the canary client id: %w", err)
	}
	seen := make(chan struct{}, 1)
	c.mu.Lock()
	c.want, c.seen = clientID, seen
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.want, c.seen = "", nil
		c.mu.Unlock()
	}()

	opts := mqtt.NewClientOptions()
	opts.AddBroker(c.brokerURL)
	opts.SetClientID(clientID)
	opts.SetUsername(c.user)
	opts.SetPassword(c.password)
	opts.SetAutoReconnect(false)
	opts.SetConnectTimeout(c.deadline)
	if c.tls != nil {
		opts.SetTLSConfig(c.tls)
	}
	client := mqtt.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(c.deadline) {
		// Torn down even though the connect never completed: paho may still finish it
		// afterwards, and an abandoned connection lingers until the next probe's
		// duplicate client id evicts it.
		client.Disconnect(0)
		c.miss("the canary could not connect to the broker within the deadline")
		return fmt.Errorf("canary MQTT connect timed out after %s", c.deadline)
	}
	if err := tok.Error(); err != nil {
		c.miss("the canary was refused by the broker")
		return fmt.Errorf("canary MQTT connect refused: %w", err)
	}
	// Disconnected on the way out either way: the connection has served its purpose
	// once the advisory is observed, and leaving it open would accumulate one idle
	// connection per probe.
	defer client.Disconnect(100)

	select {
	case <-seen:
		if c.metrics.Observed != nil {
			c.metrics.Observed.Inc()
		}
		return nil
	case <-time.After(c.deadline):
		c.miss("the broker never announced the canary's own connection")
		return fmt.Errorf("no connect advisory for the canary within %s", c.deadline)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Canary) miss(reason string) {
	if c.metrics.Missed != nil {
		c.metrics.Missed.Inc()
	}
	log.Error().Str("replica", c.replica).Str("instance", c.instanceId).
		Msg("Broker presence canary FAILED: " + reason +
			". MQTT device presence is not being observed; devices will read as inferred.")
}
