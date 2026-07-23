// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// deliveryEnvelope is command-delivery's wire payload on the device-commands subject (its fields
// and JSON tags MUST stay byte-identical to command-delivery/processor.deliveryEnvelope — the two
// services agree on this shape with no shared type). The command names its target device by
// connection token and carries its OWN token so the response can be correlated back to the
// persisted command.
type deliveryEnvelope struct {
	Token       string           `json:"token"`
	DeviceToken string           `json:"deviceToken"`
	Name        string           `json:"name"`
	Payload     *json.RawMessage `json:"payload,omitempty"`
}

// responseEnvelope is the outcome this adapter publishes on the command-responses subject; it must
// stay byte-identical to command-delivery/processor.responseEnvelope. command-delivery's
// MarkResponse maps Success→SUCCESSFUL / !Success→FAILED (a late/duplicate response to a terminal
// command is ignored), keyed by CommandToken.
type responseEnvelope struct {
	CommandToken string  `json:"commandToken"`
	Success      bool    `json:"success"`
	Payload      *string `json:"payload,omitempty"`
	Error        *string `json:"error,omitempty"`
}

// OpResult is the outcome of dispatching one command to a device: the CoAP exchange already
// mapped to command-delivery's success/payload/error shape. It is what the executor (the CoAP op
// mapping, ADR-075 L4a Stage 4) returns for every command it could attempt — including a device
// that answered 4.xx/5.xx (Success=false, Err set) and a malformed command it refused before the
// wire (Success=false, Err set). Payload carries a Read's response body.
type OpResult struct {
	Success bool
	Payload *string
	Err     *string
	// Op is the normalized command kind for metrics (read/write/execute/other) — never the raw
	// operator-authored command name, which would be an unbounded metric label (ADR-023).
	Op string
}

// executor performs one command's CoAP exchange on a device conn and maps the result. A concrete
// *Ops (Stage 4) satisfies it; the dispatcher depends on the interface so it is unit-testable with
// a fake device.
type executor interface {
	Execute(ctx context.Context, conn mux.Conn, name string, payload []byte) OpResult
}

// reader is the durable command consumer (a *messaging.natsReader over device-commands satisfies
// messaging.MessageReader). It MUST be created with ReaderWithDeliverNew so a brand-new durable
// does not replay the stream's retained history into live actuations (ADR-075 L4a B1).
type reader interface {
	ReadMessage(ctx context.Context) (messaging.Message, error)
	HandleResponse(err error)
}

// responsePublisher writes to the tenant-scoped command-responses subject (a *messaging.natsWriter
// over command-responses satisfies messaging.MessageWriter; WriteMessages takes the tenant from
// the context).
type responsePublisher interface {
	WriteMessages(ctx context.Context, msgs ...messaging.Message) error
}

// connLookup resolves a command's (tenant, deviceToken) to a live conn + reachability
// (*ConnTable satisfies it).
type connLookup interface {
	Lookup(tenant, deviceToken string) (mux.Conn, Reach)
}

// Metrics are the optional Prometheus instruments the dispatcher updates; any nil field is
// skipped. Label-free except the bounded op label (read/write/execute/other), per the ADR-023
// cardinality lesson — never a per-tenant or per-device or raw-command-name label.
type Metrics struct {
	Attempted     *prometheus.CounterVec // commands dispatched to a live device (= Succeeded + Failed), by op
	Succeeded     *prometheus.CounterVec // commands the device acknowledged 2.xx, by op
	Failed        *prometheus.CounterVec // commands the device rejected (4.xx/5.xx), timed out, or failed local validation, by op
	NotServed     prometheus.Counter     // ack-dropped: no index entry (another protocol's device, or none)
	ServedOffline prometheus.Counter     // ack-dropped: a served device with no live conn (rides TTL to TIMEOUT)
	Poison        prometheus.Counter     // ack-dropped: unparseable subject tenant or envelope
	ResponseFails prometheus.Counter     // a command response we could not publish after local retries (outcome lost to TTL)
}

// Default tuning for the dispatcher. Each is overridable via Options.
const (
	// DefaultWorkers is the per-device-sharded dispatch concurrency. Commands for one device hash
	// to one worker so they run in stream order (a firmware write then execute must not reorder,
	// ADR-075 L4a B3); different devices spread across workers for cross-device concurrency.
	DefaultWorkers = 16
	// DefaultOpTimeout bounds one CoAP exchange to a device (a CON to a marginal radio).
	DefaultOpTimeout = 10 * time.Second
	// workerQueueDepth buffers each worker so a burst to distinct devices does not immediately
	// back-pressure the single reader. NAMED LIMIT (ADR-075 L4a S4): once a shard's buffer fills
	// behind one live-but-unresponsive device (each op burning the full opTimeout), the single
	// reader BLOCKS on the routed send and the whole instance's command throughput drops to that
	// device's rate — a head-of-line convoy. The route-time non-live pre-filter keeps offline and
	// other-protocol devices out of the shards, so only a genuinely-live-but-slow device causes it;
	// L4b's durable hold-and-drain is the real fix (dispatch decouples from a slow radio entirely).
	workerQueueDepth = 8
	// responsePublishAttempts bounds the LOCAL retry of a command-response publish. After a
	// successful dispatch the command's fate is sealed (ADR-075 L4a S1): we retry the publish a few
	// times, then ack REGARDLESS — never redeliver a message whose CoAP op already ran (which would
	// re-actuate a physical device); the command then rides SENT→TIMEOUT.
	responsePublishAttempts = 3
	// responsePublishBackoff paces the local response-publish retry.
	responsePublishBackoff = 250 * time.Millisecond
	// readErrorBackoff paces the command-reader loop after a read error, so a persistent failure is
	// a slow retry rather than a tight busy-loop.
	readErrorBackoff = time.Second
)

// Dispatcher consumes device-commands and, for a command addressed to a device this adapter serves
// and is currently connected to, dispatches a CoAP Read/Write/Execute and reports the outcome on
// command-responses (ADR-075 L4a). It is the LEADER-ONLY half: Run pulls only while the leadership
// term's context is live, because the durable consumer is shared across replicas and a standby
// pull (with no device conns) would ack-drop commands the leader needed.
type Dispatcher struct {
	reader    reader
	responses responsePublisher
	conns     connLookup
	exec      executor
	metrics   Metrics
	workers   int
	opTimeout time.Duration
}

// Options configures a Dispatcher. Zero-valued fields take their package defaults.
type Options struct {
	Workers   int
	OpTimeout time.Duration
}

// NewDispatcher builds a Dispatcher over the durable command reader, the command-responses writer,
// the conn table, and the CoAP op executor.
func NewDispatcher(rdr reader, responses responsePublisher, conns connLookup, exec executor, metrics Metrics, opts Options) *Dispatcher {
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.OpTimeout <= 0 {
		opts.OpTimeout = DefaultOpTimeout
	}
	return &Dispatcher{
		reader:    rdr,
		responses: responses,
		conns:     conns,
		exec:      exec,
		metrics:   metrics,
		workers:   opts.Workers,
		opTimeout: opts.OpTimeout,
	}
}

// work is one parsed command routed to a device-sharded worker.
type work struct {
	msg    messaging.Message
	tenant string
	env    deliveryEnvelope
}

// Run consumes and dispatches until ctx is cancelled (leadership eviction / shutdown). It is the
// single reader + a fixed pool of device-sharded workers: the reader parses each message and routes
// it to worker[hash(deviceToken)], so a device's commands stay in stream order while distinct
// devices dispatch concurrently. The reader honors ctx (ReadMessage returns on cancel) so an
// evicted replica stops pulling promptly; the durable consumer is bound (not owned), so its cursor
// survives the term and the next leader resumes from the last ack.
func (d *Dispatcher) Run(ctx context.Context) {
	queues := make([]chan work, d.workers)
	var wg sync.WaitGroup
	for i := range queues {
		queues[i] = make(chan work, workerQueueDepth)
		wg.Add(1)
		go func(ch chan work) {
			defer wg.Done()
			for w := range ch {
				d.dispatch(ctx, w)
			}
		}(queues[i])
	}

	for ctx.Err() == nil {
		msg, err := d.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break // evicted / shutting down
			}
			// A read error — a NATS blip, an io.EOF from a closed/draining connection, or a failed
			// rebind. Record it and back off BEFORE retrying, so a persistent error is a paced retry,
			// not a tight busy-loop (ReadMessage returns some of these instantly and repeatedly). A
			// transient error self-heals when the next ReadMessage re-binds; a durable NATS outage
			// also stalls this term's lease renewal, which evicts the term (ctx cancel) and ends the
			// loop — so this never spins forever on a dead broker.
			d.reader.HandleResponse(err)
			if !sleepCtx(ctx, readErrorBackoff) {
				break
			}
			continue
		}
		w, ok := d.parse(msg)
		if !ok {
			continue // poison — already acked + counted in parse
		}
		// Pre-filter reachability at ROUTE time (S4 hardening): a command for a device this adapter
		// does not serve — the DOMINANT case, since this cross-tenant consumer sees every other
		// protocol adapter's device-commands too — or one that is offline is ack-dropped HERE, so it
		// never occupies a per-device worker slot. Only a live-dispatchable command is routed to a
		// shard; the worker re-checks reachability (the device may drop in between).
		if _, reach := d.conns.Lookup(w.tenant, w.env.DeviceToken); reach != ReachLive {
			d.dropNonLive(reach, w.msg)
			continue
		}
		select {
		case queues[d.shard(w.env.DeviceToken)] <- w:
		case <-ctx.Done():
			// Evicted before we could route: do NOT ack, so the message redelivers to the next
			// leader rather than being dropped by a replica that is no longer serving.
		}
	}

	for _, ch := range queues {
		close(ch)
	}
	wg.Wait()
}

// parse derives the tenant from the command subject and decodes the delivery envelope. A message
// whose subject carries no parseable tenant, or whose body will not decode, is poison: it is acked
// (so it does not redeliver) and counted. Returns ok=false for a poison message (already handled).
func (d *Dispatcher) parse(msg messaging.Message) (work, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(context.Background(), msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping an LwM2M command with no parseable tenant in its subject.")
		incr(d.metrics.Poison, 1)
		ackDrop(msg)
		return work{}, false
	}
	var env deliveryEnvelope
	if err := json.Unmarshal(msg.Value, &env); err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Dropping an undecodable LwM2M command envelope.")
		incr(d.metrics.Poison, 1)
		ackDrop(msg)
		return work{}, false
	}
	return work{msg: msg, tenant: tenant, env: env}, true
}

// dispatch handles one routed command on a worker goroutine. It looks up the device's live conn,
// and — connected-only (ADR-075 L4a) — dispatches to a live device or ack-drops otherwise:
//   - NotServed: another protocol's device (or none) — ack-drop, no response.
//   - Offline: a served device with no live conn — ack-drop, no response; the command rides
//     command-delivery's TTL to TIMEOUT (L4b adds durable hold-and-drain).
//   - Live: run the CoAP op, publish the outcome, then ACK regardless of the publish result
//     (seal-fate, S1). If ctx was cancelled mid-op (eviction), do NOT ack — the command redelivers
//     to the next leader instead of a spurious FAILED from a replica no longer serving.
func (d *Dispatcher) dispatch(ctx context.Context, w work) {
	if ctx.Err() != nil {
		return // evicted before this queued command started — leave unacked to redeliver to the next leader
	}
	conn, reach := d.conns.Lookup(w.tenant, w.env.DeviceToken)
	if reach != ReachLive {
		// A device we do not serve, or that dropped between the reader's route-time pre-filter and
		// now, is ack-dropped (no dispatch, no response) — connected-only rides TTL to TIMEOUT.
		d.dropNonLive(reach, w.msg)
		return
	}

	var payload []byte
	if w.env.Payload != nil {
		payload = *w.env.Payload
	}
	opCtx, cancel := context.WithTimeout(ctx, d.opTimeout)
	res := d.exec.Execute(opCtx, conn, w.env.Name, payload)
	cancel()

	// The CoAP op was ISSUED. Seal its fate (S1): this message must NEVER redeliver — that would
	// re-actuate a physical device — even if the term was evicted mid-op. So on an eviction here,
	// SKIP the response publish (the op result is an unreliable artifact of losing the conn/ctx — a
	// spurious FAILED) but still ACK: the true outcome is unknown, so the command rides SENT→TIMEOUT,
	// exactly the terminal S1 sanctions for a lost outcome and strictly safer than a second
	// actuation. Only the PRE-op eviction check (top of dispatch) redelivers, where the op never ran.
	if ctx.Err() != nil {
		ackDrop(w.msg)
		return
	}

	labelInc(d.metrics.Attempted, res.Op, 1)
	if res.Success {
		labelInc(d.metrics.Succeeded, res.Op, 1)
	} else {
		labelInc(d.metrics.Failed, res.Op, 1)
	}

	_ = d.publishResponse(w.tenant, responseEnvelope{
		CommandToken: w.env.Token,
		Success:      res.Success,
		Payload:      res.Payload,
		Error:        res.Err,
	})
	// Seal fate: the CoAP op already ran, so this command must NEVER redeliver (that would
	// re-actuate the device). Ack whether or not the response published — a publish we could not
	// land leaves the command to TIMEOUT, which is the correct terminal for a lost outcome and is
	// strictly safer than a second actuation.
	ackDrop(w.msg)
}

// publishResponse publishes one command outcome to the tenant's command-responses subject, with a
// bounded LOCAL retry (never a redelivery — see dispatch). The tenant context is built fresh from
// the tenant string (not the run ctx), so recording the outcome of an op we already ran is not
// aborted by a leadership eviction that lands during the publish. On exhaustion it counts
// ResponseFails and returns; the caller acks regardless (seal-fate).
func (d *Dispatcher) publishResponse(tenant string, env responseEnvelope) bool {
	data, err := json.Marshal(env)
	if err != nil {
		incr(d.metrics.ResponseFails, 1) // unreachable in practice (a fixed struct), but never drop silently
		return false
	}
	// The tenant context is built fresh from the tenant string (NOT the run ctx), so recording the
	// outcome of an op we already ran is not aborted by a leadership eviction landing during the
	// publish. Each publish is bounded by the JetStream client's own default publish timeout.
	tctx := core.WithTenant(context.Background(), tenant)
	for attempt := 0; attempt < responsePublishAttempts; attempt++ {
		if err = d.responses.WriteMessages(tctx, messaging.Message{Value: data}); err == nil {
			return true
		}
		if attempt < responsePublishAttempts-1 {
			time.Sleep(responsePublishBackoff)
		}
	}
	incr(d.metrics.ResponseFails, 1)
	log.Warn().Err(err).Str("tenant", tenant).Str("command", env.CommandToken).
		Msg("Could not publish an LwM2M command response after local retries; the command will TIMEOUT (the op already ran, so it is not redelivered).")
	return false
}

// dropNonLive ack-drops a command for a device that cannot be dispatched to now, counting the
// reachability so a not-served command (another protocol's device — a mirror of the instance's
// command traffic) is distinguished from a served-but-offline one (the operationally interesting
// signal). It is shared by the reader's route-time pre-filter and the worker's freshness re-check.
func (d *Dispatcher) dropNonLive(reach Reach, msg messaging.Message) {
	switch reach {
	case ReachNotServed:
		incr(d.metrics.NotServed, 1)
	case ReachOffline:
		incr(d.metrics.ServedOffline, 1)
	}
	ackDrop(msg)
}

// sleepCtx waits for d or ctx cancellation; it returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// shard maps a device token to a worker index so all of a device's commands run in stream order on
// one worker (per-device serialization, B3) while distinct devices spread across workers.
func (d *Dispatcher) shard(deviceToken string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(deviceToken))
	return int(h.Sum32() % uint32(d.workers))
}

// ackDrop acknowledges a message so it is not redelivered, logging (not failing) an ack error — an
// unacked message merely redelivers, which the finite MaxDeliver bounds.
func ackDrop(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Debug().Err(err).Msg("Failed to ack an LwM2M command message (it will redeliver, bounded by MaxDeliver).")
	}
}

// incr adds n to a counter, tolerating a nil counter (tests) and n <= 0.
func incr(c prometheus.Counter, n int) {
	if c != nil && n > 0 {
		c.Add(float64(n))
	}
}

// labelInc adds n to a labelled counter's op series, tolerating a nil vec.
func labelInc(v *prometheus.CounterVec, op string, n int) {
	if v != nil && n > 0 {
		v.WithLabelValues(op).Add(float64(n))
	}
}
