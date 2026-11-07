// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestMeter(c *lagClock, rate float64, burst, streams int) *LagCreditMeter {
	return NewLagCreditMeter(LagCreditMeterConfig{
		Streams: streams, Ceiling: StaticCeiling(rate, burst), Clock: c.now,
	})
}

func idleSources(n int) []LagCreditSource { return make([]LagCreditSource, n) }

// ----- the phase -----

func TestIngestPhaseHysteresis(t *testing.T) {
	p := NewIngestPhase(30 * time.Second)
	const thr = 2 * time.Second
	now := simEpoch
	obs := func(alpha time.Duration, dt time.Duration) bool {
		now = now.Add(dt)
		return p.Observe(alpha, thr, now)
	}
	require.False(t, obs(100*time.Millisecond, 0), "caught up")
	require.False(t, obs(2*time.Second, time.Second), "exactly the threshold is not past it")
	require.True(t, obs(2*time.Second+time.Nanosecond, time.Second), "enters the moment a delivery is older than the threshold")

	// Hovering between half the threshold and the threshold never leaves.
	for i := 0; i < 100; i++ {
		require.True(t, obs(1500*time.Millisecond, time.Second), "still draining")
	}
	// Calm for 29 s is not enough, and one bad observation resets the clock.
	require.True(t, obs(100*time.Millisecond, time.Second))
	require.True(t, obs(100*time.Millisecond, 29*time.Second))
	require.True(t, obs(time.Second, time.Second), "an age at half the threshold or above restarts the calm period")
	require.True(t, obs(100*time.Millisecond, time.Second))
	require.True(t, obs(100*time.Millisecond, 29*time.Second))
	epoch, _ := p.Exit()
	require.Zero(t, epoch)
	require.False(t, obs(100*time.Millisecond, time.Second), "30 s of calm leaves backlog")
	epoch, at := p.Exit()
	require.Equal(t, uint64(1), epoch)
	require.Equal(t, now, at)

	// Re-entering and flapping around the threshold costs one epoch per genuine exit, not per flap.
	require.True(t, obs(5*time.Second, time.Second))
	for i := 0; i < 50; i++ {
		obs(100*time.Millisecond, 5*time.Second)
		obs(3*time.Second, time.Second)
	}
	epoch, _ = p.Exit()
	require.Equal(t, uint64(1), epoch, "a stream that keeps relapsing never exits")
}

func TestIngestPhaseUntouchedByFreshTraffic(t *testing.T) {
	p := NewIngestPhase(0)
	for i := 0; i < 1000; i++ {
		require.False(t, p.Observe(50*time.Millisecond, 2*time.Second, simEpoch.Add(time.Duration(i)*time.Second)))
	}
	epoch, _ := p.Exit()
	require.Zero(t, epoch)
}

// ----- routing -----

// FACTOR 0 IS A BOOLEAN. At a burst of MaxInt32 a unit price capped below the burst would admit the
// message it was meant to drop; here there is no price to be outgrown.
func TestMeterFactorZeroRefusesFreshTrafficAtAnyBurst(t *testing.T) {
	for _, burst := range []int{1, 2000, math.MaxInt32} {
		clk := &lagClock{t: simEpoch}
		m := newTestMeter(clk, 1000, burst, 1)
		for i := 0; i < 100; i++ {
			d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: idleSources(1), Factor: 0})
			require.False(t, d.Admitted)
			require.Equal(t, CauseHardDrop, d.Cause)
			require.Equal(t, RouteFresh, d.Route)
		}
		d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: -1, Sources: idleSources(1), Factor: 0})
		require.False(t, d.Admitted, "unstamped traffic is fresh too")
		require.Equal(t, RouteLive, d.Route)
		// Gold passes factor 1 and is untouched.
		require.True(t, m.Admit(MeterRequest{Tenant: "gold", N: 1, Source: 0, Stamp: clk.t, Sources: idleSources(1), Factor: 1}).Admitted)
	}
}

// A drained backlog (older than the cover) is exempt from contention, including the hard drop.
func TestMeterBacklogIsNeverContentionScaled(t *testing.T) {
	clk := &lagClock{t: simEpoch.Add(time.Hour)}
	m := newTestMeter(clk, 1000, 2000, 1)
	// 60 s of a tenant at exactly its ceiling, read an hour late, under a hard drop.
	admitted := 0
	for i := 0; i < 60000; i++ {
		stamp := simEpoch.Add(time.Duration(i) * time.Millisecond)
		d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: stamp,
			Sources: []LagCreditSource{{Lag: time.Hour}}, Factor: 0})
		require.Equal(t, RouteBacklog, d.Route)
		if d.Admitted {
			admitted++
		}
	}
	require.Equal(t, 60000, admitted, "ADR: drained backlog is exempt from overload shedding")
}

// A contention denial must spend NOTHING of the ceiling budget.
func TestMeterContentionNeverChargesTheCeiling(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := newTestMeter(clk, 1000, 2000, 1)
	denied := 0
	for i := 0; i < 5000; i++ { // a flood at a quarter factor
		d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: idleSources(1), Factor: 0.25})
		if !d.Admitted {
			require.Equal(t, CauseContention, d.Cause)
			denied++
		}
	}
	require.Greater(t, denied, 4000)
	// The overload ends. The ceiling bucket holds exactly what 500 contention admissions cost it.
	n := 0
	for m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: idleSources(1), Factor: 1}).Admitted {
		n++
	}
	require.Equal(t, 2000-500, n, "the ceiling was charged only for what contention admitted")
}

// ONE CEILING BUCKET per tenant across capture, store and external MQTT: spreading a send rate over
// every source does not multiply the ceiling.
func TestMeterQoSSplitIsHeldToOneCeiling(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := newTestMeter(clk, 1000, 2000, 2)
	admitted := 0
	// 5 s at 3000/s, a third each on capture, store and external MQTT.
	for i := 0; i < 15000; i++ {
		clk.t = simEpoch.Add(time.Duration(i) * time.Second / 3000)
		req := MeterRequest{Tenant: "t", N: 1, Sources: idleSources(2), Factor: 1}
		switch i % 3 {
		case 0:
			req.Source, req.Stamp = 0, clk.t
		case 1:
			req.Source, req.Stamp = 1, clk.t
		default:
			req.Source = -1
		}
		if m.Admit(req).Admitted {
			admitted++
		}
	}
	require.LessOrEqual(t, admitted, 2000+5*1000+1, "the ceiling is 1x however many sources carry the traffic")
	require.Greater(t, admitted, 2000+5*1000-200)
}

// P1: two streams drained independently on their own timelines must not share a mark. The control
// shows the shared-mark failure the per-stream split removes.
func TestMeterBacklogBucketsAreOnePerStream(t *testing.T) {
	const rate, burst = 1000.0, 2000
	// A compliant tenant: 500/s on each of two streams, 30 s each, both read a minute late. The
	// capture stream is drained first, then the store, from the same start.
	stampAt := func(i int) time.Time { return simEpoch.Add(time.Duration(i) * 2 * time.Millisecond) }

	clk := &lagClock{t: simEpoch.Add(time.Minute)}
	m := newTestMeter(clk, rate, burst, 2)
	lagging := []LagCreditSource{{Lag: time.Minute}, {Lag: time.Minute}}
	shed := 0
	for s := 0; s < 2; s++ { // capture drained to the end, then the store from its own start
		for i := 0; i < 15000; i++ {
			if !m.Admit(MeterRequest{Tenant: "t", N: 1, Source: s, Stamp: stampAt(i), Sources: lagging, Factor: 1}).Admitted {
				shed++
			}
		}
	}
	require.Zero(t, shed, "per-stream backlog buckets shed a compliant tenant")

	// Control: the same traffic through ONE shared backlog limiter. The store's older stamps are
	// charged at the mark the capture drain left behind, where nothing accrues.
	shared := NewTenantRateLimiter(StaticCeiling(rate, burst), WithClock(clk.now))
	sharedShed := 0
	for s := 0; s < 2; s++ {
		for i := 0; i < 15000; i++ {
			if !shared.AllowNAt("t", stampAt(i), 1) {
				sharedShed++
			}
		}
	}
	require.Greater(t, sharedShed, 10000, "the control no longer shows the shared-mark failure; the test proves nothing")
}

// A tenant above its ceiling is shed by its excess on the backlog route too (nothing to buy by being replayed).
func TestMeterBacklogStillShedsAnOverCeilingTenant(t *testing.T) {
	clk := &lagClock{t: simEpoch.Add(time.Hour)}
	m := newTestMeter(clk, 1000, 2000, 1)
	admitted := 0
	for i := 0; i < 30000; i++ { // 3000/s for 10 s
		stamp := simEpoch.Add(time.Duration(i) * time.Second / 3000)
		if m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: stamp, Sources: []LagCreditSource{{Lag: time.Hour}}, Factor: 1}).Admitted {
			admitted++
		}
	}
	require.LessOrEqual(t, admitted, 2000+10*1000+1)
	require.Greater(t, admitted, 10000)
}

// The route follows the tenant's cover and the stream's phase.
func TestMeterRoutes(t *testing.T) {
	clk := &lagClock{t: simEpoch.Add(10 * time.Minute)}
	m := newTestMeter(clk, 1000, 2000, 1) // cover 2 s
	src := func(lag time.Duration) []LagCreditSource { return []LagCreditSource{{Lag: lag}} }

	require.Equal(t, RouteFresh, m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t.Add(-time.Second), Sources: src(time.Second), Factor: 1}).Route)
	require.Equal(t, RouteLive, m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Sources: src(0), Factor: 1}).Route, "no stamp")
	require.Equal(t, RouteLive, m.Admit(MeterRequest{Tenant: "t", N: 1, Source: -1, Stamp: clk.t, Sources: src(0), Factor: 1}).Route, "no source")
	require.Equal(t, RouteBacklog, m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t.Add(-3 * time.Second), Sources: src(3 * time.Second), Factor: 1}).Route)
	// The stream is now in backlog: even a current message routes there until 30 s of calm.
	inBacklog, _ := m.Phase(0, "t")
	require.True(t, inBacklog)
	inBacklog, _ = m.Phase(0, "someone else")
	require.False(t, inBacklog, "another tenant's phase is its own")
	require.Equal(t, RouteBacklog, m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: src(0), Factor: 1}).Route)
	clk.t = clk.t.Add(31 * time.Second)
	m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: src(0), Factor: 1}) // starts the calm clock
	clk.t = clk.t.Add(31 * time.Second)
	d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: src(0), Factor: 1})
	require.Equal(t, RouteFresh, d.Route)
	_, epoch := m.Phase(0, "t")
	require.Equal(t, uint64(1), epoch)
}

func TestMeterRejectsMisuseLoudly(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := newTestMeter(clk, 10, 10, 2)
	require.Panics(t, func() { m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Sources: idleSources(1), Factor: 1}) })
	require.Panics(t, func() { m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Sources: idleSources(2), Factor: 1.5}) })
	require.Panics(t, func() { m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Sources: idleSources(2), Factor: -1}) })
	require.Panics(t, func() { m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 5, Sources: idleSources(2), Factor: 1}) })
	require.Panics(t, func() { NewLagCreditMeter(LagCreditMeterConfig{Streams: 0, Ceiling: StaticCeiling(1, 1)}) })
	require.Panics(t, func() { NewLagCreditMeter(LagCreditMeterConfig{Streams: 1}) })
}

// A smooth compliant tenant (at most its rate, any phase of any stall length, split over sources
// with unstamped traffic mixed in) loses nothing through the whole meter, backlog route and phase
// transitions included. Bursting tenants are covered by the next property.
func TestMeterSmoothCompliantTenantLosesNothingThroughAnyStall(t *testing.T) {
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 60 * time.Second
		rate := float64(50 + rng.Intn(300))
		burst := 1 + rng.Intn(1500)
		cfg := simCfg{rate: rate, burst: burst, sources: 1 + rng.Intn(3)}
		cfg.base = make([]time.Duration, cfg.sources)
		cfg.offsets = make([]time.Duration, cfg.sources)
		cfg.stalls = make([][]simStall, cfg.sources)
		for k := 0; k < cfg.sources; k++ {
			cfg.offsets[k] = time.Duration(rng.Intn(400)-200) * time.Millisecond
			for tt := time.Duration(rng.Intn(5000)) * time.Millisecond; tt < horizon && rng.Intn(5) != 0; {
				dur := time.Duration(rng.Intn(25000)) * time.Millisecond
				cfg.stalls[k] = append(cfg.stalls[k], simStall{start: simEpoch.Add(tt), dur: dur})
				tt += dur + time.Duration(rng.Intn(20000))*time.Millisecond
			}
		}
		var sends []time.Time
		step := time.Duration(float64(time.Second) / (rate * (0.4 + 0.6*rng.Float64())))
		for at := simEpoch; at.Before(simEpoch.Add(horizon)); at = at.Add(step) {
			sends = append(sends, at)
		}
		msgs := buildMsgs(rng, cfg, sends, 0.15)
		queues := splitQueues(msgs, cfg.sources)
		heads := make([]int, cfg.sources)

		clk := &lagClock{t: simEpoch}
		m := newTestMeter(clk, rate, burst, cfg.sources)
		shed := 0
		for _, msg := range order(rng, msgs, cfg.sources) {
			clk.t = msg.read
			srcs := laggingSources(cfg, queues, heads, msg.read)
			d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: msg.src, Stamp: msg.stamp, Sources: srcs, Factor: 1})
			if msg.src >= 0 {
				heads[msg.src]++
			}
			if !d.Admitted {
				shed++
			}
		}
		require.Zerof(t, shed, "seed %d: %d messages of a smooth compliant tenant shed (rate %v burst %d)", seed, shed, rate, burst)
	}
}

// The conformant property through the whole meter for stalls within the cover, with bursts.
func TestMeterConformantTenantLosesNothingWithinTheCover(t *testing.T) {
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 4 * time.Second
		cfg := randomCfg(rng, horizon)
		sends := conform(genSends(rng, cfg.rate, cfg.burst, horizon, 3), cfg.rate, cfg.burst)
		msgs := buildMsgs(rng, cfg, sends, 0.2)
		queues := splitQueues(msgs, cfg.sources)
		heads := make([]int, cfg.sources)
		clk := &lagClock{t: simEpoch}
		m := newTestMeter(clk, cfg.rate, cfg.burst, cfg.sources)
		for _, msg := range order(rng, msgs, cfg.sources) {
			clk.t = msg.read
			srcs := laggingSources(cfg, queues, heads, msg.read)
			d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: msg.src, Stamp: msg.stamp, Sources: srcs, Factor: 1})
			if msg.src >= 0 {
				heads[msg.src]++
			}
			require.Truef(t, d.Admitted, "seed %d: conformant message shed on %v (cause %v)", seed, d.Route, d.Cause)
		}
	}
}

// The cover that decides the backlog route comes from the BASE ceiling. A factor of 0 stretching it to
// the fixed 5 s (a zero rate has no cover of its own) would let a message 3 s late be hard-dropped as
// "fresh" when it is a backlog the contention bands must not touch.
func TestMeterCoverIgnoresTheShedFactor(t *testing.T) {
	clk := &lagClock{t: simEpoch.Add(time.Hour)}
	// The default 5 s phase threshold, so the stream is NOT in backlog and only the tenant's cover can
	// route this message.
	m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 1, Ceiling: StaticCeiling(1000, 2000), Clock: clk.now}) // cover 2 s
	d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t.Add(-3 * time.Second),
		Sources: []LagCreditSource{{Lag: 3 * time.Second}}, Factor: 0})
	require.Equal(t, RouteBacklog, d.Route, "3 s late is past the 2 s cover whatever the shed factor")
	require.True(t, d.Admitted, "a drained backlog is exempt from the hard drop")
}

// A stream's exit from backlog takes nothing from a tenant: both the one that used the backlog route and
// a bystander find their buckets refilled, and a conformant burst after the exit is admitted whole.
func TestMeterExitShedsNoCompliantTenant(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 1, Ceiling: StaticCeiling(10, 100), Clock: clk.now}) // cover 5 s
	one := func(tenant string, stamp time.Time, lag time.Duration) MeterDecision {
		return m.Admit(MeterRequest{Tenant: tenant, N: 1, Source: 0, Stamp: stamp, Sources: []LagCreditSource{{Lag: lag}}, Factor: 1})
	}
	for one("t", clk.t, 0).Admitted { // spend the fresh bucket
	}
	for one("idle", clk.t, 0).Admitted {
	}
	require.Equal(t, RouteBacklog, one("t", clk.t.Add(-6*time.Second), 6*time.Second).Route)
	clk.t = clk.t.Add(time.Minute)
	one("t", clk.t, 0) // t's own calm traffic: the first starts the calm clock, the second 31 s on ends the phase
	clk.t = clk.t.Add(31 * time.Second)
	exit := one("t", clk.t, 0)
	inBacklog, _ := m.Phase(0, "t")
	require.False(t, inBacklog)
	require.Equal(t, RouteFresh, exit.Route)
	for _, tenant := range []string{"t", "idle"} {
		n := 0
		if tenant == "t" {
			n = 1 // the message that ended the phase was admitted on the fresh route
		}
		for one(tenant, clk.t, 0).Admitted {
			n++
		}
		require.Equal(t, 100, n, "%s: a conformant burst after the exit must be admitted whole", tenant)
	}
}

// The reviewer's repro. Default tier 1000/2000, two sources. A 2000 burst on source 0, one message on the
// backlog route, the phase exits at 90 s, then a conformant 2000 burst plus 900/s on source 1, stalled
// 1.5 s. 3351 messages, all within the ceiling: none may be shed.
func TestMeterReviewerReproNoLossAfterAnExit(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 2, Ceiling: StaticCeiling(1000, 2000), Clock: clk.now})
	cur := idleSources(2)
	admit := func(tenant string, src int, stamp time.Time, srcs []LagCreditSource) MeterDecision {
		return m.Admit(MeterRequest{Tenant: tenant, N: 1, Source: src, Stamp: stamp, Sources: srcs, Factor: 1})
	}
	for i := 0; i < 2000; i++ {
		require.True(t, admit("c", 0, clk.t, cur).Admitted)
	}
	clk.t = simEpoch.Add(10 * time.Second)
	late := admit("c", 0, clk.t.Add(-2500*time.Millisecond), []LagCreditSource{{Lag: 2500 * time.Millisecond}, {}})
	require.Equal(t, RouteBacklog, late.Route)
	for _, at := range []time.Duration{11 * time.Second, 42 * time.Second, 90 * time.Second} {
		clk.t = simEpoch.Add(at) // c's own calm trickle: calm from 11 s, so the phase ends by 42 s
		lags := cur
		if at == 90*time.Second {
			lags = []LagCreditSource{{}, {Lag: 1500 * time.Millisecond}} // source 1 has been stalled since 88.5 s
		}
		require.True(t, admit("c", 0, clk.t, lags).Admitted)
	}
	inBacklog, epoch := m.Phase(0, "c")
	require.False(t, inBacklog, "the phase exited")
	require.Equal(t, uint64(1), epoch)

	shed, total := 0, 0
	for i := 0; i < 2000; i++ { // the conformant burst, on the now-current source 0
		total++
		if !admit("c", 0, clk.t, []LagCreditSource{{}, {Lag: 1500 * time.Millisecond}}).Admitted {
			shed++
		}
	}
	for i := 0; i < 1350; i++ { // 900/s for the 1.5 s source 1 sat stalled, read now
		sent := clk.t.Add(-1500*time.Millisecond + time.Duration(i)*time.Second/900)
		total++
		if !admit("c", 1, sent, []LagCreditSource{{}, {Lag: clk.t.Sub(sent)}}).Admitted {
			shed++
		}
	}
	require.Zero(t, shed, "%d of %d conformant messages shed after the exit", shed, total)
}

// Contention is a filter and is not phase-bound: in the tail of a stall, while the stream is still in its
// backlog phase, factor 0 refuses young traffic and a fractional factor holds it to its share, while
// anything older than the cover still drains unshed.
func TestMeterPhaseTailIsContended(t *testing.T) {
	newMeter := func() (*LagCreditMeter, *lagClock) {
		clk := &lagClock{t: simEpoch.Add(time.Hour)}
		m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 1, Ceiling: StaticCeiling(1000, 2000), Clock: clk.now})
		// a 10 s stall's oldest message takes the stream into backlog
		d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t.Add(-10 * time.Second),
			Sources: []LagCreditSource{{Lag: 10 * time.Second}}, Factor: 1})
		require.Equal(t, RouteBacklog, d.Route)
		inBacklog, _ := m.Phase(0, "t")
		require.True(t, inBacklog)
		return m, clk
	}
	young := func(m *LagCreditMeter, clk *lagClock, f float64) (admitted int, last MeterDecision) {
		for i := 0; i < 3000; i++ {
			last = m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t.Add(-100 * time.Millisecond),
				Sources: []LagCreditSource{{Lag: 100 * time.Millisecond}}, Factor: f})
			if last.Admitted {
				admitted++
			}
		}
		return
	}
	m, clk := newMeter()
	n, last := young(m, clk, 0)
	require.Zero(t, n, "the hard drop reaches young traffic in the phase tail")
	require.Equal(t, CauseHardDrop, last.Cause)

	m, clk = newMeter()
	n, _ = young(m, clk, 0.25)
	require.LessOrEqual(t, n, int(0.25*(2000+1000*0.1+1000*2))+2, "a quarter factor holds young traffic to its share in the tail")
	require.Greater(t, n, 100)

	m, clk = newMeter()
	n, _ = young(m, clk, 1)
	require.Equal(t, 2000, n, "no contention: the ceiling alone")

	// a true backlog (older than the cover) is still exempt
	m, clk = newMeter()
	old := 0
	for i := 0; i < 1500; i++ {
		d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t.Add(-4*time.Second + time.Duration(i)*time.Millisecond),
			Sources: []LagCreditSource{{Lag: 4 * time.Second}}, Factor: 0})
		if d.Admitted {
			old++
		}
	}
	require.Equal(t, 1500, old, "older than the cover drains unshed even at factor 0")
}

// A conformant BURSTING tenant loses nothing through the whole meter across stalls of any length, with
// the phase entering and exiting under it: the generator that exercises the exit.
func TestMeterConformantBurstyTenantLosesNothingAcrossPhases(t *testing.T) {
	exits := 0
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 70 * time.Second
		rate := float64(50 + rng.Intn(250))
		burst := 1 + rng.Intn(1200)
		cfg := simCfg{rate: rate, burst: burst, sources: 1 + rng.Intn(3)}
		cfg.base = make([]time.Duration, cfg.sources)
		cfg.offsets = make([]time.Duration, cfg.sources)
		cfg.stalls = make([][]simStall, cfg.sources)
		for k := 0; k < cfg.sources; k++ {
			cfg.offsets[k] = time.Duration(rng.Intn(400)-200) * time.Millisecond
			for tt := time.Duration(rng.Intn(5000)) * time.Millisecond; tt < horizon && rng.Intn(5) != 0; {
				dur := time.Duration(rng.Intn(14000)) * time.Millisecond
				cfg.stalls[k] = append(cfg.stalls[k], simStall{start: simEpoch.Add(tt), dur: dur})
				tt += dur + time.Duration(rng.Intn(50000))*time.Millisecond
			}
		}
		sends := conform(genSends(rng, rate, burst, horizon, 3), rate, burst)
		msgs := buildMsgs(rng, cfg, sends, 0.15)
		queues := splitQueues(msgs, cfg.sources)
		heads := make([]int, cfg.sources)
		clk := &lagClock{t: simEpoch}
		m := newTestMeter(clk, rate, burst, cfg.sources)
		prev := make([]uint64, cfg.sources)
		for _, msg := range order(rng, msgs, cfg.sources) {
			clk.t = msg.read
			srcs := laggingSources(cfg, queues, heads, msg.read)
			d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: msg.src, Stamp: msg.stamp, Sources: srcs, Factor: 1})
			if msg.src >= 0 {
				heads[msg.src]++
			}
			require.Truef(t, d.Admitted, "seed %d: a conformant message was shed on the %v route (rate %v burst %d)", seed, d.Route, rate, burst)
			for k := range prev {
				if _, e := m.Phase(k, "t"); e > prev[k] {
					exits += int(e - prev[k])
					prev[k] = e
				}
			}
		}
	}
	require.Greater(t, exits, propertySeeds/4, "the generator barely exercised a phase exit (%d)", exits)
}

// TENANT ISOLATION OF THE PHASE. Tenants of different covers share streams, stalls and sources. Run
// together, every tenant gets exactly the verdicts it gets when its messages are presented to a meter
// alone (given the same per-source lag and skew inputs): no tenant's cover, ceiling, flood or phase
// changes another tenant's allowance. A phase shared by a stream fails this immediately: the shortest
// cover in the mix holds it open for everyone.
func TestMeterTenantsOfMixedCoversDoNotAffectEachOther(t *testing.T) {
	type tagged struct {
		simMsg
		tenant int
	}
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 20 * time.Second
		base := randomCfg(rng, horizon)
		for k := range base.stalls { // stalls of up to the longest cover, so every tenant's phase can be in play
			base.stalls[k] = append(base.stalls[k], simStall{start: simEpoch.Add(time.Duration(rng.Intn(8000)) * time.Millisecond),
				dur: time.Duration(rng.Intn(4000)) * time.Millisecond})
			sort.Slice(base.stalls[k], func(i, j int) bool { return base.stalls[k][i].start.Before(base.stalls[k][j].start) })
		}
		// the stall list must stay monotone for a FIFO reader: drop overlaps
		for k := range base.stalls {
			var kept []simStall
			var end time.Time
			for _, st := range base.stalls[k] {
				if st.start.Before(end) {
					continue
				}
				kept = append(kept, st)
				end = st.start.Add(st.dur)
			}
			base.stalls[k] = kept
		}
		type tenantCfg struct {
			name  string
			rate  float64
			burst int
			simCfg
		}
		var tenants []tenantCfg
		var all []tagged
		for ti := 0; ti < 3; ti++ {
			c := base
			c.rate = float64(5 + rng.Intn(1200))
			c.burst = 1 + rng.Intn(3000)
			if ti == 0 {
				c.rate, c.burst = 10, 1 // a 100 ms cover beside everyone else
			}
			sends := genSends(rng, c.rate, c.burst, horizon, 4)
			for _, m := range buildMsgs(rng, c, sends, 0.2) {
				all = append(all, tagged{m, ti})
			}
			tenants = append(tenants, tenantCfg{name: fmt.Sprintf("t%d", ti), rate: c.rate, burst: c.burst, simCfg: c})
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].send.Before(all[j].send) })

		resolve := func(name string) TenantCeiling {
			for _, tc := range tenants {
				if tc.name == name {
					return TenantCeiling{RatePerSecond: tc.rate, Burst: tc.burst, Source: CeilingStatic}
				}
			}
			return TenantCeiling{RatePerSecond: 1, Burst: 1}
		}
		newMeter := func(clk *lagClock) *LagCreditMeter {
			return NewLagCreditMeter(LagCreditMeterConfig{Streams: base.sources, Ceiling: resolve, Clock: clk.now})
		}
		// order all messages for delivery
		flat := make([]simMsg, len(all))
		for i, a := range all {
			a.simMsg.seq = i
			flat[i] = a.simMsg
		}
		ordered := order(rng, flat, base.sources)
		queues := splitQueues(flat, base.sources)
		heads := make([]int, base.sources)

		type call struct {
			req MeterRequest
			at  time.Time
		}
		calls := make([]call, len(ordered))
		clk := &lagClock{t: simEpoch}
		together := newMeter(clk)
		verdicts := make([]MeterDecision, len(ordered))
		for i, m := range ordered {
			clk.t = m.read
			srcs := laggingSources(base, queues, heads, m.read)
			if m.src >= 0 {
				heads[m.src]++
			}
			ti := all[m.seq].tenant
			req := MeterRequest{Tenant: tenants[ti].name, N: 1, Source: m.src, Stamp: m.stamp, Sources: srcs, Factor: 1}
			calls[i] = call{req, m.read}
			verdicts[i] = together.Admit(req)
		}
		for ti := range tenants {
			aclk := &lagClock{t: simEpoch}
			alone := newMeter(aclk)
			for i, c := range calls {
				if c.req.Tenant != tenants[ti].name {
					continue
				}
				aclk.t = c.at
				d := alone.Admit(c.req)
				require.Equalf(t, d, verdicts[i], "seed %d: tenant %s got a different verdict beside its neighbours (message %d, %v route)",
					seed, tenants[ti].name, i, verdicts[i].Route)
			}
		}
	}
}

// The per-(stream, tenant) phase table is bounded: tenants that come and go leave nothing behind past the
// idle TTL.
func TestMeterPhaseTableIsBoundedUnderTenantChurn(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 2, Ceiling: StaticCeiling(1000, 2000), Clock: clk.now})
	lag := []LagCreditSource{{Lag: 10 * time.Second}, {Lag: 10 * time.Second}}
	peak := 0
	for i := 0; i < 60000; i++ {
		clk.t = clk.t.Add(100 * time.Millisecond) // 600 tenants a minute, each seen once and in backlog
		d := m.Admit(MeterRequest{Tenant: fmt.Sprintf("tenant-%d", i), N: 1, Source: i % 2, Stamp: clk.t.Add(-10 * time.Second), Sources: lag, Factor: 1})
		require.Equal(t, RouteBacklog, d.Route)
		peak = max(peak, m.PhaseCount())
	}
	require.LessOrEqual(t, peak, 600*11+64, "idle phases were not evicted")
	require.Less(t, peak, 30000)

	// and a tenant that is never in backlog never costs an entry
	clk2 := &lagClock{t: simEpoch}
	m2 := NewLagCreditMeter(LagCreditMeterConfig{Streams: 1, Ceiling: StaticCeiling(1000, 2000), Clock: clk2.now})
	for i := 0; i < 5000; i++ {
		m2.Admit(MeterRequest{Tenant: fmt.Sprintf("calm-%d", i), N: 1, Source: 0, Stamp: clk2.t, Sources: idleSources(1), Factor: 1})
	}
	require.Zero(t, m2.PhaseCount())
}

// An expired phase is dead whatever the amortised sweep has done: a neighbour in the same shard that
// advances the sweep just before the entry's TTL must not keep it alive (the verdicts of one tenant would
// then depend on another's traffic).
func TestMeterExpiredPhaseIsNotRevivedByANeighbourSweep(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 1, Ceiling: StaticCeiling(1, 2), Clock: clk.now})
	idle := idleSources(1)
	stamped := func(tenant string, age time.Duration) MeterDecision {
		return m.Admit(MeterRequest{Tenant: tenant, N: 1, Source: 0, Stamp: clk.t.Add(-age), Sources: idle, Factor: 1})
	}
	live := func(tenant string) bool {
		return m.Admit(MeterRequest{Tenant: tenant, N: 1, Source: -1, Sources: idle, Factor: 1}).Admitted
	}
	// a neighbour that lands in the same shard as "a"
	neighbour := ""
	for i := 0; neighbour == ""; i++ {
		if n := fmt.Sprintf("neighbour-%d", i); m.phases.shardFor(n) == m.phases.shardFor("a") {
			neighbour = n
		}
	}
	require.Equal(t, RouteBacklog, stamped("a", 3*time.Second).Route)
	clk.t = simEpoch.Add(599 * time.Second)
	stamped(neighbour, 0) // sweeps the shard before "a" reaches its TTL
	clk.t = simEpoch.Add(600*time.Second + 100*time.Millisecond)
	require.True(t, live("a"))
	require.True(t, live("a")) // the fresh allowance (burst 2) is spent
	for i := 0; i < 2; i++ {
		d := stamped("a", 0)
		require.Equal(t, RouteFresh, d.Route, "the expired phase must not come back")
		require.False(t, d.Admitted, "and so the exhausted fresh bucket refuses, as it does with no neighbour")
	}
}
