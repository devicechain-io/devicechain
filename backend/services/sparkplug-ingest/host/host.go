// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package host is the Sparkplug B Host Application runtime (ADR-069). A Client is
// one tenant-bound connection: it connects OUT to a customer's MQTT broker,
// announces its Sparkplug 3.0 STATE (with a per-session, timestamp-matched OFFLINE
// Last-Will), subscribes to the configured Sparkplug groups, validates each
// edge-node/device stream through the SP2 session machine, and decodes + logs the
// traffic it receives. A Manager runs one Client per configured source and
// attributes every message on a connection to that source's tenant
// (connection-scoped tenancy, ADR-069 M7 — the tenant is fixed by which broker the
// message arrived on, never parsed from the untrusted Sparkplug topic). It emits
// nothing downstream yet — identity mapping and presence/measurement emission
// arrive in SP3b/SP4.
package host

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

const (
	// connectTimeout bounds a single connection attempt; the reconnect loop owns
	// the retry cadence.
	connectTimeout = 15 * time.Second
	// publishTimeout / subscribeTimeout bound the STATE publish and the group
	// subscribes done inside the connect handler.
	publishTimeout   = 5 * time.Second
	subscribeTimeout = 10 * time.Second
	// disconnectQuiesceMs is how long paho waits for in-flight work before
	// closing the connection on a graceful stop.
	disconnectQuiesceMs = 250
	// minReconnectBackoff / maxReconnectBackoff bound the reconnect backoff. We
	// own reconnection (rather than paho's SetAutoReconnect) so that each new
	// session gets a fresh STATE timestamp — see sessionOptions.
	minReconnectBackoff = 1 * time.Second
	maxReconnectBackoff = 30 * time.Second
	// rebirthQueueDepth bounds the buffered rebirth requests awaiting publish. The
	// per-node backoff already collapses storms, so this only absorbs a fan-out
	// across many nodes at once; a full queue drops (re-requested next window).
	rebirthQueueDepth = 256
)

// Broker is the resolved MQTT connection for one source: a customer-operated
// Sparkplug broker the adapter connects OUT to.
type Broker struct {
	URL      string      // e.g. tcp://broker.plant-a.example:1883 or ssl://... for TLS
	ClientID string      // unique per Host Application connection (a broker drops a dup id)
	Username string      // MQTT login; empty ⇒ anonymous
	Password string      // resolved from SourceBroker.PasswordEnv; empty ⇒ none
	TLS      *tls.Config // non-nil ⇒ the URL uses the ssl:// scheme
}

// Metrics are the optional Prometheus counters the receive path updates. Any nil
// field is skipped, so the host is usable in tests without a registry.
type Metrics struct {
	Messages        *prometheus.CounterVec // labeled "type" (NBIRTH/NDATA/STATE/…)
	DecodeErrors    prometheus.Counter
	RebirthRequests prometheus.Counter // rebirth NCMDs the session machine emitted
	// ConnectFailures counts failed broker connect attempts across all sources.
	// Readiness is intentionally NOT gated on broker reachability (a customer broker
	// outage must not kill the pod), so this counter is the machine-readable signal
	// that distinguishes a broken config (a bad host or rejected credential makes it
	// climb) from a healthy but idle deployment (it stays flat, as does Messages).
	ConnectFailures prometheus.Counter
}

// Client is a single tenant-bound Sparkplug Host Application connection. Build it
// with NewClient, then Connect / Stop it (the Manager owns one Client per
// configured source). Every message this connection receives is attributed to
// tenant — connection-scoped tenancy (ADR-069 M7): the tenant is fixed here, never
// derived from the untrusted, non-unique Sparkplug topic.
type Client struct {
	tenant  string
	broker  Broker
	groups  []string
	metrics Metrics

	stateTopic string
	// sessions is the SP2 per-node Sparkplug session machine; it validates the
	// stream and enqueues a rebirth when a node must re-emit its births.
	sessions *SessionTracker
	// rebirthCh carries rebirth requests from the (ordered, must-not-block) message
	// handler to the async publisher goroutine. Buffered so a burst never blocks
	// receive; a full channel drops (the per-node backoff re-requests next window).
	rebirthCh chan nodeKey
	// now supplies the session timestamp; injected so the STATE timestamp is
	// deterministic under test.
	now func() time.Time
	// newClient builds the underlying MQTT client; injected so tests can supply a
	// fake without a broker.
	newClient func(*mqtt.ClientOptions) mqtt.Client

	mu        sync.Mutex
	mc        mqtt.Client
	sessionTs int64 // the current session's STATE timestamp (for a graceful OFFLINE)
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewClient builds a tenant-bound Host Application client from one configured
// source and its resolved broker connection. now is the clock supplying each
// session's STATE timestamp (pass time.Now in production); nil defaults to it.
func NewClient(source config.SparkplugSource, broker Broker, now func() time.Time, metrics Metrics) *Client {
	if now == nil {
		now = time.Now
	}
	c := &Client{
		tenant:     source.Tenant,
		broker:     broker,
		groups:     append([]string(nil), source.Groups...),
		metrics:    metrics,
		stateTopic: StateTopic(source.HostId),
		now:        now,
		newClient:  func(o *mqtt.ClientOptions) mqtt.Client { return mqtt.NewClient(o) },
		rebirthCh:  make(chan nodeKey, rebirthQueueDepth),
	}
	c.sessions = NewSessionTracker(now, defaultRebirthBackoff, c.enqueueRebirth)
	return c
}

// subscriptions is the set of topic filters this host subscribes to: one per
// configured group, or the all-groups wildcard when none is configured.
func (c *Client) subscriptions() []string {
	if len(c.groups) == 0 {
		return []string{Namespace + "/#"}
	}
	filters := make([]string, len(c.groups))
	for i, g := range c.groups {
		filters[i] = Namespace + "/" + g + "/#"
	}
	return filters
}

// statePayload is a Sparkplug 3.0 STATE body stamped with a session timestamp.
func statePayload(online bool, sessionTs int64) []byte {
	b, _ := State{Online: online, Timestamp: sessionTs}.Marshal()
	return b
}

// Connect starts the reconnect loop and returns immediately. It does NOT block on
// the broker being reachable: the loop retries with backoff, so a briefly-absent
// (or permanently misconfigured) broker degrades this one source rather than
// failing startup. Service readiness is owned by the Manager, not gated on any
// broker connecting.
func (c *Client) Connect() {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	c.wg.Add(2)
	go c.runLoop(ctx)
	go c.rebirthWorker(ctx)
}

// runLoop owns the connection lifecycle. Each iteration is one MQTT session with
// its OWN fresh STATE timestamp: this is why paho's auto-reconnect is off (it
// would reuse the fixed Last-Will and freeze the timestamp, breaking the
// Sparkplug 3.0 per-session freshness B4 depends on). On a lost connection it
// reconnects with a new timestamp; on ctx cancellation (Stop) it exits.
func (c *Client) runLoop(ctx context.Context) {
	defer c.wg.Done()
	backoff := minReconnectBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		sessionTs := c.now().UnixMilli()
		lost := make(chan struct{}, 1)
		client := c.newClient(c.sessionOptions(sessionTs, lost))

		if err := mqtt.WaitTokenTimeout(client.Connect(), connectTimeout); err != nil {
			if c.metrics.ConnectFailures != nil {
				c.metrics.ConnectFailures.Inc()
			}
			log.Warn().Err(err).Str("tenant", c.tenant).Str("broker", c.broker.URL).Dur("retry_in", backoff).
				Msg("Broker connect failed; retrying.")
			client.Disconnect(0)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		// Connected: the OnConnect handler (in the options) has subscribed and
		// announced ONLINE.
		c.setSession(client, sessionTs)
		backoff = minReconnectBackoff

		select {
		case <-lost:
			log.Warn().Str("tenant", c.tenant).Str("broker", c.broker.URL).Msg("Broker connection lost; reconnecting with a fresh session.")
		case <-ctx.Done():
			return
		}
	}
}

// sessionOptions builds the paho options for one session, stamping BOTH the
// OFFLINE Last-Will and (via the connect handler) the ONLINE birth with the same
// sessionTs — the Sparkplug 3.0 timestamp match (B4) an edge node uses to reject
// a stale OFFLINE from a prior session.
func (c *Client) sessionOptions(sessionTs int64, lost chan struct{}) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(c.broker.URL)
	opts.SetClientID(c.broker.ClientID)
	if c.broker.Username != "" {
		opts.SetUsername(c.broker.Username)
		opts.SetPassword(c.broker.Password)
	}
	if c.broker.TLS != nil {
		opts.SetTLSConfig(c.broker.TLS)
	}
	// A Host Application uses a CLEAN session (M8): it owns no durable per-node
	// subscription state across connections — on reconnect it re-subscribes and
	// re-announces STATE, and rebuilds node state from births.
	opts.SetCleanSession(true)
	// We own reconnection so each session refreshes the will+birth timestamp.
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetConnectTimeout(connectTimeout)
	// Ordered delivery is REQUIRED by the SP2 session machine: it validates a
	// strict per-node seq (next == last+1), so paho's unordered mode (a goroutine
	// per message) would reorder a healthy stream into a false gap and rebirth a
	// healthy node. With ordering on, handlers run sequentially on paho's receive
	// goroutine — which is why the rebirth publish MUST be async (rebirthCh), never
	// blocking that goroutine on a broker round-trip.
	opts.SetOrderMatters(true)
	// B4: OFFLINE STATE Last-Will, retained + QoS 1, timestamp-matched to ONLINE.
	opts.SetBinaryWill(c.stateTopic, statePayload(false, sessionTs), 1, true)
	opts.SetOnConnectHandler(func(client mqtt.Client) { c.onConnected(client, sessionTs) })
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Warn().Err(err).Str("tenant", c.tenant).Str("broker", c.broker.URL).Msg("MQTT connection to the broker dropped.")
		select {
		case lost <- struct{}{}:
		default:
		}
	})
	return opts
}

// onConnected fires on each successful connection. It subscribes BEFORE
// announcing ONLINE (Sparkplug session order is subscribe-then-birth: an edge
// node that sees us go online may flush buffered data immediately, which a
// clean-session client with no live subscription would lose).
func (c *Client) onConnected(client mqtt.Client, sessionTs int64) {
	// Record the live client before any traffic can arrive, so a rebirth triggered
	// by the first inbound message publishes over this connection rather than a
	// stale one (runLoop also sets it once Connect returns — this just closes the
	// window between subscribe and that assignment).
	c.setSession(client, sessionTs)
	for _, filter := range c.subscriptions() {
		if err := mqtt.WaitTokenTimeout(client.Subscribe(filter, 1, c.onMessage), subscribeTimeout); err != nil {
			log.Error().Err(err).Str("tenant", c.tenant).Str("filter", filter).Msg("Failed to subscribe to Sparkplug group traffic.")
			continue
		}
		log.Info().Str("tenant", c.tenant).Str("filter", filter).Msg("Subscribed to Sparkplug group traffic.")
	}

	if err := mqtt.WaitTokenTimeout(client.Publish(c.stateTopic, 1, true, statePayload(true, sessionTs)), publishTimeout); err != nil {
		log.Error().Err(err).Str("tenant", c.tenant).Str("topic", c.stateTopic).Msg("Failed to publish ONLINE STATE.")
	} else {
		log.Info().Str("tenant", c.tenant).Str("topic", c.stateTopic).Int64("timestamp", sessionTs).Msg("Announced Host Application ONLINE.")
	}
}

// onMessage decodes and logs one received Sparkplug message. A parse/decode
// failure is counted and logged, never allowed to silently look like a clean
// message.
func (c *Client) onMessage(_ mqtt.Client, msg mqtt.Message) {
	rec, err := Describe(msg.Topic(), msg.Payload())
	if err != nil {
		if c.metrics.DecodeErrors != nil {
			c.metrics.DecodeErrors.Inc()
		}
		log.Warn().Err(err).Str("topic", msg.Topic()).Msg("Dropping undecodable Sparkplug message.")
		return
	}
	// A Host issues commands (NCMD/DCMD), it never consumes them; the group
	// wildcard subscription echoes the Host's own rebirth commands back (MQTT
	// 3.1.1 has no NoLocal), so drop them rather than count/log them as inbound
	// device traffic (the same self-ingest class as event-sources #458).
	if rec.Topic.MessageType == NCMD || rec.Topic.MessageType == DCMD {
		return
	}
	if c.metrics.Messages != nil {
		c.metrics.Messages.WithLabelValues(rec.Label()).Inc()
	}
	// Advance the SP2 session machine; it may command the node to rebirth (fired
	// via requestRebirth, outside its lock). STATE has no node session.
	if !rec.Topic.IsState && rec.Payload != nil {
		c.sessions.Observe(rec.Topic, rec.Payload)
	}
	c.logRecord(rec)
}

// enqueueRebirth is the session machine's rebirth callback. It runs on paho's
// ordered receive goroutine, so it must NOT block: it hands the request to the
// async publisher via a buffered channel and returns. A full channel is dropped
// (the per-node backoff re-requests on the next window), logged so saturation is
// visible.
func (c *Client) enqueueRebirth(group, node string) {
	select {
	case c.rebirthCh <- nodeKey{group, node}:
	default:
		log.Warn().Str("tenant", c.tenant).Str("group", group).Str("node", node).Msg("Rebirth queue full; dropping request.")
	}
}

// rebirthWorker drains the rebirth channel and publishes each command, off the
// receive goroutine, until the context is cancelled.
func (c *Client) rebirthWorker(ctx context.Context) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-c.rebirthCh:
			c.publishRebirth(key.group, key.node)
		}
	}
}

// publishRebirth publishes an NCMD "Node Control/Rebirth"=true to an edge node,
// the Sparkplug mechanism for asking a node to re-emit its NBIRTH (and DBIRTHs)
// after the Host detects a gap it cannot reconcile. Per Sparkplug B 3.0, NCMD is
// QoS 0, retain false (only STATE is QoS 1 + retained). A rebirth for a node the
// Host is not currently connected to is dropped (the node is re-validated on its
// next birth after reconnect). The metric counts commands actually put on the
// wire.
func (c *Client) publishRebirth(group, node string) {
	c.mu.Lock()
	client := c.mc
	c.mu.Unlock()
	if client == nil || !client.IsConnected() {
		log.Warn().Str("tenant", c.tenant).Str("group", group).Str("node", node).Msg("Not connected; dropping rebirth command.")
		return
	}
	topic := Namespace + "/" + group + "/" + string(NCMD) + "/" + node
	payload, err := rebirthCommand(c.now().UnixMilli())
	if err != nil {
		log.Error().Err(err).Str("group", group).Str("node", node).Msg("Failed to encode rebirth command.")
		return
	}
	if err := mqtt.WaitTokenTimeout(client.Publish(topic, 0, false, payload), publishTimeout); err != nil {
		log.Error().Err(err).Str("topic", topic).Msg("Failed to publish rebirth command.")
		return
	}
	if c.metrics.RebirthRequests != nil {
		c.metrics.RebirthRequests.Inc()
	}
	log.Warn().Str("tenant", c.tenant).Str("group", group).Str("node", node).Msg("Commanded Sparkplug node rebirth.")
}

// logRecord emits the structured per-message log line for the decode+log scope,
// tagged with the source's tenant so attribution is visible per connection.
func (c *Client) logRecord(rec Record) {
	if rec.Topic.IsState {
		e := log.Info().Str("tenant", c.tenant).Str("type", rec.Label()).Str("host", rec.Topic.HostID)
		if rec.StateOnline != nil {
			e = e.Bool("online", *rec.StateOnline)
		}
		e.Int64("timestamp", int64(rec.PayloadTimestamp)).Msg("Received Host STATE.")
		return
	}
	e := log.Info().Str("tenant", c.tenant).Str("type", rec.Label()).Str("group", rec.Topic.GroupID).Str("node", rec.Topic.EdgeNodeID)
	if rec.Topic.IsDevice() {
		e = e.Str("device", rec.Topic.DeviceID)
	}
	if rec.HasSeq {
		e = e.Uint64("seq", rec.Seq)
	}
	e.Int("metrics", rec.MetricCount).Msg("Received Sparkplug message.")
}

// Stop cancels the reconnect loop, announces a graceful OFFLINE STATE (a clean
// disconnect does NOT fire the Last-Will, so the host must publish its own death,
// timestamp-matched to the current session), and closes the connection.
func (c *Client) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	client := c.mc
	sessionTs := c.sessionTs
	c.mu.Unlock()

	if client != nil && client.IsConnected() {
		if err := mqtt.WaitTokenTimeout(client.Publish(c.stateTopic, 1, true, statePayload(false, sessionTs)), publishTimeout); err != nil {
			log.Warn().Err(err).Msg("Failed to publish graceful OFFLINE STATE.")
		}
		client.Disconnect(disconnectQuiesceMs)
	}
	c.wg.Wait()
}

// setSession records the live client and its session timestamp.
func (c *Client) setSession(client mqtt.Client, sessionTs int64) {
	c.mu.Lock()
	c.mc = client
	c.sessionTs = sessionTs
	c.mu.Unlock()
}

// nextBackoff doubles the backoff up to the ceiling.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxReconnectBackoff {
		return maxReconnectBackoff
	}
	return d
}

// sleep waits for d or ctx cancellation; it returns false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
