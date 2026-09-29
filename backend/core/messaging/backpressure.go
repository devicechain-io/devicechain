// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Backpressure on the ingest path: a stream that must not discard a message its reader has
// not read REFUSES new ones instead, while that reader is far behind.
//
// Every stream discards its oldest message when full (DiscardOld). On the two streams whose
// unread loss is device data (streams.Stream.BackpressureReaders), that eviction took events a
// device had already been told were accepted. The broker cannot make that refusal itself:
// what these streams hold at their ceiling is mostly a week of history every reader has
// ALREADY read, so discard-new would refuse on read history and stop ingest for good, and the
// retentions that would make "full" mean "full of unread" break replay (see the field's
// comment). So the platform's own writers make the refusal, from the quantity that decides
// whether anything is lost: each declared reader's UNREAD backlog against the stream's
// ceiling. The broker keeps discarding old as a counted backstop.
//
// The gate is per stream and per process. Every service that writes to a gated stream, or
// forwards into one, keeps its own copy, sampled from the broker, so none of them depends on
// another service's view or version.

// ErrStreamBackpressure is what every backpressure refusal matches (errors.Is). It is
// retryable: the stream is not broken, a reader is behind, and the refusal lifts once it
// catches up. A transport advises BackpressureRetryAfter.
var ErrStreamBackpressure = errors.New("messaging: stream is applying backpressure")

// BackpressureError says which stream refused and why.
type BackpressureError struct {
	// Stream is the full stream name.
	Stream string
	// Durable is the reader whose backlog closed the gate, "" when Stale.
	Durable string
	// Ratio is that reader's unread backlog over the stream's capacity at the last sample.
	Ratio float64
	// Stale is set when the backlog could not be measured recently enough to trust: the
	// gate then reads as closed (fail closed), whatever it last measured.
	Stale bool
}

func (e *BackpressureError) Error() string {
	if e.Stale {
		return fmt.Sprintf("messaging: stream %s is applying backpressure: its readers' backlog has not "+
			"been measured in the last %s", e.Stream, backpressureStaleAfter)
	}
	return fmt.Sprintf("messaging: stream %s is applying backpressure: consumer %s has not read %.0f%% of "+
		"what the stream can hold", e.Stream, e.Durable, e.Ratio*100)
}

// Is makes a *BackpressureError match ErrStreamBackpressure.
func (e *BackpressureError) Is(target error) bool { return target == ErrStreamBackpressure }

const (
	// backpressureCloseRatio is the unread fraction of the ceiling at which the gate closes.
	// The 10% left above it is the margin before DiscardOld would evict an unread message:
	// what is in flight when the gate closes, what the sample interval lets through, the
	// state transitions that are admitted while it is closed (Message.BypassBackpressure),
	// and the messages the broker has acknowledged but not yet signalled to its consumers
	// all land in it. That last one is why a measurement can read low: a PubAck is sent
	// before the stream signals its consumers, and ConsumerInfo.NumPending is counted from
	// that signal, so a sample reads low by whatever is still queued for it.
	backpressureCloseRatio = 0.90
	// backpressureOpenRatio is the fraction below which a closed gate opens again. The gap
	// to the close ratio keeps a reader hovering at the threshold from flapping the gate
	// open and shut on every sample.
	backpressureOpenRatio = 0.80
	// backpressureSampleEvery is how often every registered gate is re-measured.
	backpressureSampleEvery = 5 * time.Second
	// backpressureStaleAfter is how old the last successful measurement may be before the
	// gate reads as closed. A gate that cannot see the backlog cannot say it is safe to add
	// to it.
	backpressureStaleAfter = 30 * time.Second
	// backpressureSampleTimeout bounds one measurement.
	backpressureSampleTimeout = 5 * time.Second

	// BackpressureRetryAfter is what a transport advises a refused client to wait: two
	// sample intervals, so a retry lands after the gate has been measured again.
	BackpressureRetryAfter = 2 * backpressureSampleEvery
)

// These are constants, not configuration, on purpose: a knob here is a way to switch a
// fail-closed guard off, and nothing about an install changes what "about to discard an
// unread event" means.

// unreadRatio is one durable's unread backlog (NumPending + NumAckPending) over the stream's
// capacity, in whichever of messages or bytes binds first. 0 for an empty stream.
//
// 🔴 THE BYTE FORM ASSUMES UNREAD MESSAGES ARE THE SIZE OF THE STREAM'S AVERAGE. The broker
// reports the stream's total bytes, not the unread messages' bytes, so a burst of large unread
// messages over small history is under-counted and the stream can reach its ceiling before
// the ratio reaches the close threshold. The 10% above the threshold is the only margin for
// that; DiscardOld is still the backstop, and the readers' unread-loss counters still see it.
func unreadRatio(st nats.StreamState, cfg nats.StreamConfig, ci *nats.ConsumerInfo) float64 {
	unread := ci.NumPending + uint64(max(0, ci.NumAckPending))
	r := 0.0
	if cfg.MaxMsgs > 0 {
		r = float64(unread) / float64(cfg.MaxMsgs)
	}
	if cfg.MaxBytes > 0 && st.Msgs > 0 {
		avg := float64(st.Bytes) / float64(st.Msgs)
		r = max(r, float64(unread)*avg/float64(cfg.MaxBytes))
	}
	return r
}

// gateNext is the hysteresis: a gate closes at the close ratio and opens again only below
// the open ratio.
func gateNext(closed bool, ratio float64) bool {
	if closed {
		return ratio >= backpressureOpenRatio
	}
	return ratio >= backpressureCloseRatio
}

// gateState is one gated stream's last measurement. Everything is guarded by
// backpressureGates.mu.
type gateState struct {
	suffix string
	stream string
	// closed is the hysteresis state, and only a successful sample moves it. Staleness is
	// NOT written into it: it is applied when the gate is consulted, so a gate that was
	// open before a measurement outage opens straight away when measurement resumes below
	// the close ratio, rather than waiting for the open ratio as if it had really closed.
	closed bool
	// worst and ratio are the durable with the highest unread ratio and that ratio.
	worst string
	ratio float64
	// sampledAt is the last SUCCESSFUL sample; zero until the first.
	sampledAt time.Time
	// failing edge-triggers the failed-sample warning.
	failing bool
	// measured is the set of durables whose ratio series this gate last exported.
	measured []string
}

// backpressureGates is one manager's set of gates, keyed by suffix.
type backpressureGates struct {
	nmgr *NatsManager
	mu   sync.Mutex
	// now is the clock staleness is judged by; a seam for the staleness test.
	now func() time.Time
	// enabledAreas reports the functional areas the instance deploys; see gatingDurables.
	enabledAreas func() ([]string, error)
	// consumerInfo reads one durable's ConsumerInfo from the broker; a seam so a test can fail
	// that call alone while StreamInfo still answers. It is set before the sampling loop
	// starts and never written afterwards.
	consumerInfo func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error)
	gates        map[string]*gateState
	// loop starts the sampling loop, once, at the first registration.
	loop sync.Once
	// loopDone is closed when the sampling loop returns.
	loopDone chan struct{}
}

// backpressure returns this manager's gates, creating them on first use so that a manager
// assembled as a struct literal (as tests outside this package do) gates like one built by
// NewNatsManager.
func (nmgr *NatsManager) backpressure() *backpressureGates {
	nmgr.bpOnce.Do(func() {
		nmgr.bp = &backpressureGates{
			nmgr:         nmgr,
			now:          time.Now,
			enabledAreas: core.EnabledFunctionalAreas,
			consumerInfo: func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
				return nmgr.js.ConsumerInfo(stream, durable, nats.Context(ctx))
			},
			gates:    map[string]*gateState{},
			loopDone: make(chan struct{}),
		}
	})
	return nmgr.bp
}

// registerBackpressure starts gating suffix's stream in this process and takes one
// synchronous sample, so a writer built on a healthy broker is not born refusing. It is
// idempotent: the connection callback that builds writers and readers can run more than
// once, and a second registration changes nothing.
//
// A failed first sample does NOT fail the caller: it leaves the gate unmeasured, which reads
// as closed until a sample succeeds (fail closed). Failing NewWriter instead would crash-loop
// a service's start on one slow JetStream API answer during a rolling restart.
func (nmgr *NatsManager) registerBackpressure(suffix string) {
	if !streams.AppliesBackpressure(suffix) {
		return
	}
	g := nmgr.backpressure()
	g.mu.Lock()
	_, exists := g.gates[suffix]
	if !exists {
		g.gates[suffix] = &gateState{suffix: suffix, stream: StreamName(nmgr.Microservice.InstanceId, suffix)}
	}
	g.mu.Unlock()
	if exists {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backpressureSampleTimeout)
	defer cancel()
	nmgr.sampleBackpressure(ctx, suffix)
	g.loop.Do(func() { go nmgr.runBackpressure() })
}

// runBackpressure measures every registered gate every backpressureSampleEvery, for as long
// as the manager's connection is open.
//
// 🔑 IT FOLLOWS THE CONNECTION, NOT THE LIFECYCLE, deliberately. The gates are consulted by
// writers and readers, which belong to the connection (see NewNatsManager), and a stop's drain
// stage still publishes on it after the metrics sampler has been cancelled: a gate whose loop
// ended with that sampler would go stale thirty seconds into a long drain and refuse messages
// whose source was already acknowledged. Tying it to the connection also means a manager a test
// assembles by hand, which never runs ExecuteStart, is measured like a real one. The loop ends
// at the first tick after the connection closes, which a stop always reaches.
//
// A tick that cannot measure leaves a gate's last measurement in place, and the gate reads as
// refusing once no sample has succeeded for backpressureStaleAfter.
func (nmgr *NatsManager) runBackpressure() {
	defer close(nmgr.backpressure().loopDone)
	ticker := time.NewTicker(backpressureSampleEvery)
	defer ticker.Stop()
	for range ticker.C {
		if nmgr.nc == nil || nmgr.nc.IsClosed() {
			return
		}
		g := nmgr.backpressure()
		g.mu.Lock()
		suffixes := make([]string, 0, len(g.gates))
		for s := range g.gates {
			suffixes = append(suffixes, s)
		}
		g.mu.Unlock()
		for _, s := range suffixes {
			ctx, cancel := context.WithTimeout(context.Background(), backpressureSampleTimeout)
			nmgr.sampleBackpressure(ctx, s)
			cancel()
		}
	}
}

// Backpressure reports whether suffix's stream is refusing new messages: nil when it is
// not (or never refuses), else a *BackpressureError, which matches ErrStreamBackpressure.
// A gated stream this process has not registered, or has not measured within
// backpressureStaleAfter, reads as refusing.
//
// It never waits on the broker: it reads the last measurement runBackpressure made.
func (nmgr *NatsManager) Backpressure(suffix string) error {
	if !streams.AppliesBackpressure(suffix) {
		return nil
	}
	g := nmgr.backpressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	gs, ok := g.gates[suffix]
	if !ok {
		return &BackpressureError{Stream: StreamName(nmgr.Microservice.InstanceId, suffix), Stale: true}
	}
	switch {
	case gs.sampledAt.IsZero() || g.now().Sub(gs.sampledAt) > backpressureStaleAfter:
		return &BackpressureError{Stream: gs.stream, Stale: true}
	case gs.closed:
		return &BackpressureError{Stream: gs.stream, Durable: gs.worst, Ratio: gs.ratio}
	}
	return nil
}

// sampleBackpressure measures one gated stream: its StreamInfo, then the ConsumerInfo of each
// durable that gates it. It updates the per-durable ratio series, applies the hysteresis and
// logs a transition (Warn on close, Info on open). Any failure leaves the last successful
// measurement in place, so the gate goes stale — and reads as closed — once failures have
// lasted backpressureStaleAfter.
//
// A durable that does not exist yet (its service has not started on this instance) has
// nothing unread and gates nothing. Any other ConsumerInfo error fails the whole sample:
// the gate's answer is the MAXIMUM over its durables, and a maximum over the ones that
// happened to answer can say "open" while the one that did not is full.
func (nmgr *NatsManager) sampleBackpressure(ctx context.Context, suffix string) {
	g := nmgr.backpressure()
	g.mu.Lock()
	gs, ok := g.gates[suffix]
	g.mu.Unlock()
	if !ok {
		return
	}
	worst, ratio, measured, err := nmgr.measureBackpressure(ctx, gs.stream, suffix)

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		// The ratio series are withdrawn, not left at their last value: a gauge frozen at an
		// old reading is exported as a current one (see consumerPending on streamMetrics).
		// The gate itself keeps its last measurement until it goes stale.
		if nmgr.metrics != nil {
			for _, d := range gs.measured {
				nmgr.metrics.forgetUnreadRatio(gs.stream, d)
			}
		}
		gs.measured = nil
		if !gs.failing {
			gs.failing = true
			sampleFailureLog(ctx).Err(err).Str("stream", gs.stream).
				Msg("Could not measure a stream's unread backlog; if this lasts 30 s the stream is treated as full " +
					"and new events are refused")
		}
		return
	}
	if gs.failing {
		gs.failing = false
		log.Info().Str("stream", gs.stream).Msg("Measuring the stream's unread backlog again")
	}
	if nmgr.metrics != nil {
		for _, d := range gs.measured {
			if !slices.Contains(measured, d) {
				nmgr.metrics.forgetUnreadRatio(gs.stream, d)
			}
		}
	}
	gs.measured = measured
	was := gs.closed
	gs.closed = gateNext(gs.closed, ratio)
	gs.worst, gs.ratio = worst, ratio
	gs.sampledAt = g.now()
	switch {
	case gs.closed && !was:
		log.Warn().Str("stream", gs.stream).Str("durable", worst).Float64("unreadRatio", ratio).
			Msg("Refusing new messages on this stream: a consumer's unread backlog is near the stream's ceiling, " +
				"and accepting more would discard messages it has not read")
	case !gs.closed && was:
		log.Info().Str("stream", gs.stream).Float64("unreadRatio", ratio).
			Msg("Accepting new messages on this stream again: the consumer's unread backlog has fallen")
	}
}

// MeasureBackpressureForTesting measures suffix's gate now, synchronously, as a tick of
// runBackpressure does. Production never calls it: a gate is measured every few seconds, and a
// test that fills a stream faster than that needs to say when each measurement happens.
//
// It first waits, for up to backpressureSampleTimeout, until the broker has counted every
// message the stream holds against each durable that gates it (see awaitConsumerCounts), so the
// measurement reflects every publish that has returned; then it measures with a fresh
// backpressureSampleTimeout of its own. When the broker never catches up, or cannot be read, it
// fails the test and does not measure: a measurement of a count the wait could not trust is the
// plausible answer the wait exists to prevent.
//
// It takes a test's T as an interface of the two methods it calls, Helper and Fatalf, so this
// package does not import testing, as SetAckWaitForTesting does.
func (nmgr *NatsManager) MeasureBackpressureForTesting(tb interface {
	Helper()
	Fatalf(format string, args ...any)
}, suffix string) {
	tb.Helper()
	wait, cancelWait := context.WithTimeout(context.Background(), backpressureSampleTimeout)
	err := nmgr.awaitConsumerCounts(wait, suffix)
	cancelWait()
	if err != nil {
		tb.Fatalf("measuring %s's backpressure: %v", suffix, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backpressureSampleTimeout)
	defer cancel()
	nmgr.sampleBackpressure(ctx, suffix)
}

// awaitConsumerCounts waits until the broker has counted every message suffix's stream holds
// against each durable that gates it, or ctx ends.
//
// A PubAck is sent BEFORE the stream signals its consumers, and a consumer's NumPending is a
// counter kept from that signal, so a ConsumerInfo read straight after a publish can miss the
// message just acknowledged. Production does not care (it measures every few seconds, and the
// close ratio leaves a margin for it); a test that asserts the gate closed at exactly one
// message does.
//
// The count it waits for is LastSeq - max(Delivered.Stream, FirstSeq-1): every message after
// the last one delivered. That holds when every message in the stream matches the durable's
// filter and none was deleted from the middle, which is true of every fixture that measures
// through MeasureBackpressureForTesting. When it does not hold the wait times out and says so;
// it does not return a plausible answer.
//
// It reads ConsumerInfo through the gates' consumerInfo seam, the same call a sample makes.
// On ctx's end it reports the last thing it actually observed (a durable still behind, or a
// read that failed for a reason of its own), not the deadline cutting short the read in
// flight, which says nothing about the broker.
func (nmgr *NatsManager) awaitConsumerCounts(ctx context.Context, suffix string) error {
	if !streams.AppliesBackpressure(suffix) {
		return nil
	}
	if nmgr.js == nil {
		return errors.New("messaging: not connected")
	}
	stream := StreamName(nmgr.Microservice.InstanceId, suffix)
	g := nmgr.backpressure()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		behind, err := nmgr.uncountedBy(ctx, g, stream, suffix)
		switch {
		case err == nil && behind == "":
			return nil
		case err == nil:
			last = fmt.Errorf("messaging: %s: the broker had not counted every stored message against its "+
				"consumers before the deadline: %s", stream, behind)
		case ctx.Err() == nil:
			last = fmt.Errorf("messaging: %s: could not read the consumers' counts: %w", stream, err)
		}
		select {
		case <-ctx.Done():
			if last == nil {
				return fmt.Errorf("messaging: %s: could not read the consumers' counts: %w", stream, ctx.Err())
			}
			return last
		case <-tick.C:
		}
	}
}

// uncountedBy names the first gating durable whose NumPending is not yet what the stream holds
// past its delivered sequence, as "<durable>: pending N, want M"; "" when none is. A durable
// that does not exist is skipped, as measureBackpressure skips it.
//
// It compares with !=, not <: an over-count is equally a state the gate must not be measured in.
func (nmgr *NatsManager) uncountedBy(ctx context.Context, g *backpressureGates, stream, suffix string) (string, error) {
	info, err := nmgr.js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		return "", err
	}
	st := info.State
	// Every gated stream declares exactly one BackpressureReader today, so nothing tests that
	// this loop checks each durable rather than only the first: the first stream to declare a
	// second reader should add that test.
	for _, durable := range nmgr.gatingDurables(suffix) {
		ci, cerr := g.consumerInfo(ctx, stream, durable)
		if errors.Is(cerr, nats.ErrConsumerNotFound) {
			continue
		}
		if cerr != nil {
			return "", cerr
		}
		// FirstSeq-1 is the floor because a message DiscardOld has evicted is no longer
		// pending to anyone, delivered or not. A stream that has never held a message reports
		// FirstSeq 0 (and LastSeq 0); the guard only keeps FirstSeq-1 from wrapping there, since
		// want is 0 either way.
		from := ci.Delivered.Stream
		if st.FirstSeq > 0 {
			from = max(from, st.FirstSeq-1)
		}
		want := uint64(0)
		if st.LastSeq > from {
			want = st.LastSeq - from
		}
		if ci.NumPending != want {
			return fmt.Sprintf("%s: pending %d, want %d", durable, ci.NumPending, want), nil
		}
	}
	return "", nil
}

// measureBackpressure is the broker half of a sample: the worst gating durable, its ratio,
// and the durables measured.
func (nmgr *NatsManager) measureBackpressure(ctx context.Context, stream, suffix string) (worst string, ratio float64, measured []string, err error) {
	if nmgr.js == nil {
		return "", 0, nil, errors.New("messaging: not connected")
	}
	info, err := nmgr.js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		return "", 0, nil, err
	}
	for _, durable := range nmgr.gatingDurables(suffix) {
		ci, cerr := nmgr.backpressure().consumerInfo(ctx, stream, durable)
		if errors.Is(cerr, nats.ErrConsumerNotFound) {
			continue
		}
		if cerr != nil {
			return "", 0, nil, cerr
		}
		r := unreadRatio(info.State, info.Config, ci)
		if nmgr.metrics != nil {
			nmgr.metrics.setUnreadRatio(stream, durable, r)
		}
		measured = append(measured, durable)
		if worst == "" || r > ratio {
			worst, ratio = durable, r
		}
	}
	return worst, ratio, measured, nil
}

// gatingDurables names the durables whose backlog gates suffix's stream: one per area the
// stream declares in BackpressureReaders, less any area the instance does not deploy.
//
// 🔴 THE DEPLOYED-AREA FILTER IS WHAT KEEPS A REMOVED SERVICE FROM STOPPING INGEST. A durable
// outlives its service: an instance moved to a narrower profile keeps the durables of the
// areas it dropped, and nothing reads them again, so their backlog grows by every message the
// stream takes. Counted, such a durable would close the gate for good some time after the
// profile change. The deployed set is read from the per-area configuration mount every pod
// carries (core.EnabledFunctionalAreas), the same directory the chart writes one key per
// deployed area into.
//
// When that set cannot be read, or does not include this service's own area (so it is not
// the mount this assumes it is), every declared reader counts: a gate that cannot tell which
// readers are live errs toward protecting all of them.
func (nmgr *NatsManager) gatingDurables(suffix string) []string {
	declared := streams.BackpressureReadersFor(suffix)
	areas := declared
	if enabled, err := nmgr.backpressure().enabledAreas(); err == nil && slices.Contains(enabled, nmgr.Microservice.FunctionalArea) {
		areas = nil
		for _, a := range declared {
			if slices.Contains(enabled, a) {
				areas = append(areas, a)
			}
		}
	}
	out := make([]string, 0, len(areas))
	for _, a := range areas {
		out = append(out, DurableName(nmgr.Microservice.InstanceId, a, suffix))
	}
	return out
}

// gatesItsStream reports whether area's durable on suffix's stream is one whose unread
// backlog makes the stream refuse its writers: area is declared in the stream's
// BackpressureReaders. The reader's side uses it to leave that durable's unread ratio to
// the writers, who measure it for the gate (see consumerUnreadRatio).
//
// It does not apply gatingDurables' deployed-area filter, and need not: a reader is only
// built by a running service, so its area is deployed.
func gatesItsStream(suffix, area string) bool {
	return slices.Contains(streams.BackpressureReadersFor(suffix), area)
}

// bypassesBackpressure reports whether a batch is admitted past a closed gate: every message
// in it carries Message.BypassBackpressure.
func bypassesBackpressure(msgs []Message) bool {
	for i := range msgs {
		if !msgs[i].BypassBackpressure {
			return false
		}
	}
	return len(msgs) > 0
}

// countRefused records n messages a writer refused on stream.
func (nmgr *NatsManager) countRefused(stream string, n int) {
	if nmgr.metrics != nil {
		nmgr.metrics.countRefused(stream, n)
	}
}

// engagedCollector exports jetstream_backpressure_engaged at SCRAPE time, by consulting each
// gate. A gauge the sampler set would read 0 exactly when it matters most: a sample that
// fails leaves the last value in place while the gate, gone stale, refuses everything. Read
// at scrape, the series says what a writer would be told at that moment, staleness included.
type engagedCollector struct {
	desc *prometheus.Desc
	mu   sync.Mutex
	nmgr *NatsManager
}

func (c *engagedCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *engagedCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	nmgr := c.nmgr
	c.mu.Unlock()
	if nmgr == nil {
		return
	}
	g := nmgr.backpressure()
	g.mu.Lock()
	suffixes := make([]string, 0, len(g.gates))
	for s := range g.gates {
		suffixes = append(suffixes, s)
	}
	g.mu.Unlock()
	slices.Sort(suffixes)
	for _, s := range suffixes {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue,
			boolGauge(nmgr.Backpressure(s) != nil), StreamName(nmgr.Microservice.InstanceId, s))
	}
}

// bind attaches the collector to the manager whose gates it reports.
func (c *engagedCollector) bind(nmgr *NatsManager) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.nmgr = nmgr
	c.mu.Unlock()
}
