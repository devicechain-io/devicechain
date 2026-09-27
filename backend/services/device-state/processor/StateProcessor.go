// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	MESSAGE_BACKLOG_SIZE = 100 // Number of messages that can be read and waiting to be processed
)

// StateProcessor is a second, independent consumer of the resolved-events stream
// (fan-out alongside event-management's persistence). For every resolved event it
// updates the originating device's live state projection, and a background
// monitor flips devices to inactive after an inactivity timeout.
//
// Like event-management's persistence processor, the single read loop only reads
// messages and hands them to a pool of projection writers (E6, sized by
// projection.writers), and like it each writer merges the events already waiting for it,
// up to projection.maxBatch, in ONE transaction (model.MergeProjectionBatch). On a
// replicated database every commit waits for the standby, which is what bounded how fast
// one event at a time could be merged.
//
// Two writers can hold events for the same device at once. That is safe because every
// merge locks the device's row before it reads it (so same-device merges serialize), folds
// events with ONE rule over times held at stored precision (so a batch leaves what merging
// its events one at a time would), and writes the latest values under a strictly-newer
// guard (so a late or redelivered reading cannot move the projection backwards). Each
// message carries its own NATS ack handle, and is acknowledged only after the transaction
// holding it commits; a batch that does not commit is merged again one event at a time
// (mergeBatch), so every message's disposition (A3) is still decided by its own merge.
type StateProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	Api                  model.DeviceStateApi

	// The merge loop's instrumentation (E13): per-message RED metrics, batch size and
	// batch fallbacks. Nil-safe.
	metrics *StateMetrics

	// sweepMetrics are the inactivity sweep's pass signals, built once in the initialize
	// phase for the same reason metrics is. The monitor cannot adopt core.PeriodicTask —
	// its lifecycle octet is shared with the reader/worker pipeline and a carefully ordered
	// teardown, so there is nothing to delete — but the question an operator asks of it is
	// the same one, and RecordPass keeps the classification in one place.
	sweepMetrics *core.PeriodicTaskMetrics

	messages chan messaging.Message

	// projection sizes the writers: how many run, and how many events each merges in one
	// transaction. Defaulted in the constructor by the configuration's own ApplyDefaults; a
	// processor assembled by literal has it zero, which CollectBatch reads as batches of one.
	projection config.ProjectionConfiguration

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain the reader before closing the channel it feeds, so the
	// reader can never send on a closed channel at SIGTERM, and the workers drain
	// the remaining backlog to completion before exiting.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup

	// monitorWG tracks the inactivity monitor so ExecuteStop can WAIT for it rather than
	// merely signalling it. Closing quit stops the NEXT sweep from starting; it says
	// nothing about the one already running. See ExecuteStop.
	monitorWG sync.WaitGroup

	// inactivityInterval is the monitor's cadence, and ZERO MEANS "use the platform
	// default". There is no operator knob behind it — the constant in config is the only
	// production value — so it is unexported and the only thing that sets it is a test:
	// what has to hold at shutdown is that ExecuteStop does not return while a sweep is
	// still running, and a test cannot put a sweep in flight if it must first wait out a
	// one-minute tick.
	inactivityInterval time.Duration

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. Built on first use by pacer(), because this struct is also
	// assembled by literal in tests that never run the constructor.
	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// pacer returns the read loop's error pacer, building it on first use. It is touched only
// by the single read goroutine, which is the pacer's own contract.
func (sp *StateProcessor) pacer() *core.ReadPacer {
	if sp.readPacer == nil {
		sp.readPacer = core.NewReadPacer(sp.Microservice, "resolved events")
	}
	return sp.readPacer
}

// StateMetrics is the merge loop's instrumentation: the per-message RED metrics every
// processing loop exports, plus what only a batching writer has to say. Every method is
// nil-safe, because tests assemble processors by literal without metrics.
type StateMetrics struct {
	// messages is state_messages_total, _duration_seconds and _inflight. A message is in
	// flight from when a writer takes it until its disposition, which includes the time it
	// waits for its batch to commit.
	messages *core.ProcessorMetrics
	// batchSize is state_batch_size: events merged per committed transaction.
	batchSize prometheus.Observer
	// fallbacks is state_batch_fallbacks_total: batch transactions that did not commit.
	fallbacks prometheus.Counter
}

// start marks one message in flight and returns the function that records its result.
func (m *StateMetrics) start() func(result string) {
	if m == nil {
		return func(string) {}
	}
	return m.messages.Start()
}

// committed records one committed transaction holding n events.
func (m *StateMetrics) committed(n int) {
	if m != nil {
		m.batchSize.Observe(float64(n))
	}
}

// fallback records one batch transaction that did not commit.
func (m *StateMetrics) fallback() {
	if m != nil {
		m.fallbacks.Inc()
	}
}

// NewStateMetrics builds this processor's instrumentation.
//
// 🔴 IT IS SEPARATE FROM THE CONSTRUCTOR BECAUSE THE TWO RUN IN DIFFERENT PHASES.
// The processor is built inside the NATS manager's oncreate callback, which runs on
// EVERY start — it has to, because the processor holds a reader bound to the
// connection — while a Prometheus collector belongs to the PROCESS and may be
// registered only once, or the second registration panics. So the caller builds this in
// the INITIALIZE phase and hands the same instruments to every processor that callback
// builds.
func NewStateMetrics(ms *core.Microservice) *StateMetrics {
	return &StateMetrics{
		messages: ms.NewProcessorMetrics("state"),
		batchSize: ms.NewHistogramVec("state_batch_size", "Events merged per live-state projection transaction.",
			nil, []float64{1, 2, 4, 8, 16, 32, 64}).WithLabelValues(),
		fallbacks: ms.NewCounter("state_batch_fallbacks_total",
			"Batch transactions that did not commit, after which their events were merged again."),
	}
}

// StateProcessorOption configures a StateProcessor.
type StateProcessorOption func(*StateProcessor)

// WithProjection sizes the projection writers from the service's configuration. Without it
// the processor runs the configuration's defaults.
func WithProjection(cfg config.ProjectionConfiguration) StateProcessorOption {
	return func(sp *StateProcessor) {
		sp.projection = cfg
	}
}

// Create a new device-state processor.
//
// sweepMetrics are the inactivity sweep's pass signals, built once in the initialize phase
// for the same reason metrics is. The monitor cannot adopt core.PeriodicTask — its lifecycle
// octet is shared with the reader/worker pipeline and a carefully ordered teardown, so there
// is nothing to delete — but the question an operator asks of it is the same one.
//
// metrics is built once in the initialize phase (see NewStateMetrics) and shared by
// every processor this service constructs, because that callback is connection-scoped
// and the instruments are not.
func NewStateProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api model.DeviceStateApi,
	metrics *StateMetrics, sweepMetrics *core.PeriodicTaskMetrics,
	opts ...StateProcessorOption) *StateProcessor {
	sp := &StateProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		Api:                  api,
		metrics:              metrics,
		sweepMetrics:         sweepMetrics,
	}
	for _, opt := range opts {
		opt(sp)
	}
	// The same defaulting the configuration load runs, so the two cannot disagree.
	sp.projection.ApplyDefaults()

	// Create lifecycle manager.
	spname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "state-proc")
	sp.lifecycle = core.NewLifecycleManager(spname, sp, callbacks)
	return sp
}

// Initialize component.
func (sp *StateProcessor) Initialize(ctx context.Context) error {
	return sp.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (sp *StateProcessor) ExecuteInitialize(ctx context.Context) error {
	sp.quit = make(chan struct{})

	// Derive the cancelable context the read loop runs under (A5).
	sp.procCtx, sp.procCancel = context.WithCancel(ctx)

	// Initialize the pool of state-merge workers.
	sp.initializeWorkers()
	return nil
}

// Writers reports how many projection writers this processor runs, so a caller can
// confirm its configuration reached it.
func (sp *StateProcessor) Writers() int {
	return sp.projection.Writers
}

// MaxBatch reports the most events one writer merges in one transaction.
func (sp *StateProcessor) MaxBatch() int {
	return sp.projection.MaxBatch
}

// Linger reports how long a writer waits for a batch that is not full.
func (sp *StateProcessor) Linger() time.Duration {
	return sp.projection.Linger()
}

// Initialize pool of workers that merge device state in parallel.
func (sp *StateProcessor) initializeWorkers() {
	sp.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	log.Info().Int("writers", sp.projection.Writers).Int("maxBatch", sp.projection.MaxBatch).
		Dur("linger", sp.projection.Linger()).Msg("Device state projection writers started")
	for w := 1; w <= sp.projection.Writers; w++ {
		sp.workerWG.Add(1)
		// Workers run on a background context (not the cancelable read context)
		// so that on shutdown they drain the remaining buffered messages to
		// completion and ack them rather than aborting in-flight merges.
		go func() {
			defer sp.workerWG.Done()
			sp.processMessages(context.Background())
		}()
	}
}

// inactivityRecheckInterval is the monitor's cadence, falling back to the platform
// default. See the field for why it is settable at all.
func (sp *StateProcessor) inactivityRecheckInterval() time.Duration {
	if sp.inactivityInterval > 0 {
		return sp.inactivityInterval
	}
	return config.InactivityRecheckInterval * time.Second
}

// Start component.
func (sp *StateProcessor) Start(ctx context.Context) error {
	return sp.lifecycle.Start(ctx)
}

// readLoop drains the resolved-events stream into the worker pool until the context is
// cancelled, the reader reports EOF, or a run of non-EOF read errors outlasts the pacer's
// budget.
//
// It is a named method rather than the body of the goroutine in ExecuteStart so that the
// pacing tests can run the SAME line production runs. A test that reassembles the call
// itself proves the library works and says nothing about how this service calls it, which
// is where the wiring defects in this tree have actually been.
func (sp *StateProcessor) readLoop(ctx context.Context) {
	messaging.RunConsumer(ctx, sp.ResolvedEventsReader, sp.pacer(), func(msg messaging.Message) bool {
		return sp.handOff(ctx, msg)
	})
}

// handOff gives one resolved event to the worker pool, and reports whether the read loop
// should carry on.
//
// It abandons the handoff on shutdown so the loop can exit instead of blocking on a full
// channel (A5). The message is unacked at that point, so it is redelivered after restart.
func (sp *StateProcessor) handOff(ctx context.Context, msg messaging.Message) bool {
	select {
	case sp.messages <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// processMessages is the worker loop: it collects the events already waiting for it into a
// batch and merges them (mergeBatch). The A3 ack contract rides on each messaging.Message,
// so the worker that merges a message is the one that acks it (success / poison) or leaves
// it unacked for redelivery (transient). The batch collected when the channel closes is
// merged before the worker exits, so ExecuteStop still drains the backlog.
//
// ctx is the WORKER's context and carries no tenant; each message's own tenant comes from
// its subject (admit).
func (sp *StateProcessor) processMessages(ctx context.Context) {
	for {
		batch, open := messaging.CollectBatch(sp.messages, sp.projection.MaxBatch, sp.projection.Linger(),
			func(msg messaging.Message) (pendingMerge, bool) { return sp.admit(ctx, msg) })
		if len(batch) > 0 {
			sp.mergeBatch(ctx, batch)
		}
		if !open {
			log.Debug().Msg("Device state merger received shutdown signal.")
			return
		}
	}
}

// pendingMerge is one admitted message waiting for its batch.
type pendingMerge struct {
	msg    messaging.Message
	ctx    context.Context // the message's own tenant-scoped context, for the per-message path
	event  *dmmodel.ResolvedEvent
	update model.ProjectionUpdate
	done   func(result string)
}

// admit runs the checks a message must pass before it can be merged — a tenant from its
// subject, a decodable event, a mappable presence claim — and disposes of one that fails them
// (acked, recorded invalid: redelivery cannot fix any of the three), so it joins no batch. It
// is the ONE place an event becomes a model.ProjectionUpdate, for both paths.
func (sp *StateProcessor) admit(ctx context.Context, msg messaging.Message) (pendingMerge, bool) {
	// RED metrics for this merge (E13): time the message and record its
	// disposition exactly once on whichever path it leaves by.
	done := sp.metrics.start()

	// Derive the per-message tenant from the message subject and build a
	// tenant-scoped context. Without a parseable tenant the state can not be
	// updated safely (fail-closed) so the message is skipped.
	msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", msg.Subject))
		// Poison message: redelivery will not make the tenant parseable, so ack
		// to drop it rather than redeliver up to MaxDeliver times.
		msg.Ack()
		done(core.ResultInvalid)
		return pendingMerge{}, false
	}

	// Attempt to unmarshal the resolved event. A projection has no failed-events
	// channel, so an unparseable message is logged and dropped.
	event, err := dmproto.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping resolved event that could not be parsed from subject %q", msg.Subject))
		// Poison message: redelivery will not make it parseable, so ack to drop.
		msg.Ack()
		done(core.ResultInvalid)
		return pendingMerge{}, false
	}

	pt, err := presenceTransitionFor(event)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping unmappable state-change event for device %s", event.SourceDeviceToken))
		// Poison message: redelivery cannot make the state mappable. Dropping it is the
		// only safe disposition — folding it as a plain data event would let an
		// unreadable presence claim act as a liveness heartbeat.
		msg.Ack()
		done(core.ResultInvalid)
		return pendingMerge{}, false
	}

	update := model.ProjectionUpdate{
		Tenant:      tenant,
		DeviceToken: event.SourceDeviceToken,
		OccurredAt:  event.OccurredTime,
		Presence:    pt,
		Identity:    model.DeviceIdentity{ExternalId: event.ExternalId, Source: event.Source},
	}
	// A measurement event also advances the per-key latest-value projection, and a location
	// event the last-known position — IN ADDITION TO the device state, not instead of it: a
	// location is a data event like any other, so it is still a liveness heartbeat. A device
	// that only ever reports its position must not be swept inactive for saying nothing.
	switch event.EventType {
	case esmodel.Measurement:
		update.Measurements = measurementInputs(event)
	case esmodel.Location:
		update.Locations = locationInputs(event)
	}
	return pendingMerge{msg: msg, ctx: msgctx, event: event, update: update, done: done}, true
}

// mergeBatch merges batch in one transaction and acknowledges it, or — when that transaction
// does not commit — merges its messages again without the part to blame.
//
// Every statement in a batch is one tenant's, so the smallest thing a refused statement can
// be pinned on is its tenant (model.BatchRefusal). That tenant's messages are merged one at a
// time, through the unchanged per-message path, so the one that is really refused is retried
// or dropped exactly as it always was and its tenant-mates still land; the other tenants'
// messages are merged together again without them. A failure nothing can be blamed for
// (BEGIN, COMMIT, a lost connection) merges every message on its own.
//
// ctx is the WORKER's context and carries no tenant (see model.MergeProjectionBatch).
func (sp *StateProcessor) mergeBatch(ctx context.Context, batch []pendingMerge) {
	for len(batch) > 1 {
		updates := make([]model.ProjectionUpdate, len(batch))
		for i := range batch {
			updates[i] = batch[i].update
		}
		err := sp.Api.MergeProjectionBatch(ctx, updates)
		if err == nil {
			sp.metrics.committed(len(batch))
			// After COMMIT, never inside the transaction.
			for _, p := range batch {
				p.msg.Ack()
				p.done(core.ResultOK)
			}
			return
		}
		sp.metrics.fallback()
		var refused *model.BatchRefusal
		if !errors.As(err, &refused) || rdb.IsConnectionFailure(err) {
			log.Warn().Err(err).Int("events", len(batch)).
				Msg("A device state batch did not commit; merging its events one at a time")
			for _, p := range batch {
				sp.mergeAdmitted(p)
			}
			return
		}
		log.Warn().Err(err).Int("events", len(batch)).Str("tenant", refused.Tenant).
			Bool("tenantPurged", errors.Is(err, rdb.ErrTenantPurged)).
			Msg("A device state batch was refused for one tenant; merging that tenant's events one at a time")
		// A fresh slice: the remainder must not alias the batch it came from.
		rest := make([]pendingMerge, 0, len(batch))
		for _, p := range batch {
			if p.update.Tenant == refused.Tenant {
				sp.mergeAdmitted(p)
				continue
			}
			rest = append(rest, p)
		}
		if len(rest) == len(batch) {
			// A refusal naming a tenant this batch does not hold would loop forever.
			for _, p := range batch {
				sp.mergeAdmitted(p)
			}
			return
		}
		batch = rest
	}
	if len(batch) == 1 {
		sp.mergeAdmitted(batch[0])
	}
}

// mergeOne merges a single message on its own: admission, then the per-message path.
func (sp *StateProcessor) mergeOne(ctx context.Context, msg messaging.Message) {
	if p, ok := sp.admit(ctx, msg); ok {
		sp.mergeAdmitted(p)
	}
}

// mergeAdmitted is the per-message path: the originating device's live state, then its
// latest values or position, each in a transaction of its own, and the A3 disposition of the
// outcome. A batch of one takes it, and so does every message a batch gives up on.
func (sp *StateProcessor) mergeAdmitted(p pendingMerge) {
	msg, event, u := p.msg, p.event, p.update

	// disposeTransient applies the A3 retry-or-drop disposition for a transient
	// (retryable) projection-write error: redeliver until the finite MaxDeliver
	// cap, then give up (the projection is reconstructable, so dropping beats
	// looping forever). Shared by every write below.
	disposeTransient := func(err error, detail string) {
		log.Error().Err(err).Str("correlation", msg.CorrelationID()).Msg(detail)
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Giving up on %s after %d attempts", detail, msg.NumDelivered))
			msg.Ack()
			// 🔑 "dropped", NOT "failed". This projection has no dead-letter path and wants
			// none: it is rebuildable from the event history, so a state change nobody could
			// apply is discarded rather than kept for inspection. Reporting it as "failed"
			// would put it in the same bucket as the consumers that DO record what they gave
			// up on, and an operator counting dead letters would be counting this too.
			p.done(core.ResultDropped)
		} else {
			// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
			// an immediate nak would burn MaxDeliver in ~1.4ms inside an outage.
			// Reference disposition: event-sources' settler (ADR-030).
			p.done(core.ResultRetry)
		}
	}

	// Update the originating device's live connectivity projection for every event.
	if _, err := sp.Api.MergeDeviceState(p.ctx, u.DeviceToken, u.OccurredAt, u.Presence, u.Identity); err != nil {
		disposeTransient(err, fmt.Sprintf("device state projection update for device %s", u.DeviceToken))
		return
	}

	// For a measurement event, also advance the per-key latest-value projection.
	if event.EventType == esmodel.Measurement {
		if err := sp.Api.MergeLatestMeasurements(p.ctx, u.DeviceToken, u.Measurements); err != nil {
			disposeTransient(err, fmt.Sprintf("latest-measurement projection update for device %s", u.DeviceToken))
			return
		}
	}

	// For a location event, also advance the last-known-position projection (in addition
	// to the device state above: a location is a liveness heartbeat too).
	if event.EventType == esmodel.Location {
		if err := sp.Api.MergeLatestLocations(p.ctx, u.DeviceToken, u.Locations); err != nil {
			disposeTransient(err, fmt.Sprintf("latest-location projection update for device %s", u.DeviceToken))
			return
		}
	}

	// Projection updated successfully: ack so the message is not redelivered.
	sp.metrics.committed(1)
	msg.Ack()
	p.done(core.ResultOK)
}

// measurementInputs extracts the numeric measurements from a resolved measurement event,
// for the per-key latest-value projection. Non-numeric values are skipped (v1 is
// numeric-only). Each sample is stamped at ITS
// OWN time, which the resolver already decided and bounded — this reads the entry's
// time and never falls back to the envelope or re-clamps, so the projection cannot
// disagree with the history table about the same reading. A measurement event whose
// payload is not the expected shape contributes nothing (connectivity is still updated)
// rather than being treated as a retryable error.
func measurementInputs(event *dmmodel.ResolvedEvent) []model.LatestMeasurementInput {
	payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload)
	if !ok {
		return nil
	}
	inputs := make([]model.LatestMeasurementInput, 0)
	for _, entry := range payload.Entries {
		occurredAt := entry.OccurredTime
		for _, mx := range entry.Entries {
			f, err := strconv.ParseFloat(mx.Value, 64)
			if err != nil {
				// Not a numeric reading: outside the v1 latest-value projection.
				continue
			}
			var classifier *uint
			if mx.Classifier != nil {
				c := uint(*mx.Classifier)
				classifier = &c
			}
			inputs = append(inputs, model.LatestMeasurementInput{
				Name:         mx.Name,
				Value:        sql.NullFloat64{Float64: f, Valid: true},
				Classifier:   classifier,
				Unit:         mx.Unit,
				DataType:     mx.DataType,
				OccurredTime: occurredAt,
			})
		}
	}
	return inputs
}

// nullableFloat parses one optional coordinate off a resolved location entry. The
// values arrive as strings and were already range-checked at decode, so a value that
// is absent OR unparseable here yields NULL rather than a zero: a projection column
// reading 0.0 is a claim (the equator, sea level, stationary, due north), and NULL is
// the only honest encoding of "the device did not report this".
func nullableFloat(val *string) sql.NullFloat64 {
	if val == nil {
		return sql.NullFloat64{}
	}
	f, err := strconv.ParseFloat(*val, 64)
	if err != nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

// locationInputs extracts the fixes from a resolved location event, for the
// last-known-position projection. Each fix is stamped at ITS OWN time,
// already decided and bounded at resolution, mirroring the measurement path — and that
// time is also the ordering key the merge's newer-wins guard compares on, which is why
// it must arrive bounded: an unbounded future value would win forever and no real fix
// could ever supersede it.
//
// An entry carrying neither a parseable latitude nor a parseable longitude is skipped:
// it locates the device nowhere, so storing it would replace a real position with an
// empty one and stamp it as the newest fix. A location event whose payload is not the
// expected shape contributes nothing (connectivity is still updated) rather than being
// treated as a retryable error — redelivery cannot change its shape.
func locationInputs(event *dmmodel.ResolvedEvent) []model.LatestLocationInput {
	payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload)
	if !ok {
		return nil
	}
	inputs := make([]model.LatestLocationInput, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		in := model.LatestLocationInput{
			Latitude:     nullableFloat(entry.Latitude),
			Longitude:    nullableFloat(entry.Longitude),
			Elevation:    nullableFloat(entry.Elevation),
			Accuracy:     nullableFloat(entry.Accuracy),
			Speed:        nullableFloat(entry.Speed),
			Heading:      nullableFloat(entry.Heading),
			OccurredTime: entry.OccurredTime,
		}
		if !in.Latitude.Valid && !in.Longitude.Valid {
			continue
		}
		inputs = append(inputs, in)
	}
	return inputs
}

// Lifecycle callback that runs startup logic.
func (sp *StateProcessor) ExecuteStart(ctx context.Context) error {
	// Read loop for inbound resolved events. Runs under the cancelable context so
	// ExecuteStop can stop it before the channel it feeds is closed.
	sp.readerWG.Add(1)
	go func() {
		defer sp.readerWG.Done()
		sp.readLoop(sp.procCtx)
	}()

	// Background inactivity monitor, tracked so ExecuteStop can join it.
	//
	// 🔴 THE Add IS OUTSIDE THE `go` ON PURPOSE, as it is for readerWG and workerWG
	// above. sync.WaitGroup requires that an Add raising the counter from zero
	// happen-before the Wait meant to hold for it; an Add moved inside the goroutine can be
	// scheduled after Wait has already seen a zero counter and returned, which is the
	// "clean stop reported over work still running" this join exists to prevent.
	sp.monitorWG.Add(1)
	go func() {
		defer sp.monitorWG.Done()
		sp.runInactivityMonitor(ctx)
	}()
	return nil
}

// runInactivityMonitor periodically flips devices that have gone quiet to inactive. The
// sweep runs under a system context so it spans all tenants in a single pass (each row
// keeps its own tenant_id on save).
//
// Extracted from ExecuteStart so both the cadence and the shutdown behaviour are
// reachable: inline, the only way to put a sweep in flight was to wait out a real tick.
func (sp *StateProcessor) runInactivityMonitor(ctx context.Context) {
	ticker := time.NewTicker(sp.inactivityRecheckInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sp.quit:
			return
		case <-ticker.C:
			started := time.Now()
			count, err := sp.Api.SweepInactive(core.WithSystemContext(ctx), started)
			if err == nil && count > 0 {
				log.Info().Msg(fmt.Sprintf("Inactivity monitor marked %d device(s) inactive", count))
			}
			// RecordPass logs the failure, and classifies a sweep cut short by shutdown as
			// cancelled rather than failed — which the bare log.Error here could not do.
			sp.sweepMetrics.RecordPass(ctx, err, started, "inactivity-sweep")
		}
	}
}

// Stop component.
func (sp *StateProcessor) Stop(ctx context.Context) error {
	return sp.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic. It unwinds the pipeline in
// dependency order so no goroutine ever sends on a closed channel (A5): stop the
// reader, then close the channel it feeds, then wait for the workers it feeds to
// drain the backlog and exit. The inactivity monitor is stopped independently.
//
// 🔴 IT WAITS FOR THE MONITOR, AND SIGNALLING IT IS NOT THE SAME THING. Closing quit
// stops the next sweep from starting; it says nothing about the one already running. The
// caller is beforeMicroserviceStopped, which goes on to stop the RdbManager — and the pool
// is closed in RdbManager.ExecuteTerminate. Without the join, a sweep that was mid-flight
// when quit closed is still issuing queries when its pool closes, from a service that has
// already reported a clean stop.
//
// 🔑 THE WAIT IS BOUNDED BY THE ROOT CANCELLATION THAT ALREADY HAPPENED. Microservice
// shutdown cancels the root context BEFORE calling Stop, and that root context is the one
// ExecuteStart handed to the monitor, so a sweep in flight is already unwinding against a
// cancelled context by the time this wait begins. No deadline is taken from ExecuteStop's
// own context because it is context.Background(): a bounded wait on it could never fire.
func (sp *StateProcessor) ExecuteStop(context.Context) error {
	if sp.procCancel != nil {
		sp.procCancel()
	}
	sp.readerWG.Wait() // reader stopped: no more sends to messages
	close(sp.messages) //
	sp.workerWG.Wait() // workers drained + exited

	close(sp.quit)      // stop the inactivity monitor
	sp.monitorWG.Wait() // ...and wait for a sweep it may have been in the middle of
	return nil
}

// Terminate component.
func (sp *StateProcessor) Terminate(ctx context.Context) error {
	return sp.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (sp *StateProcessor) ExecuteTerminate(context.Context) error {
	return nil
}

// presenceTransitionFor maps a resolved event onto an authoritative presence transition
// (ADR-067), or nil when the event is a plain data heartbeat. The transition time is the
// event's occurred time — one canonical clock, set by the producer.
//
// 🔴 IT RETURNS AN ERROR RATHER THAN A nil, and the difference is not cosmetic. A nil
// here does not mean "nothing to do": it means "plain data event", which MergeDeviceState
// folds as an implicit heartbeat that advances LastActivityTime. So a StateChange this
// function could not map would arrive at the projection disguised as a heartbeat and
// keep an asserted device looking alive on the strength of an event nobody could read.
// An unmappable presence state is a deterministic producer defect; it is dropped as a
// poison message, loudly.
//
// 🔑 EXTRACTED SO THE SEAM CAN BE TESTED AS THE CALLER, NOT RE-IMPLEMENTED BY A TEST.
// This is a hand-written field-by-field mapping at the end of a chain of four other
// hand-written mappings, which makes it exactly the kind of place a newly added field is
// silently dropped: every layer still compiles, every existing test still passes, and the
// field simply never arrives. A test that rebuilt this mapping itself would agree with
// whatever the mapping forgot. See TestARegressedSessionSurvivesTheWholeChain.
func presenceTransitionFor(event *dmmodel.ResolvedEvent) (*model.PresenceTransition, error) {
	if event.EventType != esmodel.StateChange {
		return nil, nil
	}
	p, ok := event.Payload.(*dmmodel.ResolvedStateChangePayload)
	if !ok {
		return nil, fmt.Errorf("state-change event carries a %T payload", event.Payload)
	}
	claim, ok := p.Claim()
	if !ok {
		return nil, fmt.Errorf("state-change event carries unmappable presence state %q", p.State)
	}
	return &model.PresenceTransition{
		Claim:             claim,
		SessionId:         p.SessionId,
		ExpectedSessionId: p.ExpectedSessionId,
		OccurredAt:        event.OccurredTime,
	}, nil
}
