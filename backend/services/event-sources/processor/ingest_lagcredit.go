// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// THE LAG-CREDITED INGEST GATE (#2163).
//
// This file is the event-sources half of the gate whose decision core is core.LagCreditMeter. The core
// type owns the arithmetic (the lag-credited ceiling bucket, the separate contention bucket, the
// per-stream backlog buckets and the per-tenant phase). This file owns what the core deliberately does
// not know: how far behind each reader is (sourceView), what the broker's clock says relative to ours,
// the reading stage, and the lifecycle of the per-stream state.
//
// It runs only when eventSources.ingestMetering is lagCredit; the default (legacy) gate is untouched.

const (
	// skewSlotWidth and skewSlots give the skew estimator its window: the minimum of the caught-up
	// samples of the last five minutes, kept as thirty ten-second minima so a sample costs O(1).
	skewSlotWidth = 10 * time.Second
	skewSlots     = 30
	// skewClamp bounds the estimate. A broker clock further out than this is not corrected for; it is
	// counted instead (the clamped hook) so an operator sees it.
	skewClamp = 2 * time.Second
	// caughtUpPending is how many messages may still be queued behind a delivery for the reader to be
	// called caught up when it gets that one.
	caughtUpPending = 64
	// unknownLag is the lag of a started reader that has not yet read a message or been seen idle: it
	// could be arbitrarily far behind, so it is credited as much as the cover allows. It resolves on the
	// first read or the first idle probe.
	unknownLag = time.Hour
	// idleProbeEvery is how often a reader that has been quiet is asked the broker whether it is caught up.
	idleProbeEvery = 250 * time.Millisecond
	// idleProbeTimeout bounds the broker round trip of one probe.
	idleProbeTimeout = time.Second
)

// IngestStream names the reader streams the gate meters separately. Each has its own backlog bucket
// and its own lag and skew estimate, so two independently drained timelines never share a mark.
const (
	// StreamCapture is the device-events capture stream (every device publish, QoS 0 and 1).
	StreamCapture = 0
	// StreamStore is the MQTT message store's events durable (QoS 1 under the fail-closed store).
	StreamStore = 1
	// ingestStreams is the number of reader streams.
	ingestStreams = 2
)

var ingestStreamNames = [ingestStreams]string{"capture", "store"}

// LagCreditHooks are the accounting callbacks of the gate; all are optional. They are called on the
// message path, so they must be cheap.
type LagCreditHooks struct {
	// Shed is called for a first delivery the message stage refused, with the route that metered it and
	// the cause.
	Shed func(source, tenant string, stream string, route core.IngestRoute, cause core.IngestCause)
	// ReadingShed is called for a decoded message the reading stage refused (readings is its charge).
	ReadingShed func(source, tenant string, readings int)
	// Credit is called with the lag credit each fresh-route admission was judged against.
	Credit func(stream string, credit time.Duration)
	// Reader is called on each first delivery with the stream's corrected head-of-line age, the skew
	// estimate in force, and whether that estimate was clamped.
	Reader func(stream string, lag, skew time.Duration, clamped bool)
	// Unresolved is called once per message-stage ADMISSION metered at the platform default because the
	// tenant's own ceiling was not known (pending, unreachable, unknown tenant), as the existing gate's
	// message limiters report it through core.WithUnresolvedAdmissions. A refusal, and a resolved or static
	// ceiling, is not reported; nor is the reading stage, whose existing limiters do not report either.
	Unresolved func(core.CeilingSource)
}

// LagCreditIngestConfig builds a LagCreditIngest.
type LagCreditIngestConfig struct {
	// Ceiling is the BASE per-tenant message ceiling (never shed-adjusted).
	Ceiling core.TenantCeilingResolver
	// Reading is the BASE per-tenant reading ceiling (governance.ReadingCeiling over Ceiling).
	Reading core.TenantCeilingResolver
	// Factor maps a tenant to its contention factor in [0,1]: 1 for none (and always for gold), a value
	// in (0,1) to scale its allowance, 0 to refuse its fresh traffic outright.
	Factor func(tenant string) float64
	// Clock replaces time.Now (the stall-replay harness). Nil is time.Now.
	Clock func() time.Time
	// PhaseCalm is how long a tenant's stream stays calm before it leaves the backlog phase; zero is the default.
	PhaseCalm time.Duration
	Hooks     LagCreditHooks
}

// LagCreditIngest is the lag-credited gate shared by every source: the message-stage meter, the reading
// stage, and the estimate of each reader stream's lag and skew.
type LagCreditIngest struct {
	clock       func() time.Time
	meter       *core.LagCreditMeter
	readCeiling *core.LagCreditLimiter
	// readContention is the reading stage's contention bucket, over the reading ceiling SHED by the tenant's
	// contention factor and then floored as every reading ceiling is (see contendedReading), charged for
	// every message the message stage contended.
	readContention *core.LagCreditLimiter
	readBacklog    [ingestStreams]*core.TenantRateLimiter
	base           core.TenantCeilingResolver
	factor         func(tenant string) float64
	views          [ingestStreams]*sourceView
	hooks          LagCreditHooks
}

// NewLagCreditIngest builds the gate. A missing ceiling or factor panics: a gate that cannot resolve a
// ceiling cannot meter what is routed to it.
func NewLagCreditIngest(cfg LagCreditIngestConfig) *LagCreditIngest {
	if cfg.Ceiling == nil || cfg.Reading == nil || cfg.Factor == nil {
		panic("processor.NewLagCreditIngest: a message ceiling, a reading ceiling and a contention factor are required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	lc := &LagCreditIngest{
		clock:  clock,
		base:   cfg.Ceiling,
		factor: cfg.Factor,
		hooks:  cfg.Hooks,
		meter: core.NewLagCreditMeter(core.LagCreditMeterConfig{
			Streams: ingestStreams, Ceiling: cfg.Ceiling, PhaseCalm: cfg.PhaseCalm, Clock: clock,
		}),
		readCeiling:    core.NewLagCreditLimiter(cfg.Reading, ingestStreams, core.WithLagCreditClock(clock)),
		readContention: core.NewLagCreditLimiter(contendedReading(cfg.Reading, cfg.Factor), ingestStreams, core.WithLagCreditClock(clock)),
	}
	for i := range lc.views {
		lc.views[i] = newSourceView()
		lc.readBacklog[i] = core.NewTenantRateLimiter(cfg.Reading, core.WithClock(clock))
	}
	return lc
}

// contendedReading is the reading ceiling a tenant is held to under contention, built the way the existing
// gate builds its live reading limiter (governance.ReadingCeiling over the shed-adjusted ceiling): the rate
// and burst scaled by the factor (governance.Limits.Shed, burst floored at one), and then the burst raised
// to the most readings one event may carry. Scaling an already floored reading burst instead would hold a
// throttled tenant below one event's worth of readings, refusing its large events at any frequency.
// Given the reading ceiling over the base (governance.ReadingCeiling(base)), this equals the existing
// gate's ReadingCeiling(shed(base)): the floor is idempotent, and below it both are the floor.
func contendedReading(reading core.TenantCeilingResolver, factor func(string) float64) core.TenantCeilingResolver {
	return governance.ReadingCeiling(func(tenant string) core.TenantCeiling {
		c := reading(tenant)
		if f := factor(tenant); f < 1 {
			l := governance.Limits{RatePerSecond: c.RatePerSecond, Burst: c.Burst}.Shed(f)
			c.RatePerSecond, c.Burst = l.RatePerSecond, l.Burst
		}
		return c
	})
}

// ingestMeter is the metering decision of one message, made once at the message stage and carried to the
// reading stage in rawMessage so the two stages cannot route one message to different buckets.
type ingestMeter struct {
	lc *LagCreditIngest
	// stream is the reader stream the message came from, or -1 for a message with no stream.
	stream int
	route  core.IngestRoute
	// stamp is the broker's append time; sent is that stamp with the stream's skew taken back out (the
	// point on the send timeline a backlog message is charged at).
	stamp, sent time.Time
	// stagedAt is the pod time of the message stage, for the delay the reading stage lends.
	stagedAt time.Time
	// redelivery marks a message that paid on delivery 1; the reading stage admits it uncharged.
	redelivery bool
	// contended says the message stage applied the overload bands to this message (factor below one, and
	// the message read within its cover: every fresh or live message, and a young one the phase keeps on
	// the backlog route), so the reading stage charges its readings in the contention bucket too.
	contended bool
}

// active reports whether this message was metered by the lag-credited gate.
func (m ingestMeter) active() bool { return m.lc != nil }

// admit meters one gateway first delivery at the message stage. msg is the delivery; the verdict is
// returned with the decision the reading stage will follow.
func (lc *LagCreditIngest) admit(source, tenant string, stream int, msg messaging.Message) (ingestMeter, bool) {
	now := lc.clock()
	stamped := !msg.AppendTime.IsZero()
	src := stream
	var clamped bool
	if stamped {
		var alpha, skew time.Duration
		alpha, skew, clamped = lc.views[stream].observe(now, msg.AppendTime, msg.StreamSeq, msg.NumPending)
		if lc.hooks.Reader != nil {
			lc.hooks.Reader(ingestStreamNames[stream], alpha, skew, clamped)
		}
	} else {
		src = -1
	}
	srcs := lc.sources(now)
	factor := lc.factor(tenant)
	d := lc.meter.Admit(core.MeterRequest{
		Tenant: tenant, N: 1, Source: src, Stamp: msg.AppendTime, Sources: srcs, Factor: factor,
	})
	m := ingestMeter{lc: lc, stream: src, route: d.Route, stamp: msg.AppendTime, stagedAt: now}
	// The meter contends every message read within its own cover, in or out of the backlog phase; only a
	// true backlog (older than the cover) is exempt. The reading stage follows the same rule.
	if factor < 1 {
		age := max(now.Sub(msg.AppendTime)-srcs[stream].Skew, 0)
		m.contended = d.Route != core.RouteBacklog || age <= core.LagCreditMax(lc.base(tenant))
	}
	if stamped {
		m.sent = msg.AppendTime.Add(srcs[stream].Skew)
	}
	streamName := ""
	if stream >= 0 && stream < ingestStreams {
		streamName = ingestStreamNames[stream]
	}
	if lc.hooks.Credit != nil && d.Route != core.RouteBacklog {
		lc.hooks.Credit(streamName, d.Credit)
	}
	if !d.Admitted {
		if lc.hooks.Shed != nil {
			lc.hooks.Shed(source, tenant, streamName, d.Route, d.Cause)
		}
		return m, false
	}
	lc.countUnresolved(tenant)
	return m, true
}

// admitLive meters a message that has no stream and no stamp (external MQTT, a presence transition): the
// same ceiling and contention buckets as the fresh route, at pod time, with no lag credit of its own.
func (lc *LagCreditIngest) admitLive(source, tenant string) bool {
	now := lc.clock()
	d := lc.meter.Admit(core.MeterRequest{
		Tenant: tenant, N: 1, Source: -1, Sources: lc.sources(now), Factor: lc.factor(tenant),
	})
	if !d.Admitted && lc.hooks.Shed != nil {
		lc.hooks.Shed(source, tenant, "", d.Route, d.Cause)
	}
	if d.Admitted {
		lc.countUnresolved(tenant)
	}
	return d.Admitted
}

// countUnresolved reports one admission to the Unresolved hook when the tenant's ceiling is the platform
// default for want of its own (the sources core.WithUnresolvedAdmissions counts).
func (lc *LagCreditIngest) countUnresolved(tenant string) {
	if lc.hooks.Unresolved == nil {
		return
	}
	switch src := lc.base(tenant).Source; src {
	case core.CeilingPending, core.CeilingUnreachable, core.CeilingUnknownTenant:
		lc.hooks.Unresolved(src)
	}
}

// readings charges a decoded message's readings at the reading stage, on the route the message stage chose.
// A message the message stage contended is first charged in the contention bucket (the reading ceiling
// shed by the tenant's factor, see contendedReading), as the existing gate's live reading limiter carries
// the shed: a tenant of large events passes the message stage and is still throttled here. Then the
// ceiling: a backlog message at its corrected stamp in its stream's own bucket, never shed (the existing
// backlog reading limiter is not either); a fresh message at pod time with the lag credit plus the delay
// it sat between the two stages; a live message (no stream) at pod time with the delay only.
func (m ingestMeter) readings(source, tenant string, readings int) bool {
	lc := m.lc
	if m.redelivery || readings <= 0 {
		return true
	}
	now := lc.clock()
	delay := max(now.Sub(m.stagedAt), 0)
	cover := core.LagCreditMax(lc.base(tenant))
	// A live message has no stamp (the zero time), which the limiter reads as "feeds no last stamp".
	req := core.LagCreditRequest{
		Tenant: tenant, N: readings, Scale: 1, Source: m.stream, Stamp: m.stamp, Sources: lc.sources(now),
		Delay: delay, CreditCap: cover + delay,
	}
	ok := true
	if m.contended {
		ok = lc.readContention.Admit(req).Admitted
	}
	if ok {
		if m.route == core.RouteBacklog {
			ok = lc.readBacklog[m.stream].AllowNAt(tenant, m.sent, readings)
		} else {
			ok = lc.readCeiling.Admit(req).Admitted
		}
	}
	if !ok && lc.hooks.ReadingShed != nil {
		lc.hooks.ReadingShed(source, tenant, readings)
	}
	return ok
}

// readingsLive charges a stream-less message's readings (external MQTT) at pod time with no credit, at the
// tenant's contention factor now. A factor of 0 is the hard drop, as the existing gate's shed ceiling of
// zero refuses every reading.
func (lc *LagCreditIngest) readingsLive(source, tenant string, readings int) bool {
	factor := lc.factor(tenant)
	if factor == 0 && readings > 0 {
		if lc.hooks.ReadingShed != nil {
			lc.hooks.ReadingShed(source, tenant, readings)
		}
		return false
	}
	return ingestMeter{lc: lc, stream: -1, route: core.RouteLive, stagedAt: lc.clock(), contended: factor < 1}.readings(source, tenant, readings)
}

// sources is the view of every reader stream now.
func (lc *LagCreditIngest) sources(now time.Time) []core.LagCreditSource {
	out := make([]core.LagCreditSource, ingestStreams)
	for i, v := range lc.views {
		out[i] = v.view(now)
	}
	return out
}

// view exposes one stream's estimator (tests, and the source that owns the stream).
func (lc *LagCreditIngest) view(stream int) *sourceView { return lc.views[stream] }

// Meter is the decision core, exposed for the harness.
func (lc *LagCreditIngest) Meter() *core.LagCreditMeter { return lc.meter }

// sourceView is the platform's estimate of ONE reader stream: the broker's clock offset from ours and
// how far behind the reader is. It is fed by the stream's read loop and read by every stage that
// meters a message from it, so it is guarded by a mutex.
//
// # Skew
//
// A sample is now − AppendTime of a FIRST delivery whose stream sequence is higher than any seen and which
// the reader got while caught up (few messages queued behind it), or whose stamp is in this pod's future
// (a negative sample is valid whatever the reader's state: no message is younger than nothing). The estimate is the minimum of the
// samples of the last five minutes, held (never dropped to "unknown") through a stretch with no caught-up
// sample, and clamped to ±2 s. A positive skew means the broker's clock is behind ours (plus the base read
// latency); a negative one means it is ahead. The corrected age of a stamp is now − stamp − skew.
//
// Redeliveries are never samples and never move the lag: a redelivery carries an old stamp and is out of
// stream order.
//
// # Lag
//
// The lag of a stream is an upper bound on the corrected age of every message the reader has yet to read.
// By stream order the next unread message was appended at or after the last one read, so its age at
// pod time now is at most (now − readAt) + readAlpha, where readAlpha is the corrected age of the last
// one read. That also covers the message being admitted, which is never younger than readAlpha.
//
// 🔴 THE BOUND DOES NOT SHORTEN WHEN THE DELIVERY SAID NOTHING WAS QUEUED. NumPending describes the
// moment the broker delivered the message, and the pod may have held it for a while before reading it
// (a starved read loop, a full decode queue), so "nothing was queued then" does not say nothing was
// appended since. Only the idle probe (caughtUp) can reset the bound, and it does so as of the moment it
// asked the broker. A reader that is started but has neither read nor been seen idle is unknown and
// credited as the cover allows.
type sourceView struct {
	mu sync.Mutex

	started bool

	slots   [skewSlots]skewSlot
	curID   int64
	winMin  time.Duration
	haveWin bool
	held    time.Duration
	haveHld bool
	lastSeq uint64

	seen        bool
	readAt      time.Time
	readAlpha   time.Duration
	readPending uint64
	reads       uint64
}

type skewSlot struct {
	id  int64
	min time.Duration
}

func newSourceView() *sourceView { return &sourceView{} }

// start marks the stream as being read by this process. Before it, the stream has no reader to be behind,
// so its lag is zero (the message-store source, for one, does not run unless the fail-closed store is on).
func (v *sourceView) start() {
	v.mu.Lock()
	v.started = true
	v.mu.Unlock()
}

// stop marks the stream as no longer read by this process (its source stopped): it has no reader to be
// behind, so its lag is zero again. What it had read is forgotten, so a later start is unknown until it
// reads or is seen idle, rather than measured from a read before the stop. The skew estimate is kept: it
// describes the broker's clock, not the reader.
func (v *sourceView) stop() {
	v.mu.Lock()
	v.started, v.seen, v.readAt, v.readAlpha, v.readPending = false, false, time.Time{}, 0, 0
	v.mu.Unlock()
}

// readCount is the number of first deliveries observed (the idle probe's no-read-in-between check).
func (v *sourceView) readCount() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.reads
}

func (v *sourceView) lastRead() (time.Time, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.readAt, v.seen
}

// observe records one stamped FIRST delivery read at pod time now and returns its corrected age, the
// skew estimate in force and whether that estimate was clamped.
func (v *sourceView) observe(now, appended time.Time, seq, pending uint64) (alpha, skew time.Duration, clamped bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	d := now.Sub(appended)
	if seq > v.lastSeq {
		v.lastSeq = seq
		// A delivery read while caught up measures the skew (plus the base read latency). One that is not
		// still bounds it from above when its stamp is in OUR future (d < 0): the message cannot be younger
		// than nothing, so the broker's clock is at least that far ahead. A positive d from a backlog is
		// the backlog's age, not the skew, and is not a sample.
		if pending <= caughtUpPending || d < 0 {
			v.sampleLocked(now, d)
		}
	}
	skew, clamped = v.skewLocked(now)
	alpha = max(d-skew, 0)
	v.seen, v.readAt, v.readAlpha, v.readPending = true, now, alpha, pending
	v.reads++
	return alpha, skew, clamped
}

func (v *sourceView) sampleLocked(now time.Time, d time.Duration) {
	id := now.UnixNano() / int64(skewSlotWidth)
	s := &v.slots[id%skewSlots]
	if s.id != id {
		*s = skewSlot{id: id, min: d}
	} else if d < s.min {
		s.min = d
	}
	if v.curID != id {
		v.refreshWindowLocked(id) // folds the slot just written
	}
	if !v.haveWin || d < v.winMin {
		v.winMin, v.haveWin = d, true
	}
	v.held, v.haveHld = v.winMin, true
}

// refreshWindowLocked recomputes the window minimum when the current slot has moved on.
func (v *sourceView) refreshWindowLocked(id int64) {
	if v.curID == id {
		return
	}
	v.curID = id
	v.haveWin = false
	for _, s := range v.slots {
		if s.id == 0 || s.id <= id-skewSlots {
			continue
		}
		if !v.haveWin || s.min < v.winMin {
			v.winMin, v.haveWin = s.min, true
		}
	}
	if v.haveWin {
		v.held, v.haveHld = v.winMin, true
	}
}

func (v *sourceView) skewLocked(now time.Time) (time.Duration, bool) {
	v.refreshWindowLocked(now.UnixNano() / int64(skewSlotWidth))
	if !v.haveHld {
		return 0, false
	}
	switch {
	case v.held > skewClamp:
		return skewClamp, true
	case v.held < -skewClamp:
		return -skewClamp, true
	}
	return v.held, false
}

// caughtUp records that the reader was seen caught up at pod time at (an idle probe). It never moves the
// last read backwards.
func (v *sourceView) caughtUp(at time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen && !at.After(v.readAt) {
		return
	}
	v.seen, v.readAt, v.readAlpha, v.readPending = true, at, 0, 0
}

// view is the stream as the limiter sees it at pod time now.
func (v *sourceView) view(now time.Time) core.LagCreditSource {
	v.mu.Lock()
	defer v.mu.Unlock()
	skew, _ := v.skewLocked(now)
	if !v.started {
		return core.LagCreditSource{Skew: skew}
	}
	if !v.seen {
		return core.LagCreditSource{Lag: unknownLag, Skew: skew}
	}
	return core.LagCreditSource{Lag: max(now.Sub(v.readAt), 0) + v.readAlpha, Skew: skew}
}

// ---- the gate as a RateGate and ReadingGate ----

// NewLagCreditRateGate is the RateGate in lagCredit mode. Messages the gateway sources meter themselves
// arrive as OriginPreMetered and pass (after the lifecycle refusal composed in front of this gate). A
// message from an authenticated source that has no stream and no stamp (external MQTT, a presence
// transition) is metered here by the lag-credited gate at pod time. Everything else (HTTP, integrations,
// untrusted names, and a stamped time from any other caller) goes to the existing gate unchanged.
func NewLagCreditRateGate(lc *LagCreditIngest, legacy RateGate) RateGate {
	if lc == nil || legacy == nil {
		panic("processor.NewLagCreditRateGate: the lag-credited gate and the gate it delegates to are required")
	}
	return func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin) bool {
		if redelivery {
			return true
		}
		switch {
		case origin == OriginPreMetered:
			return true
		case origin == OriginAuthenticated && sentAt.IsZero():
			return lc.admitLive(source, tenant)
		}
		return legacy(source, tenant, sentAt, redelivery, origin)
	}
}

// NewLagCreditReadingGate is the ReadingGate in lagCredit mode, with the same routing as
// NewLagCreditRateGate. A gateway message does not come through it (it is charged by its own
// ingestMeter); an external-MQTT message does.
func NewLagCreditReadingGate(lc *LagCreditIngest, legacy ReadingGate) ReadingGate {
	if lc == nil || legacy == nil {
		panic("processor.NewLagCreditReadingGate: the lag-credited gate and the gate it delegates to are required")
	}
	return func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin, readings int) bool {
		if redelivery || readings <= 0 {
			return true
		}
		if origin == OriginAuthenticated && sentAt.IsZero() {
			return lc.readingsLive(source, tenant, readings)
		}
		return legacy(source, tenant, sentAt, redelivery, origin, readings)
	}
}

// ---- the idle probe ----

// backlogProber is a reader that can ask the broker how many messages are queued for its consumer.
type backlogProber interface {
	Backlog(ctx context.Context) (pending, ackPending uint64, err error)
}

// probeIdle runs until ctx ends. A stream that has produced nothing for a while is either idle or
// working through something its reader cannot see; the broker knows which. When the consumer has nothing
// queued, the reader is caught up as of the moment the question was asked and its lag restarts from zero,
// so a quiet stream does not carry an ever-growing lag (and credit) of silence.
//
// The answer is used only if nothing is queued or in flight at the broker (pending and ackPending both 0,
// the second covering a fetched batch the loop has not yet read out of its buffer) and the read loop did
// is not inside handle() when the answer arrives, and a read that overlapped the question wins (see caughtUp).
func (es *GatewayJetStreamSource) probeIdle(ctx context.Context, lc *LagCreditIngest, stream int, prober backlogProber) {
	v := lc.views[stream]
	t := time.NewTicker(idleProbeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := lc.clock()
		if es.inHandle.Load() {
			continue
		}
		if at, seen := v.lastRead(); seen && now.Sub(at) < idleProbeEvery {
			continue
		}
		asked := lc.clock()
		pctx, cancel := context.WithTimeout(ctx, idleProbeTimeout)
		pending, ackPending, err := prober.Backlog(pctx)
		cancel()
		// 🔴 BOTH COUNTS. pending is what the broker has not yet delivered; ackPending is what it has
		// delivered and not yet had acknowledged, which includes a batch this process fetched and has not
		// read out of its buffer. A starved read loop can sit on such a batch while pending is 0, and the
		// stream is not caught up. A stream whose acknowledgements never drain (a peer's unacked messages
		// during a rolling update) stays unknown, which credits more, never less.
		if err != nil || pending != 0 || ackPending != 0 {
			continue
		}
		// A read that happened while the question was in flight is stamped later than `asked`, and caughtUp
		// never moves the last read backwards, so such an answer changes nothing.
		if es.inHandle.Load() {
			continue
		}
		v.caughtUp(asked)
	}
}

// UseLagCredit makes this source meter itself with the lag-credited gate on the given stream. It is called
// before Start, and only in lagCredit mode.
func (es *GatewayJetStreamSource) UseLagCredit(lc *LagCreditIngest, stream int) {
	if lc == nil || stream < 0 || stream >= ingestStreams {
		panic(fmt.Sprintf("processor.UseLagCredit: a gate and a stream in [0,%d) are required", ingestStreams))
	}
	es.lc, es.lcStream = lc, stream
}

// LagCredit reports the lag-credited gate this source meters itself with, and the stream it is metered as;
// nil (and -1) in legacy mode, where the source asks the composed gate for every decision.
func (es *GatewayJetStreamSource) LagCredit() (*LagCreditIngest, int) {
	if es.lc == nil {
		return nil, -1
	}
	return es.lc, es.lcStream
}

// startLagCredit marks the stream started and, when the reader can be asked, starts the idle probe.
func (es *GatewayJetStreamSource) startLagCredit(ctx context.Context) {
	es.lc.views[es.lcStream].start()
	prober, ok := es.reader.(backlogProber)
	if !ok {
		log.Warn().Str("source", es.Id).Msg("The reader cannot report its broker backlog; a quiet stream's lag is estimated from its last read alone, which only errs lenient.")
		return
	}
	go es.probeIdle(ctx, es.lc, es.lcStream, prober)
}
