// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package downlink is the Sparkplug command dispatcher (G19 S3): the leader-only consumer of
// command-delivery's device-commands stream that turns a command for a Sparkplug device into an
// NCMD or DCMD on the Host Application session the host package already holds with the customer's
// broker.
//
// 🔴 IT PUBLISHES COMMANDS, AND IT DOES NOT CLAIM THEY WORKED. Sparkplug has no command
// acknowledgement. A published command leaves its row SENT, and is settled afterwards by what the
// node REPORTS (see confirm.go): a later report of the requested value confirms it, the end of
// the confirmation window leaves it UNANSWERED, and the awaiting set is re-armed from the KV
// bucket across a change of leader. "Confirmed" means the node reported the value; it does not
// mean the node obeyed. A command is also settled without a report when its outcome is already
// known: refused before the wire (UNSENT), replaced by a newer command for the same metric, or
// followed at once by the broker dropping the session (UNANSWERED).
//
// The gate order is the contract (see live, drain and the numbered comments): the first gate that
// matches decides, and the order is what keeps a refusal honest. A command that is not this
// service's is dropped without a word; one that is but cannot be sent now is parked, not failed;
// one that must not be sent is answered UNSENT; and the claim that takes a command out of the
// delivery sweep's reach is taken only after every refusal that does not need it and after the
// rate limit, so a command stuck waiting for its turn is never claimed and then forgotten.
package downlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/transport"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// The command keys this service executes.
const (
	CommandWrite   = "sparkplug.write"
	CommandRebirth = "sparkplug.rebirth"
	CommandReboot  = "sparkplug.reboot"

	nodeControlReboot = "Node Control/Reboot"
)

// Results of sparkplug_commands_total.
const (
	ResultPublished          = "published"
	ResultConfirmed          = "confirmed"
	ResultConfirmedUnchanged = "confirmed_unchanged"
	ResultUnconfirmed        = "unconfirmed"
	ResultSuperseded         = "superseded"
	ResultRearmed            = "rearmed"
	ResultParked             = "parked"
	ResultDrained            = "drained"
	ResultRefusedDisabled    = "refused_disabled"
	ResultRefusedBreaker     = "refused_breaker"
	ResultRefusedExpired     = "refused_expired"
	ResultRefusedPayload     = "refused_payload"
	ResultRefusedNotAllowed  = "refused_not_allowed"
	ResultRefusedMetric      = "refused_metric"
	ResultRefusedDatatype    = "refused_datatype"
	ResultRefusedRebootGate  = "refused_reboot_gate"
	ResultNotServed          = "not_served"
	ResultPoison             = "poison"
	ResultClaimLost          = "claim_lost"
	ResultKVFailed           = "kv_failed"
	ResultResponseFailed     = "response_failed"
	// The three below are not in the original vocabulary: each is a failure the others would
	// have hidden. A claim, a park or a publish that errors is a fault of a different kind from a
	// lost claim or a refusal, and one counter mixing them could not be read in an outage.
	ResultClaimFailed   = "claim_failed"
	ResultParkFailed    = "park_failed"
	ResultPublishFailed = "publish_failed"
	ResultTenantGone    = "tenant_gone"
)

// Results lists every value of the result label, so the series exist at zero before the first
// command (a rate over an absent series reads as no data, not as none).
var Results = []string{
	ResultPublished, ResultConfirmed, ResultConfirmedUnchanged, ResultUnconfirmed, ResultSuperseded,
	ResultRearmed, ResultParked, ResultDrained, ResultRefusedDisabled, ResultRefusedBreaker,
	ResultRefusedExpired, ResultRefusedPayload, ResultRefusedNotAllowed, ResultRefusedMetric,
	ResultRefusedDatatype, ResultRefusedRebootGate, ResultNotServed, ResultPoison, ResultClaimLost,
	ResultKVFailed, ResultResponseFailed, ResultClaimFailed, ResultParkFailed, ResultPublishFailed,
	ResultTenantGone,
}

// Messages the operator reads on a refused command.
const (
	msgDisabledTenant = "COMMAND_TRANSPORT_UNSUPPORTED: commands are not enabled for this tenant's Sparkplug sources"
	msgDisabledSource = "COMMAND_TRANSPORT_UNSUPPORTED: commands are not enabled for the Sparkplug source this device is on"
	msgBreaker        = "the customer broker refused the command publish; commands to this source are paused"
	msgTripped        = "broker disconnected immediately after the publish; likely refused"
	msgIndeterminate  = "the publish did not complete; it may have reached the node"
	msgRebootOffline  = "a reboot is sent only to a node that is online when it is dispatched — re-issue it when the node is online"
)

const (
	// DefaultWorkers is the per-device-sharded concurrency. A device's commands hash to one worker
	// so they run in stream order; distinct devices spread across workers.
	DefaultWorkers = 8
	// workerQueueDepth buffers each shard's live queue. A full queue holds the single reader, which
	// is the backpressure this service has: the stream keeps what it has not been given.
	workerQueueDepth = 8
	// drainBatch is how many backlogged commands one drain fetch asks for.
	drainBatch = 4
	// drainRetryDelay defers a device's next drain turn after a claim or fetch fault.
	drainRetryDelay = 5 * time.Second
	// defaultProgressEvery is how often a message waiting for its rate token is told to the broker
	// as still in progress. It sits well inside the durable's ack wait.
	defaultProgressEvery = 15 * time.Second
	// responseAttempts and responseBackoff bound the local retry of a command response.
	responseAttempts = 3
	responseBackoff  = 250 * time.Millisecond
	// bgTimeout bounds the best-effort calls made after a term has ended (parking a command back,
	// deleting its entry), which must not run on the dead term's context.
	bgTimeout = 5 * time.Second
	// tripWindow is how far back from a lost session a published command is suspected of having
	// caused it. It is the breaker's watch.
	tripWindow = 5 * time.Second
)

// Source is the slice of a host.Client the dispatcher drives. *host.Client satisfies it; the
// dispatcher depends on the interface so every gate can be exercised without a broker.
type Source interface {
	Tenant() string
	Commands() config.SourceCommands
	Locate(token string) (host.CommandTarget, bool)
	LookupMetric(target host.CommandTarget, name string) (host.MetricRef, error)
	PublishMetric(ctx context.Context, ref host.MetricRef, value any, beforeWire func(host.CommandWire) error) (host.CommandWire, error)
	PublishRebirth(ctx context.Context, target host.CommandTarget, beforeWire func(host.CommandWire) error) (host.CommandWire, error)
	BreakerState() host.BreakerState
	SessionUp() bool
	SetCommandHooks(h host.CommandHooks)
	// LastReported is the value the node last reported for the metric ref was read from, on that
	// same table; it answers false once the table has been replaced or has ended.
	LastReported(ref host.MetricRef) (host.WireValue, time.Time, bool)
	// SourceID names the source (its ingest identity), which an awaiting entry records so a
	// report is matched only against the commands published on the same broker.
	SourceID() string
}

// Reader is the durable device-commands consumer (a messaging reader built with
// ReaderWithDeliverNew, so a fresh durable never replays the stream's retained history into live
// actuations).
type Reader interface {
	ReadMessage(ctx context.Context) (messaging.Message, error)
	HandleResponse(err error)
}

// Fetcher reads a device's backlogged commands (cmddownlink.Fetcher).
type Fetcher interface {
	Pending(ctx context.Context, tenant, deviceToken string, limit int) ([]cmddownlink.DrainCommand, error)
}

// Claimer takes a command out of the delivery sweep's reach (cmddownlink.Claimer).
type Claimer interface {
	Claim(ctx context.Context, tenant, commandToken string) (string, bool, error)
	ClaimDispatch(ctx context.Context, tenant, commandToken, dispatchNonce string) (string, bool, error)
}

// Parker hands a command back to command-delivery (cmddownlink.Parker).
type Parker interface {
	Park(ctx context.Context, tenant, commandToken, dispatchNonce string) (bool, error)
}

// Metrics are the dispatcher's instruments. Nil fields are skipped.
type Metrics struct {
	// Commands is sparkplug_commands_total{result}.
	Commands *prometheus.CounterVec
	// Awaiting is sparkplug_commands_awaiting: entries in the awaiting set.
	Awaiting prometheus.Gauge
	// ConfirmSeconds is sparkplug_command_confirm_seconds: the time from the publish to the report
	// that confirmed a command.
	ConfirmSeconds prometheus.Observer
}

// Options configure a Dispatcher. Zero values take the defaults.
type Options struct {
	Workers int
	// ReadPacer bounds a run of failing reads and ends the process once they stop looking
	// transient. Required whenever a reader is supplied.
	ReadPacer *core.ReadPacer
	// TenantDeleted reports whether a tenant has been through the delete door; nil means never.
	TenantDeleted func(tenant string) bool
	// Now is the clock; time.Now outside tests.
	Now func() time.Time
	// ProgressEvery is how often a waiting message is reported as in progress.
	ProgressEvery time.Duration
	// SweepEvery is how often the awaiting set is looked at for windows that have ended.
	SweepEvery time.Duration
}

type deviceKey struct{ tenant, device string }

type nodeKey struct{ tenant, group, node string }

// Dispatcher consumes device-commands and publishes the commands that are Sparkplug's.
type Dispatcher struct {
	reader    Reader
	responses cmddownlink.ResponseWriter
	fetcher   Fetcher
	claimer   Claimer
	parker    Parker
	store     AwaitingStore
	metrics   Metrics
	pacer     *core.ReadPacer

	tenantDeleted func(string) bool
	now           func() time.Time
	progressEvery time.Duration
	sweepEvery    time.Duration
	workers       int

	byTenant map[string][]Source
	limiters map[Source]*bucket

	// shardsPtr publishes the current term's shards so a wake from a receive goroutine can reach
	// them; nil whenever no term is running, which makes a wake on a standby a safe no-op.
	shardsPtr atomic.Pointer[[]*shard]

	// mu guards the in-memory state below, all of it per term (reset by Run).
	mu sync.Mutex
	// entries is the in-memory mirror of the awaiting set (see confirm.go), by KV key; byTarget
	// finds the entries a report can settle, and byMetric the write waiting on each (device,
	// metric), so a newer command for the same metric can supersede it. nEntries lets the receive
	// path skip the lock when nothing is awaited.
	entries  map[string]*entry
	byTarget map[targetKey][]*entry
	byMetric map[awaitKey]*entry
	nEntries atomic.Int64
	// unseeded is set from the start of a term until the previous term's state (the reboot marks
	// and the awaiting entries) has been read from the bucket.
	unseeded atomic.Bool
	// settledUnseeded are the keys settled while unseeded; see addEntryIfAbsent.
	settledUnseeded map[string]struct{}
	// recent is what each source published in the last tripWindow, for the breaker.
	recent map[Source][]recentPub
	// reboots is when each node was last rebooted by this term.
	reboots map[nodeKey]time.Time
	// parkedNotBorn are the devices a command was parked for because their table was not yet there,
	// so their next birth wakes a drain even though their token is already in the index.
	parkedNotBorn map[deviceKey]struct{}
	// timers are the drain retries waiting to fire, stopped when the term ends; bg tracks the
	// goroutines the breaker's report starts, which Run waits for. termOpen gates their start so a
	// report arriving as the term ends does not Add to a WaitGroup that is being waited on.
	timers   map[*time.Timer]struct{}
	bg       sync.WaitGroup
	termOpen bool
}

type awaitKey struct {
	tenant, device, metric string
}

type recentPub struct {
	tenant, device, command, nonce string
	at                             time.Time
}

// New builds a Dispatcher over the sources it may publish to. reader may be nil only for tests
// that drive the gates directly. The claimer, parker, responder, store and fetcher are required:
// a dispatcher without a claimer would publish unclaimed commands, which is a second actuation
// waiting to happen, so a missing one panics rather than degrading.
func New(rdr Reader, responses cmddownlink.ResponseWriter, fetcher Fetcher, claimer Claimer, parker Parker,
	store AwaitingStore, sources []Source, metrics Metrics, opts Options) *Dispatcher {
	if claimer == nil || parker == nil || responses == nil || store == nil || fetcher == nil {
		panic("downlink.New: the claimer, parker, response writer, awaiting store and fetcher are all required; " +
			"a dispatcher missing one would publish commands it could not claim, settle or record")
	}
	if rdr != nil && opts.ReadPacer == nil {
		panic("downlink.New: a dispatcher with a reader needs a read pacer; without one a broken consumer " +
			"is retried forever behind a pod that reports leader and serving")
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.TenantDeleted == nil {
		opts.TenantDeleted = func(string) bool { return false }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = defaultProgressEvery
	}
	if opts.SweepEvery <= 0 {
		opts.SweepEvery = defaultSweepEvery
	}
	d := &Dispatcher{
		reader: rdr, responses: responses, fetcher: fetcher, claimer: claimer, parker: parker, store: store,
		metrics: metrics, pacer: opts.ReadPacer, tenantDeleted: opts.TenantDeleted, now: opts.Now,
		progressEvery: opts.ProgressEvery, sweepEvery: opts.SweepEvery, workers: opts.Workers,
		byTenant: map[string][]Source{}, limiters: map[Source]*bucket{},
	}
	for _, s := range sources {
		d.byTenant[s.Tenant()] = append(d.byTenant[s.Tenant()], s)
		cfg := s.Commands()
		d.limiters[s] = newBucket(cfg.PublishPerSecond, cfg.Burst, d.now())
		src := s
		s.SetCommandHooks(host.CommandHooks{
			Born:     d.OnBorn,
			Tripped:  func(at time.Time) { d.onTripped(src, at) },
			Reported: func(tenant string, rs []host.Report) { d.onReports(src, tenant, rs) },
		})
	}
	d.resetTerm()
	return d
}

func (d *Dispatcher) resetTerm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = map[string]*entry{}
	d.byTarget = map[targetKey][]*entry{}
	d.byMetric = map[awaitKey]*entry{}
	d.settledUnseeded = map[string]struct{}{}
	d.nEntries.Store(0)
	if d.metrics.Awaiting != nil {
		d.metrics.Awaiting.Set(0)
	}
	d.recent = map[Source][]recentPub{}
	d.reboots = map[nodeKey]time.Time{}
	d.parkedNotBorn = map[deviceKey]struct{}{}
	d.timers = map[*time.Timer]struct{}{}
	d.termOpen = true
}

func (d *Dispatcher) count(result string) {
	if d.metrics.Commands != nil {
		d.metrics.Commands.WithLabelValues(result).Inc()
	}
}

// ---- the term ----

// shard is one worker's queue: live commands in stream order, and the devices a wake asked it to
// drain.
type shard struct {
	ch    chan work
	mu    sync.Mutex
	wakes map[deviceKey]struct{}
	nudge chan struct{}
}

func newShard() *shard {
	return &shard{ch: make(chan work, workerQueueDepth), wakes: map[deviceKey]struct{}{}, nudge: make(chan struct{}, 1)}
}

func (s *shard) wake(k deviceKey) {
	s.mu.Lock()
	s.wakes[k] = struct{}{}
	s.mu.Unlock()
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// takeWake removes and reports a pending wake for one device.
func (s *shard) takeWake(k deviceKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.wakes[k]
	delete(s.wakes, k)
	return ok
}

func (s *shard) takeWakes() []deviceKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.wakes) == 0 {
		return nil
	}
	out := make([]deviceKey, 0, len(s.wakes))
	for k := range s.wakes {
		out = append(out, k)
	}
	s.wakes = map[deviceKey]struct{}{}
	return out
}

// work is one parsed live command.
type work struct {
	msg    messaging.Message
	tenant string
	env    cmddownlink.DeliveryEnvelope
}

func (d *Dispatcher) shardIndex(device string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(device))
	return int(h.Sum32() % uint32(d.workers))
}

// Run consumes and dispatches until ctx ends (leadership eviction or shutdown). ready, when not
// nil, is called once the term's shards are published and the workers started: the leader waits
// for it BEFORE connecting its sources, so the first birth's wake finds a shard to land on.
//
// The reader honours ctx, so an evicted replica stops pulling promptly. The durable is bound, not
// owned: its cursor survives the term and the next leader resumes from the last ack. Whatever this
// term had read and not acked redelivers to it.
func (d *Dispatcher) Run(ctx context.Context, ready func()) {
	d.resetTerm()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	var wg sync.WaitGroup
	// Before anything is served: pick up what the previous term left awaiting an outcome, and the
	// record of the reboots it sent.
	d.seedStart(runCtx, func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	})
	wg.Add(1)
	go func() { defer wg.Done(); d.sweepLoop(runCtx) }()

	shards := make([]*shard, d.workers)
	for i := range shards {
		shards[i] = newShard()
		wg.Add(1)
		go func(s *shard) {
			defer wg.Done()
			d.serve(runCtx, s)
		}(shards[i])
	}
	d.shardsPtr.Store(&shards)
	defer d.shardsPtr.Store(nil)
	if ready != nil {
		ready()
	}

	if d.reader != nil {
		messaging.RunConsumer(runCtx, d.reader, d.pacer, func(msg messaging.Message) bool {
			return d.route(runCtx, shards, msg)
		})
	} else {
		<-runCtx.Done()
	}
	stop()
	wg.Wait()
	d.endTerm()
}

// endTerm stops what the term left behind: pending drain retries, and the goroutines resolving
// commands after a breaker trip, which are waited for (they are bounded by the response retries).
func (d *Dispatcher) endTerm() {
	d.mu.Lock()
	d.termOpen = false
	for t := range d.timers {
		t.Stop()
	}
	d.timers = map[*time.Timer]struct{}{}
	d.mu.Unlock()
	d.bg.Wait()
}

// serve is one worker: a live command if there is one, otherwise a wake, otherwise wait.
func (d *Dispatcher) serve(ctx context.Context, s *shard) {
	for {
		select {
		case <-ctx.Done():
			return
		case w := <-s.ch:
			// A wake already waiting for this device is served FIRST. A live command that arrived
			// after older rows were parked for the device must not overtake them: the older rows are
			// the device's to send before it.
			if s.takeWake(deviceKey{w.tenant, w.env.DeviceToken}) {
				d.drain(ctx, deviceKey{w.tenant, w.env.DeviceToken})
				if ctx.Err() != nil {
					return
				}
			}
			d.live(ctx, w)
		case <-s.nudge:
			for _, k := range s.takeWakes() {
				if ctx.Err() != nil {
					return
				}
				d.drain(ctx, k)
			}
		}
	}
}

// OnBorn is a source's callback when a node or device is announced: it wakes the device's drain
// when the token is new to the source's index, or when a command was parked for it because its
// BIRTH table was not there yet. Called on the receive goroutine; it never blocks.
func (d *Dispatcher) OnBorn(tenant, token string, added bool) {
	k := deviceKey{tenant, token}
	if !added {
		d.mu.Lock()
		_, parked := d.parkedNotBorn[k]
		delete(d.parkedNotBorn, k)
		d.mu.Unlock()
		if !parked {
			return
		}
	}
	d.Drain(tenant, token)
}

// Drain asks the device's shard to pull its backlogged commands and dispatch them. It is
// non-blocking and never dropped, and a no-op on a standby (no term, no shards).
func (d *Dispatcher) Drain(tenant, token string) {
	p := d.shardsPtr.Load()
	if p == nil {
		return
	}
	(*p)[d.shardIndex(token)].wake(deviceKey{tenant, token})
}

// ---- gates 1-3: the reader ----

// route parses a message and either drops it or queues it on its device's shard. It reports
// whether the read loop should carry on.
func (d *Dispatcher) route(ctx context.Context, shards []*shard, msg messaging.Message) bool {
	// 1. Parse the subject's tenant and the envelope. Poison is acked and counted.
	_, tenant, ok := messaging.TenantContextFromSubject(context.Background(), msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping a command with no parseable tenant in its subject.")
		d.count(ResultPoison)
		d.ack(msg)
		return true
	}
	var env cmddownlink.DeliveryEnvelope
	if err := json.Unmarshal(msg.Value, &env); err != nil || env.Token == "" || env.DeviceToken == "" {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Dropping an undecodable command envelope.")
		d.count(ResultPoison)
		d.ack(msg)
		return true
	}
	// 2. A deleted tenant's command is not actuated, and not retried: the tenant is not coming back.
	if d.tenantDeleted(tenant) {
		d.count(ResultTenantGone)
		d.ack(msg)
		return true
	}
	// 3. Transport. The dominant case on a shared stream is another protocol's command, and it costs
	// nothing. The header is for routing only: it authorizes nothing, and an ABSENT header means
	// unknown rather than "not mine", so a command without one is served only if one of this
	// tenant's sources already has the device on its session.
	if t := msg.Headers[cmddownlink.HeaderTransport]; t != "" && t != string(transport.Sparkplug) {
		d.count(ResultNotServed)
		d.ack(msg)
		return true
	} else if t == "" && !d.knows(tenant, env.DeviceToken) {
		d.count(ResultNotServed)
		d.ack(msg)
		return true
	}
	w := work{msg: msg, tenant: tenant, env: env}
	select {
	case shards[d.shardIndex(env.DeviceToken)].ch <- w:
		return true
	case <-ctx.Done():
		return false // left unacked: it redelivers to the next leader
	}
}

func (d *Dispatcher) knows(tenant, token string) bool {
	for _, s := range d.byTenant[tenant] {
		if _, ok := s.Locate(token); ok {
			return true
		}
	}
	return false
}

func (d *Dispatcher) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Debug().Err(err).Msg("Failed to ack a command message (it will redeliver, bounded by MaxDeliver).")
	}
}

// ---- the job ----

// job is one command being worked, whether it arrived live or was drained from the backlog.
type job struct {
	tenant  string
	device  string
	token   string
	name    string
	payload []byte
	impact  string
	expires time.Time

	// nonce is the dispatch a response quotes: the envelope's until the claim, the claim's after.
	nonce   string
	claimed bool
	msg     *messaging.Message // nil for a drained command: its fate rides its command-delivery row

	src    Source
	target host.CommandTarget

	// parkedBack is set once the row has been handed back to command-delivery (or the attempt was
	// made): a drain must stop there, or it would claim the same row again at once.
	parkedBack bool
}

func (j *job) settle(d *Dispatcher) {
	if j.msg != nil {
		d.ack(*j.msg)
	}
}

type refusal struct {
	result string
	reason Reason
	text   string
}

// plan is what validation decided to send.
type plan struct {
	kind      string
	target    host.CommandTarget
	ref       host.MetricRef
	metric    string
	value     any
	requested json.RawMessage
}

// ---- the live path: gates 4-15 ----

func (d *Dispatcher) live(ctx context.Context, w work) {
	if ctx.Err() != nil {
		return // evicted before it started: unacked, redelivered to the next leader
	}
	j := &job{
		tenant: w.tenant, device: w.env.DeviceToken, token: w.env.Token, name: w.env.Name,
		impact: w.msg.Headers[cmddownlink.HeaderCommandImpact], nonce: w.env.DispatchNonce, msg: &w.msg,
	}
	if w.env.Payload != nil {
		j.payload = *w.env.Payload
	}
	if h := w.msg.Headers[cmddownlink.HeaderCommandExpires]; h != "" {
		if t, err := time.Parse(time.RFC3339, h); err == nil {
			j.expires = t
		}
	}

	// 4. Enabled for the tenant. This is authoritative because the one leader serves EVERY source,
	// and it closes the case of a Sparkplug command that would otherwise be ack-dropped with its
	// row left SENT.
	enabled, has := d.sourcesOf(w.tenant)
	if !has {
		d.refuseLive(j, ResultRefusedDisabled, ReasonCommandsNotEnabled, msgDisabledTenant)
		return
	}
	// 5. Locate. A token on an enabled source continues; one only on a disabled source is refused;
	// one on none is parked, because it is not born on our session (a death, a session that is
	// down, or a new term whose rebirths have not landed). Validation waits for the drain: there is
	// no BIRTH table to check against yet.
	src, target, found := locate(enabled, j.device)
	if !found {
		if d.onDisabledSource(w.tenant, j.device) {
			d.refuseLive(j, ResultRefusedDisabled, ReasonCommandsNotEnabled, msgDisabledSource)
			return
		}
		if j.name == CommandReboot {
			d.refuseLive(j, ResultRefusedRebootGate, ReasonNodeOffline, msgRebootOffline)
			return
		}
		d.parkLive(ctx, j, "not born on an enabled source")
		return
	}
	j.src, j.target = src, target

	// 6. Breaker.
	if src.BreakerState() == host.BreakerOpen {
		d.refuseLive(j, ResultRefusedBreaker, ReasonBrokerRefused, msgBreaker)
		return
	}
	// 7. Expiry.
	if d.expired(j) {
		d.refuseLive(j, ResultRefusedExpired, ReasonExpired, "the command expired before it could be sent")
		return
	}
	// 8. Validate.
	p, ref, park := d.validate(j)
	if park {
		if j.name == CommandReboot {
			d.refuseLive(j, ResultRefusedRebootGate, ReasonNodeOffline, msgRebootOffline)
			return
		}
		d.mu.Lock()
		d.parkedNotBorn[deviceKey{j.tenant, j.device}] = struct{}{}
		d.mu.Unlock()
		d.parkLive(ctx, j, "the target's BIRTH table is not available yet")
		return
	}
	if ref != nil {
		d.refuseLive(j, ref.result, ref.reason, ref.text)
		return
	}
	// 9. Rate, BEFORE any claim. A claimed command that waits is a stalled row, and a stalled
	// stream message that is not reported as in progress is redelivered into a duplicate.
	switch d.awaitRate(ctx, j) {
	case waitAbandoned:
		return
	case waitExpired:
		d.refuseLive(j, ResultRefusedExpired, ReasonExpired, "the command expired while it waited for its turn")
		return
	}
	// 10. Confirm the claim. It returns the CURRENT nonce; a lost claim means the dispatch this
	// envelope names is gone (re-armed, answered, cancelled, expired), and nothing is sent.
	nonce, won, err := d.claimer.ClaimDispatch(ctx, j.tenant, j.token, j.nonce)
	if err != nil {
		if ctx.Err() == nil {
			d.count(ResultClaimFailed)
			log.Debug().Err(err).Str("tenant", j.tenant).Str("command", j.token).
				Msg("Could not confirm a live command's dispatch; not publishing it, leaving it unacked to retry on redelivery.")
		}
		return // fail closed, unacked
	}
	if !won {
		d.count(ResultClaimLost)
		d.ack(w.msg)
		d.Drain(j.tenant, j.device) // the usual cause is a re-arm to PARKED, which waits for a wake
		return
	}
	j.nonce, j.claimed = nonce, true
	d.send(ctx, j, p)
}

// refuseLive answers a live command UNSENT and acks it, unless the answer could not be published
// (nothing was sealed, so it is left to redeliver and be refused again).
func (d *Dispatcher) refuseLive(j *job, result string, reason Reason, text string) {
	d.count(result)
	if err := d.respondRefused(j, reason, text); err != nil {
		return
	}
	j.settle(d)
}

// parkLive hands a live, unclaimed command back to command-delivery (SENT -> PARKED) naming the
// envelope's dispatch, then acks. An error leaves it unacked: the park is retried on redelivery.
func (d *Dispatcher) parkLive(ctx context.Context, j *job, why string) {
	if j.nonce == "" {
		// Nothing to name: a park on status alone could hand back a row that has since been
		// re-claimed and actuated. It is answered UNSENT rather than acked in silence, so the
		// operator is told it was not delivered and can re-issue it.
		log.Warn().Str("tenant", j.tenant).Str("command", j.token).Msg("Not parking a command whose delivery names no dispatch.")
		d.count(ResultParkFailed)
		_ = d.respondRefused(j, ReasonNoDispatch, "the delivery names no dispatch, so the command cannot be parked or confirmed; re-issue it")
		j.settle(d)
		return
	}
	parked, err := d.parker.Park(ctx, j.tenant, j.token, j.nonce)
	if err != nil {
		if ctx.Err() == nil {
			d.count(ResultParkFailed)
			log.Debug().Err(err).Str("tenant", j.tenant).Str("command", j.token).
				Msg("Could not park a command; leaving it unacked to retry on redelivery.")
		}
		return
	}
	if parked {
		d.count(ResultParked)
	}
	log.Debug().Str("tenant", j.tenant).Str("command", j.token).Str("why", why).Bool("parked", parked).Msg("Parked a Sparkplug command for the device's next birth.")
	j.settle(d)
}

// ---- the wake drain: gates 6-15 on a claimed row ----

func (d *Dispatcher) drain(ctx context.Context, k deviceKey) {
	if d.tenantDeleted(k.tenant) {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		// Located afresh for every batch: a node that died or rebirthed while a batch was worked
		// is no longer where the last one found it.
		enabled, _ := d.sourcesOf(k.tenant)
		src, target, found := locate(enabled, k.device)
		if !found {
			return // not born on an enabled source: its next birth wakes it again
		}
		cmds, err := d.fetcher.Pending(ctx, k.tenant, k.device, drainBatch)
		if err != nil {
			if ctx.Err() == nil {
				log.Debug().Err(err).Str("tenant", k.tenant).Str("device", k.device).Msg("Could not fetch backlogged Sparkplug commands; retrying shortly.")
				d.retryDrain(ctx, k)
			}
			return
		}
		if len(cmds) == 0 {
			return
		}
		won := 0
		for _, c := range cmds {
			if ctx.Err() != nil {
				return
			}
			j := &job{
				tenant: k.tenant, device: k.device, token: c.Token, name: c.Name, payload: c.Payload,
				expires: c.ExpiresAt, src: src, target: target,
			}
			// 9. Rate, before the claim (there is no stream message to keep alive here).
			switch d.awaitRate(ctx, j) {
			case waitAbandoned:
				return
			case waitExpired:
				// Claimed all the same: the claim is what lets the refusal be answered, and a row the
				// server has already expired simply loses it (counted as a lost claim).
			}
			// 10. Claim. markCommandSent returns the nonce of the dispatch it creates.
			nonce, claimed, err := d.claimer.Claim(ctx, k.tenant, c.Token)
			if err != nil {
				if ctx.Err() == nil {
					d.count(ResultClaimFailed)
					log.Debug().Err(err).Str("tenant", k.tenant).Str("command", c.Token).
						Msg("Could not claim a backlogged command; not publishing it (it stays dispatchable and is retried shortly, in order).")
					d.retryDrain(ctx, k)
				}
				return // stop at the first fault: skipping would send a newer row past an older one
			}
			if !claimed {
				d.count(ResultClaimLost)
				continue
			}
			won++
			d.count(ResultDrained)
			j.nonce, j.claimed = nonce, true
			// A reboot is never parked or carried over a birth: it is refused here, before any other
			// gate, with its own reason.
			if j.name == CommandReboot {
				d.refuseClaimed(j, ResultRefusedRebootGate, ReasonNodeOffline, msgRebootOffline)
				continue
			}
			// 6-8 on the claimed row: the claim is what lets a refusal be answered.
			if src.BreakerState() == host.BreakerOpen {
				d.refuseClaimed(j, ResultRefusedBreaker, ReasonBrokerRefused, msgBreaker)
				continue
			}
			if d.expired(j) {
				d.refuseClaimed(j, ResultRefusedExpired, ReasonExpired, "the command expired before it could be sent")
				continue
			}
			p, ref, park := d.validate(j)
			if park {
				d.parkClaimed(j)
				return // handed back: claiming it again now would only spin; the next birth wakes us
			}
			if ref != nil {
				d.refuseClaimed(j, ref.result, ref.reason, ref.text)
				continue
			}
			d.send(ctx, j, p)
			if j.parkedBack {
				return // the send handed the row back (session down, record not written)
			}
		}
		if won == 0 {
			d.retryDrain(ctx, k) // every row was refused at the claim: back off rather than spin
			return
		}
		if len(cmds) < drainBatch {
			return
		}
	}
}

func (d *Dispatcher) retryDrain(ctx context.Context, k deviceKey) {
	var t *time.Timer
	t = time.AfterFunc(drainRetryDelay, func() {
		d.mu.Lock()
		delete(d.timers, t)
		d.mu.Unlock()
		if ctx.Err() == nil {
			d.Drain(k.tenant, k.device)
		}
	})
	d.mu.Lock()
	d.timers[t] = struct{}{}
	d.mu.Unlock()
}

// refuseClaimed answers a claimed command UNSENT.
func (d *Dispatcher) refuseClaimed(j *job, result string, reason Reason, text string) {
	d.count(result)
	_ = d.respondRefused(j, reason, text)
	j.settle(d)
}

// parkClaimed hands a claimed command back (SENT -> PARKED) naming the claim's nonce, on a context
// of its own: it runs after the term may have ended.
func (d *Dispatcher) parkClaimed(j *job) {
	j.parkedBack = true
	pctx, cancel := context.WithTimeout(context.Background(), bgTimeout)
	defer cancel()
	parked, err := d.parker.Park(pctx, j.tenant, j.token, j.nonce)
	if err != nil {
		d.count(ResultParkFailed)
		log.Warn().Err(err).Str("tenant", j.tenant).Str("command", j.token).
			Msg("Could not hand a claimed command back; the stranded-command pass will re-arm it.")
		return
	}
	if parked {
		d.count(ResultParked)
		d.mu.Lock()
		d.parkedNotBorn[deviceKey{j.tenant, j.device}] = struct{}{}
		d.mu.Unlock()
	}
}

// ---- helpers shared by both paths ----

// sourcesOf returns the tenant's sources with commands enabled and whether there are any.
func (d *Dispatcher) sourcesOf(tenant string) ([]Source, bool) {
	var out []Source
	for _, s := range d.byTenant[tenant] {
		if s.Commands().Enabled {
			out = append(out, s)
		}
	}
	return out, len(out) > 0
}

func locate(srcs []Source, token string) (Source, host.CommandTarget, bool) {
	for _, s := range srcs {
		if t, ok := s.Locate(token); ok {
			return s, t, true
		}
	}
	return nil, host.CommandTarget{}, false
}

// onDisabledSource reports whether one of the tenant's sources with commands OFF has the device on
// its session.
func (d *Dispatcher) onDisabledSource(tenant, token string) bool {
	for _, s := range d.byTenant[tenant] {
		if s.Commands().Enabled {
			continue
		}
		if _, ok := s.Locate(token); ok {
			return true
		}
	}
	return false
}

func (d *Dispatcher) expired(j *job) bool {
	return !j.expires.IsZero() && !d.now().Before(j.expires)
}

type waitResult int

const (
	waitOK waitResult = iota
	waitAbandoned
	waitExpired
)

// awaitRate takes a token from the source's bucket and waits until it is due. While it waits, a
// live command's stream message is reported as in progress every progressEvery so the durable's
// ack wait does not redeliver it, and its expiry and the term are looked at again after every
// slice of the wait.
func (d *Dispatcher) awaitRate(ctx context.Context, j *job) waitResult {
	wait := d.limiters[j.src].reserve(d.now())
	if wait > 0 && j.msg != nil {
		// Reported as soon as it is known to be waiting, not only after the first slice: a wait
		// that is shorter than the progress interval would otherwise never be reported at all.
		if err := j.msg.InProgress(); err != nil {
			log.Debug().Err(err).Msg("Could not mark a waiting command message as in progress.")
		}
	}
	for wait > 0 {
		slice := min(wait, d.progressEvery)
		t := time.NewTimer(slice)
		select {
		case <-ctx.Done():
			t.Stop()
			return waitAbandoned
		case <-t.C:
		}
		wait -= slice
		if j.msg != nil && wait > 0 {
			if err := j.msg.InProgress(); err != nil {
				log.Debug().Err(err).Msg("Could not mark a waiting command message as in progress.")
			}
		}
		if d.expired(j) {
			return waitExpired
		}
	}
	if ctx.Err() != nil {
		return waitAbandoned
	}
	if d.expired(j) {
		return waitExpired
	}
	return waitOK
}

// respond publishes a command's outcome to command-responses. A failure is counted; the caller
// decides whether it leaves a message to redeliver.
func (d *Dispatcher) respond(j *job, success bool, reach, text string) error {
	env := cmddownlink.ResponseEnvelope{CommandToken: j.token, Success: success, Reach: reach, DispatchNonce: j.nonce}
	if text != "" {
		env.Error = &text
	}
	return d.publishResponse(j.tenant, j.device, env)
}

// respondRefused answers a command UNSENT and carries the machine-readable reason in the stored
// response, in the same {"reason": ...} document an unanswered command uses, so a client never has
// to read the error text. The text stays in the error column.
func (d *Dispatcher) respondRefused(j *job, reason Reason, text string) error {
	env := cmddownlink.ResponseEnvelope{CommandToken: j.token, Success: false, Reach: cmddownlink.ReachUnsent, DispatchNonce: j.nonce}
	if text != "" {
		env.Error = &text
	}
	if reason != "" {
		p := `{"reason":"` + string(reason) + `"}`
		env.Payload = &p
	}
	return d.publishResponse(j.tenant, j.device, env)
}

func (d *Dispatcher) publishResponse(tenant, device string, env cmddownlink.ResponseEnvelope) error {
	if err := cmddownlink.PublishResponse(d.responses, tenant, device, env, responseAttempts, responseBackoff); err != nil {
		d.count(ResultResponseFailed)
		log.Warn().Err(err).Str("tenant", tenant).Str("command", env.CommandToken).
			Msg("Could not publish a Sparkplug command response after local retries; the command will TIMEOUT.")
		return err
	}
	return nil
}

// ---- gate 8: validation ----

type writeParams struct {
	Metric *string         `json:"metric"`
	Value  json.RawMessage `json:"value"`
}

func trimJSON(b []byte) []byte { return bytes.TrimSpace(b) }

// endOfJSON reports whether nothing but whitespace follows what the decoder has read.
func endOfJSON(dec *json.Decoder) bool {
	_, err := dec.Token()
	return err == io.EOF
}

// validate decides whether a command may be sent and what exactly would go out. It returns a plan,
// or a refusal, or park=true when the only thing missing is a BIRTH table that a later birth will
// bring.
func (d *Dispatcher) validate(j *job) (plan, *refusal, bool) {
	cfg := j.src.Commands()
	refuse := func(result string, reason Reason, format string, a ...any) (plan, *refusal, bool) {
		return plan{}, &refusal{result: result, reason: reason, text: fmt.Sprintf(format, a...)}, false
	}
	switch j.name {
	case CommandRebirth:
		if err := noParams(j.payload); err != nil {
			return refuse(ResultRefusedPayload, ReasonInvalidParameters, "%s takes no parameters: %v", CommandRebirth, err)
		}
		return plan{kind: KindRebirth, target: host.CommandTarget{Group: j.target.Group, Node: j.target.Node}}, nil, false

	case CommandReboot:
		if err := noParams(j.payload); err != nil {
			return refuse(ResultRefusedPayload, ReasonInvalidParameters, "%s takes no parameters: %v", CommandReboot, err)
		}
		// All four must hold, and the order is the order an operator can act on them.
		if j.impact != "DISRUPTIVE" && j.impact != "DESTRUCTIVE" {
			return refuse(ResultRefusedRebootGate, ReasonRebootImpact, "a reboot needs the command to be declared DISRUPTIVE or DESTRUCTIVE on the device profile; its impact is %s", orUnknown(j.impact))
		}
		if !cfg.AllowReboot {
			return refuse(ResultRefusedRebootGate, ReasonRebootNotAllowed, "reboot is not allowed on this Sparkplug source (commands.allowReboot is off)")
		}
		if j.target.IsDevice() {
			return refuse(ResultRefusedRebootGate, ReasonRebootNotANode, "a reboot is addressed to an edge node, and %s is a device under one", j.target)
		}
		ref, err := j.src.LookupMetric(j.target, nodeControlReboot)
		switch {
		case errors.Is(err, host.ErrNotBorn):
			return plan{}, nil, true
		case err != nil:
			return refuse(ResultRefusedRebootGate, ReasonRebootNotDeclared, "the node %s did not declare %s in its NBIRTH", j.target, nodeControlReboot)
		}
		if ref.Datatype != 11 { // Boolean
			return refuse(ResultRefusedRebootGate, ReasonRebootNotBoolean, "%s is declared with datatype %d, not Boolean", nodeControlReboot, ref.Datatype)
		}
		if d.unseeded.Load() {
			return refuse(ResultRefusedRebootGate, ReasonRebootUnverified, "the record of recent reboots could not be read yet, so the one-reboot-per-window rule cannot be applied; re-issue the reboot shortly")
		}
		if last, ok := d.lastReboot(j, cfg); ok {
			return refuse(ResultRefusedRebootGate, ReasonRebootRateLimited, "node %s was already rebooted %s ago; at most one reboot per %s", j.target,
				d.now().Sub(last).Round(time.Second), cfg.RebootWindow())
		}
		return plan{kind: KindReboot, target: j.target, ref: ref, metric: nodeControlReboot, value: true}, nil, false

	case CommandWrite:
		var p writeParams
		dec := json.NewDecoder(bytes.NewReader(trimJSON(j.payload)))
		dec.DisallowUnknownFields()
		err := dec.Decode(&p)
		if err == nil && !endOfJSON(dec) {
			err = errors.New("unexpected data after the parameters")
		}
		if err != nil || p.Metric == nil || *p.Metric == "" || len(p.Value) == 0 {
			if err == nil {
				err = errors.New("both \"metric\" (a name) and \"value\" are required")
			}
			return refuse(ResultRefusedPayload, ReasonInvalidParameters, "%s needs {\"metric\": name, \"value\": v}: %v", CommandWrite, err)
		}
		vdec := json.NewDecoder(bytes.NewReader(p.Value))
		vdec.UseNumber()
		var value any
		if err := vdec.Decode(&value); err != nil || value == nil || !endOfJSON(vdec) {
			return refuse(ResultRefusedPayload, ReasonInvalidParameters, "the value for %q must be a boolean, a number or a string, got %s", *p.Metric, strings.TrimSpace(string(p.Value)))
		}
		name := *p.Metric
		if config.ReservedCommandMetric(name) {
			return refuse(ResultRefusedNotAllowed, ReasonMetricProtocolControl, "metric %q is a protocol control metric and cannot be written; Rebirth and Reboot have commands of their own", name)
		}
		if !cfg.WritableMetric(name) {
			return refuse(ResultRefusedNotAllowed, ReasonMetricNotWritable, "metric %q is not on this source's writable-metric allowlist (commands.writableMetrics)", name)
		}
		ref, err := j.src.LookupMetric(j.target, name)
		switch {
		case errors.Is(err, host.ErrNotBorn):
			return plan{}, nil, true
		case err != nil:
			return refuse(ResultRefusedMetric, ReasonMetricNotDeclared, "metric %q is not declared in the BIRTH of %s", name, j.target)
		}
		if _, err := host.EncodeMetricValue(ref.Datatype, value); err != nil {
			if errors.Is(err, host.ErrUnsupportedDatatype) {
				return refuse(ResultRefusedDatatype, ReasonDatatypeUnsupported, "metric %q has datatype %d in the BIRTH of %s, which commands do not carry in this release", name, ref.Datatype, j.target)
			}
			return refuse(ResultRefusedPayload, ReasonValueNotAccepted, "metric %q is declared datatype %d in the BIRTH of %s and cannot take %s: %v", name, ref.Datatype, j.target, strings.TrimSpace(string(p.Value)), err)
		}
		return plan{kind: KindWrite, target: j.target, ref: ref, metric: name, value: value, requested: append(json.RawMessage(nil), p.Value...)}, nil, false
	}
	return refuse(ResultRefusedPayload, ReasonUnknownCommand, "unknown Sparkplug command %q (this service executes %s, %s and %s)", j.name, CommandWrite, CommandRebirth, CommandReboot)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// noParams requires a payload that is absent, null or an empty object.
func noParams(raw []byte) error {
	b := trimJSON(raw)
	if len(b) == 0 || string(b) == "null" || string(b) == "{}" {
		return nil
	}
	return errors.New("unexpected parameters")
}

func (d *Dispatcher) lastReboot(j *job, cfg config.SourceCommands) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.reboots[nodeKey{j.tenant, j.target.Group, j.target.Node}]
	if !ok || d.now().Sub(t) >= cfg.RebootWindow() {
		return time.Time{}, false
	}
	return t, true
}

// ---- gates 11-15: publish ----

// send runs a claimed, validated command to the wire.
func (d *Dispatcher) send(ctx context.Context, j *job, p plan) {
	cfg := j.src.Commands()

	// 11. Supersede: a command for the same (device, metric) that is still awaiting its outcome is
	// resolved now, with its own stored nonce, as unanswered: this newer command replaces it.
	d.supersede(j, p)

	// 12 and 13. The awaiting entry is written inside the publish, after its last re-check and
	// immediately before the bytes go out, so the record describes exactly what was sent.
	key := AwaitingKey(j.tenant, j.token)
	wrote, marked := false, false
	hook := func(w host.CommandWire) error {
		a := Awaiting{
			Command: j.token, Device: j.device, Source: j.src.SourceID(), Target: p.target.String(), Nonce: j.nonce,
			Kind: p.kind, Metric: p.metric, Alias: w.Alias, TableGen: w.Gen, Expires: j.expires,
			Requested: p.requested, Datatype: w.Datatype, SessionEpoch: w.Epoch, BdSeq: w.BdSeq, HasBdSeq: w.HasBdSeq,
			HostRole: cfg.HostRole, AtRequestedValue: w.AtRequestedValue, LastReportedAt: w.LastReportedAt,
			PublishedAt: w.PublishedAt, Wire: awaitingWire(w),
		}
		a.Deadline = w.PublishedAt.Add(d.windowOf(cfg, p.kind))
		if !j.expires.IsZero() && j.expires.Before(a.Deadline) {
			a.Deadline = j.expires
		}
		if err := d.store.Put(ctx, key, a); err != nil {
			d.count(ResultKVFailed)
			log.Warn().Err(err).Str("tenant", j.tenant).Str("command", j.token).Msg("Could not record a Sparkplug command before publishing it; not publishing.")
			return err
		}
		wrote = true
		if p.kind == KindReboot {
			// The one-reboot-per-window rule is kept in the bucket, not only in this term's memory:
			// a new leader must not send a second reboot into the same window. Written with the
			// record and before the bytes go out, like it, and taken back if nothing was sent.
			if err := d.store.PutReboot(ctx, j.tenant, p.target.Group, p.target.Node, w.PublishedAt); err != nil {
				d.count(ResultKVFailed)
				log.Warn().Err(err).Str("tenant", j.tenant).Str("command", j.token).Msg("Could not record a Sparkplug reboot before publishing it; not publishing.")
				return err
			}
			marked = true
			d.mu.Lock()
			d.reboots[nodeKey{j.tenant, p.target.Group, p.target.Node}] = w.PublishedAt
			d.mu.Unlock()
		}
		// In the mirror BEFORE the bytes are on the wire: a node that answers at once must find
		// the entry it is answering.
		d.addEntry(d.newEntry(j.tenant, a, j.src))
		return nil
	}
	undo := func() {
		d.forget(key)
		if wrote {
			d.deleteEntry(key)
			wrote = false
		}
		if marked {
			d.unmarkReboot(j.tenant, p.target)
			marked = false
		}
	}

	var err error
	// 14. Publish QoS 0, retain false, on the session the Client holds. If the node's table moved
	// while the record was being written, the record is retracted and the command is asked again
	// against the new table (it is re-resolved by name there); a node that keeps moving is refused.
	for attempt := 0; ; attempt++ {
		switch p.kind {
		case KindRebirth:
			_, err = j.src.PublishRebirth(ctx, p.target, hook)
		default:
			_, err = j.src.PublishMetric(ctx, p.ref, p.value, hook)
		}
		if !errors.Is(err, host.ErrTableMoved) {
			break
		}
		undo()
		if attempt >= 2 {
			d.refuseClaimed(j, ResultRefusedMetric, ReasonNodeRebirthing, fmt.Sprintf("the node %s kept re-birthing while the command was being recorded", p.target))
			return
		}
	}
	if err != nil {
		d.failedSend(ctx, j, p, undo, err)
		return
	}

	d.count(ResultPublished)
	d.watch(j)
	// 15. Ack. The row stays SENT: what became of the command is learned from the node's reports.
	j.settle(d)
}

// watch notes a command that reached (or may have reached) the wire, for the breaker's watch.
func (d *Dispatcher) watch(j *job) {
	d.mu.Lock()
	d.recent[j.src] = append(pruneRecent(d.recent[j.src], d.now()), recentPub{
		tenant: j.tenant, device: j.device, command: j.token, nonce: j.nonce, at: d.now(),
	})
	d.mu.Unlock()
}

// unmarkReboot takes back a reboot mark for a reboot that was not sent.
func (d *Dispatcher) unmarkReboot(tenant string, t host.CommandTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), bgTimeout)
	defer cancel()
	if err := d.store.DeleteReboot(ctx, tenant, t.Group, t.Node); err != nil {
		d.count(ResultKVFailed)
		log.Warn().Err(err).Str("tenant", tenant).Str("node", t.String()).
			Msg("Could not take back the mark of a reboot that was not sent; the node cannot be rebooted again until its window ends.")
		return
	}
	d.mu.Lock()
	delete(d.reboots, nodeKey{tenant, t.Group, t.Node})
	d.mu.Unlock()
}

func (d *Dispatcher) windowOf(cfg config.SourceCommands, kind string) time.Duration {
	if kind == KindReboot {
		return cfg.RebootWindow()
	}
	return cfg.ConfirmWindow()
}

// failedSend settles a command whose publish did not happen, undoing what was done for it.
func (d *Dispatcher) failedSend(ctx context.Context, j *job, p plan, undo func(), err error) {
	var indeterminate *host.PublishIndeterminateError
	if !errors.As(err, &indeterminate) {
		undo()
	}
	var before *host.BeforeWireError
	switch {
	case errors.As(err, &indeterminate):
		// The outcome is unknown: the bytes may have reached the node, so this is NOT "not sent".
		// The record STAYS, in the bucket and in memory, armed: it is the only account of what may
		// have gone out, and the window settles it like any other published command (a report of
		// the value confirms it; the end of the window leaves it UNANSWERED, which is TIMEOUT/UNKNOWN
		// and not the retry-safe NOT_DELIVERED). It is never handed back to be sent a second time.
		d.count(ResultPublishFailed)
		d.watch(j)
		log.Warn().Err(indeterminate.Err).Str("tenant", j.tenant).Str("command", j.token).
			Msg("A Sparkplug command's publish did not complete; it may have reached the node and is awaited like any other.")
		j.settle(d)
	case errors.Is(err, host.ErrTermEnded):
		// The term ended between the claim and the wire. Nothing was published. The claim already
		// moved the row onto a new dispatch, so the redelivered envelope will lose its own
		// confirmation: hand the row back so the next leader's wake delivers it, and leave the
		// message unacked.
		d.parkClaimed(j)
	case errors.As(err, &before), errors.Is(err, host.ErrSessionDown), errors.Is(err, host.ErrNotBorn):
		// Not sent (the record could not be written, the session is down, or the table is not
		// there): back to PARKED, to try again at the next birth. A reboot is never parked.
		d.handBack(j)
	case errors.Is(err, host.ErrBreakerOpen):
		d.refuseClaimed(j, ResultRefusedBreaker, ReasonBrokerRefused, msgBreaker)
	case errors.Is(err, host.ErrMetricUnknown):
		d.refuseClaimed(j, ResultRefusedMetric, ReasonMetricNotDeclared, fmt.Sprintf("metric %q is no longer declared in the BIRTH of %s (the node re-birthed)", p.metric, p.target))
	case errors.Is(err, host.ErrDatatypeChanged):
		d.refuseClaimed(j, ResultRefusedDatatype, ReasonDatatypeChanged, err.Error())
	case errors.Is(err, host.ErrUnsupportedDatatype):
		d.refuseClaimed(j, ResultRefusedDatatype, ReasonDatatypeUnsupported, err.Error())
	case errors.Is(err, host.ErrValueRefused):
		d.refuseClaimed(j, ResultRefusedPayload, ReasonValueNotAccepted, err.Error())
	default:
		d.count(ResultPublishFailed)
		d.refuseClaimedKeepCount(j, "the command could not be sent: "+err.Error())
	}
}

// handBack returns a claimed command that was not sent to command-delivery for the device's next
// birth, except a reboot, which is refused instead: it is sent to a node that is online when it is
// dispatched, or not at all.
func (d *Dispatcher) handBack(j *job) {
	if j.name == CommandReboot {
		j.parkedBack = true // the row is settled; a drain must still stop and look again
		d.refuseClaimed(j, ResultRefusedRebootGate, ReasonNodeOffline, msgRebootOffline)
		return
	}
	d.parkClaimed(j)
	j.settle(d)
}

// refuseClaimedKeepCount answers UNSENT without counting a refusal result (the caller counted).
func (d *Dispatcher) refuseClaimedKeepCount(j *job, text string) {
	_ = d.respondRefused(j, ReasonPublishFailed, text)
	j.settle(d)
}

func (d *Dispatcher) deleteEntry(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), bgTimeout)
	defer cancel()
	if err := d.store.Delete(ctx, key); err != nil {
		d.count(ResultKVFailed)
		log.Warn().Err(err).Str("key", key).Msg("Could not delete an awaiting entry; it ages out with the bucket's lifetime.")
	}
}

// supersede resolves the awaiting write this command replaces, if any.
func (d *Dispatcher) supersede(j *job, p plan) {
	if p.kind != KindWrite {
		return
	}
	d.mu.Lock()
	old := d.byMetric[awaitKey{j.tenant, j.device, p.metric}]
	d.mu.Unlock()
	if old == nil || old.a.Command == j.token {
		return
	}
	d.resolveNow(old, &outcome{
		reason: ReasonSuperseded,
		result: ResultSuperseded,
		text:   fmt.Sprintf("superseded by %s before it was confirmed", j.token),
	})
}

func pruneRecent(in []recentPub, now time.Time) []recentPub {
	out := in[:0]
	for _, r := range in {
		if now.Sub(r.at) < 4*tripWindow {
			out = append(out, r)
		}
	}
	return out
}

// onTripped is the breaker's report that a source's session ended right after a command publish.
// Every command the source published in the watch before it is suspected of having been refused:
// it is resolved UNANSWERED now, so the tenant's undelivered ceiling is freed at once and the
// operator is told what most likely happened. Off the receive goroutine, on contexts of its own.
func (d *Dispatcher) onTripped(src Source, at time.Time) {
	d.mu.Lock()
	var hit []recentPub
	for _, r := range d.recent[src] {
		if at.Sub(r.at) < tripWindow && !r.at.After(at) {
			hit = append(hit, r)
		}
	}
	d.recent[src] = nil
	open := d.termOpen
	if open && len(hit) > 0 {
		d.bg.Add(1)
	}
	d.mu.Unlock()
	if len(hit) == 0 || !open {
		return
	}
	go func() {
		defer d.bg.Done()
		for _, r := range hit {
			// Only a command still awaiting is blamed: one a report already confirmed is not.
			d.mu.Lock()
			e := d.entries[AwaitingKey(r.tenant, r.command)]
			d.mu.Unlock()
			if e != nil {
				d.resolveNow(e, &outcome{reason: ReasonSessionDropped, result: ResultUnconfirmed, text: msgTripped})
			}
		}
	}()
}
