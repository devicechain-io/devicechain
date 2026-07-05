// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

const (
	// streamMaxAge bounds how long undelivered/retained messages live in a
	// JetStream stream. Mirrors a Kafka retention window; durable consumers
	// track their own position independently.
	streamMaxAge = 7 * 24 * time.Hour

	// fetchTimeout bounds a single pull-consumer Fetch so an idle reader can
	// periodically check for shutdown instead of blocking forever.
	fetchTimeout = 1 * time.Second

	// fetchBatch is how many messages a single Fetch pulls. Batching amortizes
	// the request-reply round trip across many messages so consume throughput is
	// not capped at ~one RTT per message (ADR-022 review B1). Messages are then
	// handed to the caller one per ReadMessage from an internal buffer. The whole
	// batch starts its AckWait timer at fetch, so fetchBatch is kept well below
	// ackWait * throughput (and within the processors' MESSAGE_BACKLOG channel)
	// so the tail of a batch is not redelivered while still in the pipeline.
	fetchBatch = 64

	// ackWait is how long JetStream waits for an ack before redelivering a
	// message. It must comfortably exceed the time a fetched batch takes to clear
	// the worker pipeline (worst-case per-message DB persist latency * batch /
	// workers) so a slow-but-succeeding message is not redelivered underneath the
	// worker (ADR-022 review A4).
	ackWait = 60 * time.Second

	// MaxDeliver bounds redelivery of a poison message: after this many delivery
	// attempts the broker stops redelivering, and consumers route the message to
	// their dead-letter path (failed-events) on the final attempt rather than
	// looping forever (ADR-022 review A4). Consumers compare Message.NumDelivered
	// against this.
	MaxDeliver = 5

	// readerMaxAckPending pins the consumer's max in-flight unacked messages. It
	// matches the value the legacy PullSubscribe path set implicitly (its delivery
	// channel capacity, SubChanLen) so AddConsumer stays idempotent against durables
	// an older build created, and so freshly-created and upgraded-in-place consumers
	// do not silently diverge (an unset value would default to 1000 on the server).
	readerMaxAckPending = 65536

	// rebindBackoffMin/Max bound the self-heal retry when a reader's durable consumer
	// goes away (e.g. deleted by an older pod's Unsubscribe during a rolling update).
	// The reader re-binds with capped exponential backoff so a persistent failure does
	// not busy-loop the way a bare Fetch-error retry would.
	rebindBackoffMin = 500 * time.Millisecond
	rebindBackoffMax = 5 * time.Second

	// livenessProbeAfterTimeouts is how many consecutive empty (timed-out) fetches
	// trigger a consumer-existence probe. A deleted consumer does not always surface
	// as an explicit error on the next fetch — depending on broker/timing it can look
	// like an ordinary empty fetch — so a reader that has gone quiet cheaply confirms
	// its durable still exists (one ConsumerInfo call per ~this-many idle seconds) and
	// re-binds if it has vanished, rather than idling silently on a dead consumer.
	livenessProbeAfterTimeouts = 5

	// liveBuffer bounds a live subscription's in-flight buffer. A fan-out live
	// feed (SubscribeLive) prefers dropping under a slow client to stalling the
	// shared pipeline, so a full buffer drops (NATS slow-consumer) rather than
	// applying backpressure — history is served by the queries, not this feed.
	liveBuffer = 256
)

// natsAck adapts a JetStream *nats.Msg to the transport-neutral Acknowledger so
// the ack handle can ride the Message envelope to the worker that ultimately
// handles it (ADR-022 review A3: ack only after the message is durably handled).
type natsAck struct{ nm *nats.Msg }

func (a natsAck) Ack() error { return a.nm.Ack() }
func (a natsAck) Nak() error { return a.nm.Nak() }

// NatsManager manages the lifecycle of NATS JetStream interactions for a
// microservice. It mirrors the former KafkaManager's lifecycle shape so the
// service mains change minimally.
type NatsManager struct {
	Microservice *core.Microservice

	oncreate  func(*NatsManager) error
	nc        *nats.Conn
	js        nats.JetStreamContext
	readers   []*natsReader
	writers   []*natsWriter
	lifecycle core.LifecycleManager
}

// NewNatsManager creates a new NATS manager. oncreate is invoked on Start to
// instantiate the service's readers/writers (mirrors KafkaManager).
func NewNatsManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	oncreate func(*NatsManager) error) *NatsManager {
	nmgr := &NatsManager{
		Microservice: ms,
		oncreate:     oncreate,
		readers:      make([]*natsReader, 0),
		writers:      make([]*natsWriter, 0),
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "nats")
	nmgr.lifecycle = core.NewLifecycleManager(name, nmgr, callbacks)
	return nmgr
}

// Conn exposes the underlying NATS connection for core (non-JetStream)
// request/reply patterns such as the ADR-025 auth-callout responder. It is nil
// until ExecuteInitialize has connected.
func (nmgr *NatsManager) Conn() *nats.Conn {
	return nmgr.nc
}

// NatsUrl returns the NATS connection url from instance configuration.
func (nmgr *NatsManager) NatsUrl() string {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	return fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port)
}

// streamReplicas returns the configured JetStream replica count (defaulting to
// 1 when unset) so a single-node dev cluster and an HA cluster (ADR-018) share
// one code path.
func (nmgr *NatsManager) streamReplicas() int {
	r := int(nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas)
	if r < 1 {
		return 1
	}
	return r
}

// ensureStream creates the per-suffix stream if it does not already exist. The
// stream captures every tenant's subjects for the suffix via the wildcard
// subject, so a single stream backs both the scoped producers and the shared
// wildcard consumer.
func (nmgr *NatsManager) ensureStream(suffix string) (string, error) {
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	// Retry on connection/server errors so a few seconds of NATS lag on a cluster
	// restart degrades into a retry rather than a crash-loop (A6). A stream that
	// does not yet exist (ErrStreamNotFound) is the normal first-run case and is
	// handled by creating it, not retried.
	err := core.RetryInfraConnect(context.Background(), "nats jetstream", func(context.Context) error {
		if _, err := nmgr.js.StreamInfo(name); err == nil {
			return nil
		} else if !errors.Is(err, nats.ErrStreamNotFound) {
			return err
		}
		if _, err := nmgr.js.AddStream(&nats.StreamConfig{
			Name:      name,
			Subjects:  []string{WildcardSubject(nmgr.Microservice.InstanceId, suffix)},
			Storage:   nats.FileStorage,
			Retention: nats.LimitsPolicy,
			Discard:   nats.DiscardOld,
			MaxAge:    streamMaxAge,
			Replicas:  nmgr.streamReplicas(),
		}); err != nil {
			return err
		}
		log.Info().Str("stream", name).Msg("Created JetStream stream")
		return nil
	})
	if err != nil {
		return "", err
	}
	return name, nil
}

// ----------------
// Writer (producer)
// ----------------

// natsWriter publishes to a per-suffix subject, deriving the tenant-scoped
// subject from context at write time (fail-closed when no tenant is present).
type natsWriter struct {
	nmgr   *NatsManager
	suffix string
}

// NewWriter creates a producer for the given subject suffix. The stream backing
// the suffix is created if needed. The returned writer builds the fully-scoped
// subject ("{instance}.{tenant}.{suffix}") per message from the tenant in
// context.
func (nmgr *NatsManager) NewWriter(suffix string) (MessageWriter, error) {
	if _, err := nmgr.ensureStream(suffix); err != nil {
		return nil, err
	}
	w := &natsWriter{nmgr: nmgr, suffix: suffix}
	nmgr.writers = append(nmgr.writers, w)
	log.Info().Str("suffix", suffix).Msg("Added new NATS writer")
	return w, nil
}

// WriteMessages publishes each message to the writer's tenant-scoped subject.
// The tenant is taken from context and is the single source of the subject
// (fail-closed): a write with no tenant in context is rejected rather than
// published unscoped. All messages in one call share the caller's tenant, so
// the subject is derived once.
//
// The tenant is validated against the global token grammar (ADR-042) before it is
// spliced into the subject — the universal belt-and-suspenders behind ADR-025's
// broker grant. Every producer's tenant flows through here, and some of it
// originates from device-controlled addressing (an event-source derives it from an
// MQTT topic / HTTP path). A tenant carrying "." / "*" / ">" would shift subject
// segments or inject a cross-tenant wildcard, so a malformed tenant is rejected
// here rather than published to a corrupted subject. Legitimate tenants always
// pass: the same grammar is enforced when a tenant is created.
func (w *natsWriter) WriteMessages(ctx context.Context, msgs ...Message) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	if err := core.ValidateToken(tenant); err != nil {
		return fmt.Errorf("messaging: refusing to publish to a subject for an invalid tenant: %w", err)
	}
	subject := ScopedSubject(w.nmgr.Microservice.InstanceId, tenant, w.suffix)
	for i := range msgs {
		nm := &nats.Msg{Subject: subject, Data: msgs[i].Value, Header: nats.Header{}}
		// Carry the correlation id, generating one when the producer did not
		// propagate it, so any message can be followed across the pipeline (E15).
		cid := msgs[i].CorrelationID()
		if cid == "" {
			cid = uuid.NewString()
		}
		nm.Header.Set(HeaderCorrelationID, cid)
		for k, v := range msgs[i].Headers {
			if k != HeaderCorrelationID {
				nm.Header.Set(k, v)
			}
		}
		if _, err := w.nmgr.js.PublishMsg(nm); err != nil {
			return err
		}
	}
	return nil
}

// HandleResponse logs the result of a write operation.
func (w *natsWriter) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Str("suffix", w.suffix).Msg("nats write operation failed")
	} else if log.Debug().Enabled() {
		log.Debug().Str("suffix", w.suffix).Msg("nats write operation successful")
	}
}

// ----------------
// Reader (consumer)
// ----------------

// natsReader is a durable pull-consumer over the cross-tenant wildcard subject
// for a suffix. The shared microservice consumes all tenants' messages here and
// derives the per-message tenant from the delivered subject.
type natsReader struct {
	nmgr    *NatsManager
	suffix  string
	stream  string
	subject string
	durable string
	// sub is the current pull subscription. It is written by the read-loop goroutine
	// (bind, on first attach and on self-heal re-bind) and read by the lifecycle
	// goroutine (ExecuteStop's Unsubscribe), so it is an atomic pointer rather than a
	// plain field — the self-heal made it mutable across goroutines.
	sub atomic.Pointer[nats.Subscription]
	// gate pauses consumption until the service's data plane is ready (ADR-022
	// decision 3): a degraded service parks in ReadMessage instead of draining
	// messages without live auth.
	gate *core.ReadinessGate
	// pending buffers the remainder of the last batch Fetch so ReadMessage can
	// hand messages out one at a time while fetching in batches (B1). Messages
	// are not acked here; the ack handle rides the returned Message (A3).
	pending []*nats.Msg
	// consecutiveTimeouts counts empty fetches since the last delivery, driving the
	// periodic consumer-liveness probe. Only the single read-loop goroutine touches
	// it, so it needs no synchronization.
	consecutiveTimeouts int
}

// consumerConfig is the durable pull-consumer configuration (A4) — explicit-ack so a
// message is only removed once a consumer acks it after durable handling (A3), a
// deliberate AckWait sized to persistence latency, a finite MaxDeliver so a poison
// message stops redelivering, and the cross-tenant wildcard filter.
//
// Every field here MUST match what the prior PullSubscribe(subject, durable,
// AckExplicit, AckWait, MaxDeliver) created, so that AddConsumer is idempotent
// against a durable an older build already created on an existing cluster (nats.go's
// checkConfig only compares fields the config sets, and rejects a mismatch). In
// particular: DeliverPolicy is left at its zero value (DeliverAll) to match, and
// MaxAckPending is pinned to the value the old PullSubscribe path used implicitly
// (its subscription channel capacity) rather than left unset — unset would inherit
// the server default (1000) on freshly-created consumers while upgraded-in-place
// durables kept the old value, a silent config fork. WARNING: changing any field
// here (or adding a newly-compared one) will make AddConsumer reject an existing
// durable and crash-loop startup on a non-fresh cluster — a config change must ride a
// fresh bring-up (down+up) or an explicit consumer migration (pre-GA: prefer the
// decisive cutover).
func (r *natsReader) consumerConfig() *nats.ConsumerConfig {
	return &nats.ConsumerConfig{
		Durable:       r.durable,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    MaxDeliver,
		MaxAckPending: readerMaxAckPending,
		FilterSubject: r.subject,
	}
}

// bind (re)creates the durable consumer if needed and binds a pull subscription to
// it WITHOUT owning its lifecycle. This is the fix for the rolling-update hazard: a
// pull subscription that CREATED its consumer deletes it on Unsubscribe/Drain
// (nats.go sets the delete-consumer flag), so during a rolling update — where the
// old and new pods briefly share this one durable — the terminating pod would delete
// the consumer out from under the new pod, which then hot-spins on a dead
// subscription. Creating the consumer out-of-band (AddConsumer is idempotent for a
// matching durable) and attaching with nats.Bind leaves the client's delete flag off,
// so the durable survives every pod's shutdown. bind is also the self-heal path:
// ReadMessage calls it to re-attach if the consumer ever does go away.
func (r *natsReader) bind() error {
	// Release any stale subscription first. With a bound sub this does not delete the
	// durable; it just releases the old (dead) subscription on a re-bind. The old
	// pointer stays published until the new one is ready, so a concurrent
	// ExecuteStop always sees a non-nil, safe-to-Unsubscribe subscription.
	if old := r.sub.Load(); old != nil {
		_ = old.Unsubscribe()
	}
	if _, err := r.nmgr.js.AddConsumer(r.stream, r.consumerConfig()); err != nil {
		return err
	}
	sub, err := r.nmgr.js.PullSubscribe(r.subject, r.durable, nats.Bind(r.stream, r.durable))
	if err != nil {
		return err
	}
	r.sub.Store(sub)
	return nil
}

// NewReader creates a durable pull consumer for the given subject suffix,
// subscribing to the cross-tenant wildcard so one shared pod drains every
// tenant. The durable name is scoped to the instance + functional area + suffix
// (not the tenant), so every replica of a service shares one consumer and each
// message is delivered to exactly one of them.
func (nmgr *NatsManager) NewReader(suffix string) (MessageReader, error) {
	stream, err := nmgr.ensureStream(suffix)
	if err != nil {
		return nil, err
	}
	r := &natsReader{
		nmgr:    nmgr,
		suffix:  suffix,
		stream:  stream,
		subject: WildcardSubject(nmgr.Microservice.InstanceId, suffix),
		durable: DurableName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, suffix),
		gate:    nmgr.Microservice.Readiness,
	}
	if err := r.bind(); err != nil {
		return nil, err
	}
	nmgr.readers = append(nmgr.readers, r)
	log.Info().Str("durable", r.durable).Str("subject", r.subject).Msg("Added new NATS reader")
	return r, nil
}

// isConsumerGone reports whether a Fetch error means the durable consumer no longer
// exists (deleted, not found, inactive) or is unreachable (no responders) — the
// conditions the reader self-heals from by re-binding, as opposed to a transient
// timeout or a shutdown-time connection close.
func isConsumerGone(err error) bool {
	return errors.Is(err, nats.ErrConsumerDeleted) ||
		errors.Is(err, nats.ErrConsumerNotFound) ||
		errors.Is(err, nats.ErrConsumerNotActive) ||
		errors.Is(err, nats.ErrNoResponders)
}

// rebindWithBackoff re-attaches the reader to its durable consumer (recreating it if
// needed), retrying with capped exponential backoff until it succeeds or ctx is
// cancelled (shutdown). It never gives up on its own: a consumer the service is
// meant to be draining should be restored, not abandoned.
func (r *natsReader) rebindWithBackoff(ctx context.Context) error {
	backoff := rebindBackoffMin
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if err := r.bind(); err != nil {
			// A closing/draining connection means the service is shutting down (or the
			// broker went away): stop trying and let the caller unwind as EOF, rather
			// than looping forever — some readers stop by draining the connection
			// instead of cancelling this context (e.g. command-delivery).
			if errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, nats.ErrConnectionDraining) {
				return err
			}
			log.Error().Err(err).Str("durable", r.durable).Msg("Re-bind of durable consumer failed; will retry")
			if backoff *= 2; backoff > rebindBackoffMax {
				backoff = rebindBackoffMax
			}
			continue
		}
		log.Info().Str("durable", r.durable).Msg("Re-bound durable consumer")
		return nil
	}
}

// ReadMessage returns the next message, blocking until one is available, the
// context is cancelled, or the subscription closes. Messages are fetched in
// batches (B1) and buffered, so most calls return from the buffer without a
// round trip. The message is NOT acked here (A3): its ack handle rides the
// returned envelope so the consumer can Ack only after durably handling it, or
// Nak to request redelivery. On shutdown (ctx cancelled or subscription/
// connection closed) it returns io.EOF so the existing processor EOF handling
// applies.
func (r *natsReader) ReadMessage(ctx context.Context) (Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, io.EOF
		}
		// Stay parked until the data plane is released (ADR-022 decision 3). A
		// cancelled context here means shutdown, which surfaces as EOF below.
		if r.gate != nil {
			if err := r.gate.WaitReady(ctx); err != nil {
				return Message{}, io.EOF
			}
		}
		if len(r.pending) == 0 {
			msgs, err := r.sub.Load().Fetch(fetchBatch, nats.MaxWait(fetchTimeout))
			if err != nil {
				if errors.Is(err, nats.ErrTimeout) {
					// An empty fetch. A run of them can also mean the consumer was
					// deleted without the next fetch surfacing an explicit error, so
					// periodically confirm it still exists and re-bind if it has gone.
					r.consecutiveTimeouts++
					if r.consecutiveTimeouts >= livenessProbeAfterTimeouts {
						r.consecutiveTimeouts = 0
						if _, cerr := r.nmgr.js.ConsumerInfo(r.stream, r.durable); errors.Is(cerr, nats.ErrConsumerNotFound) {
							log.Warn().Str("durable", r.durable).Msg("Durable consumer missing on liveness probe; re-binding")
							if rebindErr := r.rebindWithBackoff(ctx); rebindErr != nil {
								return Message{}, io.EOF
							}
						}
					}
					continue
				}
				r.consecutiveTimeouts = 0
				if errors.Is(err, nats.ErrConnectionClosed) ||
					errors.Is(err, nats.ErrSubscriptionClosed) ||
					errors.Is(err, nats.ErrConnectionDraining) {
					return Message{}, io.EOF
				}
				// The durable consumer went away underneath us — e.g. an older pod's
				// Unsubscribe during a rolling update, or a broker restart. Re-bind to
				// it (recreating it if needed) with bounded backoff instead of
				// hot-spinning on a dead subscription. A cancelled context during the
				// backoff means shutdown, surfaced as EOF.
				//
				// Note the at-least-once cost: if the consumer was truly deleted, the
				// re-created consumer starts at DeliverAll and replays retained (up to
				// streamMaxAge) messages once. This happens on the FIRST rollout onto
				// this fix, because the terminating old-code pod still deletes the
				// durable it owned; from then on the durable survives and there is no
				// replay. A fresh bring-up (down+up) avoids the one-time transition
				// replay entirely.
				if isConsumerGone(err) {
					log.Warn().Err(err).Str("durable", r.durable).Msg("Durable consumer unavailable; re-binding")
					if rebindErr := r.rebindWithBackoff(ctx); rebindErr != nil {
						return Message{}, io.EOF
					}
					continue
				}
				return Message{}, err
			}
			if len(msgs) == 0 {
				continue
			}
			r.consecutiveTimeouts = 0
			r.pending = msgs
		}
		nm := r.pending[0]
		r.pending = r.pending[1:]
		return NewConsumedMessage(nm.Subject, nm.Data, deliveryCount(nm), natsHeaders(nm), natsAck{nm: nm}), nil
	}
}

// SubscribeLive opens an ephemeral, tenant-scoped fan-out subscription over a
// suffix's live subject, for streaming events to a connected client (the
// GraphQL subscription bridge, ADR-037/ADR-039). Unlike NewReader's durable,
// load-balanced pull consumer, each SubscribeLive is its own core NATS
// subscription bound to a single tenant's subject ("{instance}.{tenant}.{suffix}"):
// every subscriber receives every message for its tenant (fan-out, not
// load-balanced), there are no acks, and there is no backlog replay — a client
// sees events from subscribe time onward. The subscription is torn down when ctx
// is cancelled (the client unsubscribed or the socket closed). A slow reader
// drops messages (bounded buffer) rather than stalling the pipeline.
func (nmgr *NatsManager) SubscribeLive(ctx context.Context, tenant string, suffix string) (<-chan Message, error) {
	// Validate the tenant before it becomes a subscription filter — the read-side
	// twin of the WriteMessages guard, and the higher-blast-radius one: a tenant of
	// "*"/">" here would subscribe across EVERY tenant's live feed, not just corrupt
	// one publish. Legitimate tenants (a verified JWT claim, grammar-checked at
	// creation) always pass; a malformed one is rejected rather than fanned in.
	if err := core.ValidateToken(tenant); err != nil {
		return nil, fmt.Errorf("messaging: refusing to subscribe to a subject for an invalid tenant: %w", err)
	}
	subject := ScopedSubject(nmgr.Microservice.InstanceId, tenant, suffix)
	raw := make(chan *nats.Msg, liveBuffer)
	sub, err := nmgr.nc.ChanSubscribe(subject, raw)
	if err != nil {
		return nil, err
	}
	out := make(chan Message)
	go func() {
		defer close(out)
		defer func() { _ = sub.Unsubscribe() }()
		for {
			select {
			case <-ctx.Done():
				return
			case nm, ok := <-raw:
				if !ok {
					return
				}
				// A live message is never acked (no acknowledger): it is a
				// fire-and-forget fan-out to the connected client, not a
				// durable-processing handoff. Headers are dropped (nil): the live
				// feed's consumer reads only the payload, so building a per-message
				// header map would be wasted work on the hot path.
				msg := NewConsumedMessage(nm.Subject, nm.Data, 0, nil, nil)
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	log.Info().Str("subject", subject).Msg("Opened live NATS subscription")
	return out, nil
}

// natsHeaders flattens a delivered message's NATS headers into the transport-
// neutral map carried on the envelope (E15), or nil when there are none.
func natsHeaders(nm *nats.Msg) map[string]string {
	if len(nm.Header) == 0 {
		return nil
	}
	headers := make(map[string]string, len(nm.Header))
	for k := range nm.Header {
		headers[k] = nm.Header.Get(k)
	}
	return headers
}

// deliveryCount returns the JetStream delivery attempt count for a consumed
// message (1 on first delivery), or 0 when metadata is unavailable (which can
// happen for a non-JetStream message and should not block handling).
func deliveryCount(nm *nats.Msg) int {
	md, err := nm.Metadata()
	if err != nil {
		return 0
	}
	return int(md.NumDelivered)
}

// HandleResponse logs the result of a read operation.
func (r *natsReader) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Str("suffix", r.suffix).Msg("nats read operation failed")
	} else if log.Debug().Enabled() {
		log.Debug().Str("suffix", r.suffix).Msg("nats read operation successful")
	}
}

// ----------------
// Lifecycle
// ----------------

// Initialize component.
func (nmgr *NatsManager) Initialize(ctx context.Context) error {
	return nmgr.lifecycle.Initialize(ctx)
}

// ExecuteInitialize connects to NATS and obtains a JetStream context.
func (nmgr *NatsManager) ExecuteInitialize(context.Context) error {
	url := nmgr.NatsUrl()
	natscfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	opts := []nats.Option{
		nats.Name(nmgr.Microservice.FunctionalArea),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
	}
	// When the broker terminates TLS (ADR-025) every client must dial over TLS or
	// the handshake on the TLS-required port fails; verify the server against the
	// CA threaded into the instance config.
	tlsConfig, err := natscfg.TLSConfig(natscfg.Hostname)
	if err != nil {
		return err
	}
	if tlsConfig != nil {
		opts = append(opts, nats.Secure(tlsConfig))
	}
	// Present the shared service credential once broker auth is enabled (ADR-025);
	// internal services are exempt from the device callout via auth_users, so this
	// static login is what places them in the APP account with full permissions.
	if natscfg.Auth.User != "" {
		opts = append(opts, nats.UserInfo(natscfg.Auth.User, natscfg.Auth.Password))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return err
	}
	nmgr.nc = nc
	nmgr.js = js
	log.Info().Msg(fmt.Sprintf("Verified connectivity to NATS at '%s'", url))
	return nil
}

// Start component.
func (nmgr *NatsManager) Start(ctx context.Context) error {
	return nmgr.lifecycle.Start(ctx)
}

// ExecuteStart instantiates the service's readers/writers via oncreate.
func (nmgr *NatsManager) ExecuteStart(context.Context) error {
	if err := nmgr.oncreate(nmgr); err != nil {
		return err
	}
	log.Info().Msg("NATS component creation completed successfully.")
	return nil
}

// Stop component.
func (nmgr *NatsManager) Stop(ctx context.Context) error {
	return nmgr.lifecycle.Stop(ctx)
}

// ExecuteStop unsubscribes readers and drains the connection.
func (nmgr *NatsManager) ExecuteStop(context.Context) error {
	log.Info().Msg("Shutting down NATS readers.")
	for _, r := range nmgr.readers {
		// A bound subscription's Unsubscribe does NOT delete the durable (that is the
		// whole point of the Bind attach), so this releases local interest without
		// disturbing the consumer other replicas share.
		if s := r.sub.Load(); s != nil {
			if err := s.Unsubscribe(); err != nil {
				log.Error().Err(err).Str("suffix", r.suffix).Msg("Error unsubscribing NATS reader.")
			}
		}
	}
	if nmgr.nc != nil {
		if err := nmgr.nc.Drain(); err != nil {
			log.Error().Err(err).Msg("Error draining NATS connection.")
		}
	}
	return nil
}

// Terminate component.
func (nmgr *NatsManager) Terminate(ctx context.Context) error {
	return nmgr.lifecycle.Terminate(ctx)
}

// ExecuteTerminate closes the NATS connection.
func (nmgr *NatsManager) ExecuteTerminate(context.Context) error {
	if nmgr.nc != nil && !nmgr.nc.IsClosed() {
		nmgr.nc.Close()
	}
	return nil
}
