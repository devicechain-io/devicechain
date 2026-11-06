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
//   - GATEWAY is the real lagCredit wiring. Not built yet (slice S3), so its rows are skipped, loudly,
//     by name.

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
	// redeliverEvery, when positive, interleaves a redelivery of an earlier message after every Nth message.
	redeliverEvery int
	// noFreshBound turns off the contended-tenant bound on fresh traffic for a row, naming why. The
	// model keeps a stream on the backlog route for the whole phase (drain plus 30 s of calm), so the
	// young traffic of a stalled stream is held to the ceiling but not contention-scaled in that tail.
	noFreshBound string
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

// cover is the time a tenant's base burst covers: the cutoff the legacy gate and the lag credit share.
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
	base := core.StaticCeiling(sc.rate, sc.burst)
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
		Streams: len(g.sc.sources), Ceiling: core.StaticCeiling(g.sc.rate, g.sc.burst), Clock: g.clk.now,
		PhaseThreshold: g.sc.cover(),
	})
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
	return replayOutcome{admitted: d.Admitted, cause: d.Cause, route: d.Route}
}

// The model judges the message stage; the reading stage is wired with the gateway (S3).
func (g *modelGate) reading(replayMsg, time.Duration) bool { return true }

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
	bucket := newReplayBucket(sc.rate, sc.burst)
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
	cover := sc.cover()
	for i, m := range r.msgs {
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
		if contended && sc.factorOf(ti) > 0 && sc.noFreshBound == "" {
			// Policy: fresh traffic is held to the factor's share of the ceiling, plus a cover of lag credit.
			f := sc.factorOf(ti)
			span := fresh[ti].last.Sub(fresh[ti].first).Seconds()
			bound := f*(float64(sc.burst)+sc.rate*(span+cover.Seconds())) + 1
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
			tol := (sc.burst + int(sc.rate*cover.Seconds())) * max(sc.episodes, 1)
			if c.shed > want+tol || c.shed < want-tol {
				errs = append(errs, fmt.Sprintf("%s: shed %d, the offline bucket sheds %d (tolerance %d)", tn.name, c.shed, want, tol))
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
		id: "A-over-ceiling-3s", about: "TestAPlatformStallStillShedsATenantAboveItsCeiling: 3x the ceiling, 3 s stall",
		sources: []replaySource{capture(replayStallWindow{0, 3 * s})}, episodes: 1,
		tenants: []replayTenant{{name: "flood", kind: kindOver, class: governance.ShedBronze, sends: steady(3*platformRate, 0, 3*s)}},
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
		noFreshBound: "after a 60 s stall the stream is in its backlog phase for the tail of the drain; see TestReplayModelPhaseTailIsNotContended",
		tenants:      []replayTenant{{name: "besteffort", kind: kindCompliant, class: governance.ShedBestEffort, sends: steady(platformRate, 0, 60*s)}},
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
	// A reading stage that decodes a second late.
	add(&replayScenario{
		id: "C6-decode-delay-1s", about: "compliant tenant, 1 s of decode delay between the stages, no stall",
		sources: []replaySource{{base: 5 * ms, decodeDelay: s}},
		tenants: []replayTenant{{name: "steady", kind: kindCompliant, class: governance.ShedBronze, sends: steady(platformRate, 0, 5*s)}},
	})
	// Stall and decode delay together on a bursting tenant.
	add(&replayScenario{
		id: "C6-stall-and-decode-delay", about: "1.5 s stall plus 1 s decode delay, bursting compliant tenant",
		sources:     []replaySource{{base: 5 * ms, decodeDelay: s, stalls: []replayStallWindow{{0, 1500 * ms}}}},
		tenants:     []replayTenant{bursty("bursty", 1500*ms, 1100*ms, 1000)},
		legacyLoses: true,
	})
	// Review fix (a): the pod restarts while one of its two sources lags and the other is current, so
	// the tenant's bucket is born knowing nothing about the lagging source.
	add(&replayScenario{
		id: "R-restart-two-sources", about: "restart 1.2 s into a 2.8 s stall of the store source; capture is current",
		sources: []replaySource{capture(), capture(replayStallWindow{0, 2800 * ms})},
		tenants: []replayTenant{{name: "qos1", kind: kindCompliant, class: governance.ShedBronze,
			sends: join(steady(platformRate, 0, 2800*ms), burstOf(1000, 2400*ms)), source: func(i int) int { return i % 2 }}},
		restartAt: 1200 * ms,
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
		cover := sc.cover()
		for ti, tn := range sc.tenants {
			hasOver = hasOver || tn.kind == kindOver || (sc.level > 0 && sc.noFreshBound == "")
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

// THE GATEWAY ARM. The lag-credited gate is not wired into the gateway yet (slice S3), so every row is
// skipped by name and the reason is printed. When S3 lands, build the real gate here and drop the skip:
// the rows, the oracle and the expectations are already the ones it will be judged against.
func TestReplayLagCreditGateway(t *testing.T) {
	for _, sc := range replayTable() {
		sc := sc
		t.Run(sc.id, func(t *testing.T) {
			t.Skip("lagCredit is not wired into the gateway until S3; the model arm (TestReplayModelMeetsEveryRow) judges the library")
		})
	}
}

// PINS A FINDING, not a virtue. While a stream is in its backlog phase every message on it is metered on
// the backlog route, and the backlog route is exempt from contention: so after a stall long enough to
// enter the phase, the young traffic of that stream escapes the contention factor for the drain and the
// 30 s of calm that follow, though it is still held to the ceiling. Contention reaches only traffic that
// is read within the cover of its send AND while the stream is out of backlog. If the maintainer rules
// that young traffic must stay contended, this test is the one to change.
func TestReplayModelPhaseTailIsNotContended(t *testing.T) {
	var sc *replayScenario
	for _, r := range replayTable() {
		if r.id == "B2-60s-stall-at-L1" {
			sc = r
		}
	}
	require.NotNil(t, sc)
	res := sc.run(newModelGate)
	cover := sc.cover()
	young, admitted := 0, 0
	for i, m := range res.msgs {
		if m.readAge <= cover {
			young++
			if res.outcomes[i].admitted {
				admitted++
			}
		}
	}
	require.Greater(t, young, 1000)
	require.Equal(t, young, admitted, "young traffic in the phase tail is expected to ride the backlog route, unscaled")
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
