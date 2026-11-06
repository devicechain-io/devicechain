// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestMeter(c *lagClock, rate float64, burst, streams int) *LagCreditMeter {
	return NewLagCreditMeter(LagCreditMeterConfig{
		Streams: streams, Ceiling: StaticCeiling(rate, burst), Clock: c.now,
		PhaseThreshold: LagCreditMax(TenantCeiling{RatePerSecond: rate, Burst: burst}),
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
	require.True(t, m.Phase(0).Backlog())
	require.Equal(t, RouteBacklog, m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: src(0), Factor: 1}).Route)
	clk.t = clk.t.Add(31 * time.Second)
	m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: src(0), Factor: 1}) // starts the calm clock
	clk.t = clk.t.Add(31 * time.Second)
	d := m.Admit(MeterRequest{Tenant: "t", N: 1, Source: 0, Stamp: clk.t, Sources: src(0), Factor: 1})
	require.Equal(t, RouteFresh, d.Route)
	epoch, _ := m.Phase(0).Exit()
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
// transitions included. Bursts right after a long backlog are the documented residual (the exit grant
// is suppressed) and are not part of this property.
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

// The meter hands the limiter its streams' exit marks: after a stream leaves backlog, a tenant that had
// spent its fresh bucket is not refilled for the whole phase, only for the cover before the exit.
func TestMeterPassesExitMarksToTheCeiling(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	m := NewLagCreditMeter(LagCreditMeterConfig{Streams: 1, Ceiling: StaticCeiling(10, 100), Clock: clk.now}) // cover 5 s
	one := func(tenant string, stamp time.Time, lag time.Duration) MeterDecision {
		return m.Admit(MeterRequest{Tenant: tenant, N: 1, Source: 0, Stamp: stamp, Sources: []LagCreditSource{{Lag: lag}}, Factor: 1})
	}
	for one("t", clk.t, 0).Admitted { // spend the fresh bucket
	}
	require.False(t, one("t", clk.t, 0).Admitted)
	for one("idle", clk.t, 0).Admitted { // a bystander spends its bucket too, then sits out the phase
	}

	// The tenant sends one message 6 s late on the backlog route (and so spends that allowance) and takes
	// the stream into backlog; a minute and a half of calm traffic brings it out again.
	require.Equal(t, RouteBacklog, one("t", clk.t.Add(-6*time.Second), 6*time.Second).Route)
	clk.t = clk.t.Add(time.Minute)
	one("other", clk.t, 0)
	clk.t = clk.t.Add(31 * time.Second)
	one("other", clk.t, 0)
	require.False(t, m.Phase(0).Backlog())

	n := 0
	for one("t", clk.t, 0).Admitted {
		n++
	}
	require.Equal(t, 50, n, "after the exit the tenant earned only the last cover (5 s at 10/s), not the whole phase")
	// The bystander used no backlog allowance, so the exit takes nothing from what it earned while idle.
	n = 0
	for one("idle", clk.t, 0).Admitted {
		n++
	}
	require.Equal(t, 100, n, "a tenant that never used the backlog route is not penalised for another tenant's stall")
}
