// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
)

// THE STALL-REPLAY HARNESS (#2163).
//
// A deterministic, in-process replay of what a platform stall does to the ingest gate. Everything
// runs on a virtual clock injected into both limiter types and into the routing decision
// (newMeterAtClock), so a run is exact and takes milliseconds, and nothing here sleeps.
//
// A scenario scripts, per tenant, the TRUE send times; per reader source, the broker's clock (an
// offset, plus steps for a leader change or an NTP correction), the stalls of the reader and the
// delay of the decode stage; and the contention level. The harness stamps each message the way the
// broker does (send time plus the source's offset at that moment), has the source's reader deliver it
// when it gets to it (FIFO per source, held across a stall), and drives the REAL gates through the
// same calls handle() and the decode worker make:
//
//	meterAt  := meter(tenant, stamp)                       // decided once, at the message stage
//	admitted := RefuseDeletedTenants(NewRateGate(...))(...)  // the message stage
//	         && ReadingGate(...)                             // the reading stage, after the decode delay
//
// The ORACLE is the one thing the gate never sees: an offline token bucket over the TRUE send
// sequence, which defines what a tenant within its ceiling is. The assertions are accepted-set
// assertions, in the shape the review adopted: every message the broker acknowledged is either
// stored or counted shed, and the question put to each row is which ones were shed and whether the
// tenant earned that.
//
//   - a compliant tenant (its sends conform to the oracle) is shed ZERO;
//   - an over-ceiling tenant is shed within a burst-and-cover of the oracle's excess per stall;
//   - a contended tenant is shed on fresh traffic only, never on a backlog older than its cover,
//     and the hard drop (factor 0) is a boolean refusal of fresh traffic;
//   - a gold tenant is never shed by contention.
//
// # Three arms
//
//   - LEGACY runs the gate on main. Its rows that violate the expectations above are the defects of
//     #2163, and they are pinned: a row marked legacyLoses must keep failing here until the lagCredit
//     gate replaces the code under test, and a row not marked must keep passing. RATEGATE_REPLAY_STRICT=1
//     runs legacy against the strict expectations and is RED on exactly the marked rows.
//   - MODEL runs core.LagCreditMeter fed the exact lag and skew the harness knows. It is the library
//     doing what the spec says with perfect measurement; it is not the gateway.
//   - GATEWAY is the real lagCredit wiring (LagCreditIngest: the measured lag and skew, the meter, the
//     reading stage, the same calls handle() and the decode worker make), judged by the same rows and the
//     same oracle. It is in rate_gate_replay_gateway_test.go.

// ---- the scenario DSL ----

type replayKind int

const (
	// kindCompliant: the tenant's true sends conform to the ceiling. Nothing may be shed for a stall.
	kindCompliant replayKind = iota
	// kindOver: the tenant sends above its ceiling and is shed by its excess.
	kindOver
)

type replayTenant struct {
	name  string
	kind  replayKind
	class governance.ShedClass
	// rate and burst override the scenario's ceiling for this tenant (a mixed-tier row); zero keeps it.
	rate  float64
	burst int
	sends []time.Time
	// source says which reader source carries message i: 0.. an index into sources, -1 external MQTT
	// (no stamp, metered at arrival). Nil means source 0 for every message.
	source func(i int) int
}

type replayStallWindow struct{ from, to time.Duration } // offsets from the scenario start

type replayStep struct {
	at    time.Duration // messages sent at or after this offset carry the delta
	delta time.Duration
}

type replaySource struct {
	offset      time.Duration // the broker's clock minus true time
	steps       []replayStep
	base        time.Duration // the reader's latency when it is keeping up
	stalls      []replayStallWindow
	decodeDelay time.Duration // the delay between the message stage and the reading stage
}

type replayScenario struct {
	id, about string
	rate      float64
	burst     int
	sources   []replaySource
	tenants   []replayTenant
	// level is the contention level in force (0 = none).
	level int
	// restartAt, when non-zero, rebuilds every limiter at that read-time offset: a pod restart.
	restartAt time.Duration
	// episodes is how many distinct stalls an over-ceiling tenant lives through (widens its tolerance).
	episodes int
	// legacyLoses marks a row on which the gate on main violates the expectations: the #2163 defect.
	legacyLoses bool
	// modelSkip names why the model arm cannot judge a row, when it cannot.
	modelSkip string
	// gatewayResidual names a documented residual of the MEASURED gate that this row exercises: the
	// gateway arm is expected to violate the expectations here (the model, told the truth, does not), and
	// the arm fails if it ever stops, so the residual cannot be fixed or worsened unnoticed.
	gatewayResidual string
	// redeliverEvery, when positive, interleaves a redelivery of an earlier message after every Nth message.
	redeliverEvery int
}

func at(offset time.Duration) time.Time { return replayEpoch.Add(offset) }

var replayEpoch = time.Unix(1_900_000_000, 0)

// steady returns sends at rate/s over [from, to).
func steady(rate float64, from, to time.Duration) []time.Time {
	var out []time.Time
	step := time.Duration(float64(time.Second) / rate)
	for t := from; t < to; t += step {
		out = append(out, at(t))
	}
	return out
}

// burstOf returns n sends spread over a millisecond at offset.
func burstOf(n int, offset time.Duration) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = at(offset).Add(time.Duration(i) * time.Microsecond)
	}
	return out
}

func join(parts ...[]time.Time) []time.Time {
	var out []time.Time
	for _, p := range parts {
		out = append(out, p...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// ---- the replay ----

type replayMsg struct {
	tenant  int
	index   int
	src     int
	send    time.Time
	stamp   time.Time
	read    time.Time
	readAge time.Duration // read − send: how late the platform delivered it
}

type replayOutcome struct {
	admitted bool
	cause    core.IngestCause // the model arm only
	route    core.IngestRoute // the model arm only
}

type replayResult struct {
	sc       *replayScenario
	msgs     []replayMsg
	outcomes []replayOutcome
	// redelivered counts the redeliveries interleaved into the run, and redeliveriesRefused the ones the gate refused.
	redelivered, redeliveriesRefused int
}

type replayClock struct{ t time.Time }

func (c *replayClock) now() time.Time { return c.t }

func (sc *replayScenario) offsetOf(k int, send time.Time) time.Duration {
	if k < 0 {
		return 0
	}
	o := sc.sources[k].offset
	for _, st := range sc.sources[k].steps {
		if !send.Before(at(st.at)) {
			o += st.delta
		}
	}
	return o
}

func (sc *replayScenario) readTime(k int, send time.Time) time.Time {
	if k < 0 {
		return send
	}
	r := send.Add(sc.sources[k].base)
	for _, st := range sc.sources[k].stalls {
		if !r.Before(at(st.from)) && r.Before(at(st.to)) {
			r = at(st.to)
		}
	}
	return r
}

// ceilOf is a tenant's base ceiling: its own when the row gives it one, the scenario's otherwise.
func (sc *replayScenario) ceilOf(ti int) (float64, int) {
	tn := sc.tenants[ti]
	if tn.rate > 0 && tn.burst > 0 {
		return tn.rate, tn.burst
	}
	return sc.rate, sc.burst
}

// resolver is the base ceiling resolver every arm meters against.
func (sc *replayScenario) resolver() core.TenantCeilingResolver {
	return func(tenant string) core.TenantCeiling {
		for ti, tn := range sc.tenants {
			if tn.name == tenant {
				r, b := sc.ceilOf(ti)
				return core.TenantCeiling{RatePerSecond: r, Burst: b, Source: core.CeilingStatic}
			}
		}
		return core.TenantCeiling{RatePerSecond: sc.rate, Burst: sc.burst, Source: core.CeilingStatic}
	}
}

// coverOf is the time a tenant's base burst covers: the cutoff the legacy gate and the lag credit share.
func (sc *replayScenario) coverOf(ti int) time.Duration {
	r, b := sc.ceilOf(ti)
	return core.LagCreditMax(core.TenantCeiling{RatePerSecond: r, Burst: b})
}

// cover is the scenario default tenant's cover.
func (sc *replayScenario) cover() time.Duration {
	return core.LagCreditMax(core.TenantCeiling{RatePerSecond: sc.rate, Burst: sc.burst})
}

// messages builds every message of the scenario and the order the readers deliver them in:
// by read time, with the sources' ties broken by source index so a run is deterministic.
func (sc *replayScenario) messages() []replayMsg {
	var msgs []replayMsg
	for ti, tn := range sc.tenants {
		for i, send := range tn.sends {
			k := 0
			if tn.source != nil {
				k = tn.source(i)
			}
			m := replayMsg{tenant: ti, index: i, src: k, send: send, read: sc.readTime(k, send)}
			if k >= 0 {
				m.stamp = send.Add(sc.offsetOf(k, send))
			}
			m.readAge = m.read.Sub(m.send)
			msgs = append(msgs, m)
		}
	}
	// FIFO per source requires processing each source's messages in send order; across sources
	// and tenants the order is by read time.
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i], msgs[j]
		if !a.read.Equal(b.read) {
			return a.read.Before(b.read)
		}
		if a.src != b.src {
			return a.src < b.src
		}
		if !a.send.Equal(b.send) {
			return a.send.Before(b.send)
		}
		return a.tenant < b.tenant
	})
	return msgs
}

// replayGate is an arm: it judges each first delivery, and sees the harness clock.
type replayGate interface {
	// restart discards every piece of limiter state (a pod restart).
	restart()
	// message judges one first delivery at the message stage.
	message(m replayMsg) replayOutcome
	// reading judges the same message at the reading stage, decodeDelay later, when the message was admitted.
	reading(m replayMsg, delay time.Duration) bool
}

func (sc *replayScenario) tenantName(i int) string { return sc.tenants[i].name }

func (sc *replayScenario) factorOf(ti int) float64 {
	return governance.ShedFactor(sc.tenants[ti].class, sc.level)
}

// run replays the scenario through an arm.
func (sc *replayScenario) run(newGate func(sc *replayScenario, clk *replayClock) replayGate) *replayResult {
	clk := &replayClock{t: replayEpoch}
	gate := newGate(sc, clk)
	msgs := sc.messages()
	res := &replayResult{sc: sc, msgs: msgs, outcomes: make([]replayOutcome, len(msgs))}

	// Message-stage events and reading-stage events, merged by time. A reading event exists only for a
	// message the message stage admitted.
	type event struct {
		at      time.Time
		reading bool
		msg     int
	}
	var pending []event // reading events, kept sorted
	restarted := sc.restartAt == 0
	redeliverer, _ := gate.(interface{ redeliver(replayMsg) bool })
	process := func(upTo time.Time) {
		for len(pending) > 0 && !pending[0].at.After(upTo) {
			e := pending[0]
			pending = pending[1:]
			clk.t = e.at
			m := msgs[e.msg]
			if !gate.reading(m, sc.sources[m.src].decodeDelay) {
				res.outcomes[e.msg].admitted = false
			}
		}
	}
	for i, m := range msgs {
		process(m.read)
		clk.t = m.read
		if !restarted && !m.read.Before(at(sc.restartAt)) {
			gate.restart()
			restarted = true
		}
		out := gate.message(m)
		res.outcomes[i] = out
		if sc.redeliverEvery > 0 && redeliverer != nil && i%sc.redeliverEvery == 0 && i >= 3 {
			// The broker re-offers a message it delivered earlier, interleaved into the drain.
			res.redelivered++
			if !redeliverer.redeliver(msgs[i-3]) {
				res.redeliveriesRefused++
			}
		}
		if out.admitted && m.src >= 0 && sc.sources[m.src].decodeDelay > 0 {
			e := event{at: m.read.Add(sc.sources[m.src].decodeDelay), reading: true, msg: i}
			pending = append(pending, e)
			sort.SliceStable(pending, func(a, b int) bool { return pending[a].at.Before(pending[b].at) })
		}
	}
	process(replayEpoch.Add(24 * time.Hour))
	return res
}

// ---- arm 1: the gate on main ----

type legacyGate struct {
	sc    *replayScenario
	clk   *replayClock
	gate  RateGate
	read  ReadingGate
	meter MeterAt
	// meterAt is the routing decision the message stage made for each message, carried to the reading
	// stage exactly as the Meter rides rawMessage: decided ONCE.
	meterAt map[[2]int]time.Time
}

func newLegacyGate(sc *replayScenario, clk *replayClock) replayGate {
	g := &legacyGate{sc: sc, clk: clk}
	g.restart()
	return g
}

func (g *legacyGate) restart() {
	sc := g.sc
	base := sc.resolver()
	class := func(tenant string) governance.ShedClass {
		for _, tn := range sc.tenants {
			if tn.name == tenant {
				return tn.class
			}
		}
		return governance.ShedBronze
	}
	// main's shedAdjusted: the contention factor rides on the LIVE, HTTP and integration limiters
	// and never on the backlog limiter.
	shed := func(b core.TenantCeilingResolver) core.TenantCeilingResolver {
		return func(tenant string) core.TenantCeiling {
			c := b(tenant)
			if sc.level <= 0 {
				return c
			}
			l := governance.Limits{RatePerSecond: c.RatePerSecond, Burst: c.Burst}.Shed(governance.ShedFactor(class(tenant), sc.level))
			return core.TenantCeiling{RatePerSecond: l.RatePerSecond, Burst: l.Burst, Source: c.Source}
		}
	}
	mk := func(r core.TenantCeilingResolver) *core.TenantRateLimiter {
		return core.NewTenantRateLimiter(r, core.WithClock(g.clk.now))
	}
	live := mk(shed(base))
	g.gate = RefuseDeletedTenants(func(string) bool { return false },
		NewRateGate(live, mk(base), mk(shed(base)), mk(shed(base)), nil), nil)
	g.read = NewReadingGate(mk(governance.ReadingCeiling(shed(base))), mk(governance.ReadingCeiling(base)),
		mk(governance.ReadingCeiling(shed(base))), mk(governance.ReadingCeiling(shed(base))), nil)
	g.meter = newMeterAtClock(live, g.clk.now)
	g.meterAt = map[[2]int]time.Time{}
}

func (g *legacyGate) message(m replayMsg) replayOutcome {
	tenant := g.sc.tenantName(m.tenant)
	var meterAt time.Time
	if m.src >= 0 { // the capture and store sources; external MQTT is metered at arrival
		meterAt = g.meter(tenant, m.stamp)
	}
	g.meterAt[[2]int{m.tenant, m.index}] = meterAt
	return replayOutcome{admitted: g.gate(fmt.Sprintf("src-%d", m.src), tenant, meterAt, false, OriginAuthenticated)}
}

// redeliver presents a message the broker is re-offering (NumDelivered > 1), with the routing decision
// handle() would make for it.
func (g *legacyGate) redeliver(m replayMsg) bool {
	tenant := g.sc.tenantName(m.tenant)
	var meterAt time.Time
	if m.src >= 0 {
		meterAt = g.meter(tenant, m.stamp)
	}
	return g.gate(fmt.Sprintf("src-%d", m.src), tenant, meterAt, true, OriginAuthenticated)
}

func (g *legacyGate) reading(m replayMsg, _ time.Duration) bool {
	tenant := g.sc.tenantName(m.tenant)
	// The route was decided ONCE, at the message stage, and the reading stage follows it.
	meterAt := g.meterAt[[2]int{m.tenant, m.index}]
	return g.read("src", tenant, meterAt, false, OriginAuthenticated, 1)
}

// ---- arm 2: core.LagCreditMeter with perfect measurement ----

type modelGate struct {
	sc    *replayScenario
	clk   *replayClock
	meter *core.LagCreditMeter
	// per-source queues of unread messages in send order, and how many of each have been read
	queues [][]replayMsg
	heads  []int
	// the reading stage: a lag-credited ceiling over the reading ceiling for fresh traffic, and one
	// backlog bucket per stream for the rest, following the route the message stage chose
	readCeiling *core.LagCreditLimiter
	readBacklog []*core.TenantRateLimiter
	routes      map[[2]int]core.IngestRoute
}

func newModelGate(sc *replayScenario, clk *replayClock) replayGate {
	g := &modelGate{sc: sc, clk: clk}
	g.queues = make([][]replayMsg, len(sc.sources))
	g.heads = make([]int, len(sc.sources))
	all := sc.messages()
	for _, m := range all {
		if m.src >= 0 {
			g.queues[m.src] = append(g.queues[m.src], m)
		}
	}
	for k := range g.queues {
		sort.SliceStable(g.queues[k], func(i, j int) bool { return g.queues[k][i].send.Before(g.queues[k][j].send) })
	}
	g.restart()
	return g
}

func (g *modelGate) restart() {
	g.meter = core.NewLagCreditMeter(core.LagCreditMeterConfig{
		Streams: len(g.sc.sources), Ceiling: g.sc.resolver(), Clock: g.clk.now,
	})
	base := governance.ReadingCeiling(g.sc.resolver())
	g.readCeiling = core.NewLagCreditLimiter(base, len(g.sc.sources), core.WithLagCreditClock(g.clk.now))
	g.readBacklog = nil
	for range g.sc.sources {
		g.readBacklog = append(g.readBacklog, core.NewTenantRateLimiter(base, core.WithClock(g.clk.now)))
	}
	g.routes = map[[2]int]core.IngestRoute{}
}

// sourcesAt is the exact corrected view of every source now: the lag is the true age of its next
// unread message, and the skew the broker's true offset (the estimator, measured perfectly).
func (g *modelGate) sourcesAt() []core.LagCreditSource {
	srcs := make([]core.LagCreditSource, len(g.sc.sources))
	for k := range srcs {
		if g.heads[k] < len(g.queues[k]) {
			head := g.queues[k][g.heads[k]]
			if !head.send.After(g.clk.t) {
				srcs[k].Lag = g.clk.t.Sub(head.send)
			}
			srcs[k].Skew = head.send.Sub(head.stamp)
		}
	}
	return srcs
}

func (g *modelGate) message(m replayMsg) replayOutcome {
	srcs := g.sourcesAt()
	if m.src >= 0 {
		// the skew is minus the broker's offset: a leader running AHEAD stamps early, so its skew is negative
		srcs[m.src].Skew = m.send.Sub(m.stamp)
	}
	factor := g.sc.factorOf(m.tenant)
	d := g.meter.Admit(core.MeterRequest{Tenant: g.sc.tenantName(m.tenant), N: 1, Source: m.src, Stamp: m.stamp,
		Sources: srcs, Factor: factor})
	if m.src >= 0 {
		g.heads[m.src]++
	}
	g.routes[[2]int{m.tenant, m.index}] = d.Route
	return replayOutcome{admitted: d.Admitted, cause: d.Cause, route: d.Route}
}

// reading judges the decoded message after the decode delay, on the route the message stage chose (the
// route is decided once and carried). The fresh route lends the delay as extra credit, capped at the
// cover plus the delay; the backlog route is charged at the corrected stamp. Contention is judged at the
// message stage only.
func (g *modelGate) reading(m replayMsg, delay time.Duration) bool {
	tenant := g.sc.tenantName(m.tenant)
	if g.routes[[2]int{m.tenant, m.index}] == core.RouteBacklog {
		return g.readBacklog[m.src].AllowNAt(tenant, m.stamp.Add(m.send.Sub(m.stamp)), 1)
	}
	srcs := g.sourcesAt()
	return g.readCeiling.Admit(core.LagCreditRequest{Tenant: tenant, N: 1, Scale: 1, Source: m.src, Stamp: m.stamp,
		Sources: srcs, Delay: delay, CreditCap: g.sc.coverOf(m.tenant) + delay}).Admitted
}

// ---- control arms ----

type fixedGate struct{ admit bool }

func (g fixedGate) restart()                                   {}
func (g fixedGate) message(replayMsg) replayOutcome            { return replayOutcome{admitted: g.admit} }
func (g fixedGate) reading(replayMsg, time.Duration) bool      { return g.admit }
func replayAdmitAll(*replayScenario, *replayClock) replayGate  { return fixedGate{true} }
func replayRefuseAll(*replayScenario, *replayClock) replayGate { return fixedGate{false} }

// ---- the oracle and the expectations ----

// oracleExcess counts how many of a tenant's TRUE sends an offline token bucket at the base ceiling refuses.
func (sc *replayScenario) oracleExcess(ti int) int {
	rate, burst := sc.ceilOf(ti)
	bucket := newReplayBucket(rate, burst)
	excess := 0
	for _, s := range sc.tenants[ti].sends {
		if !bucket.take(s) {
			excess++
		}
	}
	return excess
}

type replayBucket struct {
	rate, burst, tokens float64
	last                time.Time
	started             bool
}

func newReplayBucket(rate float64, burst int) *replayBucket {
	return &replayBucket{rate: rate, burst: float64(burst), tokens: float64(burst)}
}

func (b *replayBucket) take(t time.Time) bool {
	if !b.started {
		b.started, b.last = true, t
	}
	if t.After(b.last) {
		b.tokens = math.Min(b.burst, b.tokens+b.rate*t.Sub(b.last).Seconds())
		b.last = t
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// validate checks the scenario describes what it says: every kindCompliant tenant really conforms.
func (sc *replayScenario) validate(t *testing.T) {
	t.Helper()
	for ti, tn := range sc.tenants {
		if tn.kind == kindCompliant {
			require.Zerof(t, sc.oracleExcess(ti), "%s: tenant %s is declared compliant but its sends exceed the ceiling", sc.id, tn.name)
		} else {
			require.NotZerof(t, sc.oracleExcess(ti), "%s: tenant %s is declared over its ceiling but conforms", sc.id, tn.name)
		}
	}
}

// expect judges a result against the strict expectations, returning every violation. nil means the
// row is satisfied.
func (r *replayResult) expect() error {
	sc := r.sc
	var errs []string
	counts := make([]struct{ sent, admitted, shed int }, len(sc.tenants))
	// fresh traffic of a contended tenant: how much was admitted, and over what span of read times
	fresh := make([]struct {
		admitted    int
		first, last time.Time
	}, len(sc.tenants))
	for i, m := range r.msgs {
		cover := sc.coverOf(m.tenant)
		c := &counts[m.tenant]
		c.sent++
		if r.outcomes[i].admitted {
			c.admitted++
		} else {
			c.shed++
		}
		tn := sc.tenants[m.tenant]
		factor := sc.factorOf(m.tenant)
		aged := m.readAge > cover
		if !aged {
			f := &fresh[m.tenant]
			if f.first.IsZero() {
				f.first = m.read
			}
			f.last = m.read
			if r.outcomes[i].admitted {
				f.admitted++
			}
		}
		switch {
		case tn.class == governance.ShedGold && !r.outcomes[i].admitted && tn.kind == kindCompliant:
			// Gold is never shed by contention; a compliant gold tenant is never shed at all.
			errs = append(errs, fmt.Sprintf("gold tenant %s message %d shed", tn.name, m.index))
		case tn.kind == kindCompliant && sc.level > 0 && factor < 1 && aged && !r.outcomes[i].admitted:
			errs = append(errs, fmt.Sprintf("%s: a backlog message %v late was shed by contention (it drains unshed)", tn.name, m.readAge))
		case tn.kind == kindCompliant && sc.level > 0 && factor == 0 && !aged && r.outcomes[i].admitted:
			errs = append(errs, fmt.Sprintf("%s: the hard drop admitted a fresh message (it is a boolean refusal)", tn.name))
		}
	}
	for ti, tn := range sc.tenants {
		c := counts[ti]
		if c.sent != c.admitted+c.shed {
			errs = append(errs, fmt.Sprintf("%s: %d sent but %d admitted + %d shed", tn.name, c.sent, c.admitted, c.shed))
		}
		contended := sc.level > 0 && sc.factorOf(ti) < 1
		rate, burst := sc.ceilOf(ti)
		cover := sc.coverOf(ti)
		if contended && sc.factorOf(ti) > 0 {
			// Policy: fresh traffic is held to the factor's share of the ceiling, plus a cover of lag credit.
			f := sc.factorOf(ti)
			span := fresh[ti].last.Sub(fresh[ti].first).Seconds()
			bound := f*(float64(burst)+rate*(span+cover.Seconds())) + 1
			if float64(fresh[ti].admitted) > bound {
				errs = append(errs, fmt.Sprintf("%s: %d fresh messages admitted under a factor of %v; policy allows %.0f",
					tn.name, fresh[ti].admitted, f, bound))
			}
		}
		switch {
		case tn.kind == kindCompliant && !contended && c.shed > 0:
			errs = append(errs, fmt.Sprintf("%s: %d of %d messages of a tenant within its ceiling were shed", tn.name, c.shed, c.sent))
		case tn.kind == kindOver && !contended:
			want := sc.oracleExcess(ti)
			// ONE-SIDED on both ends. A tenant may not be admitted more than the offline bucket admits plus one
			// burst and one cover of lag credit per stall episode, and may not be shed more than the offline
			// bucket sheds (plus a hair for the quantisation of the sends).
			wantAdmitted := c.sent - want
			tol := (burst + int(rate*cover.Seconds())) * max(sc.episodes, 1)
			if c.admitted > wantAdmitted+tol {
				errs = append(errs, fmt.Sprintf("%s: admitted %d, the offline bucket admits %d (allowance %d): over its ceiling", tn.name, c.admitted, wantAdmitted, tol))
			}
			if slack := max(20, c.sent/100); c.shed > want+slack {
				errs = append(errs, fmt.Sprintf("%s: shed %d, the offline bucket sheds %d: shed beyond its excess", tn.name, c.shed, want))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	// dedupe, keep the list short
	seen := map[string]int{}
	var order []string
	for _, e := range errs {
		if seen[e] == 0 {
			order = append(order, e)
		}
		seen[e]++
	}
	var b strings.Builder
	for i, e := range order {
		if i == 4 {
			fmt.Fprintf(&b, "; and %d more", len(order)-4)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(e)
		if seen[e] > 1 {
			fmt.Fprintf(&b, " (x%d)", seen[e])
		}
	}
	return fmt.Errorf("%s", b.String())
}

// summary is a one-line account of a result for the log.
func (r *replayResult) summary() string {
	shed := make([]int, len(r.sc.tenants))
	sent := make([]int, len(r.sc.tenants))
	for i, m := range r.msgs {
		sent[m.tenant]++
		if !r.outcomes[i].admitted {
			shed[m.tenant]++
		}
	}
	var parts []string
	for ti, tn := range r.sc.tenants {
		parts = append(parts, fmt.Sprintf("%s %d/%d shed", tn.name, shed[ti], sent[ti]))
	}
	return strings.Join(parts, ", ")
}

// ---- the table ----

// bursty is the compliant shape the field cases take: a tenant sending its sustained rate with a
// burst of n on top, still within (rate, burst) by construction (validated against the oracle).
func bursty(name string, to time.Duration, burstAt time.Duration, n int) replayTenant {
	return replayTenant{name: name, kind: kindCompliant, class: governance.ShedBronze,
		sends: join(steady(platformRate, 0, to), burstOf(n, burstAt))}
}

// lane is a tenant's sends on one reader source.
type lane struct {
	src   int
	sends []time.Time
}

// merge interleaves lanes into one send sequence and the source of each message in it.
func merge(lanes ...lane) ([]time.Time, func(int) int) {
	type tagged struct {
		at  time.Time
		src int
	}
	var all []tagged
	for _, l := range lanes {
		for _, t := range l.sends {
			all = append(all, tagged{t, l.src})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	sends := make([]time.Time, len(all))
	srcs := make([]int, len(all))
	for i, a := range all {
		sends[i], srcs[i] = a.at, a.src
	}
	return sends, func(i int) int { return srcs[i] }
}

func capture(stalls ...replayStallWindow) replaySource {
	return replaySource{base: 5 * time.Millisecond, stalls: stalls}
}

func replayTable() []*replayScenario {
	s := time.Second
	ms := time.Millisecond
	one := func(sc *replayScenario) *replayScenario {
		sc.rate, sc.burst = platformRate, platformBurst
		return sc
	}
	var rows []*replayScenario
	add := func(sc *replayScenario) { rows = append(rows, one(sc)) }

	// F1: the field case. A compliant tenant (1000/s plus a 1000-message burst) and a stall.
	for _, stall := range []time.Duration{1900 * ms, 2800 * ms, 3 * s, 4500 * ms, 5700 * ms} {
		id := fmt.Sprintf("F1-%dms", stall.Milliseconds())
		if stall == 1900*ms {
			id = "M1-1900ms"
		}
		add(&replayScenario{
			id: id, about: "compliant bursting tenant, one reader stall",
			sources:     []replaySource{capture(replayStallWindow{0, stall})},
			tenants:     []replayTenant{bursty("bursty", stall, stall-400*ms, 1000)},
			legacyLoses: true,
		})
	}
	// A stall with nothing bursting is absorbed: the baseline the F1 rows are measured against.
	add(&replayScenario{
		id: "F1-control-3s-no-burst", about: "tenant at exactly its rate, 3 s stall: absorbed by the live burst",
		sources: []replaySource{capture(replayStallWindow{0, 3 * s})},
		tenants: []replayTenant{{name: "steady", kind: kindCompliant, class: governance.ShedBronze, sends: steady(platformRate, 0, 3*s)}},
	})
	// The counterweight: above the ceiling is still shed.
	add(&replayScenario{
		id: "A-over-ceiling-3s", about: "TestAPlatformStallStillShedsATenantAboveItsCeiling: 3x the ceiling for 6 s, 3 s stall",
		sources: []replaySource{capture(replayStallWindow{0, 3 * s})}, episodes: 1,
		tenants: []replayTenant{{name: "flood", kind: kindOver, class: governance.ShedBronze, sends: steady(3*platformRate, 0, 6*s)}},
	})
	// P1: capture and the message store drain independently and share a mark on main.
	add(&replayScenario{
		id: "P1-capture-and-store", about: "QoS 1 fail-closed arm: two sources drained on independent timelines",
		sources: []replaySource{capture(replayStallWindow{0, 20 * s}), capture(replayStallWindow{0, 30 * s})},
		tenants: []replayTenant{{name: "qos1", kind: kindCompliant, class: governance.ShedBronze,
			sends:  steady(platformRate, 0, 30*s),
			source: func(i int) int { return i % 2 }}},
		legacyLoses: true,
	})
	// P1 with bursts, shorter stalls: F1 and the shared mark together.
	add(&replayScenario{
		id: "P1-F1-short-stalls", about: "capture and store both stalled 2.5 s, bursting tenant spread over both",
		sources: []replaySource{capture(replayStallWindow{0, 2500 * ms}), capture(replayStallWindow{0, 2500 * ms})},
		tenants: []replayTenant{{name: "qos1", kind: kindCompliant, class: governance.ShedBronze,
			sends: join(steady(platformRate, 0, 2500*ms), burstOf(1000, 2200*ms)), source: func(i int) int { return i % 2 }}},
		legacyLoses: true,
	})
	// Restart in the middle of a stall (review fix a): every limiter and last stamp is gone.
	add(&replayScenario{
		id: "R-restart-mid-stall", about: "pod restarts 1.2 s into a 2.8 s stall; compliant bursting tenant",
		sources:   []replaySource{capture(replayStallWindow{0, 2800 * ms})},
		tenants:   []replayTenant{bursty("bursty", 2800*ms, 2400*ms, 1000)},
		restartAt: 1200 * ms, legacyLoses: true,
	})
	// QoS split: the ceiling is held once, not once per lane.
	add(&replayScenario{
		id: "B1-qos-split-steady-state", about: "3x the ceiling over capture, store and external MQTT, no stall: 1x admitted",
		sources: []replaySource{capture(), capture()}, episodes: 1,
		tenants: []replayTenant{{name: "split", kind: kindOver, class: governance.ShedBronze,
			sends: steady(3*platformRate, 0, 5*s), source: func(i int) int { return []int{0, 1, -1}[i%3] }}},
	})
	// Gateway plus external MQTT share the one fresh allowance.
	add(&replayScenario{
		id: "D4-paho-and-gateway", about: "2x the ceiling on the gateway and 2x on external MQTT",
		sources: []replaySource{capture()}, episodes: 1,
		tenants: []replayTenant{{name: "both", kind: kindOver, class: governance.ShedBronze,
			sends: steady(4*platformRate, 0, 4*s), source: func(i int) int { return []int{0, -1}[i%2] }}},
	})
	// Broker clock skew.
	add(&replayScenario{
		id: "B4-clock-2s-ahead-3s-stall", about: "the stream leader's clock runs 2 s ahead of the pods; 3 s stall",
		sources:     []replaySource{{offset: 2 * s, base: 5 * ms, stalls: []replayStallWindow{{0, 3 * s}}}},
		tenants:     []replayTenant{bursty("bursty", 3*s, 2600*ms, 1000)},
		legacyLoses: true,
		gatewayResidual: "a pod that starts inside the stall has no caught-up sample to learn the leader's clock from: the first messages " +
			"are aged without the 2 s correction, so the credit is undersized (residual: an unestimated skew)",
	})
	// The same stall, after a healthy stretch the skew was learned from.
	add(&replayScenario{
		id: "B4-warm-clock-2s-ahead-3s-stall", about: "leader's clock 2 s ahead, learned in 4 s of healthy reading; then a 3 s stall",
		sources:     []replaySource{{offset: 2 * s, base: 5 * ms, stalls: []replayStallWindow{{4 * s, 7 * s}}}},
		tenants:     []replayTenant{bursty("bursty", 7*s, 6600*ms, 1000)},
		legacyLoses: true,
	})
	// A broker clock behind the pods makes young traffic look old and routes it to the unshed backlog.
	add(&replayScenario{
		id: "D2-clock-behind-escapes-contention", about: "leader 2.5 s behind: a best-effort tenant at L1 should be throttled to 25%",
		sources: []replaySource{{offset: -2500 * ms, base: 5 * ms}}, level: 1, episodes: 1,
		tenants: []replayTenant{{name: "besteffort", kind: kindCompliant, class: governance.ShedBestEffort,
			sends: steady(platformRate, 0, 8*s)}},
		legacyLoses: true,
	})
	// A small backward step after the burst is spent (D-1): the live path is on pod time, so nothing is lost.
	add(&replayScenario{
		id: "D1-backward-step-300ms", about: "leader change steps the broker clock back 300 ms after a spent burst",
		sources: []replaySource{{base: 5 * ms, steps: []replayStep{{3 * s, -300 * ms}}}},
		tenants: []replayTenant{bursty("bursty", 6*s, 2900*ms, 1000)},
	})
	// A 3 s step back (C-4).
	add(&replayScenario{
		id: "C4-backward-step-3s", about: "NTP corrects the leader's clock 3 s backwards mid-run",
		sources: []replaySource{{base: 5 * ms, steps: []replayStep{{4 * s, -3 * s}}}},
		tenants: []replayTenant{{name: "steady", kind: kindCompliant, class: governance.ShedBronze, sends: steady(platformRate, 0, 8*s)}},
	})
	// Flapping stalls every 15 s (D-6).
	var flaps []replayStallWindow
	var flapSends [][]time.Time
	for i := 0; i < 4; i++ {
		from := time.Duration(i) * 15 * s
		flaps = append(flaps, replayStallWindow{from, from + 2500*ms})
		flapSends = append(flapSends, join(steady(platformRate, from, from+2500*ms), burstOf(900, from+2200*ms)))
	}
	add(&replayScenario{
		id: "D6-flapping-stalls", about: "a 2.5 s stall every 15 s, a burst at the end of each",
		sources:     []replaySource{capture(flaps...)},
		tenants:     []replayTenant{{name: "bursty", kind: kindCompliant, class: governance.ShedBronze, sends: join(flapSends...)}},
		legacyLoses: true,
	})
	// A long stall under contention: the backlog drains unshed (B-2 / C-1).
	add(&replayScenario{
		id: "B2-60s-stall-at-L1", about: "60 s stall, best-effort tenant, L1: the stalled backlog drains unshed",
		sources: []replaySource{capture(replayStallWindow{0, 60 * s})}, level: 1,
		tenants: []replayTenant{{name: "besteffort", kind: kindCompliant, class: governance.ShedBestEffort, sends: steady(platformRate, 0, 60*s)}},
	})
	// The hard drop at L3 is a boolean, at any burst (P2-4), and gold is untouched beside it.
	for _, burst := range []int{platformBurst, math.MaxInt32} {
		burst := burst
		add(&replayScenario{
			id: fmt.Sprintf("P2-4-hard-drop-burst-%d", burst), about: "L3: best-effort refused outright, gold untouched, whatever the burst",
			sources: []replaySource{capture()}, level: 3,
			tenants: []replayTenant{
				{name: "besteffort", kind: kindCompliant, class: governance.ShedBestEffort, sends: steady(500, 0, 4*s)},
				{name: "gold", kind: kindCompliant, class: governance.ShedGold, sends: steady(500, 0, 4*s)},
			},
		})
		rows[len(rows)-1].burst = burst
	}
	// A long stall drained through a decode stage that is a second late, on a leader whose clock runs 2 s ahead
	// (learned in the 5 s before the stall). The messages are backlog (older than the cover): the reading stage
	// must follow them onto the backlog route and charge them at the stamp with the skew taken back out.
	add(&replayScenario{
		id: "X-backlog-readings-clock-ahead", about: "30 s stall, leader 2 s ahead, 1 s decode delay: the reading stage charges the drained backlog on the send timeline",
		sources:     []replaySource{{offset: 2 * s, base: 5 * ms, decodeDelay: s, stalls: []replayStallWindow{{5 * s, 35 * s}}}},
		tenants:     []replayTenant{{name: "steady", kind: kindCompliant, class: governance.ShedBronze, sends: steady(platformRate, 0, 35*s)}},
		legacyLoses: true,
	})
	// A reading stage that decodes a second late.
	add(&replayScenario{
		id: "C6-decode-delay-1s", about: "compliant tenant, 1 s of decode delay between the stages, no stall",
		sources: []replaySource{{base: 5 * ms, decodeDelay: s}},
		tenants: []replayTenant{{name: "steady", kind: kindCompliant, class: governance.ShedBronze, sends: steady(platformRate, 0, 5*s)}},
	})
	// A stall nearly as long as the cover plus the decode delay: the reading stage's credit is the cover AND the delay.
	add(&replayScenario{
		id: "C6-stall-1900-and-decode-delay", about: "1.9 s stall plus 1 s decode delay, bursting compliant tenant",
		sources:     []replaySource{{base: 5 * ms, decodeDelay: s, stalls: []replayStallWindow{{0, 1900 * ms}}}},
		tenants:     []replayTenant{bursty("bursty", 1900*ms, 1500*ms, 1000)},
		legacyLoses: true,
	})
	// Stall and decode delay together on a bursting tenant.
	add(&replayScenario{
		id: "C6-stall-and-decode-delay", about: "1.5 s stall plus 1 s decode delay, bursting compliant tenant",
		sources:     []replaySource{{base: 5 * ms, decodeDelay: s, stalls: []replayStallWindow{{0, 1500 * ms}}}},
		tenants:     []replayTenant{bursty("bursty", 1500*ms, 1100*ms, 1000)},
		legacyLoses: true,
	})
	// Review fix (a): the pod restarts while one of its two sources lags and the other is current, so the
	// tenant's bucket is born knowing nothing about the lagging source. 600/s is on the current source and
	// 400/s plus a 1500-burst on the source that is stalled until 1.9 s (inside the 2 s cover, so it rides
	// the fresh route); the restart is at 1.2 s. The conformant dump the lagging source delivers at 1.9 s
	// exceeds the base burst: it is admitted only because the unknown source is credited its lag, and
	// "an unknown last stamp counts as 0" sheds it here.
	lanes, laneOf := merge(lane{0, steady(600, 0, 1900*ms)}, lane{1, join(steady(400, 0, 1900*ms), burstOf(1500, 1800*ms))})
	add(&replayScenario{
		id: "R-restart-two-sources", about: "restart at 1.2 s; capture is current, the store source is stalled until 1.9 s",
		sources:   []replaySource{capture(), capture(replayStallWindow{0, 1900 * ms})},
		tenants:   []replayTenant{{name: "qos1", kind: kindCompliant, class: governance.ShedBronze, sends: lanes, source: laneOf}},
		restartAt: 1200 * ms, legacyLoses: true,
	})
	// Exit marks. A 2000 burst, one backlog-route message that takes capture into its backlog phase, the
	// phase exits (the tenant's own calm trickle ends it: the phase is the tenant's alone), and then a conformant 2000 burst plus 900/s on a source stalled 1.5 s: 3351 messages,
	// all within the ceiling. Withholding the phase's accrual at the exit sheds 1342 of them.
	exitLanes, exitOf := merge(lane{0, join(burstOf(2000, 0), burstOf(1, 11*s), steady(1, 14*s, 89*s), burstOf(2000, 90*s))},
		lane{1, steady(900, 88500*ms, 90*s)})
	add(&replayScenario{
		id: "X-exit-then-burst", about: "phase exits, then a conformant burst plus a stalled second source",
		sources: []replaySource{capture(replayStallWindow{10 * s, 13500 * ms}), capture(replayStallWindow{88500 * ms, 90 * s})},
		tenants: []replayTenant{
			{name: "c", kind: kindCompliant, class: governance.ShedBronze, sends: exitLanes, source: exitOf},
			{name: "calm", kind: kindCompliant, class: governance.ShedBronze, sends: steady(10, 0, 95*s)},
		},
		legacyLoses: true,
	})
	// The saw band. A 3x tenant (200/s, burst 400, so a 2 s cover) whose reader lag saws across its cover
	// every 3 s for a minute, beside a tenant of another tier (cover 5 s, so a stream-wide default
	// threshold would be 5 s): the route must not flip between the two allowances message by message.
	var saw []replayStallWindow
	for k := 0; k < 20; k++ {
		from := time.Duration(1+3*k) * s
		length := 2300 * ms
		if k%2 == 1 {
			length = 2100 * ms
		}
		saw = append(saw, replayStallWindow{from, from + length})
	}
	add(&replayScenario{
		id: "F2-saw-band-mixed-tier", about: "3x tenant with the lag sawing across its 2 s cover; a tenant of another tier beside it",
		sources: []replaySource{capture(saw...)}, episodes: 1,
		tenants: []replayTenant{
			{name: "flood", kind: kindOver, class: governance.ShedBronze, rate: 200, burst: 400, sends: steady(600, 0, 60*s)},
			{name: "small", kind: kindCompliant, class: governance.ShedBronze, rate: 50, burst: 500, sends: steady(25, 0, 60*s)},
		},
		legacyLoses: true,
	})
	// Contention in the tail of a stall. After a 10 s stall the stream is in its backlog phase for the drain
	// and 30 s of calm; contention is a filter and must still reach the current traffic.
	add(&replayScenario{
		id: "T-L3-phase-tail", about: "L3 after a 10 s stall: best-effort young traffic refused through the phase tail, gold untouched",
		sources: []replaySource{capture(replayStallWindow{0, 10 * s})}, level: 3,
		tenants: []replayTenant{
			{name: "besteffort", kind: kindCompliant, class: governance.ShedBestEffort, sends: steady(500, 0, 40*s)},
			{name: "gold", kind: kindCompliant, class: governance.ShedGold, sends: steady(500, 0, 40*s)},
		},
		legacyLoses: true,
	})
	add(&replayScenario{
		id: "T-L1-phase-tail", about: "L1 after a 10 s stall: best-effort held to a quarter of its ceiling through the phase tail",
		sources: []replaySource{capture(replayStallWindow{0, 10 * s})}, level: 1,
		tenants: []replayTenant{{name: "besteffort", kind: kindCompliant, class: governance.ShedBestEffort, sends: steady(platformRate, 0, 40*s)}},
	})
	// Tenant isolation of the phase. A neighbour at 10/s with burst 1 has a 100 ms cover, and the stream's
	// constant 200 ms reader lag is past it, so the neighbour is permanently in its OWN backlog phase.
	// That must change nothing for the 1000/2000 tenant beside it, which is within its cover: with a phase
	// shared by the stream it was pushed onto the backlog route too and was admitted a second allowance
	// alongside its external-MQTT one (about 2x its ceiling, indefinitely).
	isoLanes, isoOf := merge(lane{0, steady(1000, 0, 30*s)}, lane{-1, steady(1000, 0, 30*s)})
	add(&replayScenario{
		id: "I-short-cover-neighbour", about: "a 10/s burst-1 neighbour at a constant 200 ms lag beside a 1000/2000 tenant spread over the gateway and external MQTT",
		sources: []replaySource{{base: 200 * ms}}, episodes: 1,
		tenants: []replayTenant{
			{name: "target", kind: kindOver, class: governance.ShedBronze, sends: isoLanes, source: isoOf},
			{name: "tiny", kind: kindCompliant, class: governance.ShedBronze, rate: 10, burst: 1, sends: steady(1, 0, 30*s)},
		},
	})
	return rows
}

// ---- the tests ----

func TestReplayScenariosDescribeWhatTheySay(t *testing.T) {
	for _, sc := range replayTable() {
		sc.validate(t)
	}
}

// LEGACY PINS THE DEFECT. On main, every row marked legacyLoses violates the strict expectations, and
// every other row meets them. When the lagCredit gate replaces this code, the marked rows are the ones
// that must turn green (TestReplayModel already proves the design does).
func TestReplayLegacyGateLosesCompliantTenantsOnAStall(t *testing.T) {
	for _, sc := range replayTable() {
		sc := sc
		t.Run(sc.id, func(t *testing.T) {
			res := sc.run(newLegacyGate)
			err := res.expect()
			t.Logf("legacy: %s", res.summary())
			if sc.legacyLoses {
				require.Errorf(t, err, "%s: the gate on main is expected to violate the expectations (%s)", sc.id, sc.about)
				t.Logf("violation: %v", err)
				return
			}
			require.NoErrorf(t, err, "%s: the gate on main is expected to meet this row (%s)", sc.id, sc.about)
		})
	}
}

// RED EVIDENCE. With RATEGATE_REPLAY_STRICT=1 the legacy gate is held to the strict expectations on every
// row, and fails exactly the rows marked legacyLoses. It is skipped otherwise: it is the negative control
// that proves the harness can fail, not a standing test.
func TestReplayLegacyGateAgainstTheStrictExpectations(t *testing.T) {
	if os.Getenv("RATEGATE_REPLAY_STRICT") != "1" {
		t.Skip("set RATEGATE_REPLAY_STRICT=1 to see the legacy gate RED against the strict expectations")
	}
	for _, sc := range replayTable() {
		sc := sc
		t.Run(sc.id, func(t *testing.T) {
			require.NoError(t, sc.run(newLegacyGate).expect())
		})
	}
}

// THE LIBRARY, with perfect measurement, meets every row, including the ones legacy loses.
func TestReplayModelMeetsEveryRow(t *testing.T) {
	for _, sc := range replayTable() {
		sc := sc
		t.Run(sc.id, func(t *testing.T) {
			if sc.modelSkip != "" {
				t.Skip(sc.modelSkip)
			}
			res := sc.run(newModelGate)
			t.Logf("model: %s", res.summary())
			require.NoError(t, res.expect())
		})
	}
}

// NEGATIVE CONTROLS FOR THE ORACLE. A gate that admits everything must fail the rows that police an
// over-ceiling tenant or a contended one, and a gate that refuses everything must fail the rows that
// protect a compliant tenant. If either passed a row the row would prove nothing.
func TestReplayOracleNegativeControls(t *testing.T) {
	policing, protecting := 0, 0
	for _, sc := range replayTable() {
		hasOver, hasCompliant := false, false
		for ti, tn := range sc.tenants {
			cover := sc.coverOf(ti)
			hasOver = hasOver || tn.kind == kindOver || sc.level > 0
			contended := sc.level > 0 && sc.factorOf(ti) < 1
			if tn.kind == kindCompliant && !contended {
				hasCompliant = true
			}
			if tn.kind == kindCompliant && contended {
				for _, m := range sc.messages() {
					hasCompliant = hasCompliant || (m.tenant == ti && m.readAge > cover) // a drained backlog is protected
				}
			}
		}
		if hasOver {
			policing++
			require.Errorf(t, sc.run(replayAdmitAll).expect(), "%s: admitting everything passed a row that polices a tenant", sc.id)
		}
		if hasCompliant {
			protecting++
			require.Errorf(t, sc.run(replayRefuseAll).expect(), "%s: refusing everything passed a row that protects a tenant", sc.id)
		}
	}
	require.Greater(t, policing, 5)
	require.Greater(t, protecting, 10)
}

// CONTENTION IS NOT PHASE-BOUND. After a 10 s stall the stream is in its backlog phase for the drain and the
// calm period, and the CEILING route is phase-wide, but the overload bands still reach every message read
// within its cover: at L3 best-effort young traffic is refused outright (a boolean), at L1 it is held to
// its share, and whatever is older than the cover still drains unshed. Gold is untouched.
func TestReplayModelPhaseTailIsContended(t *testing.T) {
	find := func(id string) *replayScenario {
		for _, r := range replayTable() {
			if r.id == id {
				return r
			}
		}
		t.Fatalf("no row %s", id)
		return nil
	}
	sc := find("T-L3-phase-tail")
	res := sc.run(newModelGate)
	cover := sc.cover()
	young, youngAdmitted, aged, agedAdmitted, goldShed := 0, 0, 0, 0, 0
	for i, m := range res.msgs {
		switch {
		case sc.tenants[m.tenant].class == governance.ShedGold:
			if !res.outcomes[i].admitted {
				goldShed++
			}
		case m.readAge <= cover:
			young++
			if res.outcomes[i].admitted {
				youngAdmitted++
			}
		default:
			aged++
			if res.outcomes[i].admitted {
				agedAdmitted++
			}
		}
	}
	require.Greater(t, young, 15000, "the tail must be long enough to mean something")
	require.Zero(t, youngAdmitted, "the hard drop must refuse young traffic in the phase tail (legacy and the previous routing admitted all of it)")
	require.Positive(t, aged)
	require.Equal(t, aged, agedAdmitted, "a backlog older than the cover drains unshed even at L3")
	require.Zero(t, goldShed)

	sc = find("T-L1-phase-tail")
	res = sc.run(newModelGate)
	youngAdmitted = 0
	for i, m := range res.msgs {
		if m.readAge <= cover && res.outcomes[i].admitted {
			youngAdmitted++
		}
	}
	// 30 s of tail at a quarter of 1000/s is about 7500; unscaled it would be about 30000
	require.Less(t, youngAdmitted, 12000, "best-effort at L1 must be held to its share in the phase tail")
	require.Greater(t, youngAdmitted, 4000)
}

// REDELIVERIES ARE INERT. A message the broker re-offers during a drain is admitted uncharged and moves
// nothing: every other message gets exactly the verdict it gets with no redeliveries at all.
func TestReplayRedeliveriesAreInert(t *testing.T) {
	for _, id := range []string{"F1-3000ms", "P1-capture-and-store", "A-over-ceiling-3s"} {
		var sc *replayScenario
		for _, r := range replayTable() {
			if r.id == id {
				sc = r
			}
		}
		require.NotNilf(t, sc, id)
		clean := sc.run(newLegacyGate)
		sc.redeliverEvery = 3
		noisy := sc.run(newLegacyGate)
		sc.redeliverEvery = 0
		require.Greaterf(t, noisy.redelivered, 100, "%s: the row interleaved no redeliveries", id)
		require.Zerof(t, noisy.redeliveriesRefused, "%s: a redelivery was charged and refused", id)
		for i := range clean.outcomes {
			require.Equalf(t, clean.outcomes[i].admitted, noisy.outcomes[i].admitted, "%s: message %d got a different verdict because of a redelivery", id, i)
		}
	}
}
