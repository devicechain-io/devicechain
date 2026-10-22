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
	"errors"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/transport"
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
	//
	// The same budget bounds the PUBLISH of what a message produced: it is submitted without
	// the receive goroutine waiting for its acknowledgement (see Publisher), and one that
	// fails is resubmitted, from a goroutine of its own, up to ingestMaxAttempts in all with
	// ingestRetryBackoff between them, then abandoned and counted exactly as before.
	ingestAttemptTimeout = 3 * time.Second
	ingestMaxAttempts    = 3
	ingestRetryBackoff   = 500 * time.Millisecond
	// publishRetryDepth bounds the failed publishes waiting for their next attempt, and a
	// full queue abandons the publish, counted, rather than blocking the writer's goroutine.
	//
	// It is twice the LARGEST window, so it fits two whole windows of failures at any
	// configured ingest.publishWindow. A broker failure fails at most one window at once:
	// after it the writer holds the window's slots back and lets one probe through per
	// backoff until a publish succeeds (messaging's pace), so a second window can fail only
	// after a success has let the first one's retries back in. Overflow therefore needs
	// failures faster than the writer itself admits, and is counted like any other loss.
	// At the default window of 32 the queue normally holds at most 32. It is a channel of
	// pointers, so the headroom costs 16 KiB per connection.
	publishRetryDepth = 2 * messaging.MaxOrderedWindow
	// reconcileQueryTimeout bounds the device-state read the reconciliation runs BEFORE
	// subscribing (to floor the epoch generator). It caps how long a slow/absent
	// device-state can stall connection setup — on timeout the floor+probe are skipped
	// this cycle, not the connection.
	//
	// 🔑 IT COVERS A PAGED WALK, NOT ONE ROUND TRIP. The asserted-active read is bounded
	// per page, so a tenant's fleet costs ceil(devices/500) sequential calls; the five
	// seconds this was originally sized at is one call's worth, and a large fleet
	// reconnecting against a busy device-state — which is exactly the restart storm this
	// path runs in — would miss the budget on every attempt and never floor its epoch
	// generator. The walk is bounded elsewhere by its own page ceiling, so a generous
	// value here cannot become an unbounded stall.
	reconcileQueryTimeout = 30 * time.Second
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
	// RebirthEnqueued counts rebirth requests accepted onto the publish queue and
	// RebirthDropped counts the ones discarded because that queue was already full.
	//
	// 🔑 A DROP IS A LATENCY SIGNAL, NOT AN ERROR RATE. The request is not lost: the
	// per-node backoff re-requests on its next window, so the cost of a drop is that the
	// node re-emits its births a window later than it could have.
	//
	// What the pair buys is visibility, and nothing else here provides it. RebirthRequests
	// moves only on a successful publish, so once the queue is saturating it is CAPPED at
	// what the single publisher goroutine can get onto the wire — pinned at that ceiling,
	// or flat if the publishes are themselves timing out. It does not decrease; it stops
	// tracking DEMAND, which is the number an operator is actually reading it for, and
	// flat is indistinguishable from an idle deployment. Enqueued is the counterweight:
	// it is demand, so it separates "the queue is overflowing" from "nothing is asking
	// for a rebirth".
	//
	// They cover the QUEUE, which is not every rebirth this Host emits: the failover
	// reconcile probe runs off the receive goroutine and so publishes synchronously,
	// bypassing the queue entirely. It moves RebirthRequests and neither of these, which
	// is why a saturated queue is read off RebirthDropped directly rather than inferred
	// from the two counters diverging.
	RebirthEnqueued prometheus.Counter
	RebirthDropped  prometheus.Counter
	// ConnectFailures counts failed broker connect attempts across all sources.
	// Readiness is intentionally NOT gated on broker reachability (a customer broker
	// outage must not kill the pod), so this counter is the machine-readable signal
	// that distinguishes a broken config (a bad host or rejected credential makes it
	// climb) from a healthy but idle deployment (it stays flat, as does Messages).
	ConnectFailures prometheus.Counter
	// SubscribeFailures counts sessions this host ABANDONED because a group subscription
	// failed after the broker had accepted the connection — most often a refusal (SUBACK
	// 0x80: the source's credential may not read that group). It is the counterpart of
	// ConnectFailures for the half of a broken configuration that connects cleanly.
	//
	// 🔴 ONE REFUSED GROUP STOPS THE WHOLE SOURCE. A host announces ONE STATE for all of
	// its groups, so it cannot be online for some and offline for others; announcing
	// ONLINE with a group missing would tell that group's edge nodes to flush their
	// store-and-forward buffers into a subscription that does not exist, and the reconcile
	// probe would then declare every asserted device in that group DISCONNECTED for
	// staying silent. So the session is ended and retried with backoff instead, and the
	// source ingests none of its groups until every one is granted. A climbing counter is
	// therefore an ingest outage for that source, not a presence nuance.
	SubscribeFailures prometheus.Counter
	// IngestFailures counts every abandonment of an ingest, one per abandoned ingest
	// call: an accepted message's samples or presence transitions, or a reconciliation
	// sweep's synthetic DISCONNECTED batch (which comes from no message). Samples are
	// dropped at once when inbound-events refused them under backpressure (not retried;
	// presence bypasses the gate), and anything either after the retry budget was exhausted
	// (device-management or NATS unreachable, whether the resolve failed or the publish was
	// not acknowledged) or on connection shutdown.
	// One message can count twice, once for its samples and once for its transitions. A clean-session
	// Host gets no broker redelivery, so this is real (bounded) loss — the signal that
	// ingest, not just connectivity, is degraded.
	IngestFailures prometheus.Counter
	// SamplesSkipped counts the metrics of accepted messages that produced no sample, labelled
	// by reason (SkipReasons). Without it a node publishing booleans
	// or strings looks identical to one publishing nothing. May be nil (decode-only tests).
	SamplesSkipped *prometheus.CounterVec
	// PresenceProbes counts liveness probes that reached a verdict, labelled by what
	// triggered them (ProbeTriggerConnect, ProbeTriggerPeriodic). A probe that aborted
	// because its session ended is not counted: it decided nothing.
	//
	// 🔑 THE PERIODIC SERIES IS THE POINT. Without it, a Host whose periodic probe never
	// runs — a loop that was never started, a reconciler that is nil — is indistinguishable
	// from a healthy fleet in which every device answers: both emit no disconnects. The same
	// reason presence_tap_off exists for the platform broker's tap.
	PresenceProbes *prometheus.CounterVec
	// PresenceProbeDisconnects counts devices a probe declared DISCONNECTED for not
	// answering a rebirth, by the same trigger label. In a healthy fleet it is near zero; a
	// standing periodic rate means death certificates are being lost.
	PresenceProbeDisconnects *prometheus.CounterVec
	// PresenceProbePass files each periodic-probe slot on the same pass series every other
	// maintenance pass uses (duration, outcome, last success). It is NOT presence_probes_total:
	// that counts verdicts, and a slot that could not read the projection reached none, which
	// is exactly the case this series exists to show as a failure.
	PresenceProbePass *core.PeriodicTaskMetrics
}

// Probe trigger labels for Metrics.PresenceProbes and Metrics.PresenceProbeDisconnects.
const (
	// ProbeTriggerConnect is the failover reconciliation probe run once per session.
	ProbeTriggerConnect = "connect"
	// ProbeTriggerPeriodic is the liveness probe run every probeInterval on a steady session.
	ProbeTriggerPeriodic = "periodic"
)

// SampleIngester turns an accepted message's samples into durable telemetry
// (resolve/register the device, then emit) and its presence transitions into durable
// StateChange events (ADR-067). The Client depends on the interface so a host test
// can drive the full receive→ingest path with a fake, asserting what would be
// ingested without a device-management or a NATS. A nil ingester makes the receive
// path decode+log only (the SP1/SP2 behavior).
//
// It PREPARES rather than ingests: resolving the device and building the messages is the
// half that must stay on the receive goroutine, in order, and the publish is the half that
// leaves it (see Publisher). A nil Publication with a nil error is a definitive drop.
type SampleIngester interface {
	PrepareIngest(ctx context.Context, tenant string, policy IngestPolicy, externalId string, samples []Sample) (*Publication, error)
	PreparePresence(ctx context.Context, tenant string, policy IngestPolicy, events []PresenceEvent) (*Publication, error)
}

// Publisher is where a Client's prepared publications go, without the goroutine that
// prepared them waiting for the stream's acknowledgement. *messaging.AsyncWriter satisfies
// it, and a Client owns exactly one for the life of the process (not one per leadership
// term: a writer holds a reply subscription until the connection closes).
//
// Submissions made from one goroutine reach the stream, and have their done run, in that
// goroutine's call order. done may run on the writer's goroutine or, when the call is never
// taken, on the caller's before Submit returns; it must be quick and must not call Submit.
// Settle returns once every Submit that returned before it has had its done run, or ctx
// ends: it is the barrier a leadership term drains with before the lease is released.
type Publisher interface {
	Submit(ctx context.Context, msgs []messaging.Message, done func(error))
	Settle(ctx context.Context) error
}

// SampleLimiter charges a DATA message's samples against its tenant's ingest ceiling,
// counted in readings, and returns how many leading samples it admitted (a whole number
// of events; see adapter.IngestLimiter.AdmitSamples, which implements it).
type SampleLimiter interface {
	AdmitSamples(tenant string, n int) int
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
	// limiter charges each DATA message's samples against the tenant's ingest ceiling
	// before they are ingested. Never nil when ingester is set (NewClient refuses that).
	limiter SampleLimiter

	// reconciler reads the device-state presence projection to repopulate presence on
	// (re)connect (ADR-067 SP4b failover reconciliation) and on every periodic liveness
	// probe. Nil disables both (decode-only tests, or a build without the device-state
	// coordinate); shared across connections. seen (guarded by seenMu) records when each
	// external id last produced traffic on the current session, so a probe can tell a live
	// node from a dead one and the periodic probe can pick out the devices that have gone
	// quiet. It is reset at connect, so an id's presence in the map means "heard from on
	// THIS session".
	reconciler reconcileSource
	seenMu     sync.Mutex
	seen       map[string]time.Time
	// probeWindow is how long the reconcile probe waits before declaring silent nodes
	// dead (config.Presence.ProbeWindowSeconds, ADR-067 SP4b). It must comfortably exceed
	// a healthy node's rebirth→births round trip so a live-but-slow node is not falsely
	// disconnected (and if one is, its later births — higher epochs — supersede the
	// timeout). Defaulted by NewClient, set by the service before Connect.
	probeWindow time.Duration
	// probeInterval is how often the periodic liveness probe re-checks the asserted set on
	// a steadily connected session (config.Presence.ProbeSeconds). Defaulted to the
	// platform default by NewClient; the service sets the configured one before Connect.
	probeInterval time.Duration
	// probing is set while a probe (connect or periodic) is between its rebirths and its
	// verdict, so a periodic tick that lands inside another probe's window is skipped
	// rather than rebirthing the same nodes a second time.
	probing atomic.Bool
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
	// subscribe makes one confirmed group subscription; defaulted to
	// messaging.SubscribeMqttConfirmed. It is a seam because a test fake cannot build a
	// GRANTED paho SubscribeToken (its result map is unexported), so without it a fake
	// client can only ever be refused.
	subscribe func(client mqtt.Client, filter string, qos byte, cb mqtt.MessageHandler, timeout time.Duration) error
	// backoffSleep waits out a reconnect backoff (false if ctx ended first); defaulted to
	// sleep, overridable so a test can read the backoff sequence runLoop chose without
	// waiting it out.
	backoffSleep func(ctx context.Context, d time.Duration) bool

	mu        sync.Mutex
	mc        mqtt.Client
	sessionTs int64 // the current session's STATE timestamp (for a graceful OFFLINE)
	cancel    context.CancelFunc
	runCtx    context.Context // the Connect lifecycle context; bounds the ingest retry
	wg        sync.WaitGroup
	// probeWG tracks in-flight reconcile probes so Stop drains them (they abort promptly
	// on the session context, so this just closes the goroutine-leak-on-shutdown gap).
	// Only ONE-SHOT probes: a test waits on it to know the connect probe has delivered its
	// verdict, which is why the session-long periodic loop is tracked separately.
	probeWG sync.WaitGroup
	// loopWG tracks each session's periodic liveness probe loop (presenceProbeLoop), which
	// lives as long as its session context and runs its probes inline. Stop drains it after
	// probeWG; a session's end cancels it.
	loopWG sync.WaitGroup

	// publisher takes the publications the receive path prepares. It is built once for the
	// life of the process and kept across leadership terms (a Stop drains it with Settle and
	// leaves it open); whoever built it closes it. nil only for a decode-only client.
	publisher Publisher
	// settleTimeout bounds the drain at Stop; a field so a test can shorten it.
	settleTimeout time.Duration

	// subMu orders every submission against Stop. A submission holds the read side across
	// its Submit, and Stop takes the write side to set stopped, so once Stop holds it there
	// is no Submit in progress and none will start: that is what "no publish after Stop"
	// rests on. Nothing that holds the read side waits for anything that needs the write
	// side: Submit blocks only until the writer takes the call, the writer's goroutines never
	// take subMu, and Stop cancels the context a blocked Submit is waiting on first.
	subMu   sync.RWMutex
	stopped bool

	// retryCh carries a publish that failed to the goroutine that resubmits it, and retryMu
	// guards it and retryClosed. The writer calls done on its own goroutine, which must not
	// block and must not Submit, so a failure is handed over here without waiting. Both are
	// replaced by Connect and closed by Stop; a failure that arrives once it is closed is
	// abandoned, counted, because there is nobody left to resubmit it.
	retryMu     sync.Mutex
	retryCh     chan *publishItem
	retryClosed bool
}

// publishItem is one prepared publication on its way to the stream, with the attempts spent
// on it. onDrop reports its abandonment (the log line that names what was lost).
type publishItem struct {
	pub      *Publication
	attempts int
	retryAt  time.Time
	onDrop   func(reason string, err error)
}

// publishSettleTimeout is how long a stopping Client waits for the publications it submitted
// to be stored before the lease can be released. Each publish in flight runs its own 5 s
// ceiling concurrently, so after the last submission returns everything settles within about
// one ceiling; this is longer than that.
//
// 🔴 IT IS SIZED AGAINST THE SHUTDOWN BUDGET, NOT ONLY AGAINST THE CEILING. With the chart's
// defaults (30 s grace, 5 s readiness drain) the teardown gets 30 - 5 - 2 = 23 s, and a
// stop that runs every step to its bound spends: the OFFLINE STATE publish (publishTimeout,
// 5 s), the disconnect quiesce (0.25 s), this settle, and then closing the writer, which
// ends every backoff and waits for what the settle left: one ceiling (5 s) for what was
// already sent, one more only for a piece that was still waiting for room. At 8 s the usual
// worst case (one of the two brokers failing) is 8 + 5, about 13 s, and even the 5 s STATE
// publish on top leaves some 4.7 s for the lease release and the broker manager's drain.
// Both brokers failing at once with a piece waiting for room can exceed the budget by a
// fraction of a second, which the service reports as a teardown that did not finish, never
// as a silent kill. A settle that times out loses nothing by itself: the writer's Close
// still waits for, and counts, what it left. The external MQTT source uses the same 8 s.
const publishSettleTimeout = 8 * time.Second

var errNoPublisher = errors.New("this Sparkplug client has no publisher: its publications cannot be stored")
var errClientStopped = errors.New("the Sparkplug connection is shutting down")

// NewClient builds a tenant-bound Host Application client from one configured
// source and its resolved broker connection. ingester turns accepted messages into
// durable telemetry (nil ⇒ decode+log only, the SP1/SP2 behavior). limiter charges
// each DATA message's samples against the tenant's ingest ceiling; it is required
// whenever ingester is set, and NewClient panics without it, because a client that
// ingests with no limiter would ingest unmetered and nothing at the call site would
// show it. now is the clock supplying each session's STATE timestamp (pass time.Now
// in production); nil defaults to it.
func NewClient(source config.SparkplugSource, broker Broker, ingester SampleIngester, limiter SampleLimiter,
	now func() time.Time, metrics Metrics) *Client {
	if ingester != nil && limiter == nil {
		panic("host.NewClient: a client that ingests needs a sample limiter")
	}
	if now == nil {
		now = time.Now
	}
	// Every source's client files on the one presence_probe series, but each probe loop keeps
	// its own failure-log streak: one source's healthy slots must not end another's outage.
	metrics.PresenceProbePass = metrics.PresenceProbePass.WithOwnFailureLog()
	c := &Client{
		tenant:   source.Tenant,
		broker:   broker,
		groups:   append([]string(nil), source.Groups...),
		metrics:  metrics,
		ingester: ingester,
		limiter:  limiter,
		policy: IngestPolicy{
			// Built through transport.Qualify rather than by concatenating ":" here: the
			// separator is half of a match whose other half lives in command-delivery, and a
			// hand-written one on either side is how the two drift apart. That already
			// happened once in the other direction — the deny list held a bare "sparkplug"
			// and matched the whole value, so it missed every real device.
			Source:          transport.Qualify(transport.Sparkplug, source.HostId),
			DeviceTypeToken: source.DeviceTypeToken,
			AutoRegister:    source.AutoRegister,
		},
		stateTopic:    StateTopic(source.HostId),
		now:           now,
		newClient:     func(o *mqtt.ClientOptions) mqtt.Client { return mqtt.NewClient(o) },
		subscribe:     messaging.SubscribeMqttConfirmed,
		backoffSleep:  sleep,
		rebirthCh:     make(chan nodeKey, rebirthQueueDepth),
		probeWindow:   config.DefaultPresenceProbeWindowSeconds * time.Second,
		probeInterval: config.DefaultPresenceProbeSeconds * time.Second,
		settleTimeout: publishSettleTimeout,
	}
	c.sessions = NewSessionTracker(now, defaultRebirthBackoff, c.enqueueRebirth)
	c.rebirthPub = c.publishRebirth
	return c
}

// Metrics returns the metric set this client updates, so the SERVICE's own wiring can be
// gated rather than only the behaviour behind it.
//
// 🔴 IT EXISTS BECAUSE A CLIENT BUILT WITH A ZERO Metrics IS SILENT, NOT BROKEN. Every field
// here is optional and every use of one is nil-guarded, which is what lets the host be driven
// in a test with no registry — and is also what makes a wiring slip invisible: resolveSources
// handing NewClient an empty Metrics leaves the counters registered, exported and permanently
// zero, which on a dashboard is indistinguishable from a fleet that never dropped a rebirth.
// Nothing inside this package reads it; it is the seam the service's own wiring test asserts
// against, so that test can compare against the counters it registered rather than a copy of
// them.
func (c *Client) Metrics() Metrics {
	return c.metrics
}

// SampleLimiter returns the ceiling this client charges DATA readings against, for the
// same reason as Metrics: so the service's wiring of it can be gated.
func (c *Client) SampleLimiter() SampleLimiter {
	return c.limiter
}

// SetReconciler binds the device-state reconciler that repopulates presence on
// (re)connect (ADR-067 SP4b). It is shared across connections and set once at wiring
// time before Connect; a nil reconciler (the default) disables reconciliation.
func (c *Client) SetReconciler(r reconcileSource) {
	c.reconciler = r
}

// SetPresenceProbeInterval sets how often the periodic liveness probe re-checks the
// asserted set on a steady session (config.Presence.ProbeInterval). Set at wiring time
// before Connect; a non-positive value is refused by panic rather than read as "off",
// because the configuration layer has already defaulted and floored it and a zero here can
// only be a wiring slip — the one that would reopen the hole the probe closes.
func (c *Client) SetPresenceProbeInterval(d time.Duration) {
	if d <= 0 {
		panic("host.SetPresenceProbeInterval: the interval must be positive (config.Presence.ProbeInterval never yields otherwise)")
	}
	c.probeInterval = d
}

// PresenceProbeInterval returns the periodic probe's interval, so the SERVICE's wiring of
// the setting can be gated, for the same reason as Metrics.
func (c *Client) PresenceProbeInterval() time.Duration {
	return c.probeInterval
}

// SetPresenceProbeWindow sets how long a probe (connect or periodic) waits for a node it
// asked to rebirth to answer (config.Presence.ProbeWindow). Set at wiring time before
// Connect; a non-positive value panics for the same reason as SetPresenceProbeInterval —
// a zero window declares every asked node dead before it can answer.
func (c *Client) SetPresenceProbeWindow(d time.Duration) {
	if d <= 0 {
		panic("host.SetPresenceProbeWindow: the window must be positive (config.Presence.ProbeWindow never yields otherwise)")
	}
	c.probeWindow = d
}

// PresenceProbeWindow returns the probe's answer window, so the service's wiring of the
// setting can be gated.
func (c *Client) PresenceProbeWindow() time.Duration {
	return c.probeWindow
}

// SetPublisher binds the publisher the receive path submits its prepared publications to. It
// is set once at wiring time before Connect, and kept for the life of the process: the
// Client drains it (Settle) when a leadership term ends and never closes it. A client with
// an ingester and no publisher cannot store anything, and abandons (and counts) every
// message it accepts.
func (c *Client) SetPublisher(p Publisher) {
	c.publisher = p
}

// Publisher returns the publisher this client submits to, so the SERVICE's own wiring of it
// can be gated, for the same reason as Metrics.
func (c *Client) Publisher() Publisher {
	return c.publisher
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
	// A new term: the previous Stop closed submission and the retry queue, and the writer it
	// drained is still open, so this only reopens the doors.
	retryCh := make(chan *publishItem, publishRetryDepth)
	c.retryMu.Lock()
	c.retryCh = retryCh
	c.retryClosed = false
	c.retryMu.Unlock()
	c.subMu.Lock()
	c.stopped = false
	c.subMu.Unlock()
	c.wg.Add(3)
	go c.runLoop(ctx)
	go c.rebirthWorker(ctx)
	go c.retryWorker(ctx, retryCh)
}

// runLoop owns the connection lifecycle. Each iteration is one MQTT session with
// its OWN fresh STATE timestamp: this is why paho's auto-reconnect is off (it
// would reuse the fixed Last-Will and freeze the timestamp, breaking the
// Sparkplug 3.0 per-session freshness B4 depends on). On a lost connection it
// reconnects with a new timestamp; on ctx cancellation (Stop) it exits.
//
// 🔑 THE BACKOFF RESETS ONLY AFTER A FULL SESSION. A session is full once onConnected
// has subscribed to every group AND announced ONLINE (it sets the session's up flag).
// Connecting is not enough: a broker that accepts the connection and refuses a group
// would otherwise be re-dialled every second forever, because each attempt "succeeds"
// at the step the backoff used to be reset on. A session that ends before it was full
// (abandoned by onConnected, or lost mid-setup) backs off like a failed connect, and a
// full session that is later lost reconnects at once, as it always has.
func (c *Client) runLoop(ctx context.Context) {
	defer c.wg.Done()
	backoff := minReconnectBackoff
	// waitOut sleeps the current backoff and grows it; false means ctx ended.
	waitOut := func() bool {
		if !c.backoffSleep(ctx, backoff) {
			return false
		}
		backoff = nextBackoff(backoff)
		return true
	}
	for {
		if ctx.Err() != nil {
			return
		}
		sessionTs := c.now().UnixMilli()
		lost := make(chan struct{}, 1)
		// up is set by onConnected only once THIS session is fully established; it is
		// per session so a late handler from an earlier session cannot mark this one up.
		up := new(atomic.Bool)
		// One context per MQTT session, cancelled the instant this connection ends, so
		// the reconcile probe launched by onConnected aborts rather than declaring a mass
		// death against a broker it can no longer hear (ADR-067 SP4b). It is handed to
		// onConnected as an argument rather than read back from a field, so a handler that
		// finishes late can only ever act under ITS OWN session's context.
		sessionCtx, sessionCancel := context.WithCancel(ctx)
		client := c.newClient(c.sessionOptions(sessionCtx, sessionTs, lost, up))

		if err := mqtt.WaitTokenTimeout(client.Connect(), connectTimeout); err != nil {
			if c.metrics.ConnectFailures != nil {
				c.metrics.ConnectFailures.Inc()
			}
			log.Warn().Err(err).Str("tenant", c.tenant).Str("broker", c.broker.URL).Dur("retry_in", backoff).
				Msg("Broker connect failed; retrying.")
			client.Disconnect(0)
			sessionCancel()
			if !waitOut() {
				return
			}
			continue
		}
		// Connected. The OnConnect handler (in the options) subscribes and announces
		// ONLINE on paho's own goroutine; it signals lost if it has to abandon the session.

		select {
		case <-lost:
			// Cancel first: onConnected installs its client only while its session is
			// live (setSessionIfLive), so once this is cancelled the clearSession below
			// cannot be undone by a handler that is still finishing.
			sessionCancel()
			// 🔴 AN ABANDONED SESSION ANNOUNCES ITS OWN OFFLINE BEFORE THE CLEAN DISCONNECT.
			// A clean DISCONNECT discards the Last-Will (see Stop), and an ONLINE publish
			// that timed out waiting for its PUBACK may still have been stored by the
			// broker as the retained STATE. Left alone, that ONLINE outlives this session,
			// and a later session abandoned over a refused group publishes no STATE at
			// all, so the edge nodes see this host ONLINE for as long as the refusal lasts
			// and flush their store-and-forward buffers into nothing. Best-effort: a
			// session that refused a group before publishing anything is overwritten with
			// the OFFLINE its own will would have carried. A full session (up) only ever
			// ends here because the broker dropped it, and then the will speaks for it.
			if !up.Load() && client.IsConnected() {
				if err := mqtt.WaitTokenTimeout(client.Publish(c.stateTopic, 1, true, statePayload(false, sessionTs)), publishTimeout); err != nil {
					log.Warn().Err(err).Str("tenant", c.tenant).Str("topic", c.stateTopic).
						Msg("Failed to publish OFFLINE STATE for an abandoned session.")
				}
			}
			// Disconnect here, on runLoop's goroutine, never inside onConnected: paho runs
			// that handler on a goroutine of its own and disconnecting from it risks a
			// deadlock. For a connection the broker already dropped this is a no-op.
			client.Disconnect(disconnectQuiesceMs)
			// Stop pointing the rebirth publisher at a connection that has ended.
			c.clearSession(client)
			if up.Load() {
				backoff = minReconnectBackoff
				log.Warn().Str("tenant", c.tenant).Str("broker", c.broker.URL).Msg("Broker connection lost; reconnecting with a fresh session.")
				continue
			}
			log.Warn().Str("tenant", c.tenant).Str("broker", c.broker.URL).Dur("retry_in", backoff).
				Msg("Session ended before it was fully established (a group subscription or the ONLINE announcement failed); retrying.")
			if !waitOut() {
				return
			}
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
func (c *Client) sessionOptions(sessionCtx context.Context, sessionTs int64, lost chan struct{}, up *atomic.Bool) *mqtt.ClientOptions {
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
	opts.SetOnConnectHandler(func(client mqtt.Client) { c.onConnected(sessionCtx, client, sessionTs, lost, up) })
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Warn().Err(err).Str("tenant", c.tenant).Str("broker", c.broker.URL).Msg("MQTT connection to the broker dropped.")
		signalLost(lost)
	})
	return opts
}

// signalLost tells runLoop this session has ended. Non-blocking: lost is buffered for
// one, and a second signal (a drop racing an abandon) carries no new information.
func signalLost(lost chan struct{}) {
	select {
	case lost <- struct{}{}:
	default:
	}
}

// onConnected fires on each successful connection. It subscribes BEFORE announcing
// ONLINE (Sparkplug session order is subscribe-then-birth: an edge node that sees us go
// online may flush buffered data immediately, which a clean-session client with no live
// subscription would lose). Around that it runs the two-phase failover reconciliation
// (ADR-067 SP4b): FIRST floor the epoch generator from the device-state projection
// (before subscribing, so no birth on this session can mint a sub-floor, stale-rejected
// epoch), THEN — after ONLINE — launch the liveness probe under this session's context.
//
// 🔴 IT IS ALL OR NOTHING. If ANY group subscription fails, or the ONLINE announcement
// does, the session is abandoned: no ONLINE, no probe, and lost is signalled so runLoop
// disconnects and retries with backoff. Carrying on with the groups that were granted is
// what this used to do, and it is worse than stopping: the one ONLINE STATE speaks for
// every group, so the refused group's edge nodes flush their store-and-forward buffers
// into a subscription that does not exist, and the probe then declares that group's
// asserted devices DISCONNECTED for staying silent. Without ONLINE the probe must not run
// either: the rebirths it sends are answered by nodes that still see this host offline.
func (c *Client) onConnected(sessionCtx context.Context, client mqtt.Client, sessionTs int64, lost chan struct{}, up *atomic.Bool) {
	// Record the live client before any traffic can arrive, so a rebirth triggered by
	// the first inbound message publishes over this connection rather than a stale one.
	c.setSessionIfLive(sessionCtx, client, sessionTs)
	// Begin a fresh reconciliation window BEFORE subscribing, so every birth/data on this
	// session (including a retained NBIRTH that flushes the instant we subscribe) counts
	// toward liveness. A node that re-births between connect and the probe is genuinely
	// alive; wiping its signal at probe-start would wrongly declare it dead.
	c.resetSeen()

	// Phase 1 (before subscribe): establish the epoch floor from the projection so no
	// birth on this session mints a stale-rejected epoch. Returns the asserted set to
	// probe. A read failure degrades to no-floor + no-probe this cycle (logged). If the
	// session is then abandoned the raised floor is harmless: it only ever moves up.
	asserted := c.establishEpochFloor(sessionCtx)

	for _, filter := range c.subscriptions() {
		// Confirmed rather than merely awaited: paho's WaitTokenTimeout returns the
		// token's Error(), which a broker REFUSAL leaves nil (see
		// SubscribeMqttConfirmed). Unconfirmed, a host whose credential may not read a
		// group's topics logs that it subscribed to all of them and then sits silent,
		// indistinguishable from a group with no devices publishing.
		if err := c.subscribe(client, filter, 1, c.onMessage, subscribeTimeout); err != nil {
			if c.metrics.SubscribeFailures != nil {
				c.metrics.SubscribeFailures.Inc()
			}
			ev := log.Error().Err(err).Str("tenant", c.tenant).Str("filter", filter)
			if errors.Is(err, messaging.ErrSubscriptionRefused) {
				ev = ev.Str("hint", "the source's broker credential is most likely not permitted to read this group")
			}
			// Stop at the first failure: the remaining filters are pointless on a session
			// that is being abandoned.
			ev.Msg("Sparkplug group subscription failed; ending this session without announcing ONLINE. The source ingests none of its groups until every group is granted.")
			signalLost(lost)
			return
		}
		log.Info().Str("tenant", c.tenant).Str("filter", filter).Msg("Subscribed to Sparkplug group traffic.")
	}

	if err := mqtt.WaitTokenTimeout(client.Publish(c.stateTopic, 1, true, statePayload(true, sessionTs)), publishTimeout); err != nil {
		// Same path as a refused group: an edge node that never saw ONLINE will not answer
		// the probe's rebirths, so probing now would declare live devices dead.
		log.Error().Err(err).Str("tenant", c.tenant).Str("topic", c.stateTopic).
			Msg("Failed to publish ONLINE STATE; ending this session without probing.")
		signalLost(lost)
		return
	}
	log.Info().Str("tenant", c.tenant).Str("topic", c.stateTopic).Int64("timestamp", sessionTs).Msg("Announced Host Application ONLINE.")
	up.Store(true)

	// Phase 2 (after ONLINE): probe the asserted set for liveness, under THIS session's
	// context so a broker drop mid-probe aborts it before it can declare a mass death.
	// Tracked in probeWG so Stop drains it; goWhileLive is what keeps that Add from racing
	// Stop's probeWG.Wait (onConnected runs on paho's goroutine and can outlive runLoop).
	// The zero `since` counts
	// anything heard on this session (the seen set was reset above) as alive.
	if len(asserted) > 0 {
		c.goWhileLive(sessionCtx, &c.probeWG, func() {
			c.reconcileProbe(sessionCtx, asserted, time.Time{}, ProbeTriggerConnect)
		})
	}

	// And for the rest of the session: the periodic liveness probe, under the same context
	// but its own WaitGroup (it lives as long as the session, where probeWG holds one-shot
	// work). It is launched whether or not anything is asserted NOW — a device becomes
	// asserted the first time it births, which can be minutes after this connect — and
	// only when there is a projection to read.
	if c.reconciler != nil {
		c.goWhileLive(sessionCtx, &c.loopWG, func() { c.presenceProbeLoop(sessionCtx) })
	}
}

// goWhileLive starts fn on its own goroutine, tracked in wg, unless the session has
// already ended, and reports whether it did.
//
// 🔴 onConnected RUNS ON PAHO'S GOROUTINE, NOT runLoop's, so it can still be finishing
// after Stop has cancelled the term and runLoop has returned — and then Stop is in, or
// about to be in, wg.Wait. A WaitGroup Add from zero racing a Wait is a misuse the
// runtime may panic on, and an Add after the Wait returned is a goroutine Stop never
// drained. The check and the Add happen under mu, and Stop cancels the term (which
// cancels every session context synchronously) under the same mu, so either the Add
// lands before the cancel — and therefore before Stop's Wait — or the session is seen
// ended and nothing starts.
func (c *Client) goWhileLive(sessionCtx context.Context, wg *sync.WaitGroup, fn func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionCtx.Err() != nil {
		return false
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		fn()
	}()
	return true
}

// presenceProbeLoop closes the half of the missed-death hole the connect probe cannot
// reach: a death certificate lost while the Host stays steadily connected. The connect
// probe runs once per session, so a Host that never reconnects never re-checks, and a
// device whose NDEATH the broker never delivered reads online for as long as the session
// lasts (ADR-067 SP4b closed the no-host window; this closes the steady one).
//
// Every probeInterval each node with an asserted-active device on this source gets one
// look, and only the devices that have produced NO traffic on this session within the
// last interval are asked about. That selection is what keeps the cost bounded: a device
// heard from inside the interval is alive by its own evidence and is neither rebirthed nor
// judged. The interval is cut into slots (probeSlots) and each node is asked in its own
// slot (slotOf), so the rebirths of a quiet fleet are spread across the interval instead
// of arriving on one tick; see probeTick.
//
// 🔴 IT JUDGES AGAINST ITS OWN START, NOT THE SESSION'S. A device that was heard from an
// hour ago and not since is exactly the one being asked about, so "seen at any time this
// session" would never declare it. The probe records `since` BEFORE it sends its rebirths,
// so only an answer to this probe (or any traffic after it began) counts.
//
// It reads the projection through readAssertedAndFloor, which also raises the epoch floor.
// That is harmless — the floor only ever moves up — and it keeps the two readers of the
// asserted set the same code. A tick that lands while the CONNECT probe is still in its
// window stands down before it reads anything, and its slot runs on the next tick instead:
// rebirthing the same nodes twice inside one window buys nothing, and the in-flight probe
// will deliver the verdict. The connect probe
// is the only probe a tick can overlap — this loop runs its own probes inline, so a tick
// cannot arrive while one of them is in its window.
//
// It runs under the session context, so it ends with the session, and a tick's probe
// aborts without emitting if the session ends mid-window, like the connect probe.
func (c *Client) presenceProbeLoop(ctx context.Context) {
	round := &probeRound{slots: probeSlots(c.probeInterval, c.probeWindow)}
	ticker := time.NewTicker(c.probeInterval / time.Duration(round.slots))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if c.probing.Load() {
			// The slot is not consumed: it runs on the next tick, late rather than skipped.
			log.Debug().Str("tenant", c.tenant).Msg("Deferring a periodic presence probe slot: another probe is still in its window.")
			continue
		}
		started := time.Now()
		err := c.probeTick(ctx, round)
		// ctx is the SESSION's: a slot cut short because the session ended is cancelled, not failed.
		c.metrics.PresenceProbePass.RecordPass(ctx, err, started, "presence-probe")
	}
}

// probeRound is the periodic probe's position in its rotation: which slot runs next, how
// many there are, and the asserted set read at the start of the current round.
type probeRound struct {
	slot     int
	slots    int
	asserted []AssertedDevice
}

// probeSlots is how many slots one interval is divided into: as many as fit with each
// slot at least twice the answer window, so a slot's probe (its rebirths, then its window)
// is over before the next slot is due. One slot when the window is too long for more.
func probeSlots(interval, window time.Duration) int {
	if window <= 0 {
		return 1
	}
	if n := int(interval / (2 * window)); n > 1 {
		return n
	}
	return 1
}

// slotOf is a node's slot in the rotation: a hash of its group/node identity, so a node
// is always asked at the same point of every round and the fleet's rebirths are spread
// evenly across the interval rather than all landing on one tick.
func slotOf(key nodeKey, slots int) int {
	if slots <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(nodeExternalId(key)))
	return int(h.Sum32() % uint32(slots))
}

// probeTick runs one slot of the periodic probe.
//
// 🔑 THE ROTATION SPREADS THE COST, NOT THE RULE. Every node is still judged once per
// interval, against the same silence cutoff (no traffic within the last interval) and
// with its own full answer window: a slot asks only its own nodes, waits the window, and
// delivers their verdict before the next slot starts. What changes is that a fleet's
// rebirths arrive a slot at a time instead of all at once.
//
// The asserted set is read at the start of each round (slot 0) and reused for the round's
// slots, so device-state is queried once per interval, as before. A device asserted
// mid-round waits for the next round. A failed read consumes nothing — the round retries
// on the next tick — and counts nothing; every slot that runs counts as a verdict, even one
// with nothing to ask.
//
// It returns an error only when the slot could not read the projection, which is the one way
// a slot fails to do its work; the caller files it as a failed pass.
func (c *Client) probeTick(ctx context.Context, r *probeRound) error {
	if r.slot == 0 {
		asserted, read := c.readAssertedAndFloor(ctx)
		if !read {
			// decided nothing, so counts nothing: a flat series is the right alarm
			return errors.New("could not read the asserted-active set from device-state")
		}
		r.asserted = asserted
	}
	slot := r.slot
	r.slot = (r.slot + 1) % r.slots

	now := c.now()
	mine := make([]AssertedDevice, 0)
	for _, d := range r.asserted {
		if key, ok := parseReconcileNodeKey(d.ExternalId); ok && slotOf(key, r.slots) == slot {
			mine = append(mine, d)
		}
	}
	silent := c.silentDevices(mine, now.Add(-c.probeInterval))
	if len(silent) == 0 {
		if c.metrics.PresenceProbes != nil {
			c.metrics.PresenceProbes.WithLabelValues(ProbeTriggerPeriodic).Inc()
		}
		return nil
	}
	c.reconcileProbe(ctx, silent, now, ProbeTriggerPeriodic)
	return nil
}

// silentDevices returns the devices among asserted that have NOT produced traffic on this
// session at or after cutoff: never heard from on this session, or last heard from before
// it. These are the ones a periodic probe asks about.
func (c *Client) silentDevices(asserted []AssertedDevice, cutoff time.Time) []AssertedDevice {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	silent := make([]AssertedDevice, 0)
	for _, d := range asserted {
		if last, ok := c.seen[d.ExternalId]; ok && !last.Before(cutoff) {
			continue
		}
		silent = append(silent, d)
	}
	return silent
}

// establishEpochFloor reads the device-state projection for this source's asserted-active
// devices and raises the epoch generator's floor above their max session (ADR-067 SP4b
// Major-4), so any birth processed on this session mints an epoch the projection will
// accept rather than reject as stale. It returns the asserted set (for the probe) or nil
// on a read failure / no reconciler — reconciliation is best-effort, retried next
// (re)connect, never a connection blocker. The read is bounded so a slow/absent
// device-state cannot stall connection setup.
func (c *Client) establishEpochFloor(ctx context.Context) []AssertedDevice {
	devices, _ := c.readAssertedAndFloor(ctx)
	return devices
}

// readAssertedAndFloor is establishEpochFloor that also reports whether the projection
// was READ: false when there is no reconciler or the read failed, true for a successful
// read, empty or not. The periodic probe needs the difference — a tick that read an empty
// set reached a verdict (nothing to ask), while one that could not read decided nothing.
func (c *Client) readAssertedAndFloor(ctx context.Context) ([]AssertedDevice, bool) {
	if c.reconciler == nil {
		return nil, false
	}
	qctx, cancel := context.WithTimeout(ctx, reconcileQueryTimeout)
	devices, maxSession, err := c.reconciler.AssertedActive(qctx, c.tenant, c.policy.Source)
	cancel()
	if err != nil {
		log.Warn().Err(err).Str("tenant", c.tenant).Msg("Presence reconciliation could not read device-state; skipping the floor + probe this cycle (the periodic probe retries on its next tick, the connect probe on the next reconnect).")
		return nil, false
	}
	if len(devices) == 0 {
		return nil, true
	}
	c.sessions.SetEpochFloor(maxSession + 1)
	return devices, true
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
	//
	// A device's message also proves its NODE alive: in Sparkplug a device has no
	// connection of its own, and everything it says is published over its edge node's
	// MQTT session. Without this a node whose devices report constantly but which
	// publishes no NDATA of its own — the common shape — reads as silent to every
	// periodic probe and is rebirthed every other interval for nothing.
	if !rec.Topic.IsState {
		c.markSeen(SparkplugExternalId(rec.Topic))
		if rec.Topic.IsDevice() {
			c.markSeen(nodeExternalId(nodeKey{rec.Topic.GroupID, rec.Topic.EdgeNodeID}))
		}
	}
	// Advance the SP2 session machine; it may command the node to rebirth (fired
	// via enqueueRebirth, outside its lock) and returns the numeric samples the
	// message contributes plus any presence transitions it asserts (ADR-067). STATE
	// has no node session.
	if !rec.Topic.IsState && rec.Payload != nil && c.ingester != nil {
		obs := c.sessions.Observe(rec.Topic, rec.Payload)
		c.countSkipped(obs.Skipped)
		if obs.Skipped.Oversize > 0 {
			log.Warn().Str("topic", msg.Topic()).Int("metrics", obs.Skipped.Oversize).
				Int("limit", MaxMetricsPerMessage).
				Msg("Refusing the readings of a Sparkplug message with more metrics than one message may carry; none of them were stored.")
		}
		samples := obs.Samples
		// Only a DATA message's readings are charged against the tenant's ingest ceiling.
		// A BIRTH's are not: Sparkplug reports by exception, so a birth is the only place
		// a rarely-changing metric appears, and a rebirth storm (a Host reconnect, a
		// leader handover, the reconcile probe) re-sends every node's whole metric set at
		// once — shedding a birth's tail would lose those values until they next change,
		// with no redelivery to bring them back. A birth is bounded by the session machine
		// instead: once per bdSeq, and a requested rebirth is backoff-gated — except the
		// liveness probe's, which is bounded by the probe interval (a quiet node at most
		// every other presence.probeSeconds).
		if len(samples) > 0 && isData(rec.Topic.MessageType) {
			samples = c.admitSamples(samples)
		}
		if len(samples) > 0 {
			c.ingestSamples(SparkplugExternalId(rec.Topic), samples)
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

// countSkipped records the metrics a message produced no sample from.
func (c *Client) countSkipped(k Skips) {
	if c.metrics.SamplesSkipped == nil || k.Total() == 0 {
		return
	}
	for reason, n := range map[string]int{SkipNonNumeric: k.NonNumeric, SkipNull: k.Null, SkipUnnamed: k.Unnamed, SkipOversize: k.Oversize} {
		if n > 0 {
			c.metrics.SamplesSkipped.WithLabelValues(reason).Add(float64(n))
		}
	}
}

// isData reports whether a message type carries DATA (NDATA or DDATA), the messages whose
// readings are charged against the tenant's ingest ceiling.
func isData(t MessageType) bool { return t == NDATA || t == DDATA }

// admitSamples charges a DATA message's samples against the tenant's ingest ceiling, ONCE
// per message and before ingestWithRetry, so an in-handler retry is never charged again. It
// returns the admitted leading samples: a whole number of events, the rest dropped and
// counted by the limiter on ingest_samples_shed_total. A dropped tail is real loss, since a
// clean-session Host gets no broker redelivery; that is what a ceiling is.
func (c *Client) admitSamples(samples []Sample) []Sample {
	return samples[:c.limiter.AdmitSamples(c.tenant, len(samples))]
}

// ingestSamples resolves the device and builds its samples' events, retrying a transient
// device-management failure within a bounded budget, and submits them to the publisher
// WITHOUT waiting for the stream to acknowledge them.
//
// The resolve runs ON paho's ordered receive goroutine (by design: the session machine
// needs strict ordering, and this is called in the order the messages arrived), so it must
// return in bounded time. A clean-session Host gets no broker redelivery, so a short retry
// saves a sample across a brief blip, but a prolonged outage is left to the Sparkplug
// host-offline path rather than blocking receive forever. Only the publish leaves that
// goroutine: it used to wait one acknowledgement (about 11 ms on a replicated broker) per
// message, which capped a connection near 88 messages a second. A publish that then fails
// is resubmitted (see publishItem) and, when the budget is spent, dropped and counted. A
// message larger than one event is emitted as several (adapter.Emitter.BuildMeasurements),
// so "dropped" then means "not fully stored": a publish that failed part-way may have stored
// any subset of its events, and each resubmission dedups them.
func (c *Client) ingestSamples(externalId string, samples []Sample) {
	c.ingest(
		func(ctx context.Context) (*Publication, error) {
			return c.ingester.PrepareIngest(ctx, c.tenant, c.policy, externalId, samples)
		},
		func(reason string, err error) {
			// A backpressure drop is logged at debug: the refusal is counted
			// (IngestFailures, and the writer's refused counter) and the gate logs its own
			// close and open, so a warning per dropped message would only bury those.
			ev := log.Warn()
			if errors.Is(err, messaging.ErrStreamBackpressure) {
				ev = log.Debug()
			}
			ev.Err(err).Str("tenant", c.tenant).Str("externalId", externalId).Int("samples", len(samples)).
				Str("reason", reason).Msg("Dropping Sparkplug samples (a clean-session Host gets no broker redelivery); " +
				"a message split across several events may already have stored some of its events.")
		},
	)
}

// ingestPresence emits the presence transitions an accepted message asserted, on the
// same bounded-retry contract as ingestSamples: a clean-session Host gets no broker
// redelivery, so a brief device-management/NATS blip is retried, but a prolonged
// outage is left to the host-offline path rather than blocking receive. The whole
// batch is submitted as one call and re-tried whole; the DedupID makes an already-emitted
// transition idempotent.
func (c *Client) ingestPresence(events []PresenceEvent) {
	c.ingest(
		func(ctx context.Context) (*Publication, error) {
			return c.ingester.PreparePresence(ctx, c.tenant, c.policy, events)
		},
		func(reason string, err error) {
			log.Warn().Err(err).Str("tenant", c.tenant).Int("presence", len(events)).
				Str("reason", reason).Msg("Dropping Sparkplug presence (a clean-session Host gets no broker redelivery).")
		},
	)
}

// ingest prepares a publication under the bounded in-handler retry and submits it.
func (c *Client) ingest(prepare func(ctx context.Context) (*Publication, error), onDrop func(reason string, err error)) {
	var pub *Publication
	ok := c.ingestWithRetry(func(ctx context.Context) error {
		p, err := prepare(ctx)
		pub = p
		return err
	}, onDrop)
	if !ok || pub == nil {
		return // abandoned (counted) or definitively dropped by the ingester (counted there)
	}
	c.submit(&publishItem{pub: pub, onDrop: onDrop})
}

// publishContext is the context a publication is submitted under: the tenant for its
// subject, and the connection's lifetime, so a Submit blocked behind a full window is
// released when the connection stops. It carries NO deadline of its own: the writer applies
// its publish ceiling from the moment each message is sent, and a deadline counted from
// here would also be charged for the time spent waiting for room in the window.
func (c *Client) publishContext(tenant string) context.Context {
	c.mu.Lock()
	ctx := c.runCtx
	c.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return core.WithTenant(ctx, tenant)
}

// submit hands a publication to the publisher and returns once the publisher has room for
// it, which is before the stream's acknowledgement.
//
// 🔴 ORDER IS PER SUBMITTING GOROUTINE. The receive goroutine submits in the order the
// messages arrived, which is the order they reach the stream; the reconcile probe and the
// retry goroutine submit from their own, and the publisher promises no order between
// goroutines. That is safe here because every message carries a content-derived dedup id
// and presence is ordered by (session, time), not by arrival.
func (c *Client) submit(it *publishItem) {
	c.subMu.RLock()
	defer c.subMu.RUnlock()
	if c.stopped {
		c.drop(it, "connection shutting down", errClientStopped)
		return
	}
	if c.publisher == nil {
		log.Error().Str("tenant", c.tenant).Msg("A Sparkplug client with an ingest path has no publisher; abandoning what it accepted.")
		c.drop(it, "no publisher", errNoPublisher)
		return
	}
	c.publisher.Submit(c.publishContext(it.pub.Tenant), it.pub.Messages, func(err error) { c.published(it, err) })
}

// published is the publisher's done for one attempt. It runs on the writer's goroutine (or
// on the submitter's, for a call the writer never took), so it never blocks and never
// submits: a failure is handed to the retry goroutine.
func (c *Client) published(it *publishItem, err error) {
	if err == nil {
		it.pub.Delivered()
		return
	}
	// Not retried: the stream is refusing new events (backpressure), which lasts far longer
	// than this budget. Presence is admitted past the gate (BypassBackpressure), so what
	// reaches here is samples.
	if errors.Is(err, messaging.ErrStreamBackpressure) {
		c.drop(it, "ingest backpressure", err)
		return
	}
	it.attempts++
	if it.attempts >= ingestMaxAttempts {
		c.drop(it, "ingest retry budget exhausted", err)
		return
	}
	it.retryAt = time.Now().Add(ingestRetryBackoff)
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	if c.retryClosed {
		c.drop(it, "connection shutting down", err)
		return
	}
	select {
	case c.retryCh <- it:
	default:
		c.drop(it, "publish retry queue full", err)
	}
}

// drop abandons a publication: counted on IngestFailures and logged by its own onDrop, so
// an abandonment is never silent.
func (c *Client) drop(it *publishItem, reason string, err error) {
	if c.metrics.IngestFailures != nil {
		c.metrics.IngestFailures.Inc()
	}
	it.onDrop(reason, err)
}

// retryWorker resubmits publishes that failed, each once its backoff has passed, from a
// goroutine of its own. It is the only submitter whose order does not matter: a resubmission
// lands behind later messages, which the dedup ids make safe. On the connection's end it
// stops, and what is still queued is abandoned and counted by Stop.
func (c *Client) retryWorker(ctx context.Context, ch chan *publishItem) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-ch:
			if wait := time.Until(it.retryAt); wait > 0 && !sleep(ctx, wait) {
				c.drop(it, "connection shutting down", ctx.Err())
				return
			}
			c.submit(it)
		}
	}
}

// drainPublishes ends a leadership term's publishing: nothing already queued for a retry is
// resubmitted, no new submission is taken, and everything already submitted is waited for,
// bounded, so the lease is released only once nothing of this term is still on its way to
// the stream. Stop calls it after the connection and every goroutine of the term are gone.
//
// A publish that fails during the wait is abandoned and counted rather than retried: the
// term is over, and a resubmission after the lease is released is the one thing the barrier
// exists to prevent. The host is a clean-session client, so what is abandoned here is lost,
// and it is counted.
func (c *Client) drainPublishes() {
	c.retryMu.Lock()
	c.retryClosed = true
	ch := c.retryCh
	c.retryMu.Unlock()
	for drained := false; ch != nil && !drained; {
		select {
		case it := <-ch:
			c.drop(it, "connection shutting down", errClientStopped)
		default:
			drained = true
		}
	}

	c.subMu.Lock()
	c.stopped = true
	c.subMu.Unlock()

	if c.publisher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.settleTimeout)
	defer cancel()
	if err := c.publisher.Settle(ctx); err != nil {
		log.Error().Err(err).Str("tenant", c.tenant).Dur("waited", c.settleTimeout).
			Msg("A Sparkplug connection could not confirm that everything it published is stored before it stopped: " +
				"what is still unconfirmed is lost, or may be stored after the next leader has begun. " +
				"Every message carries a content-derived id, so a copy stored twice is stored once.")
	}
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
// as a death.
//
// `since` is the instant from which traffic counts as an answer. The connect probe passes
// the zero time: the seen set was reset in onConnected, so anything heard on this session
// — including a re-birth that arrived before the probe — proves the device alive. The
// periodic probe passes its own start, because the device it is asking about may well
// have been heard from earlier in the session; see presenceProbeLoop. `trigger` labels the
// probe's metrics.
func (c *Client) reconcileProbe(ctx context.Context, devices []AssertedDevice, since time.Time, trigger string) {
	// One probe at a time: a periodic tick checks this before reading the projection.
	c.probing.Store(true)
	defer c.probing.Store(false)

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
		if c.seenSince(d.ExternalId, since) {
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
	// The verdict is in: count the probe, and what it declared.
	if c.metrics.PresenceProbes != nil {
		c.metrics.PresenceProbes.WithLabelValues(trigger).Inc()
	}
	if len(stale) > 0 {
		if c.metrics.PresenceProbeDisconnects != nil {
			c.metrics.PresenceProbeDisconnects.WithLabelValues(trigger).Add(float64(len(stale)))
		}
		log.Warn().Str("tenant", c.tenant).Str("trigger", trigger).Int("disconnected", len(stale)).
			Msg("Presence probe declaring unresponsive asserted devices DISCONNECTED (asked to rebirth, no answer inside the window).")
		c.ingestPresence(stale)
	}
}

// resetSeen begins a fresh session's record: it clears the last-heard map so only
// traffic from here on counts toward liveness.
func (c *Client) resetSeen() {
	c.seenMu.Lock()
	c.seen = make(map[string]time.Time)
	c.seenMu.Unlock()
}

// markSeen records that an external id produced traffic now, on the current session
// (no-op before the first session has begun — seen is nil).
func (c *Client) markSeen(externalId string) {
	now := c.now()
	c.seenMu.Lock()
	if c.seen != nil {
		c.seen[externalId] = now
	}
	c.seenMu.Unlock()
}

// seenSince reports whether an external id has produced traffic on this session at or
// after since. The zero since asks "at any time this session".
func (c *Client) seenSince(externalId string, since time.Time) bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	last, ok := c.seen[externalId]
	return ok && !last.Before(since)
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
// presence paths, around the PREPARE step (resolving the device and building the messages;
// the publish is retried separately, see published). It runs ON paho's ordered
// receive goroutine (by design — the session machine needs strict ordering), so it must
// return in bounded time: a clean-session Host gets no broker redelivery, so a short retry
// saves work across a brief blip, but a prolonged outage is left to the Sparkplug
// host-offline path rather than blocking receive forever. EVERY abandonment path routes
// through onDrop so a drop (real, bounded loss) is never silent; onDrop also increments the
// shared IngestFailures counter. It reports whether attempt succeeded.
func (c *Client) ingestWithRetry(attempt func(ctx context.Context) error, onDrop func(reason string, err error)) bool {
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
			return true
		}
		// (A backpressure refusal is not in this loop: the stream's gate is consulted by the
		// publish, which is no longer attempted here. See published.)
		if ctx.Err() != nil {
			drop("connection shutting down", err)
			return false
		}
		if n >= ingestMaxAttempts {
			drop("ingest retry budget exhausted", err)
			return false
		}
		if !sleep(ctx, ingestRetryBackoff) {
			drop("connection shutting down", err)
			return false
		}
	}
}

// enqueueRebirth is the session machine's rebirth callback. It runs on paho's
// ordered receive goroutine, so it must NOT block: it hands the request to the
// async publisher via a buffered channel and returns. A full channel is dropped
// (the per-node backoff re-requests on the next window), logged and counted so
// saturation is visible on a series and not only in a log line — see
// Metrics.RebirthEnqueued for why the accepted side is counted too.
func (c *Client) enqueueRebirth(group, node string) {
	select {
	case c.rebirthCh <- nodeKey{group, node}:
		if c.metrics.RebirthEnqueued != nil {
			c.metrics.RebirthEnqueued.Inc()
		}
	default:
		if c.metrics.RebirthDropped != nil {
			c.metrics.RebirthDropped.Inc()
		}
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
	// And the periodic loop, which the same cancellation ended (a probe it was running
	// aborted with it).
	c.loopWG.Wait()
	// Last, and bounded: wait for the publications this term submitted to be stored. Stop
	// is what a leadership term ends with before it releases the lease, and "nothing of
	// this term is published after the next leader has begun" holds only once the writer has
	// nothing in flight, which is not the same as the receive goroutine having returned.
	c.drainPublishes()
}

// setSessionIfLive records the live client and its session timestamp, unless its
// session has already ended. runLoop cancels a session's context BEFORE it clears the
// session (clearSession), and both run under mu, so a connect handler finishing late
// cannot reinstate a connection runLoop has already abandoned.
func (c *Client) setSessionIfLive(sessionCtx context.Context, client mqtt.Client, sessionTs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionCtx.Err() != nil {
		return
	}
	c.mc = client
	c.sessionTs = sessionTs
}

// clearSession forgets client as the live connection, if it still is the live one, so
// the rebirth publisher and Stop stop addressing a connection that has ended.
func (c *Client) clearSession(client mqtt.Client) {
	c.mu.Lock()
	if c.mc == client {
		c.mc = nil
		c.sessionTs = 0
	}
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
