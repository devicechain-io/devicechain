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

	// DispatchNonce names the claim this publish belongs to. It is quoted back when
	// confirming the dispatch before actuating (claimLive), so a late or redelivered copy
	// cannot actuate a row that has since been re-armed or re-dispatched, and when parking an
	// undeliverable command, so a request still in redelivery cannot hand back a row that has
	// since been re-claimed and actuated. Empty means the publisher stamped none: the command
	// is then not actuated, and parking is declined rather than attempted on status alone.
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

	// DispatchNonce names the dispatch this outcome answers, and command-delivery REFUSES a
	// response that carries none. This adapter answers on the device's behalf, so it quotes the
	// nonce its claim returned: the live confirmation's on the live path (NOT the envelope's,
	// which the confirmation superseded), and the drain claim's on the wake-drain path, where
	// there is no envelope at all.
	DispatchNonce string `json:"dispatchNonce"`
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

// drainFetcher reads a device's backlogged commands (HELD or PARKED — drainStatuses) from
// command-delivery, oldest first and at most limit of them (*CommandFetcher satisfies it). It is
// the read side of the durable hold (ADR-075 L4b, Architecture D): a command withheld for a device
// known absent, or one this dispatcher handed back because the device was asleep, its shard was
// full or its gate was up, is pulled here by a drain turn.
//
// It is REQUIRED alongside a reader (NewDispatcher refuses the combination without it): every
// command this dispatcher parks is delivered by a drain, and a gate is lifted only by one, so a
// dispatcher that could park but not drain would strand every gated device's commands silently.
// nil is allowed only for the disposition unit tests, which construct a dispatcher with no reader.
//
// It takes no clock: the expiry horizon that decides which rows are still drainable is applied
// in the database against the SERVER's clock, so a skewed pod cannot drop a live command.
type drainFetcher interface {
	Pending(ctx context.Context, tenant, deviceToken string, limit int) ([]DrainCommand, error)
}

// commandClaimer is the WRITE half of the drain (*CommandClaimer satisfies it): it moves a
// still-dispatchable command to SENT and reports whether THIS caller won it. It is a
// separate seam beside drainFetcher for the same reason drainFetcher is one —
// so the claim-then-dispatch ordering below is unit-testable without a live command-delivery
// or a minted service token, including its LOST and ERRORED branches, which are the two that
// must not actuate and are therefore the two most easily faked past.
//
// It also carries the LIVE-path claim (ClaimDispatch), which confirms a command received on the
// delivery stream immediately before it actuates — see claimLive.
//
// nil disables claiming, which means NO command is dispatched at all — neither a backlogged
// (HELD or PARKED) one (see claim) nor a live one (see claimLive). That is fail-closed on
// purpose: an unclaimed dispatch is a duplicate actuation waiting to happen. The service
// refuses to start without the command-delivery coordinate this is built from, so a nil
// claimer is a test fixture, never a deployment.
type commandClaimer interface {
	Claim(ctx context.Context, tenant, commandToken string) (string, bool, error)
	ClaimDispatch(ctx context.Context, tenant, commandToken, dispatchNonce string) (string, bool, error)
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
// seam fails OPEN. The service refuses to start without the command-delivery coordinate, so
// in a deployment the parker is always wired; nil exists for the tests that construct a
// dispatcher without one.
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
	// L4b drain instruments (ADR-075). Drained/DrainErrors are the operationally interesting
	// signals; DrainTurns is a load signal.
	Drained     prometheus.Counter // a backlogged command dispatched to a device by a drain turn
	DrainErrors prometheus.Counter // a drain fetch that failed (retried after drainRetryDelay while the device stays live)
	DrainTurns  prometheus.Counter // drain turns run (each fetches at most drainTurnMax rows for one device)
	// OverflowParked counts live commands handed back to command-delivery instead of being
	// dispatched, by reason (full / offline / bind / unconfirmed — see the parkReason constants).
	// They are delivered in order by a drain moments later. The reasons are kept apart because
	// they mean different things: a slow device, queue mode working, a device reconnecting, and
	// command-delivery failing to confirm. One number mixing them could not be read.
	OverflowParked *prometheus.CounterVec
	// OverflowBlocked counts the times the reader, or a shard worker handing over a park, had to
	// WAIT for the overflow pool: every park worker busy and its queue full. Parks take a
	// command-delivery round trip, so this rises only while command-delivery is slow or down; it is
	// the one place the reader still blocks.
	OverflowBlocked prometheus.Counter
	// Live-path confirmation outcomes, split for the reason the drain's claim outcomes are.
	// StaleDispatch is BENIGN: a late or duplicate delivery the platform had already re-armed
	// or re-sent, discarded rather than actuated — a duplicate actuation avoided. LiveClaimErrors
	// is a FAULT: the confirmation could not be established (or the envelope named no dispatch),
	// so a live command was not actuated and waits for redelivery.
	StaleDispatch   prometheus.Counter
	LiveClaimErrors prometheus.Counter
	// Claim outcomes, split because they mean opposite things operationally. LOST is BENIGN —
	// someone else owns that command and we correctly declined to actuate it twice. ERRORS is a
	// FAULT: command-delivery could not be reached, so a claimable command went undispatched
	// (fail-closed) and the device's turn is retried after drainRetryDelay. One counter would let
	// a rising outage hide inside a normal-looking race count.
	DrainClaimLost   prometheus.Counter // a held command another dispatcher/the sweep claimed first — no actuation, correct
	DrainClaimErrors prometheus.Counter // a claim that could not be established; the command is NOT dispatched, retried shortly
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
	// workerQueueDepth buffers each shard's LIVE queue. When it is full, route does not wait: the
	// command is parked in command-delivery and its device gated, and a drain delivers it in order
	// moments later (see gate.go). So a slow device no longer holds the single reader, and with it
	// the whole instance's command throughput, to its own rate — the head-of-line convoy this
	// used to name as a limit. What still back-pressures the reader is the overflow pool, and only
	// while command-delivery itself is slow (see overflowWorkers).
	workerQueueDepth = 8
	// drainTurnMax bounds one drain turn: at most this many backlogged rows for ONE device, after
	// which the shard's worker takes its next live task before another turn. So the worst a
	// device sharing a shard with a backlogged one waits is about (1 + drainTurnMax) ops, 50s at
	// the default opTimeout, where a full live queue used to cost it depth × opTimeout.
	//
	// It is 4 because it sits below AckWait / DefaultOpTimeout (60s / 10s = 6): a live task queued
	// behind one full turn is dispatched inside its own ack deadline, so a turn never makes a live
	// command redeliver. It also replaced the old per-wake cap of 32 as the device-edge flood
	// governor: a backlog now reaches a device 4 at a time, interleaved with the shard's live work,
	// and keeps going while the device stays live instead of waiting for a wake that a connected
	// device never sends.
	drainTurnMax = 4
	// overflowWorkers is the park pool's size, and overflowDepth its queue. Each park is one
	// command-delivery round trip, bounded by the service client's 10s request timeout. The pool
	// fills only while command-delivery is slow; the reader, and a shard worker with a park to hand
	// over, then wait on it (counted on OverflowBlocked) rather than dropping or reordering. The
	// bound on that wait is the convoy this leaves: while command-delivery is down, the reader
	// moves at overflowWorkers parks per 10s timeout. A live command would not get through then either, since its confirmation needs
	// the same service.
	overflowWorkers = 4
	overflowDepth   = 64
	// drainRetryDelay defers a device's next drain turn after a fetch or claim error, so an outage
	// does not spin every shard. A timer retries it; see pickEligible.
	drainRetryDelay = 5 * time.Second
	// responsePublishAttempts bounds the LOCAL retry of a command-response publish. After a
	// successful dispatch the command's fate is sealed (ADR-075 L4a S1): we retry the publish a few
	// times, then ack REGARDLESS — never redeliver a message whose CoAP op already ran (which would
	// re-actuate a physical device); the command then rides SENT→TIMEOUT.
	responsePublishAttempts = 3
	// responsePublishBackoff paces the local response-publish retry.
	responsePublishBackoff = 250 * time.Millisecond
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
	// readPacer bounds a run of failing reads and ends the process once they stop looking
	// transient. See Options.ReadPacer for why this is not optional in production.
	readPacer *core.ReadPacer
	// tenantDeleted reports whether a tenant has been through the ADR-077 delete door.
	// Never nil; see NewDispatcher.
	tenantDeleted func(tenant string) bool
	workers       int
	opTimeout     time.Duration
	// shardsPtr publishes the CURRENT leadership term's shards so Drain (called from the /rd
	// handler goroutine, off the Run loop) can gate a device and wake its shard. It is nil whenever
	// this replica is not the serving leader (before Run, and after it returns), so a Drain on a
	// standby is a safe no-op. atomic so the handler goroutine reads it without a lock. The shards,
	// and every gate in them, are per term: a new leader starts with none, and rebuilds them from
	// each device's first bind (see gate.go).
	shardsPtr atomic.Pointer[[]*shardState]
	// clock times the gates' deadlines and the deferred-turn timer. realClock outside tests.
	clock dispatchClock
	// ready is closed by Run once shardsPtr is published and the workers are started. The leader waits
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
	// ReadPacer bounds how long the command-reader loop will retry a failing read before
	// declaring this process unfit. REQUIRED whenever a real reader is supplied;
	// NewDispatcher refuses the combination rather than defaulting it.
	//
	// 🔴 THE REFUSAL IS THE POINT, AND IT FOLLOWS THIS TYPE'S OWN RULE. The comment on
	// Parker below draws the line: positional when omitting it fails CLOSED, optional when
	// omitting it degrades visibly. A missing pacer fails closed and then hides — the loop
	// retries forever behind a pod that reports Ready, leader and serving, while consuming
	// no commands, and Metrics has no read-error counter to show it. It is an Option rather
	// than a positional parameter only because the routing, drain and park tests construct a
	// dispatcher with a NIL reader and never read at all; the refusal below is what keeps
	// that convenience from reaching production.
	ReadPacer *core.ReadPacer
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
// the conn table, the CoAP op executor, the drain fetcher (required with a reader; nil only for
// reader-less unit tests, where it disables draining) and the claimer (nil means no command, live
// or backlogged, is dispatched — fail-closed).
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
	if rdr != nil && opts.ReadPacer == nil {
		panic("lwm2m-ingest: NewDispatcher needs a read pacer when it is given a reader; without " +
			"one a command-reader loop whose consumer is broken retries forever while the term " +
			"stays healthy, and the pod reports leader and serving while dispatching nothing")
	}
	if rdr != nil && fetcher == nil {
		panic("lwm2m-ingest: NewDispatcher needs a drain fetcher when it is given a reader; every " +
			"command it parks is delivered by a drain, and a device's gate is lifted only by one, " +
			"so without it a gated device's commands would be parked and never delivered")
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
		readPacer:     opts.ReadPacer,
		tenantDeleted: opts.TenantDeleted,
		workers:       opts.Workers,
		opTimeout:     opts.OpTimeout,
		clock:         realClock{},
		ready:         make(chan struct{}),
	}
}

// Ready returns a channel closed once Run has published this term's shards — the leader waits on it
// before serving the transport so no wake is lost to a serve-before-Run window.
func (d *Dispatcher) Ready() <-chan struct{} { return d.ready }

// work is one parsed live command (a consumed device-commands message) routed to a device-sharded
// worker.
type work struct {
	msg    messaging.Message
	tenant string
	env    deliveryEnvelope
}

// drainJob names one device whose backlog a drain turn serves. It runs on that device's shard
// worker, so it serializes with the device's live commands (no reorder, no concurrent
// double-dispatch).
type drainJob struct {
	tenant      string
	deviceToken string
}

// task is the union a shard worker processes: a live command OR a drain turn. Both carry the device
// token and run on the same shard(deviceToken) worker — the seam that makes a device's drain and
// live dispatch strictly serial. A live task reaches the worker through the shard's queue; a drain
// turn is started by the worker itself (runOneDrainTurn), never queued, so it can never be dropped.
type task struct {
	deviceToken string
	live        *work
	drain       *drainJob
}

// overflowItem is one live command bound for the park pool rather than the live queue.
type overflowItem struct {
	shard  *shardState
	key    deviceKey
	w      work
	reason string
}

// Run consumes and dispatches until ctx is cancelled (leadership eviction / shutdown). It is the
// single reader + a fixed pool of device-sharded workers + a small park pool: the reader parses each
// message and either queues it on worker[hash(deviceToken)] or, when the device must not be
// dispatched live right now, hands it to the park pool (see route). A device's commands stay in
// stream order while distinct devices dispatch concurrently. The reader honors ctx (ReadMessage
// returns on cancel) so an evicted replica stops pulling promptly; the durable consumer is bound
// (not owned), so its cursor survives the term and the next leader resumes from the last ack.
func (d *Dispatcher) Run(ctx context.Context) {
	// The workers run on a ctx this function can end on its own, not just on the term's.
	//
	// Before the read loop could give up, "the loop stopped" and "the term ended" were the
	// same event, so deriving the workers' ctx from the caller's was enough. They are no
	// longer the same: an exhausted read budget breaks the loop with the term still HELD.
	// Without this, Run would then sit on wg.Wait() until something else cancelled the
	// term -- which does happen (the give-up calls FailNow, whose teardown reaches
	// leadershipCancel), so this is NOT a hang, and an earlier version of this comment was
	// wrong to imply one. What it buys is narrower and still worth having: Run's contract
	// becomes "returns when its loop ends" rather than "returns once a third party
	// cancels", which is what lets the give-up be tested at all.
	//
	// The one behaviour change is stated rather than hidden: an in-flight d.process is
	// aborted when the budget is exhausted instead of when teardown arrives. That is the
	// same abort eviction already performs -- the command is left unacked and redelivers
	// to the next leader.
	runCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()

	shards := make([]*shardState, d.workers)
	overflow := make(chan overflowItem, overflowDepth)
	var wg sync.WaitGroup
	for i := range shards {
		shards[i] = newShardState()
		shards[i].overflow = overflow
		wg.Add(1)
		go func(s *shardState) {
			defer wg.Done()
			d.serveShard(runCtx, s)
		}(shards[i])
	}
	for range overflowWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A command left in the overflow queue at eviction is unacked and redelivers to the
			// next leader, like one left in a shard queue.
			for {
				select {
				case <-runCtx.Done():
					return
				case it := <-overflow:
					d.parkTracked(runCtx, it.shard, it.key, it.w, it.reason)
				}
			}
		}()
	}
	// Publish this term's shards so Drain (handler goroutine) can wake them; clear on return so a
	// post-eviction Drain is a no-op rather than gating shards whose workers have exited. Signal
	// ready AFTER the publish so the leader only starts serving once wakes can be accepted.
	d.shardsPtr.Store(&shards)
	close(d.ready)
	defer d.shardsPtr.Store(nil)

	// The read loop's own shutdown check, its terminal error set and its pacing all live in
	// messaging.RunConsumer now. What stays here is the routing, which is this dispatcher's
	// alone.
	//
	// 🔴 THE PACER IS NOT REDUNDANT WITH LEASE EVICTION, WHICH IS WHAT THIS COMMENT USED TO
	// CLAIM. It said a durable NATS outage stalls this term's lease renewal, which evicts the
	// term and ends the loop, "so this never spins forever on a dead broker." That is true and
	// it is beside the point. The lease renews over the SAME connection the reader uses, so it
	// covers exactly the case where the whole broker is gone — and the errors that actually
	// reach here (a JetStream API error, a 409 at a MaxAckPending or MaxWaiting ceiling, a
	// consumer whose leadership keeps moving) happen on a connection that is perfectly HEALTHY.
	// Lease.KeepAlive gives up only when Renew FAILS and the TTL window has passed
	// (core/messaging/lease.go), so with the broker up the term is renewed indefinitely while
	// this loop would retry a broken consumer once a second, forever, behind a pod reporting
	// leader and serving. Metrics carries no read-error counter, so nothing would show it.
	messaging.RunConsumer(ctx, d.reader, d.readPacer, func(msg messaging.Message) bool {
		return d.route(ctx, shards, overflow, msg)
	})

	// Ends the workers on both exits: an evicted term, where the caller's ctx is already
	// cancelled and this is a no-op, and an exhausted read budget, where it is not.
	stopWorkers()
	wg.Wait()
}

// serveShard is one shard's worker loop: take a live task if there is one, otherwise wait for a live
// task or a nudge; then run at most ONE drain turn. So a shard alternates one live task with one
// bounded drain turn (drainTurnMax ops), and neither can starve the other.
//
// 🔴 THE LIVE QUEUE IS CHECKED FIRST, WITHOUT BLOCKING, and that is what makes the alternation
// strict rather than a coin toss. With a single select over both channels Go picks at random
// when both are ready, so a device sharing the shard with a deep backlog could wait any number
// of turns. Checking the queue first bounds that wait at one turn.
//
// A ctx-select loop (not `range ch`) so an evicted term stops immediately and the channel is never
// closed. A live command left buffered at eviction is simply unprocessed (unacked → redelivers to
// the next leader), and the gates die with the term: the next leader rebuilds them from each
// device's first bind.
func (d *Dispatcher) serveShard(ctx context.Context, s *shardState) {
	for {
		var t task
		got := false
		select {
		case <-ctx.Done():
			return
		case t = <-s.ch:
			got = true
		default:
			select {
			case <-ctx.Done():
				return
			case t = <-s.ch:
				got = true
			case <-s.nudge:
			}
		}
		if got {
			key := deviceKey{t.live.tenant, t.deviceToken}
			if reason, park := s.dequeue(key); park {
				// The device was gated after this command was queued. It goes to the backlog
				// behind whatever gated it, rather than past it — through the park pool, so a slow
				// command-delivery does not hold this shard's other devices for a round trip.
				_, reach := d.conns.Lookup(key.tenant, key.deviceToken)
				d.parkFromWorker(ctx, s, key, *t.live, gatedParkReason(reason, reach))
			} else {
				d.process(ctx, t)
			}
		}
		d.runOneDrainTurn(ctx, s)
	}
}

// runOneDrainTurn serves at most one ready device, at most drainTurnMax of its backlogged rows, and
// then settles its gate (finishTurn). If more devices want a turn it nudges its own shard, so the
// worker comes straight back after taking any live task that is waiting.
//
// 🔴 THE TRIGGER IS LOCAL, AND IT HAS TO BE. A device that stays connected sends keepalive Updates
// on its live connection, and those fire no wake (ConnTable.Refresh returns early on an unchanged
// conn). A drain that waited for the device to wake would leave a connected device's backlog —
// parked because its queue was full, or re-armed by the platform — until it next re-handshaked or
// its commands expired. So the park settle, the gate set and this worker's own loop are what start
// a turn, and a device drains for as long as it stays live and has rows.
func (d *Dispatcher) runOneDrainTurn(ctx context.Context, s *shardState) {
	if ctx.Err() != nil {
		return
	}
	key, gen0, ok := s.pickEligible(d.clock)
	if !ok {
		return
	}
	incr(d.metrics.DrainTurns, 1)
	res := d.process(ctx, task{deviceToken: key.deviceToken, drain: &drainJob{tenant: key.tenant, deviceToken: key.deviceToken}})
	if ctx.Err() != nil {
		return // the term is ending, and the gates with it
	}
	if s.finishTurn(key, gen0, res, d.clock.Now()) {
		s.poke()
	}
}

// route places one command message on its device's shard, or hands it to the park pool, and reports
// whether the read loop should carry on.
//
// 🔴 IT IS THE ONE GOROUTINE THAT MUST NOT MAKE A NETWORK ROUND TRIP, AND IT NO LONGER WAITS ON A
// SHARD. It used to block on a full shard queue, so one live-but-slow device (each op burning the
// full opTimeout) held the single reader, and with it every other device on the instance, to that
// device's rate. Now a command that cannot be queued live is parked in command-delivery by the
// park pool and its device gated; a drain delivers it in order moments later.
//
// A command goes to the park pool, rather than the live queue, when its device:
//   - is OFFLINE (served, but no live connection): parking is a network round trip, which this
//     goroutine must not make, and the pool keeps it out of the device's shard so a
//     command-delivery outage cannot fill shards with parks timing out;
//   - is GATED: an earlier command of its has taken the backlog road, and this one must follow it;
//   - has a FULL shard queue.
//
// The one remaining wait is on the park pool itself, when all its workers are busy and its queue
// is full: that happens only while command-delivery is slow, and is counted on OverflowBlocked.
// Dropping the command instead, or leaving it unacked, would lose its place: a redelivered copy
// could arrive after a drain had already served newer rows.
func (d *Dispatcher) route(ctx context.Context, shards []*shardState, overflow chan<- overflowItem, msg messaging.Message) bool {
	w, ok := d.parse(msg)
	if !ok {
		return true // poison — already acked + counted in parse
	}
	// Pre-filter reachability at ROUTE time (S4 hardening): a command for a device this adapter
	// does not serve — the DOMINANT case, since this cross-tenant consumer sees every other
	// protocol adapter's device-commands too — is ack-dropped HERE, so it never occupies a
	// per-device worker slot. It is another protocol's traffic and there is nothing for this
	// adapter to record about it.
	_, reach := d.conns.Lookup(w.tenant, w.env.DeviceToken)
	if reach == ReachNotServed {
		d.dropNonLive(reach, w.msg)
		return true
	}
	key := deviceKey{w.tenant, w.env.DeviceToken}
	s := shards[d.shard(w.env.DeviceToken)]
	reason := s.admit(key, reach, task{deviceToken: w.env.DeviceToken, live: &w})
	if reason == "" {
		return true // queued live
	}
	// Evicted before it could be handed over: do NOT ack, so the message redelivers to the next
	// leader rather than being dropped by a replica that is no longer serving. The gate state dies
	// with the term.
	return d.handOff(ctx, overflow, overflowItem{shard: s, key: key, w: w, reason: reason})
}

// handOff gives one park to the park pool, waiting (counted on OverflowBlocked) while every park
// worker is busy and its queue is full. It reports false only when the term ended first, leaving
// the message unacked.
func (d *Dispatcher) handOff(ctx context.Context, overflow chan<- overflowItem, it overflowItem) bool {
	select {
	case overflow <- it:
		return true
	default:
	}
	incr(d.metrics.OverflowBlocked, 1)
	select {
	case overflow <- it:
		return true
	case <-ctx.Done():
		return false
	}
}

// parkFromWorker parks, through the park pool, a live command a shard worker has already counted
// against its device's gate (dequeue or beginPark). The pool is where every park the dispatcher
// makes for a live command runs: a park is a command-delivery round trip of up to the service
// client's timeout, and on the worker it would hold every other device on the shard for that long
// while command-delivery is slow. The worker still waits when the pool itself is full, like the
// reader does. A shard with no pool (a test driving a shard directly) parks inline.
func (d *Dispatcher) parkFromWorker(ctx context.Context, s *shardState, key deviceKey, w work, reason string) {
	if s.overflow == nil {
		d.parkTracked(ctx, s, key, w, reason)
		return
	}
	d.handOff(ctx, s.overflow, overflowItem{shard: s, key: key, w: w, reason: reason})
}

// Drain gates a device and asks its shard for a drain turn, so the leader pulls that device's
// backlogged commands (HELD or PARKED) from command-delivery and dispatches them, in order, to its
// live conn (ADR-075 L4b). It is called when a device becomes live on a fresh connection (Register,
// or a re-handshake Update — the LwM2M queue-mode wake), and when a live delivery turns out to have
// been re-armed already (claimLive).
//
// 🔴 IT GATES AS WELL AS WAKES. The gates are in memory and per term, so on a new leader — or
// after any reconnect — nothing here knows whether the device has PARKED rows waiting (left by an
// old leader's overflow, or re-armed by command-delivery's stranded pass). A live command arriving
// before the drain has looked would overtake them. So the device is gated until a turn has seen its
// backlog empty. The cost is that a live command landing between the bind and the end of that
// first turn is parked and drained rather than dispatched directly: one fetch round trip, which the
// bind already paid.
//
// It is non-blocking and it is NEVER DROPPED: it records the request under the shard's lock and
// nudges the worker. (It used to enqueue onto the shard's channel and drop the wake when the shard
// was full, promising that "the next wake re-triggers" — which a device that stays connected never
// sends.) A call on a standby (no active term) is a no-op.
func (d *Dispatcher) Drain(tenant, deviceToken string) {
	if d.fetcher == nil {
		return // draining disabled (reader-less unit tests only; NewDispatcher refuses it otherwise)
	}
	p := d.shardsPtr.Load()
	if p == nil {
		return // not the serving leader right now
	}
	(*p)[d.shard(deviceToken)].wake(deviceKey{tenant, deviceToken})
}

// shardFor returns the current term's shard for a device, or nil when no term is running (a
// standby, or a unit test calling dispatch directly).
func (d *Dispatcher) shardFor(deviceToken string) *shardState {
	p := d.shardsPtr.Load()
	if p == nil {
		return nil
	}
	return (*p)[d.shard(deviceToken)]
}

// process runs one shard-worker task: a live command or a drain turn. It returns what a drain turn
// found (the zero value for a live command).
//
// 🔴 The ADR-077 lifecycle gate sits HERE, at the union, because there are TWO ways a
// command reaches a device and command-delivery's own gate covers neither completely:
//
//   - the LIVE path reads the device-commands stream, and a command published in the
//     moments before the delete is already durable in that stream — command-delivery
//     refusing to publish more does not unpublish those;
//   - the DRAIN re-fetches commands still HELD or PARKED and fires them when a sleeping
//     device next registers, which can be long after the tenant was deleted. Nothing on
//     that path had a lifecycle check: the (tenant, token) is remembered from a
//     registration that predates the delete, and PSK bindings are static config.
//
// A command is a PHYSICAL ACTUATION, so "the sweep gate stops most of them" is not a
// standard this path can be held to. Gating the union rather than the two callers means a
// third task kind cannot be added without one. (Parking is not actuation, so the park paths do
// not pass through here: a deleted tenant's parked command is refused by the drain that would
// have delivered it.)
func (d *Dispatcher) process(ctx context.Context, t task) turnResult {
	switch {
	case t.live != nil:
		if d.tenantDeleted(t.live.tenant) {
			// Acked, not retried: the tenant is not coming back, and leaving the message
			// unacked would redeliver this refusal until the stream ages out.
			d.ackRefused(t.live.msg, t.live.tenant)
			return turnResult{}
		}
		d.dispatch(ctx, *t.live)
	case t.drain != nil:
		if d.tenantDeleted(t.drain.tenant) {
			return turnResult{stopped: true}
		}
		return d.drain(ctx, *t.drain)
	}
	return turnResult{}
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
		// claimed by the drain on the device's next wake. (A command routed while the device was
		// already offline never reaches here: route sends it straight to the park pool. This is
		// the device that dropped after its command was queued.)
		//
		// It is tracked on the device's gate like every other park, so a park that errors holds
		// the device's later commands behind its redelivery.
		if s := d.shardFor(w.env.DeviceToken); s != nil {
			key := deviceKey{w.tenant, w.env.DeviceToken}
			s.beginPark(key, parkReasonOffline)
			d.parkFromWorker(ctx, s, key, w, parkReasonOffline)
			return
		}
		d.park(ctx, w, parkReasonOffline)
		return
	}
	if reach != ReachLive {
		// Not served: another protocol's device, or none. Nothing to record — ack-dropping just
		// advances our cursor. This is also the freshness re-check for a command the reader let
		// through, though a device cannot become not-served between the two.
		d.dropNonLive(reach, w.msg)
		return
	}
	// Confirm the dispatch on its row BEFORE actuating: the live-path claim. See claimLive for
	// why a live envelope cannot simply be trusted.
	nonce, ok := d.claimLive(ctx, w)
	if !ok {
		return // claimLive has already settled (or deliberately not settled) the message
	}
	if ctx.Err() != nil {
		// Evicted between the confirmation and the op. The op never ran, but the row is now on
		// the NEW dispatch, so the redelivered envelope (which quotes the old one) will lose its
		// own confirmation on the next leader. Hand the row back so the next wake drain delivers
		// it; the message is left unacked, and its redelivery is then discarded as stale.
		d.parkConfirmed(w, nonce)
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
	// SENT→TIMEOUT. The PRE-op eviction checks (top of dispatch, and after the confirmation)
	// are the only ones that leave the message for redelivery, and in both the op never ran.
	//
	// 🔴 The response quotes the CONFIRMED nonce, not the envelope's: the confirmation moved the
	// row onto a new dispatch, and command-delivery matches an answer against the current one.
	d.executeAndReport(ctx, conn, w.tenant, w.env.DeviceToken, w.env.Name, w.env.Token,
		nonce, payload)
	// Seal fate: the CoAP op already ran, so ack whether or not the response published — a publish we
	// could not land leaves the command to TIMEOUT, which is the correct terminal for a lost outcome
	// and strictly safer than a second actuation.
	ackDrop(w.msg)
}

// claimLive confirms a live command's dispatch with command-delivery immediately before it is
// actuated, and reports the NEW dispatch nonce to quote in its response. ok=false means do not
// actuate, and the message has already been dealt with as below.
//
// 🔴 WHY THE LIVE PATH CLAIMS. The delivery sweep claims a row and publishes it, and a
// publish can reach this dispatcher LATE: redelivered after it waited out its ack deadline
// queued behind a slow device, redelivered to a new leader after a failover, or pulled for
// the first time long after it was published because no replica was reading — and an
// envelope nobody has pulled has no ack timer running at all. In the meantime command-delivery
// may have re-armed the row (the stranded-SENT pass parks it) and a wake drain may have
// claimed and actuated it. Before this confirmation the live path actuated whatever arrived,
// so that sequence moved the hardware twice. The confirmation is a conditional UPDATE on
// (SENT, envelope nonce) that rotates the nonce, so the late envelope loses, and so does every
// later redelivered copy of an envelope that was already confirmed once.
//
// Its outcomes, and what each does with the message:
//   - WON: actuate, quoting the returned nonce.
//   - LOST: the dispatch this envelope names is gone. Ack-drop and count StaleDispatch, then
//     call Drain for the device: the usual cause is that the row was re-armed to PARKED, and a
//     device that stays connected sends no wake of its own, so without it that row would wait
//     for a re-handshake. Drain also gates the device, so none of its later live commands can
//     overtake the re-armed row. It only takes this worker's own shard lock briefly and never
//     blocks, so calling it from here cannot deadlock.
//   - ERROR: FAIL CLOSED. Count LiveClaimErrors and leave the message UNACKED, so it
//     redelivers at the ack deadline — never Nak'd, which would spend the whole delivery
//     budget in the instant of an outage. Actuating on a confirmation we could not obtain is
//     the one outcome that cannot be taken back. The device is GATED (reason "unconfirmed")
//     until that redelivery has been parked or its budget has run out: see below.
//   - ERROR because the term was evicted: return unacked and uncounted, as claim does — it
//     is the eviction, not command-delivery, and counting it would make every failover
//     look like an outage.
//   - NO NONCE in the envelope, or NO CLAIMER wired: refuse. An envelope with no nonce names
//     no dispatch, so no confirmation can ever succeed and redelivering it only burns the
//     budget; it is ack-dropped and counted as an error (a publisher fault). A nil claimer
//     is a wiring fault; the command is left unacked and counted, like any other failure to
//     establish ownership.
//
// 🔴 WHY AN ERROR GATES THE DEVICE. The unacked message redelivers at the ack deadline, and if
// command-delivery recovers before then, a LATER command for the same device would arrive, be
// confirmed and actuate first — a reorder, of exactly the kind a firmware write followed by its
// execute cannot survive. So the error gates the device and holds the gate for this command's
// redelivery (gate.go): the device's later commands are parked behind it, the redelivered copy
// finds the device gated and is parked too, and a drain then serves them all oldest-first.
//
// Order can still break in TWO ways. One is a command-delivery outage longer than the whole
// redelivery budget, after which the broker gives up on the message and the gate stops waiting
// for it. The other is an error that was not a failure: the confirmation COMMITTED on
// command-delivery and only its answer was lost (the service client's timeout, a dropped
// connection). The row is then SENT on a nonce this adapter never learned, the redelivered
// envelope's park matches nothing and settles as "moved on", the gate releases it, and the drain
// serves the later commands while this one waits in SENT for command-delivery's stranded pass to
// re-arm it after its grace; it is then delivered on the device's next bind, after them, or not at
// all if it expires first. Nothing on this side can tell that settle from a benign one
// (the command answered, cancelled or expired); closing it needs command-delivery to recognise a
// retried confirmation.
func (d *Dispatcher) claimLive(ctx context.Context, w work) (string, bool) {
	if w.env.DispatchNonce == "" {
		incr(d.metrics.LiveClaimErrors, 1)
		log.Warn().Str("tenant", w.tenant).Str("command", w.env.Token).
			Msg("Refusing an LwM2M command whose delivery names no dispatch; it cannot be confirmed, so it is not actuated.")
		ackDrop(w.msg)
		return "", false
	}
	if d.claimer == nil {
		incr(d.metrics.LiveClaimErrors, 1)
		log.Debug().Str("tenant", w.tenant).Str("command", w.env.Token).
			Msg("Not actuating a live LwM2M command: no command claimer is wired, so its dispatch cannot be confirmed.")
		return "", false
	}
	nonce, won, err := d.claimer.ClaimDispatch(ctx, w.tenant, w.env.Token, w.env.DispatchNonce)
	if err != nil {
		if ctx.Err() == nil {
			incr(d.metrics.LiveClaimErrors, 1)
			log.Debug().Err(err).Str("tenant", w.tenant).Str("command", w.env.Token).
				Msg("Could not confirm a live LwM2M command's dispatch; not actuating it, leaving it unacked to retry on redelivery.")
			if s := d.shardFor(w.env.DeviceToken); s != nil {
				s.gateUnconfirmed(deviceKey{w.tenant, w.env.DeviceToken}, w.env.Token, d.clock.Now())
			}
		}
		return "", false
	}
	if !won {
		incr(d.metrics.StaleDispatch, 1)
		ackDrop(w.msg)
		d.Drain(w.tenant, w.env.DeviceToken)
		return "", false
	}
	return nonce, true
}

// parkConfirmedTimeout bounds the best-effort hand-back after an eviction. It runs on a context
// detached from the evicted term's, which is already cancelled.
const parkConfirmedTimeout = 5 * time.Second

// parkConfirmed hands back a row this dispatcher confirmed but did not actuate because its term
// was evicted in between. It quotes the CONFIRMED nonce — the envelope's no longer matches the
// row. It is best effort: if it fails, the row stays SENT on a dispatch nobody holds and the
// stranded-SENT pass re-arms it once its grace has passed.
//
// 🔑 IT CAN DELAY THE HANDOFF BY UP TO parkConfirmedTimeout. It runs synchronously on the
// evicted term's shard worker; Run waits for its workers before returning, and the term's
// unwind in main.go waits for Run (dispatcherDone) before it releases the lease. So a slow or
// unreachable command-delivery holds the lease this long past the eviction before the next
// leader can take it. It is a rare path (an eviction landing between a confirmation and its op),
// and the bound is what keeps it a delay rather than a stall.
func (d *Dispatcher) parkConfirmed(w work, nonce string) {
	if d.parker == nil {
		return
	}
	parkCtx, cancel := context.WithTimeout(context.Background(), parkConfirmedTimeout)
	defer cancel()
	if _, err := d.parker.Park(parkCtx, w.tenant, w.env.Token, nonce); err != nil {
		log.Debug().Err(err).Str("tenant", w.tenant).Str("command", w.env.Token).
			Msg("Could not hand back a confirmed LwM2M command after losing leadership; the stranded-command pass will re-arm it.")
	}
}

// drain is ONE drain turn for a device: it pulls at most drainTurnMax of the device's backlogged
// commands (HELD or PARKED) from command-delivery, oldest first, and dispatches them to its live
// conn (ADR-075 L4b). It runs on the device's shard worker (so it never races or reorders the
// device's live commands), leader-only (Drain no-ops on a standby), and reports what it found so
// the worker can decide whether the device's gate lifts, it needs another turn, or it retries
// later (finishTurn).
//
// A fetch failure is counted and retried after drainRetryDelay. The device dropping or the term
// being evicted mid-turn stops cleanly, leaving the remaining rows untouched for the next wake or
// the next leader (never a partial ack of something not dispatched).
//
// 🔴 The ordering inside the loop is CLAIM, THEN DISPATCH, and it is that way round because
// dispatching is IRREVERSIBLE. See claim below for why.
//
// 🔴 A CLAIM ERROR ENDS THE TURN, and it used to skip to the next row. Skipping kept the rest of
// the backlog moving on the next wake, but it dispatched a NEWER row while an older one stayed
// behind, and a firmware write and its execute cannot survive that. The turn now stops at the
// first row it could not claim and retries from it after drainRetryDelay, so the backlog still
// moves, in order.
func (d *Dispatcher) drain(ctx context.Context, job drainJob) turnResult {
	if ctx.Err() != nil || d.fetcher == nil {
		return turnResult{stopped: true}
	}
	if _, reach := d.conns.Lookup(job.tenant, job.deviceToken); reach != ReachLive {
		return turnResult{stopped: true} // not live: its next bind wakes it again
	}
	cmds, err := d.fetcher.Pending(ctx, job.tenant, job.deviceToken, drainTurnMax)
	if err != nil {
		if ctx.Err() != nil { // a fetch aborted by eviction is not a drain error
			return turnResult{stopped: true}
		}
		incr(d.metrics.DrainErrors, 1)
		log.Debug().Err(err).Str("tenant", job.tenant).Str("device", job.deviceToken).
			Msg("Could not fetch backlogged LwM2M commands; retrying shortly while the device stays live.")
		return turnResult{failed: true}
	}
	for _, c := range cmds {
		if ctx.Err() != nil {
			return turnResult{stopped: true} // evicted mid-turn: the remaining rows are untouched, for the next leader
		}
		conn, reach := d.conns.Lookup(job.tenant, job.deviceToken)
		if reach != ReachLive {
			return turnResult{stopped: true} // the device dropped mid-turn: the remaining rows are untouched, for its next wake
		}
		// A lost claim below does not cost this turn a delivery, even though the page is exactly
		// drainTurnMax rows with no over-fetch: it means someone else has already moved the row
		// out of the dispatchable set, so a skipped row is one that has left the backlog, not a
		// slot taken from a row still awaiting delivery. (A row the live path just dispatched is
		// SENT, which is not drainable, so it was never on the page.)
		//
		// Take the command out of command-delivery's dispatchable set BEFORE actuating. A
		// command we could not claim is one we must not fire.
		nonce, won, failed := d.claim(ctx, job.tenant, c)
		if failed {
			if ctx.Err() != nil {
				return turnResult{stopped: true}
			}
			return turnResult{fetched: len(cmds), failed: true}
		}
		if !won {
			continue
		}
		d.executeAndReport(ctx, conn, job.tenant, job.deviceToken, c.Name, c.Token, nonce, c.Payload)
		incr(d.metrics.Drained, 1)
	}
	if ctx.Err() != nil {
		return turnResult{stopped: true}
	}
	return turnResult{fetched: len(cmds)}
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
// 🔴 Nothing held in this pod's memory could be this guarantee: it would say nothing about
// another replica or about this pod after a restart — exactly the two situations a leadership
// change produces. (A per-pod recently-dispatched cache used to sit beside the claims; the
// live-path confirmation made every route to a device claimed on its row, and it was removed.)
//
// 🔴 EVERY DRAINED ROW IS CLAIMED, WITH NO EXCEPTION, AND THE EXCEPTION THIS REPLACES WAS
// THE PLATFORM'S ONLY UNCLAIMED DISPATCH. A SENT row used to return true here immediately,
// on the argument that SENT has already left the dispatchable set and nothing returns it
// there. The argument was sound and the premise was not: a SENT row could be a command that
// went NOWHERE — published to a registered-but-sleeping device and ack-dropped by this very
// dispatcher — so the drain was re-dispatching commands whose only protection against a
// second actuation was a per-pod cache of the kind disclaimed two paragraphs above. Those rows are
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
//     actuate is recoverable (the row is still dispatchable, and the turn retries from it after
//     drainRetryDelay), whereas actuating on an unconfirmed claim is not. Logged at debug like
//     the fetch-failure path: on a real outage this fires once per device per retry, and a warn
//     each time would bury the outage in its own noise. failed reports it, so the turn stops.
//
// 🔴 IT ALSO RETURNS THE DISPATCH NONCE, AND THE DRAIN CANNOT REPORT AN OUTCOME WITHOUT IT.
// This path has no delivery envelope — it dispatches a row it read, not a message it consumed
// — so the claim is the only place the identity of the dispatch it just created exists. A
// response that names no dispatch is refused by command-delivery, so a drain that dropped this
// value would actuate devices and settle nothing.
func (d *Dispatcher) claim(ctx context.Context, tenant string, c DrainCommand) (nonce string, won, failed bool) {
	if d.claimer == nil {
		// Claiming disabled but a claimable row arrived: refuse rather than fire. Counted as
		// an error, not a loss — nobody else took this command, the platform simply cannot
		// prove it owns it, and that is a wiring fault worth seeing on a graph.
		incr(d.metrics.DrainClaimErrors, 1)
		log.Debug().Str("tenant", tenant).Str("command", c.Token).Str("status", c.Status).
			Msg("Not dispatching a held LwM2M command: no command claimer is wired, so ownership cannot be established.")
		return "", false, true
	}
	nonce, won, err := d.claimer.Claim(ctx, tenant, c.Token)
	if err != nil {
		if ctx.Err() == nil { // a claim aborted by eviction is not a claim failure
			incr(d.metrics.DrainClaimErrors, 1)
			log.Debug().Err(err).Str("tenant", tenant).Str("command", c.Token).
				Msg("Could not claim a backlogged LwM2M command; not dispatching it (it stays dispatchable and is retried shortly, in order).")
		}
		return "", false, true
	}
	if !won {
		// Someone else moved it out of the dispatchable set first. Nothing is wrong; this is
		// the mechanism working.
		incr(d.metrics.DrainClaimLost, 1)
		return "", false, false
	}
	return nonce, true, false
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
//
// dispatchNonce names the dispatch being answered and is quoted straight into the response —
// from the live confirmation on the live path, from the claim on the drain path. It is not
// re-derived or defaulted here: command-delivery refuses an answer that names no dispatch, and
// an answer this adapter invented a name for would be worse than one it refused to send.
func (d *Dispatcher) executeAndReport(ctx context.Context, conn mux.Conn, tenant, deviceToken, name,
	token, dispatchNonce string, payload []byte) {
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
		CommandToken:  token,
		Success:       res.Success,
		Payload:       res.Payload,
		Error:         res.Err,
		DispatchNonce: dispatchNonce,
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
	// publish. With no deadline here, each publish is bounded by the core messaging writer's 5 s
	// per-publish ceiling.
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

// parkTracked parks one live command for a device whose gate it has already been counted
// against (admit, dequeue or beginPark), and settles the gate with the outcome. Every park this
// dispatcher makes for a live command outside a unit test goes through here, so the gate always
// knows what is still on its way into the device's backlog.
func (d *Dispatcher) parkTracked(ctx context.Context, s *shardState, key deviceKey, w work, reason string) {
	out := d.park(ctx, w, reason)
	s.settlePark(key, w.env.Token, out, d.clock.Now())
}

// park hands a live command back to command-delivery (SENT -> PARKED) instead of dispatching it,
// and then settles the message. reason says why (see the parkReason constants): its device was
// offline, its shard queue was full, or its gate was up. It runs on a park-pool worker, never on
// the reader goroutine or a shard worker, because it makes a network call. (It runs inline only
// where no term is running: a unit test calling dispatch directly.)
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
// 🔴 BE HONEST ABOUT THE FLOOR: A ROW WHOSE PARK NEVER LANDS IS NOT DELIVERED BY THE DRAIN. SENT
// is invisible to the drain, the sweep and the cancel alike, so once the retry budget is spent —
// MaxDeliver × AckWait, on the order of minutes — the row waits in SENT for command-delivery's
// stranded-SENT pass, which re-arms it to PARKED once its grace has passed. Until then it is
// neither delivered nor cancellable, and the device's gate has stopped waiting for it, so it is
// also one of the two places a device's commands can be delivered out of order. The exposure needs
// a command-delivery outage spanning the whole budget. (The other is a live confirmation whose
// answer was lost after it committed: see settlePark in gate.go.)
func (d *Dispatcher) park(ctx context.Context, w work, reason string) parkOutcome {
	// 🔑 COUNTED WHERE THE MESSAGE SETTLES, NOT ON ENTRY. Counting at the top looks equivalent
	// and is not: a park that errors is retried by redelivery, so one offline command would
	// increment these once per attempt and read as several. The inflation would land during
	// exactly the outages an operator consults them to understand.
	settle := func(parked bool) {
		if reason == parkReasonOffline {
			incr(d.metrics.ServedOffline, 1)
		}
		if parked && d.metrics.OverflowParked != nil {
			d.metrics.OverflowParked.WithLabelValues(reason).Inc()
		}
		ackDrop(w.msg)
	}

	if d.parker == nil || w.env.DispatchNonce == "" {
		incr(d.metrics.ParkSkipped, 1)
		log.Debug().Str("tenant", w.tenant).Str("command", w.env.Token).
			Bool("parkerWired", d.parker != nil).
			Msg("Not parking an undeliverable LwM2M command; it stays SENT and rides its TTL.")
		settle(false)
		return parkSkipped
	}

	parked, err := d.parker.Park(ctx, w.tenant, w.env.Token, w.env.DispatchNonce)
	if err != nil {
		if ctx.Err() != nil {
			return parkEvicted // evicted mid-park: leave unacked so the next leader redelivers it
		}
		incr(d.metrics.ParkErrors, 1)
		log.Debug().Err(err).Str("tenant", w.tenant).Str("command", w.env.Token).
			Msg("Could not park an undeliverable LwM2M command; leaving it unacked to retry on redelivery.")
		return parkErrored
	}
	if !parked {
		// The command moved on under us — answered, cancelled, expired, or re-claimed by a
		// wake drain. Settled, not failed.
		incr(d.metrics.ParkSettled, 1)
	}
	settle(parked)
	return parkDone
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
