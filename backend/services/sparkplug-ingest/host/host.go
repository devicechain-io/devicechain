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
	"strings"
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
	// ingestAttemptTimeout bounds a single resolve+emit attempt (a device-management
	// lookup/create plus the JetStream write). ingestMaxAttempts / ingestRetryBackoff
	// bound the in-handler retry: a transient device-management or NATS blip is
	// retried so a clean-session Host (which gets NO broker redelivery) does not lose
	// the sample, but the total is bounded so a prolonged outage degrades into the
	// Sparkplug host-offline path (a stalled handler starves paho's keepalive → the
	// broker fires the OFFLINE will → edge nodes store-and-forward) instead of
	// blocking the ordered receive goroutine forever.
	ingestAttemptTimeout = 3 * time.Second
	ingestMaxAttempts    = 3
	ingestRetryBackoff   = 500 * time.Millisecond
	// reconcileProbeWindow is how long the failover reconciliation waits after
	// rebirthing a tenant's asserted-active nodes before it declares the still-silent
	// ones dead (ADR-067 SP4b). It must comfortably exceed a healthy node's
	// rebirth→NBIRTH round-trip so a live-but-slow node is not falsely disconnected
	// (and if one is, its later NBIRTH — a higher epoch — supersedes the timeout).
	reconcileProbeWindow = 10 * time.Second
	// reconcileQueryTimeout bounds the device-state read the reconciliation runs BEFORE
	// subscribing (to floor the epoch generator). It caps how long a slow/absent
	// device-state can stall connection setup — on timeout the floor+probe are skipped
	// this cycle, not the connection.
	reconcileQueryTimeout = 5 * time.Second
)

// Broker is the resolved MQTT connection for one source: a customer-operated
// Sparkplug broker the adapter connects OUT to.
type Broker struct {
	URL string // e.g. tcp://broker.plant-a.example:1883 or ssl://... for TLS
	// ClientID is stable per (instance, source) and therefore IDENTICAL across every
	// replica of the instance — a deliberate leader-election invariant, not just
	// per-source uniqueness. Only the lease holder connects (ADR-070), but in the
	// brief handover window the new leader's connect carries the same ClientID as the
	// old leader's lingering connection, so the broker's dup-id takeover-kick
	// disconnects the zombie (and fires its will) before the new ONLINE lands. A
	// per-pod-unique id would DEFEAT that kick — do not make it unique per replica.
	ClientID string
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
	// IngestFailures counts accepted messages whose samples were dropped after the
	// in-handler retry budget was exhausted (device-management or NATS unreachable).
	// A clean-session Host gets no broker redelivery, so this is real (bounded) loss —
	// the signal that ingest, not just connectivity, is degraded.
	IngestFailures prometheus.Counter
}

// SampleIngester turns an accepted message's samples into durable telemetry
// (resolve/register the device, then emit) and its presence transitions into durable
// StateChange events (ADR-067). The Client depends on the interface so a host test
// can drive the full receive→ingest path with a fake, asserting what would be
// ingested without a device-management or a NATS. A nil ingester makes the receive
// path decode+log only (the SP1/SP2 behavior).
type SampleIngester interface {
	Ingest(ctx context.Context, tenant string, policy IngestPolicy, externalId string, samples []Sample) error
	IngestPresence(ctx context.Context, tenant string, policy IngestPolicy, events []PresenceEvent) error
}

// reconcileSource reads the device-state presence projection for the failover
// reconciliation (ADR-067 SP4b). *Reconciler implements it; the Client depends on the
// interface so a host test can drive reconcile with a fake, without a device-state.
type reconcileSource interface {
	AssertedActive(ctx context.Context, tenant, source string) ([]AssertedDevice, uint64, error)
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

	// ingester + policy turn accepted messages into durable telemetry. ingester is
	// shared across all connections; policy is this source's (tenant is attributed
	// per connection, ADR-069 M7). A nil ingester leaves the receive path decode-only.
	ingester SampleIngester
	policy   IngestPolicy

	// reconciler reads the device-state presence projection to repopulate presence on
	// (re)connect (ADR-067 SP4b failover reconciliation). Nil disables reconciliation
	// (decode-only tests, or a build without the device-state coordinate); shared across
	// connections. reconciling guards against overlapping reconcile goroutines when the
	// connection flaps. seen (guarded by seenMu) records the external ids observed since
	// the current reconcile began, so the probe can tell a live node from a dead one.
	reconciler reconcileSource
	seenMu     sync.Mutex
	seen       map[string]struct{}
	// probeWindow is how long the reconcile probe waits before declaring silent nodes
	// dead; defaulted to reconcileProbeWindow, overridable in tests.
	probeWindow time.Duration
	// rebirthPub publishes a rebirth NCMD directly and reports whether it reached the
	// wire; defaulted to publishRebirth, overridable in tests so the probe is drivable
	// without a live broker. The reconcile probe uses it (off the receive goroutine, so
	// it may publish synchronously) so it can account for which nodes were actually asked
	// to speak and never force-disconnect a node whose rebirth never went out.
	rebirthPub func(group, node string) bool

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
	runCtx    context.Context // the Connect lifecycle context; bounds the ingest retry
	// sessionCtx is the CURRENT MQTT session's context — cancelled the instant that
	// connection is lost (or on Stop). The reconcile probe runs under it so a broker drop
	// mid-window aborts the probe before it can declare a mass death against a broker it
	// can no longer hear (ADR-067 SP4b). A new session installs a fresh one.
	sessionCtx    context.Context
	sessionCancel context.CancelFunc
	wg            sync.WaitGroup
	// probeWG tracks in-flight reconcile probes so Stop drains them (they abort promptly
	// on the session context, so this just closes the goroutine-leak-on-shutdown gap).
	probeWG sync.WaitGroup
}

// NewClient builds a tenant-bound Host Application client from one configured
// source and its resolved broker connection. ingester turns accepted messages into
// durable telemetry (nil ⇒ decode+log only, the SP1/SP2 behavior). now is the clock
// supplying each session's STATE timestamp (pass time.Now in production); nil
// defaults to it.
func NewClient(source config.SparkplugSource, broker Broker, ingester SampleIngester, now func() time.Time, metrics Metrics) *Client {
	if now == nil {
		now = time.Now
	}
	c := &Client{
		tenant:   source.Tenant,
		broker:   broker,
		groups:   append([]string(nil), source.Groups...),
		metrics:  metrics,
		ingester: ingester,
		policy: IngestPolicy{
			Source:          "sparkplug:" + source.HostId,
			DeviceTypeToken: source.DeviceTypeToken,
			AutoRegister:    source.AutoRegister,
		},
		stateTopic:  StateTopic(source.HostId),
		now:         now,
		newClient:   func(o *mqtt.ClientOptions) mqtt.Client { return mqtt.NewClient(o) },
		rebirthCh:   make(chan nodeKey, rebirthQueueDepth),
		probeWindow: reconcileProbeWindow,
	}
	c.sessions = NewSessionTracker(now, defaultRebirthBackoff, c.enqueueRebirth)
	c.rebirthPub = c.publishRebirth
	return c
}

// SetReconciler binds the device-state reconciler that repopulates presence on
// (re)connect (ADR-067 SP4b). It is shared across connections and set once at wiring
// time before Connect; a nil reconciler (the default) disables reconciliation.
func (c *Client) SetReconciler(r reconcileSource) {
	c.reconciler = r
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
	c.runCtx = ctx
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
		// One context per MQTT session, cancelled the instant this connection ends, so
		// the reconcile probe launched by onConnected aborts rather than declaring a mass
		// death against a broker it can no longer hear (ADR-067 SP4b).
		sessionCtx, sessionCancel := context.WithCancel(ctx)
		c.setSessionCtx(sessionCtx, sessionCancel)
		client := c.newClient(c.sessionOptions(sessionTs, lost))

		if err := mqtt.WaitTokenTimeout(client.Connect(), connectTimeout); err != nil {
			if c.metrics.ConnectFailures != nil {
				c.metrics.ConnectFailures.Inc()
			}
			log.Warn().Err(err).Str("tenant", c.tenant).Str("broker", c.broker.URL).Dur("retry_in", backoff).
				Msg("Broker connect failed; retrying.")
			client.Disconnect(0)
			sessionCancel()
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
			sessionCancel()
			log.Warn().Str("tenant", c.tenant).Str("broker", c.broker.URL).Msg("Broker connection lost; reconnecting with a fresh session.")
		case <-ctx.Done():
			sessionCancel()
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

// setSessionCtx installs the current MQTT session's context + cancel.
func (c *Client) setSessionCtx(ctx context.Context, cancel context.CancelFunc) {
	c.mu.Lock()
	c.sessionCtx = ctx
	c.sessionCancel = cancel
	c.mu.Unlock()
}

// currentSessionCtx returns the current session's context (Background if none yet).
func (c *Client) currentSessionCtx() context.Context {
	c.mu.Lock()
	ctx := c.sessionCtx
	c.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// onConnected fires on each successful connection. It subscribes BEFORE announcing
// ONLINE (Sparkplug session order is subscribe-then-birth: an edge node that sees us go
// online may flush buffered data immediately, which a clean-session client with no live
// subscription would lose). Around that it runs the two-phase failover reconciliation
// (ADR-067 SP4b): FIRST floor the epoch generator from the device-state projection
// (before subscribing, so no birth on this session can mint a sub-floor, stale-rejected
// epoch), THEN — after ONLINE — launch the liveness probe under this session's context.
func (c *Client) onConnected(client mqtt.Client, sessionTs int64) {
	// Record the live client before any traffic can arrive, so a rebirth triggered by
	// the first inbound message publishes over this connection rather than a stale one.
	c.setSession(client, sessionTs)
	// Begin a fresh reconciliation window BEFORE subscribing, so every birth/data on this
	// session (including a retained NBIRTH that flushes the instant we subscribe) counts
	// toward liveness. A node that re-births between connect and the probe is genuinely
	// alive; wiping its signal at probe-start would wrongly declare it dead.
	c.resetSeen()

	// Phase 1 (before subscribe): establish the epoch floor from the projection so no
	// birth on this session mints a stale-rejected epoch. Returns the asserted set to
	// probe. A read failure degrades to no-floor + no-probe this cycle (logged).
	asserted := c.establishEpochFloor(c.currentSessionCtx())

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

	// Phase 2 (after ONLINE): probe the asserted set for liveness, under THIS session's
	// context so a broker drop mid-probe aborts it before it can declare a mass death.
	// Tracked in probeWG so Stop drains it. onConnected only runs while runLoop is alive,
	// so this Add never races Stop's post-runLoop probeWG.Wait.
	if len(asserted) > 0 {
		sctx := c.currentSessionCtx()
		c.probeWG.Add(1)
		go func() {
			defer c.probeWG.Done()
			c.reconcileProbe(sctx, asserted)
		}()
	}
}

// establishEpochFloor reads the device-state projection for this source's asserted-active
// devices and raises the epoch generator's floor above their max session (ADR-067 SP4b
// Major-4), so any birth processed on this session mints an epoch the projection will
// accept rather than reject as stale. It returns the asserted set (for the probe) or nil
// on a read failure / no reconciler — reconciliation is best-effort, retried next
// (re)connect, never a connection blocker. The read is bounded so a slow/absent
// device-state cannot stall connection setup.
func (c *Client) establishEpochFloor(ctx context.Context) []AssertedDevice {
	if c.reconciler == nil {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, reconcileQueryTimeout)
	devices, maxSession, err := c.reconciler.AssertedActive(qctx, c.tenant, c.policy.Source)
	cancel()
	if err != nil {
		log.Warn().Err(err).Str("tenant", c.tenant).Msg("Presence reconciliation could not read device-state; skipping the floor + probe this cycle (retries on the next reconnect).")
		return nil
	}
	if len(devices) == 0 {
		return nil
	}
	c.sessions.SetEpochFloor(maxSession + 1)
	return devices
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
	// Record that this external id produced traffic, so an in-flight reconciliation
	// probe (ADR-067 SP4b) knows the node/device is alive and does not force it
	// DISCONNECTED. Any node/device message counts — a DEATH included, since the node
	// answered and its own signal already drove the projection.
	if !rec.Topic.IsState {
		c.markSeen(SparkplugExternalId(rec.Topic))
	}
	// Advance the SP2 session machine; it may command the node to rebirth (fired
	// via enqueueRebirth, outside its lock) and returns the numeric samples the
	// message contributes plus any presence transitions it asserts (ADR-067). STATE
	// has no node session.
	if !rec.Topic.IsState && rec.Payload != nil && c.ingester != nil {
		obs := c.sessions.Observe(rec.Topic, rec.Payload)
		if len(obs.Samples) > 0 {
			c.ingestSamples(SparkplugExternalId(rec.Topic), obs.Samples)
		}
		if len(obs.Presence) > 0 {
			c.ingestPresence(obs.Presence)
		}
	} else if !rec.Topic.IsState && rec.Payload != nil {
		// Decode-only (nil ingester, the SP1/SP2 posture): still advance the session
		// machine so rebirth decisions and logging are unchanged.
		c.sessions.Observe(rec.Topic, rec.Payload)
	}
	c.logRecord(rec)
}

// ingestSamples resolves the device and emits its samples, retrying a transient
// device-management/NATS failure within a bounded budget. It runs ON paho's ordered
// receive goroutine (by design — the session machine needs strict ordering), so it
// must return in bounded time: a clean-session Host gets no broker redelivery, so a
// short retry saves a sample across a brief blip, but a prolonged outage is left to
// the Sparkplug host-offline path rather than blocking receive forever. On budget
// exhaustion the samples are dropped and counted.
func (c *Client) ingestSamples(externalId string, samples []Sample) {
	c.ingestWithRetry(
		func(ctx context.Context) error {
			return c.ingester.Ingest(ctx, c.tenant, c.policy, externalId, samples)
		},
		func(reason string, err error) {
			log.Warn().Err(err).Str("tenant", c.tenant).Str("externalId", externalId).Int("samples", len(samples)).
				Str("reason", reason).Msg("Dropping Sparkplug samples (a clean-session Host gets no broker redelivery).")
		},
	)
}

// ingestPresence emits the presence transitions an accepted message asserted, on the
// same bounded-retry contract as ingestSamples: a clean-session Host gets no broker
// redelivery, so a brief device-management/NATS blip is retried, but a prolonged
// outage is left to the host-offline path rather than blocking receive. The whole
// batch is re-tried; the DedupID makes an already-emitted transition idempotent.
func (c *Client) ingestPresence(events []PresenceEvent) {
	c.ingestWithRetry(
		func(ctx context.Context) error {
			return c.ingester.IngestPresence(ctx, c.tenant, c.policy, events)
		},
		func(reason string, err error) {
			log.Warn().Err(err).Str("tenant", c.tenant).Int("presence", len(events)).
				Str("reason", reason).Msg("Dropping Sparkplug presence (a clean-session Host gets no broker redelivery).")
		},
	)
}

// reconcileProbe is phase 2 of failover reconciliation (ADR-067 SP4b): it closes the
// missed-death hole (an ASSERTED device skips the inactivity sweep, so a node that dies
// while no Host is connected would sit CONNECTED forever) for the asserted set phase 1
// already floored the epoch generator against. The sequence:
//  1. Mint ONE stale epoch — AFTER the phase-1 floor and BEFORE the probe window — so a
//     genuine re-birth during the window gets a LATER (higher) epoch and supersedes a
//     racily-emitted timeout: a slow-but-alive node self-heals.
//  2. Rebirth every distinct node DIRECTLY (we are off the receive goroutine, so we may
//     publish synchronously) and record which reached the wire.
//  3. Wait the probe window; any device whose node WAS rebirthed but produced no traffic
//     is declared dead — DISCONNECTED at the stale epoch (accepted: it exceeds the stored
//     session). A node whose rebirth never went out, or one seen alive, is left untouched.
//
// It fails safe: it runs under this session's context, so a broker drop mid-window aborts
// it WITHOUT emitting (a probe gone deaf must never declare a mass death); and it only
// force-disconnects a node it actually asked to speak, so a dropped rebirth never reads
// as a death. The seen set was reset in onConnected, so it already holds any pre-probe
// re-birth.
func (c *Client) reconcileProbe(ctx context.Context, devices []AssertedDevice) {
	// Mint the one epoch the timeout-DISCONNECTs will carry (ordering guarantee above).
	staleEpoch := c.sessions.MintEpoch()

	// Rebirth every distinct node directly, tracking which reached the wire. A node whose
	// rebirth we could not publish (connection already gone) is NOT probed — we never
	// asked it to speak.
	rebirthed := make(map[nodeKey]bool)
	for _, d := range devices {
		key, ok := parseReconcileNodeKey(d.ExternalId)
		if !ok {
			continue
		}
		if _, done := rebirthed[key]; done {
			continue
		}
		if ctx.Err() != nil {
			return // connection gone — abort before touching anyone
		}
		if c.rebirthPub(key.group, key.node) {
			rebirthed[key] = true
			// If the tracker already holds this node's live session (a reconnect where
			// the node's own MQTT session persisted, so it will re-birth with the SAME
			// bdSeq), mark the rebirth pending so onNBirth re-adopts the seq baseline
			// instead of skipping the answer as a redelivery (the SP2 livelock fix).
			c.sessions.MarkRebirthPending(key.group, key.node)
		}
	}
	log.Info().Str("tenant", c.tenant).Int("assertedDevices", len(devices)).Int("nodes", len(rebirthed)).
		Msg("Reconciling Sparkplug presence: rebirthed asserted nodes; probing for liveness.")

	// Wait out the probe window; abort WITHOUT emitting if the connection dropped (ctx
	// cancelled) or we are shutting down — a deaf probe must not declare a mass death.
	if !sleep(ctx, c.probeWindow) {
		return
	}

	now := c.now()
	stale := make([]PresenceEvent, 0)
	for _, d := range devices {
		key, ok := parseReconcileNodeKey(d.ExternalId)
		if !ok || !rebirthed[key] {
			continue // not a Sparkplug identity, or its node was never rebirthed on this session
		}
		if c.sawExternalId(d.ExternalId) {
			continue // produced traffic → alive (or drove its own DEATH); leave it
		}
		stale = append(stale, PresenceEvent{
			ExternalId: d.ExternalId,
			Connected:  false,
			Reason:     "reconcile-timeout",
			SessionId:  staleEpoch,
			OccurredAt: now,
		})
	}
	if len(stale) > 0 {
		log.Warn().Str("tenant", c.tenant).Int("disconnected", len(stale)).
			Msg("Presence reconciliation declaring unresponsive asserted devices DISCONNECTED (missed death during a no-host window).")
		c.ingestPresence(stale)
	}
}

// resetSeen begins a fresh reconciliation window: it clears the observed-id set so
// only traffic from here on counts toward liveness.
func (c *Client) resetSeen() {
	c.seenMu.Lock()
	c.seen = make(map[string]struct{})
	c.seenMu.Unlock()
}

// markSeen records that an external id produced traffic during the current
// reconciliation window (no-op when no reconcile has begun — seen is nil).
func (c *Client) markSeen(externalId string) {
	c.seenMu.Lock()
	if c.seen != nil {
		c.seen[externalId] = struct{}{}
	}
	c.seenMu.Unlock()
}

// sawExternalId reports whether an external id has produced traffic since the current
// reconciliation window began.
func (c *Client) sawExternalId(externalId string) bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	_, ok := c.seen[externalId]
	return ok
}

// parseReconcileNodeKey extracts the edge node ({group}, {node}) from a device's
// external id — "{group}/{node}" for a node or "{group}/{node}/{device}" for a leaf,
// the slash-joined form SparkplugExternalId builds. It returns false for anything that
// is not a Sparkplug node/device identity (a device asserted by some other transport),
// which the reconcile then skips.
func parseReconcileNodeKey(externalId string) (nodeKey, bool) {
	parts := strings.SplitN(externalId, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nodeKey{}, false
	}
	return nodeKey{group: parts[0], node: parts[1]}, true
}

// ingestWithRetry runs one bounded, in-handler retry loop shared by the sample and
// presence paths. It runs ON paho's ordered receive goroutine (by design — the
// session machine needs strict ordering), so it must return in bounded time: a
// clean-session Host gets no broker redelivery, so a short retry saves work across a
// brief blip, but a prolonged outage is left to the Sparkplug host-offline path
// rather than blocking receive forever. EVERY abandonment path routes through onDrop
// so a drop (real, bounded loss) is never silent; onDrop also increments the shared
// IngestFailures counter.
func (c *Client) ingestWithRetry(attempt func(ctx context.Context) error, onDrop func(reason string, err error)) {
	c.mu.Lock()
	ctx := c.runCtx
	c.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	drop := func(reason string, err error) {
		if c.metrics.IngestFailures != nil {
			c.metrics.IngestFailures.Inc()
		}
		onDrop(reason, err)
	}
	for n := 1; ; n++ {
		attemptCtx, cancel := context.WithTimeout(ctx, ingestAttemptTimeout)
		err := attempt(attemptCtx)
		cancel()
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			drop("connection shutting down", err)
			return
		}
		if n >= ingestMaxAttempts {
			drop("ingest retry budget exhausted", err)
			return
		}
		if !sleep(ctx, ingestRetryBackoff) {
			drop("connection shutting down", err)
			return
		}
	}
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
// wire. It returns whether the command reached the wire, so the reconcile probe can
// account for which nodes it actually asked to speak (a node whose rebirth was dropped
// must not be force-disconnected for staying silent).
func (c *Client) publishRebirth(group, node string) bool {
	c.mu.Lock()
	client := c.mc
	c.mu.Unlock()
	if client == nil || !client.IsConnected() {
		log.Warn().Str("tenant", c.tenant).Str("group", group).Str("node", node).Msg("Not connected; dropping rebirth command.")
		return false
	}
	topic := Namespace + "/" + group + "/" + string(NCMD) + "/" + node
	payload, err := rebirthCommand(c.now().UnixMilli())
	if err != nil {
		log.Error().Err(err).Str("group", group).Str("node", node).Msg("Failed to encode rebirth command.")
		return false
	}
	if err := mqtt.WaitTokenTimeout(client.Publish(topic, 0, false, payload), publishTimeout); err != nil {
		log.Error().Err(err).Str("topic", topic).Msg("Failed to publish rebirth command.")
		return false
	}
	if c.metrics.RebirthRequests != nil {
		c.metrics.RebirthRequests.Inc()
	}
	log.Warn().Str("tenant", c.tenant).Str("group", group).Str("node", node).Msg("Commanded Sparkplug node rebirth.")
	return true
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
	c.wg.Wait() // runLoop exited: no new session, so no new probe can be launched
	// Drain any in-flight reconcile probe. runLoop's exit cancelled the session context,
	// so a probe in its window aborts promptly; this just makes shutdown wait for it.
	c.probeWG.Wait()
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
