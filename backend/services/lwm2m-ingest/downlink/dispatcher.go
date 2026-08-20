// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"sync"
	"sync/atomic"
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

	// DispatchNonce names the claim this publish belongs to. It is quoted back when parking
	// an undeliverable command, so a request still in redelivery cannot hand back a row that
	// has since been re-claimed and actuated. Empty means the publisher stamped none, and
	// parking is then declined rather than attempted on status alone (see park).
	DispatchNonce string `json:"dispatchNonce,omitempty"`
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

// responsePublisher writes to a device's own command-responses subject (a *messaging.natsWriter
// over command-responses satisfies messaging.MessageWriter; WriteToDevice takes the tenant from
// the context and the device from its argument).
//
// 🔴 WriteToDevice, NOT WriteMessages, AND THE COMPILER WILL NOT TELL YOU. command-responses is
// per-device: the responding device's token is a subject segment, because that is what makes a
// response attributable to the device that sent it. The tenant-wide method refuses this suffix at
// RUNTIME rather than failing to build, so a call site that reaches for the familiar name here
// publishes nothing and reports an error into a path that only counts it. Narrowing this interface
// to the one correct method is what turns that into a build failure.
type responsePublisher interface {
	WriteToDevice(ctx context.Context, deviceToken string, msgs ...messaging.Message) error
}

// connLookup resolves a command's (tenant, deviceToken) to a live conn + reachability
// (*ConnTable satisfies it).
type connLookup interface {
	Lookup(tenant, deviceToken string) (mux.Conn, Reach)
}

// drainFetcher reads a waking device's backlogged commands (HELD or PARKED — drainStatuses)
// from command-delivery (*CommandFetcher satisfies it). It is the read side of the durable
// hold (ADR-075 L4b, Architecture D): a command withheld for a device known absent, or one
// the live path published and this dispatcher handed back because the device turned out to be
// asleep, is pulled here at the device's next wake. nil disables draining (an inert or
// pre-L4b-wiring build, and the disposition unit tests).
//
// It takes no clock: the expiry horizon that decides which rows are still drainable is applied
// in the database against the SERVER's clock, so a skewed pod cannot drop a live command.
type drainFetcher interface {
	Pending(ctx context.Context, tenant, deviceToken string) ([]DrainCommand, error)
}

// commandClaimer is the WRITE half of the drain (*CommandClaimer satisfies it): it moves a
// still-dispatchable command to SENT and reports whether THIS caller won it. It is a
// separate, one-method seam beside drainFetcher for the same reason drainFetcher is one —
// so the claim-then-dispatch ordering below is unit-testable without a live command-delivery
// or a minted service token, including its LOST and ERRORED branches, which are the two that
// must not actuate and are therefore the two most easily faked past.
//
// nil disables claiming, which means a backlogged (HELD or PARKED) command is NOT dispatched
// (see claim below) — fail-closed, and it applies to the WHOLE drainable set, not just the
// held half, because an unclaimed dispatch is a duplicate actuation waiting for the next
// sweep tick.
type commandClaimer interface {
	Claim(ctx context.Context, tenant, commandToken string) (bool, error)
}

// commandParker is the HAND-BACK seam (*CommandParker satisfies it): it moves a command this
// adapter could not deliver from SENT to PARKED, naming the dispatch it arrived on.
//
// It is the counterpart to commandClaimer on the LIVE path rather than the drain: presence
// says a registered device is reachable, and only this adapter learns that a queue-mode
// device was asleep. Its own seam for the same testability reason — the branches that must
// NOT ack, and the settled-false branch that must, are exactly the ones a fake makes easy to
// get wrong.
//
// nil disables parking, which degrades to the behaviour before PARKED existed: the row stays
// SENT and rides its TTL. That is a worse outcome, not an unsafe one, so unlike claiming this
// seam fails OPEN — refusing to ack instead would wedge the consumer on an instance that
// simply has no command-delivery endpoint configured.
type commandParker interface {
	Park(ctx context.Context, tenant, commandToken, dispatchNonce string) (bool, error)
}

// Metrics are the optional Prometheus instruments the dispatcher updates; any nil field is
// skipped. Label-free except the bounded op label (read/write/execute/other), per the ADR-023
// cardinality lesson — never a per-tenant or per-device or raw-command-name label.
type Metrics struct {
	Attempted     *prometheus.CounterVec // commands dispatched to a live device (= Succeeded + Failed), by op
	Succeeded     *prometheus.CounterVec // commands the device acknowledged 2.xx, by op
	Failed        *prometheus.CounterVec // commands the device rejected (4.xx/5.xx), timed out, or failed local validation, by op
	NotServed     prometheus.Counter     // ack-dropped: no index entry (another protocol's device, or none)
	ServedOffline prometheus.Counter     // a served device with no live conn — the command is parked in command-delivery and drained on its next wake (L4b)
	// Park outcomes for a served-but-offline command, split the same way and for the same
	// reason as the claim outcomes below. ParkErrors is a FAULT — the message is left unacked
	// to redeliver, and a sustained rate means commands are sitting in SENT telling the old
	// lie. ParkSettled is BENIGN: the row moved on under us (answered, cancelled, expired, or
	// re-claimed by a drain), which is an outcome, not a failure, and must not be retried.
	ParkErrors    prometheus.Counter // a park that could not be established (redelivers)
	ParkSettled   prometheus.Counter // a park that moved no row because the command had already moved on
	ParkSkipped   prometheus.Counter // a park not attempted: no parker wired, or the envelope carried no dispatch nonce
	Poison        prometheus.Counter // ack-dropped: unparseable subject tenant or envelope
	ResponseFails prometheus.Counter // a command response we could not publish after local retries (outcome lost to TTL)
	// L4b wake-drain instruments (ADR-075). Drained/DrainErrors are the operationally interesting
	// signals; DrainDropped/DrainDedup are load/health signals.
	Drained      prometheus.Counter // a held command dispatched to a device on its Register/Update wake
	DrainErrors  prometheus.Counter // a wake-drain fetch that failed (retried on the device's next wake)
	DrainDropped prometheus.Counter // a wake-drain trigger dropped because the device's shard was busy (next wake re-triggers)
	DrainDedup   prometheus.Counter // a command skipped because it was already dispatched (drain/live overlap) — a re-actuation avoided
	// Claim outcomes, split because they mean opposite things operationally. LOST is BENIGN —
	// someone else owns that command and we correctly declined to actuate it twice. ERRORS is a
	// FAULT: command-delivery could not be reached, so a claimable command went undispatched
	// (fail-closed) and the device waits for its next wake. One counter would let a rising
	// outage hide inside a normal-looking race count.
	DrainClaimLost   prometheus.Counter // a held command another dispatcher/the sweep claimed first — no actuation, correct
	DrainClaimErrors prometheus.Counter // a claim that could not be established; the command is NOT dispatched, retried next wake
	// TenantGoneRefused counts live commands ack-dropped because their tenant has been
	// deleted (ADR-077). Distinct from Poison: the command is well formed, the platform is
	// declining to actuate an offboarded customer's hardware. Wake-drains refused for the
	// same reason are not counted here — a drain is a trigger, not a command, and it
	// carries no count of what it would have fired.
	TenantGoneRefused prometheus.Counter
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
	// dedupeTTL bounds how long a just-dispatched (deviceToken, commandToken) suppresses a
	// re-dispatch of the SAME command by the other path — a wake drain fetching a row the live
	// stream is also delivering, or vice versa. It only needs to cover the window between an op
	// issuing and its command-responses reply terminalizing the row (after which the drain no longer
	// fetches it), so a modest TTL a few times the op timeout is ample.
	//
	// 🔑 IT CARRIES MUCH LESS WEIGHT THAN IT USED TO, AND THAT IS THE POINT OF PARKED. The drain
	// once dispatched out of SENT with NO CLAIM, so this cache was the only thing between an
	// ambiguous SENT row and a second physical actuation — a job it was never fit for, being
	// per-pod and TTL-bounded. Every drained row is claimed now, so the exclusion is structural
	// and this is back to being what it says it is below: an optimization.
	dedupeTTL = 60 * time.Second
	// dedupeMax caps the dedup set; beyond it the set is pruned/cleared. The dedup is a re-actuation
	// OPTIMIZATION, not a correctness guarantee (seal-fate and the drain's claim already bound
	// re-fire), so clearing under a pathological burst degrades to the documented at-least-once.
	dedupeMax = 8192
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
	fetcher   drainFetcher
	claimer   commandClaimer
	parker    commandParker
	metrics   Metrics
	// tenantDeleted reports whether a tenant has been through the ADR-077 delete door.
	// Never nil; see NewDispatcher.
	tenantDeleted func(tenant string) bool
	workers       int
	opTimeout     time.Duration
	dedupe        *dedupe
	// queuesPtr publishes the CURRENT leadership term's worker channels so Drain (called from the
	// /rd handler goroutine, off the Run loop) can enqueue a wake-drain onto a device's shard. It is
	// nil whenever this replica is not the serving leader (before Run, and after it returns), so a
	// Drain on a standby is a safe no-op. atomic so the handler goroutine reads it without a lock.
	queuesPtr atomic.Pointer[[]chan task]
	// ready is closed by Run once queuesPtr is published and the workers are started. The leader waits
	// on it BEFORE serving the transport, so the first Register (which fires Drain) never lands in a
	// serve-before-Run window where Drain would no-op and silently defer the wake-drain to the device's
	// next Update — a real gap at failover, when the whole fleet re-Registers at once. One-shot per
	// Dispatcher; the Dispatcher is rebuilt per leadership term.
	ready chan struct{}
}

// Options configures a Dispatcher. Zero-valued fields take their package defaults.
type Options struct {
	Workers   int
	OpTimeout time.Duration
	// TenantDeleted reports whether a tenant has been through the ADR-077 delete door.
	// A closure rather than the governance resolver type so this package is testable
	// without a live user-management. Nil disables the gate; see NewDispatcher.
	TenantDeleted func(tenant string) bool
	// Parker hands a command back to command-delivery when this adapter finds the device
	// unreachable (SENT -> PARKED). Nil disables parking.
	//
	// 🔑 IT IS HERE RATHER THAN A POSITIONAL PARAMETER, AND THE DISTINCTION IS THE ONE
	// NewDispatcher's comment draws, not an exception to it. fetcher and claimer are
	// positional so that omitting the claimer is a COMPILE error, because a nil claimer
	// takes a FAIL-CLOSED branch whose symptom — a device that never receives its backlog
	// — is indistinguishable from having nothing queued. A nil parker fails OPEN: the row
	// stays SENT and rides its TTL, which is exactly the behaviour that existed before
	// parking did, and it is counted on ParkSkipped rather than being silent. An omission
	// that degrades to the previous release and shows up on a graph does not need the
	// compiler to catch it.
	Parker commandParker
}

// NewDispatcher builds a Dispatcher over the durable command reader, the command-responses writer,
// the conn table, the CoAP op executor, the wake-drain fetcher (nil disables draining) and the
// wake-drain claimer (nil means a backlogged HELD or PARKED command is not dispatched —
// fail-closed).
// tenantDeleted gates ACTUATION on the ADR-077 tenant lifecycle (Options.TenantDeleted).
// Nil disables the gate, matching the resolver's own fail-open.
//
// 🔴 fetcher and claimer are separate PARAMETERS rather than Options fields, and that is a
// TRADEOFF, not a constraint. Nothing prevents exporting them: an unexported interface type is
// unnameable outside this package but still assignable to, and both seams declare only exported
// methods, so an external type can satisfy them and an exported Options field of that type would
// compile at every call site. What is bought by keeping them positional is that omitting the
// claimer is a COMPILE error rather than a zero value — and the zero value takes the fail-closed
// branch, where no backlogged command (HELD or PARKED) is ever dispatched. That failure is
// silent and looks exactly like
// a device having nothing queued. What is paid is a long signature and a change at every call
// site; if a third seam arrives, move all of them into Options together and make the
// nil-claimer case loud some other way.
func NewDispatcher(rdr reader, responses responsePublisher, conns connLookup, exec executor, fetcher drainFetcher, claimer commandClaimer, metrics Metrics, opts Options) *Dispatcher {
	if opts.TenantDeleted == nil {
		opts.TenantDeleted = func(string) bool { return false }
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.OpTimeout <= 0 {
		opts.OpTimeout = DefaultOpTimeout
	}
	return &Dispatcher{
		reader:        rdr,
		responses:     responses,
		conns:         conns,
		exec:          exec,
		fetcher:       fetcher,
		claimer:       claimer,
		parker:        opts.Parker,
		metrics:       metrics,
		tenantDeleted: opts.TenantDeleted,
		workers:       opts.Workers,
		opTimeout:     opts.OpTimeout,
		dedupe:        newDedupe(dedupeTTL, dedupeMax),
		ready:         make(chan struct{}),
	}
}

// Ready returns a channel closed once Run has published this term's worker queues — the leader waits
// on it before serving the transport so no wake-drain is lost to a serve-before-Run window.
func (d *Dispatcher) Ready() <-chan struct{} { return d.ready }

// work is one parsed live command (a consumed device-commands message) routed to a device-sharded
// worker.
type work struct {
	msg    messaging.Message
	tenant string
	env    deliveryEnvelope
}

// drainJob is a wake-drain trigger for one device, routed to that device's shard so it serializes
// with the device's live commands (no reorder, no concurrent double-dispatch).
type drainJob struct {
	tenant      string
	deviceToken string
}

// task is the union a shard worker processes: a live command OR a wake-drain. Both carry the device
// token so Run and Drain route them to the same shard(deviceToken) worker — the seam that makes a
// device's drain and live dispatch strictly serial.
type task struct {
	deviceToken string
	live        *work
	drain       *drainJob
}

// Run consumes and dispatches until ctx is cancelled (leadership eviction / shutdown). It is the
// single reader + a fixed pool of device-sharded workers: the reader parses each message and routes
// it to worker[hash(deviceToken)], so a device's commands stay in stream order while distinct
// devices dispatch concurrently. The reader honors ctx (ReadMessage returns on cancel) so an
// evicted replica stops pulling promptly; the durable consumer is bound (not owned), so its cursor
// survives the term and the next leader resumes from the last ack.
func (d *Dispatcher) Run(ctx context.Context) {
	queues := make([]chan task, d.workers)
	var wg sync.WaitGroup
	for i := range queues {
		queues[i] = make(chan task, workerQueueDepth)
		wg.Add(1)
		go func(ch chan task) {
			defer wg.Done()
			// A ctx-select worker (not `range ch`) so an evicted term stops immediately and the
			// channels are never closed — Drain, called from the /rd handler goroutine, can then
			// send without racing a close(). A live command left buffered at eviction is simply
			// unprocessed (unacked → redelivers to the next leader); a drain trigger left buffered
			// is re-fired by the next wake. Either is safe.
			for {
				select {
				case <-ctx.Done():
					return
				case t := <-ch:
					d.process(ctx, t)
				}
			}
		}(queues[i])
	}
	// Publish this term's queues so Drain (handler goroutine) can enqueue; clear on return so a
	// post-eviction Drain is a no-op rather than feeding channels whose workers have exited. Signal
	// ready AFTER the publish so the leader only starts serving once wake-drains can be accepted.
	d.queuesPtr.Store(&queues)
	close(d.ready)
	defer d.queuesPtr.Store(nil)

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
		// protocol adapter's device-commands too — is ack-dropped HERE, so it never occupies a
		// per-device worker slot. It is another protocol's traffic and there is nothing for this
		// adapter to record about it.
		//
		// 🔴 AN OFFLINE SERVED DEVICE IS NO LONGER DROPPED HERE, AND THAT IS DELIBERATE. Its
		// command has to be PARKED in command-delivery, which is a network round trip, and this
		// is the JetStream read loop — the one goroutine that must not make one. So it is routed
		// to the device's shard worker, which parks it there. The alternative considered and
		// rejected was a third task kind with a non-blocking send like Drain's: a dropped park
		// task has no next wake to re-trigger it, so a full shard would silently leave the row in
		// SENT with no signal — losing exactly the fix this exists to deliver.
		//
		// The cost is stated rather than hidden: offline-device commands now occupy shard slots,
		// so while command-delivery is unreachable a shard can be blocked by parks timing out.
		// That is bounded, self-healing, and preferable to a silent loss.
		if _, reach := d.conns.Lookup(w.tenant, w.env.DeviceToken); reach == ReachNotServed {
			d.dropNonLive(reach, w.msg)
			continue
		}
		// The live-command send BLOCKS on a full shard (the named head-of-line convoy, L4a S4) — a
		// live command must not be dropped. The drain send (Drain) is non-blocking by contrast, so a
		// wake never stalls the handler.
		select {
		case queues[d.shard(w.env.DeviceToken)] <- task{deviceToken: w.env.DeviceToken, live: &w}:
		case <-ctx.Done():
			// Evicted before we could route: do NOT ack, so the message redelivers to the next
			// leader rather than being dropped by a replica that is no longer serving.
		}
	}

	wg.Wait()
}

// Drain enqueues a wake-drain for a device onto its shard worker, so the leader pulls that device's
// backlogged commands (HELD or PARKED) from command-delivery and dispatches them to the now-live conn
// (ADR-075 L4b).
// The /rd handler calls it after a device becomes live (Register/Update) — the LwM2M queue-mode wake
// signal. It is NON-BLOCKING: a wake must never stall the CoAP read loop, so a full shard drops the
// trigger (counted) and the device's next Update re-triggers. A call on a standby (no active term)
// is a no-op. Draining and live dispatch for the same device share one shard worker, so they never
// run concurrently or reorder.
func (d *Dispatcher) Drain(tenant, deviceToken string) {
	if d.fetcher == nil {
		return // draining disabled (inert / pre-wiring build)
	}
	p := d.queuesPtr.Load()
	if p == nil {
		return // not the serving leader right now
	}
	queues := *p
	t := task{deviceToken: deviceToken, drain: &drainJob{tenant: tenant, deviceToken: deviceToken}}
	select {
	case queues[d.shard(deviceToken)] <- t:
	default:
		incr(d.metrics.DrainDropped, 1) // shard busy — the next wake re-triggers
	}
}

// process dispatches one shard-worker task: a live command or a wake-drain.
//
// 🔴 The ADR-077 lifecycle gate sits HERE, at the union, because there are TWO ways a
// command reaches a device and command-delivery's own gate covers neither completely:
//
//   - the LIVE path reads the device-commands stream, and a command published in the
//     moments before the delete is already durable in that stream — command-delivery
//     refusing to publish more does not unpublish those;
//   - the WAKE-DRAIN re-fetches commands still HELD or PARKED and fires them when a sleeping
//     device next registers, which can be long after the tenant was deleted. Nothing on
//     that path had a lifecycle check: the (tenant, token) is remembered from a
//     registration that predates the delete, and PSK bindings are static config.
//
// A command is a PHYSICAL ACTUATION, so "the sweep gate stops most of them" is not a
// standard this path can be held to. Gating the union rather than the two callers means a
// third task kind cannot be added without one.
func (d *Dispatcher) process(ctx context.Context, t task) {
	switch {
	case t.live != nil:
		if d.tenantDeleted(t.live.tenant) {
			// Acked, not retried: the tenant is not coming back, and leaving the message
			// unacked would redeliver this refusal until the stream ages out.
			d.ackRefused(t.live.msg, t.live.tenant)
			return
		}
		d.dispatch(ctx, *t.live)
	case t.drain != nil:
		if d.tenantDeleted(t.drain.tenant) {
			return
		}
		d.drain(ctx, *t.drain)
	}
}

// ackRefused settles a live command refused by the lifecycle gate. It is counted apart
// from a poison message: nothing is wrong with the command, the platform is declining to
// actuate hardware on behalf of a tenant an operator has deleted.
func (d *Dispatcher) ackRefused(msg messaging.Message, tenant string) {
	incr(d.metrics.TenantGoneRefused, 1)
	log.Debug().Str("tenant", tenant).
		Msg("Refusing to actuate an LwM2M command for a tenant that has been deleted.")
	ackDrop(msg)
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
//   - Offline: a served device with no live conn — PARK the command back in command-delivery so
//     the drain claims it on the device's next wake. (It used to ack-drop and ride the TTL to
//     TIMEOUT, which blamed the device for a command that reached nothing.)
//   - Live: run the CoAP op, publish the outcome, then ACK regardless of the publish result
//     (seal-fate, S1). If ctx was cancelled mid-op (eviction), do NOT ack — the command redelivers
//     to the next leader instead of a spurious FAILED from a replica no longer serving.
func (d *Dispatcher) dispatch(ctx context.Context, w work) {
	if ctx.Err() != nil {
		return // evicted before this queued command started — leave unacked to redeliver to the next leader
	}
	conn, reach := d.conns.Lookup(w.tenant, w.env.DeviceToken)
	if reach == ReachOffline {
		// A device this adapter SERVES that has no live conn — either it was asleep when the
		// command was published (the queue-mode case, which reached here because the reader no
		// longer pre-filters it) or it dropped between the route-time check and now. Either way
		// the command went nowhere, so hand it back: park it in command-delivery, where it can be
		// cancelled, expires as EXPIRED rather than blaming the device with TIMEOUT, and is
		// claimed by the drain on the device's next wake.
		d.park(ctx, w)
		return
	}
	if reach != ReachLive {
		// Not served: another protocol's device, or none. Nothing to record — ack-dropping just
		// advances our cursor. This is also the freshness re-check for a command the reader let
		// through, though a device cannot become not-served between the two.
		d.dropNonLive(reach, w.msg)
		return
	}
	// Drain/live dedup (L4b): this command may already have been dispatched by a wake drain that
	// fetched its PARKED row moments ago (drain and live share this device's one worker, but a
	// fetch can still overlap a stream delivery). Re-running it would re-actuate the device, so skip
	// the op and ACK (seal-fate — it was actuated, must not redeliver).
	if d.dedupe.recentlyDispatched(w.tenant, w.env.DeviceToken, w.env.Token) {
		incr(d.metrics.DrainDedup, 1)
		ackDrop(w.msg)
		return
	}

	var payload []byte
	if w.env.Payload != nil {
		payload = *w.env.Payload
	}
	// executeAndReport issues the op and, unless the term was evicted mid-op, publishes the response.
	// Either way the op is ISSUED, so the fate is sealed below: this message must NEVER redeliver
	// (that would re-actuate a physical device). On a mid-op eviction the publish is skipped (the
	// result is an unreliable artifact of losing the conn — a spurious FAILED) and the command rides
	// SENT→TIMEOUT; only the PRE-op eviction check (top of dispatch) redelivers, where the op never ran.
	d.executeAndReport(ctx, conn, w.tenant, w.env.DeviceToken, w.env.Name, w.env.Token, payload)
	d.dedupe.mark(w.tenant, w.env.DeviceToken, w.env.Token)
	// Seal fate: the CoAP op already ran, so ack whether or not the response published — a publish we
	// could not land leaves the command to TIMEOUT, which is the correct terminal for a lost outcome
	// and strictly safer than a second actuation.
	ackDrop(w.msg)
}

// drain pulls a waking device's backlogged commands (HELD or PARKED) from command-delivery and
// dispatches them to its now-live conn, oldest-first (ADR-075 L4b). It runs on the device's shard
// worker (so it never races or reorders the device's live commands), leader-only (Drain no-ops on a
// standby). A fetch failure is counted and retried on the device's next wake; the device dropping or
// the term being evicted mid-drain stops cleanly, leaving the remaining rows untouched for the next
// wake (never a partial ack of something not dispatched).
//
// 🔴 The ordering inside the loop is CLAIM, THEN DISPATCH, and it is that way round because
// dispatching is IRREVERSIBLE. See claim below for why.
func (d *Dispatcher) drain(ctx context.Context, job drainJob) {
	if ctx.Err() != nil || d.fetcher == nil {
		return
	}
	cmds, err := d.fetcher.Pending(ctx, job.tenant, job.deviceToken)
	if err != nil {
		if ctx.Err() == nil { // a fetch aborted by eviction is not a drain error
			incr(d.metrics.DrainErrors, 1)
			log.Debug().Err(err).Str("tenant", job.tenant).Str("device", job.deviceToken).
				Msg("Could not fetch held LwM2M commands at wake; retrying on the device's next Register/Update.")
		}
		return
	}
	for _, c := range cmds {
		if ctx.Err() != nil {
			return // evicted mid-drain: the remaining rows are untouched, for the next leader's wake
		}
		conn, reach := d.conns.Lookup(job.tenant, job.deviceToken)
		if reach != ReachLive {
			return // the device dropped mid-drain: the remaining rows are untouched, for the next wake
		}
		// Neither of the two skips below costs this wake a delivery, even though the page is now
		// exactly maxDrainPerWake rows with no over-fetch: a row the live path just dispatched is
		// SENT (not drainable, so it was never on the page), and a lost claim means someone else
		// has already moved the row out of the dispatchable set. A skipped row is one that has
		// left the backlog, not a slot taken from a row still awaiting delivery. See
		// maxDrainPerWake in fetcher.go.
		if d.dedupe.recentlyDispatched(job.tenant, job.deviceToken, c.Token) {
			continue // already fired by the live path or a prior drain — do not re-actuate
		}
		// Take the command out of command-delivery's dispatchable set BEFORE actuating. A
		// command we could not claim is one we must not fire.
		if !d.claim(ctx, job.tenant, c) {
			continue
		}
		d.executeAndReport(ctx, conn, job.tenant, job.deviceToken, c.Name, c.Token, c.Payload)
		d.dedupe.mark(job.tenant, job.deviceToken, c.Token)
		incr(d.metrics.Drained, 1)
	}
}

// claim takes ownership of one backlogged command and reports whether the drain may dispatch
// it. It is the CLAIM half of claim-then-dispatch.
//
// 🔴 Why claiming exists at all: a drain that ran the CoAP op without first claiming would
// leave the row HELD, and HELD is not a resting place — command-delivery's reconciler
// releases a hold back to QUEUED the moment the device reads as present, which this very
// registration makes true. The next delivery sweep then publishes it down the live path: the
// same command delivered twice, which for a command is a second PHYSICAL ACTUATION (a valve
// opened again, a firmware update re-applied), not a duplicate log line. Claiming first makes
// the exclusion structural: whoever wins the conditional UPDATE actuates, everyone else
// declines.
//
// 🔴 The in-memory dedupe is NOT this guarantee and must never be mistaken for it. It is
// per-pod and TTL-bounded, so it says nothing about another replica or about this pod after a
// restart — exactly the two situations a leadership change produces. It is defence in depth
// against the drain/live overlap within one pod, no more.
//
// 🔴 EVERY DRAINED ROW IS CLAIMED, WITH NO EXCEPTION, AND THE EXCEPTION THIS REPLACES WAS
// THE PLATFORM'S ONLY UNCLAIMED DISPATCH. A SENT row used to return true here immediately,
// on the argument that SENT has already left the dispatchable set and nothing returns it
// there. The argument was sound and the premise was not: a SENT row could be a command that
// went NOWHERE — published to a registered-but-sleeping device and ack-dropped by this very
// dispatcher — so the drain was re-dispatching commands whose only protection against a
// second actuation was the per-pod dedupe disclaimed two paragraphs above. Those rows are
// now PARKED, which is claimable, so the exclusion is structural for the whole backlog.
//
// 🔑 Both branches are LIVE, which neither was when this was first written: the presence
// gate produces HELD for a device known absent, and this dispatcher produces PARKED for one
// that read present and turned out to be asleep. The claim is the ordinary path for a
// queue-mode device, and every drained backlog goes through it. The round trip per drained
// command is the price of that, and it is the correct price — the guarantee the status was
// previously assumed to carry, it did not carry.
//
// Both non-dispatch outcomes are counted, and they are counted apart (see Metrics):
//   - LOST (false, nil): another dispatcher or the sweep won it. Benign, no actuation.
//   - ERROR: command-delivery unreachable/forbidden/failing. FAIL CLOSED — declining to
//     actuate is recoverable (the row is still dispatchable and the device's next wake
//     retries), whereas actuating on an unconfirmed claim is not. Logged at debug like the
//     fetch-failure path: on a real outage this fires once per backlogged command per wake,
//     and a warn per row would bury the outage in its own noise.
func (d *Dispatcher) claim(ctx context.Context, tenant string, c DrainCommand) bool {
	if d.claimer == nil {
		// Claiming disabled but a claimable row arrived: refuse rather than fire. Counted as
		// an error, not a loss — nobody else took this command, the platform simply cannot
		// prove it owns it, and that is a wiring fault worth seeing on a graph.
		incr(d.metrics.DrainClaimErrors, 1)
		log.Debug().Str("tenant", tenant).Str("command", c.Token).Str("status", c.Status).
			Msg("Not dispatching a held LwM2M command: no command claimer is wired, so ownership cannot be established.")
		return false
	}
	won, err := d.claimer.Claim(ctx, tenant, c.Token)
	if err != nil {
		if ctx.Err() == nil { // a claim aborted by eviction is not a claim failure
			incr(d.metrics.DrainClaimErrors, 1)
			log.Debug().Err(err).Str("tenant", tenant).Str("command", c.Token).
				Msg("Could not claim a held LwM2M command at wake; not dispatching it (it stays dispatchable and retries on the device's next Register/Update).")
		}
		return false
	}
	if !won {
		// Someone else moved it out of the dispatchable set first. Nothing is wrong; this is
		// the mechanism working.
		incr(d.metrics.DrainClaimLost, 1)
		return false
	}
	return true
}

// executeAndReport runs one command's CoAP op on a live conn and, unless the term was evicted mid-op,
// records metrics and publishes the outcome on command-responses. It is the shared core of the live
// (dispatch) and wake-drain (drain) paths — the ONLY difference between them is what seals the
// command's fate afterward (the live path acks its JetStream message; the drain has none — the
// command's fate rides its command-delivery row).
//
// 🔴 A DRAINED COMMAND THAT NEVER ANSWERS IS NOT RETRIED, and this comment used to claim it was.
// The drain claims each row into SENT before actuating, and SENT is invisible to the drain, the
// sweep and the cancel alike — so a response terminalizes the row, and its absence lets the row sit
// until it reaches TIMEOUT. There is no "stays for the next wake": that was true only while SENT
// was drainable, which is the arrangement PARKED replaced. For this window the drain path is
// at-most-once by construction; closing it is the stranded-SENT reconciler's job, not this
// function's. It never acks or redelivers anything itself.
func (d *Dispatcher) executeAndReport(ctx context.Context, conn mux.Conn, tenant, deviceToken, name, token string, payload []byte) {
	opCtx, cancel := context.WithTimeout(ctx, d.opTimeout)
	res := d.exec.Execute(opCtx, conn, name, payload)
	cancel()
	if ctx.Err() != nil {
		return // evicted mid-op: skip the publish (a spurious FAILED); the true outcome is unknown
	}
	labelInc(d.metrics.Attempted, res.Op, 1)
	if res.Success {
		labelInc(d.metrics.Succeeded, res.Op, 1)
	} else {
		labelInc(d.metrics.Failed, res.Op, 1)
	}
	_ = d.publishResponse(tenant, deviceToken, responseEnvelope{
		CommandToken: token,
		Success:      res.Success,
		Payload:      res.Payload,
		Error:        res.Err,
	})
}

// publishResponse publishes one command outcome to the DEVICE's command-responses subject, with a
// bounded LOCAL retry (never a redelivery — see dispatch). The tenant context is built fresh from
// the tenant string (not the run ctx), so recording the outcome of an op we already ran is not
// aborted by a leadership eviction that lands during the publish. On exhaustion it counts
// ResponseFails and returns; the caller acks regardless (seal-fate).
func (d *Dispatcher) publishResponse(tenant, deviceToken string, env responseEnvelope) bool {
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
		if err = d.responses.WriteToDevice(tctx, deviceToken, messaging.Message{Value: data}); err == nil {
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

// park hands a served-but-offline device's command back to command-delivery (SENT -> PARKED)
// and then settles the message. It runs on the device's shard worker, never on the reader
// goroutine, because it makes a network call.
//
// 🔴 THE ACK DECISION IS THE WHOLE FUNCTION, AND EACH BRANCH IS DELIBERATE:
//
//   - parked, or SETTLED (the row moved on under us): ACK. In both cases the row is where it
//     should be, and redelivering would at best repeat work and at worst — see below — be the
//     thing the nonce exists to stop.
//   - ERROR: do NOT ack. The message redelivers at AckWait and we try again.
//   - no parker wired, or NO NONCE in the envelope: ack-drop, counted on ParkSkipped. An empty
//     nonce means the publisher did not stamp one — a command published by an older build, or
//     by something other than the delivery sweep — and parking on a match that ignores the
//     nonce is precisely the re-arm this design refuses. Better to leave the row in SENT than
//     to park the wrong dispatch.
//
// 🔴 NOT Nak(). The retry is an ack-wait expiry, not a negative acknowledgement: a Nak
// redelivers immediately, which turns MaxDeliver into a fuse measured in milliseconds and
// burns the whole retry budget during the instant of an outage.
//
// 🔴 BE HONEST ABOUT THE FLOOR: A ROW WHOSE PARK NEVER LANDS IS WORSE OFF THAN BEFORE PARKING
// EXISTED, NOT EQUAL TO IT. An earlier version of this comment called exhaustion "the
// pre-PARKED behaviour", and that is wrong in the one direction that matters. Before this
// change, SENT was in drainStatuses, so a stuck row was still DELIVERED on the device's next
// wake. Now SENT is invisible to the drain, the sweep and the cancel alike, so once the retry
// budget is spent — MaxDeliver × AckWait, on the order of minutes — nothing moves that row
// again and it lapses to TIMEOUT, which is precisely the mislabel PARKED exists to remove.
// The exposure needs a command-delivery outage spanning the whole budget. It is bounded, it is
// rare, and it is the concrete argument for the stranded-SENT reconciler that is filed and not
// built: that reconciler, not this retry, is what would make the floor honest.
func (d *Dispatcher) park(ctx context.Context, w work) {
	// 🔑 COUNTED WHERE THE MESSAGE SETTLES, NOT ON ENTRY. Counting at the top looks equivalent
	// and is not: a park that errors is retried by redelivery, so one offline command would
	// increment this once per attempt and read as several. The inflation would land during
	// exactly the outages an operator consults this counter to understand.
	settle := func() {
		incr(d.metrics.ServedOffline, 1)
		ackDrop(w.msg)
	}

	if d.parker == nil || w.env.DispatchNonce == "" {
		incr(d.metrics.ParkSkipped, 1)
		log.Debug().Str("tenant", w.tenant).Str("command", w.env.Token).
			Bool("parkerWired", d.parker != nil).
			Msg("Not parking an undeliverable LwM2M command; it stays SENT and rides its TTL.")
		settle()
		return
	}

	parked, err := d.parker.Park(ctx, w.tenant, w.env.Token, w.env.DispatchNonce)
	if err != nil {
		if ctx.Err() != nil {
			return // evicted mid-park: leave unacked so the next leader redelivers it
		}
		incr(d.metrics.ParkErrors, 1)
		log.Debug().Err(err).Str("tenant", w.tenant).Str("command", w.env.Token).
			Msg("Could not park an undeliverable LwM2M command; leaving it unacked to retry on redelivery.")
		return
	}
	if !parked {
		// The command moved on under us — answered, cancelled, expired, or re-claimed by a
		// wake drain. Settled, not failed.
		incr(d.metrics.ParkSettled, 1)
	}
	settle()
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

// dedupe is a small time-bounded set of recently-dispatched (deviceToken, commandToken) keys that
// suppresses a re-dispatch of the same command by the OTHER path (a wake drain fetching a PARKED
// row the live stream just delivered, or vice versa) — a re-actuation avoided. It is an
// optimization, NOT a correctness guarantee: seal-fate, the fact that the drain only reads rows
// still in drainStatuses, and the claim now taken before EVERY drained dispatch already bound
// re-fire, so under a pathological burst the set may be cleared, degrading to the platform's
// documented at-least-once actuation posture rather than growing without bound. Safe for
// concurrent use (different devices dispatch on different shard workers).
type dedupe struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	max  int
}

func newDedupe(ttl time.Duration, max int) *dedupe {
	return &dedupe{seen: make(map[string]time.Time), ttl: ttl, max: max}
}

// dedupeKey includes the TENANT: device tokens AND command tokens are only per-tenant unique
// (ADR-042), so two tenants can each legitimately have device "pump-1" carrying command "cmd-1".
// Keying without the tenant would let one tenant's dispatch suppress the other's — the live path
// would ack the second tenant's message without executing OR publishing, silently losing the command
// (it stays SENT with no wake to drain it on an always-connected device). This is the ADR-044 "every
// key adds tenant_id" shape.
func dedupeKey(tenant, deviceToken, commandToken string) string {
	return tenant + "\x00" + deviceToken + "\x00" + commandToken
}

// recentlyDispatched reports whether (tenant, deviceToken, commandToken) was marked within the TTL.
func (d *dedupe) recentlyDispatched(tenant, deviceToken, commandToken string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.seen[dedupeKey(tenant, deviceToken, commandToken)]
	return ok && time.Since(t) < d.ttl
}

// mark records (tenant, deviceToken, commandToken) as dispatched now, pruning first if at capacity.
func (d *dedupe) mark(tenant, deviceToken, commandToken string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.seen) >= d.max {
		d.pruneLocked()
	}
	d.seen[dedupeKey(tenant, deviceToken, commandToken)] = time.Now()
}

// pruneLocked drops expired keys; if the set is still at capacity (a pathological burst of distinct
// live commands within the TTL) it is cleared wholesale — the dedup is best-effort, so shedding it
// degrades to at-least-once rather than leaking memory. The caller holds d.mu.
func (d *dedupe) pruneLocked() {
	for k, t := range d.seen {
		if time.Since(t) >= d.ttl {
			delete(d.seen, k)
		}
	}
	if len(d.seen) >= d.max {
		d.seen = make(map[string]time.Time)
	}
}
