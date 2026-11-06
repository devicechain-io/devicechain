// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lagClock is an injected clock.
type lagClock struct{ t time.Time }

func (c *lagClock) now() time.Time { return c.t }

// propertySeeds is how many random histories each property replays. LAGCREDIT_SEEDS raises it for a
// deep run (a thousand takes minutes); the default keeps the suite to seconds.
var propertySeeds = seedsFromEnv(120)

func seedsFromEnv(def int) int {
	if v, err := strconv.Atoi(os.Getenv("LAGCREDIT_SEEDS")); err == nil && v > 0 {
		return v
	}
	return def
}

// runConformant replays a conformant history through a LagCreditLimiter and returns the number of
// messages it refused. restartAt, when >= 0, replaces the limiter (a pod restart: every bucket and
// last stamp gone) before that message.
func runConformant(t *testing.T, seed int64, liveShare float64, restart bool) (refused, total int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	horizon := 4 * time.Second
	cfg := randomCfg(rng, horizon)
	sends := conform(genSends(rng, cfg.rate, cfg.burst, horizon, 3), cfg.rate, cfg.burst)
	msgs := buildMsgs(rng, cfg, sends, liveShare)
	ordered := order(rng, msgs, cfg.sources)
	queues := splitQueues(msgs, cfg.sources)
	heads := make([]int, cfg.sources)

	clk := &lagClock{t: simEpoch}
	newLimiter := func() *LagCreditLimiter {
		return NewLagCreditLimiter(StaticCeiling(cfg.rate, cfg.burst), cfg.sources, WithLagCreditClock(clk.now))
	}
	l := newLimiter()
	restartAt := -1
	if restart && len(ordered) > 2 {
		restartAt = 1 + rng.Intn(len(ordered)-1)
	}
	for i, m := range ordered {
		if i == restartAt {
			l = newLimiter()
		}
		clk.t = m.read
		srcs := laggingSources(cfg, queues, heads, m.read)
		d := l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: m.src, Stamp: m.stamp, Sources: srcs})
		if m.src >= 0 {
			heads[m.src]++
		}
		total++
		if !d.Admitted {
			refused++
		}
	}
	return refused, total
}

// THE CORE PROPERTY (I-A). A tenant whose true send sequence conforms to its ceiling is never refused,
// for any reader stall of up to its cover, however its messages are split across sources, whatever
// order the sources are read in, with unstamped (external MQTT) traffic mixed in, and across a pod
// restart in the middle of a stall. The oracle is an offline token bucket over the TRUE send times.
func TestLagCreditNeverRefusesAConformantTenant(t *testing.T) {
	for _, tc := range []struct {
		name      string
		liveShare float64
		restart   bool
	}{
		{"stamped", 0, false},
		{"with external MQTT", 0.25, false},
		{"restart mid-stall", 0, true},
		{"restart with external MQTT", 0.25, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refusedAny, total int
			for seed := int64(0); seed < int64(propertySeeds); seed++ {
				refused, n := runConformant(t, seed, tc.liveShare, tc.restart)
				total += n
				if refused > 0 {
					refusedAny++
					t.Fatalf("seed %d: %d of %d conformant messages refused", seed, refused, n)
				}
			}
			require.Greater(t, total, propertySeeds*50, "the generator produced too little traffic to mean anything")
			require.Zero(t, refusedAny)
		})
	}
}

// The counterweight to the property above: the SAME histories with the stalls removed and no credit
// to give are admitted too, but a plain bucket charged at pod time is NOT enough once there is a
// stall. This pins that the generator really does stress the credit: a limiter given no lag credit
// (every source reporting itself current) refuses conformant tenants in a large share of the seeds.
func TestLagCreditGeneratorActuallyNeedsTheCredit(t *testing.T) {
	failing := 0
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 4 * time.Second
		cfg := randomCfg(rng, horizon)
		sends := conform(genSends(rng, cfg.rate, cfg.burst, horizon, 3), cfg.rate, cfg.burst)
		msgs := buildMsgs(rng, cfg, sends, 0)
		clk := &lagClock{t: simEpoch}
		l := NewLagCreditLimiter(StaticCeiling(cfg.rate, cfg.burst), cfg.sources, WithLagCreditClock(clk.now))
		refused := 0
		for _, m := range order(rng, msgs, cfg.sources) {
			clk.t = m.read
			// No credit: every source claims to be current.
			if !l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: m.src, Stamp: m.stamp,
				Sources: make([]LagCreditSource, cfg.sources)}).Admitted {
				refused++
			}
		}
		if refused > 0 {
			failing++
		}
	}
	require.Greater(t, failing, propertySeeds/10, "without lag credit only %d of %d seeds shed a conformant tenant: the generator is too gentle", failing, propertySeeds)
}

// No minting (I-D). Whatever the sender does and whatever garbage the lag and skew inputs say, the
// bucket admits at most burst + rate·(Δnow + cover) in ANY window of read times.
func TestLagCreditNeverAdmitsMoreThanTheBound(t *testing.T) {
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 4 * time.Second
		cfg := randomCfg(rng, horizon)
		sends := genSends(rng, cfg.rate, cfg.burst, horizon, 6) // up to ~7x the ceiling
		msgs := buildMsgs(rng, cfg, sends, 0.2)
		cover := LagCreditMax(TenantCeiling{RatePerSecond: cfg.rate, Burst: cfg.burst})

		clk := &lagClock{t: simEpoch}
		l := NewLagCreditLimiter(StaticCeiling(cfg.rate, cfg.burst), cfg.sources, WithLagCreditClock(clk.now))
		var admitted []time.Time
		for _, m := range order(rng, msgs, cfg.sources) {
			clk.t = m.read
			srcs := make([]LagCreditSource, cfg.sources)
			for k := range srcs { // hostile inputs: lags far beyond the cover, wild skews
				srcs[k].Lag = time.Duration(rng.Int63n(int64(60 * time.Second)))
				srcs[k].Skew = time.Duration(rng.Int63n(int64(4*time.Second))) - 2*time.Second
			}
			stamp := m.stamp
			if rng.Intn(3) == 0 && m.src >= 0 {
				stamp = m.read.Add(-time.Duration(rng.Int63n(int64(60 * time.Second)))) // forged ages
			}
			if l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: m.src, Stamp: stamp, Sources: srcs}).Admitted {
				admitted = append(admitted, m.read)
			}
		}
		// For every window [i, j] of admissions: (j−i+1) ≤ burst + rate·(t_j − t_i + cover). With
		// f(j) = j − rate·t_j that is f(j) − f(i) + 1 ≤ burst + rate·cover, so the worst j for each i is
		// the suffix maximum of f.
		f := make([]float64, len(admitted))
		for j := range admitted {
			f[j] = float64(j) - cfg.rate*admitted[j].Sub(simEpoch).Seconds()
		}
		sufMax := make([]float64, len(admitted)+1)
		sufMax[len(admitted)] = math.Inf(-1)
		for j := len(admitted) - 1; j >= 0; j-- {
			sufMax[j] = math.Max(f[j], sufMax[j+1])
		}
		limit := float64(cfg.burst) + cfg.rate*cover.Seconds()
		for i := range admitted {
			if sufMax[i]-f[i]+1 > limit+1e-6 {
				t.Fatalf("seed %d: a window starting at admission %d exceeds burst+rate·(Δ+cover): %.2f > %.2f",
					seed, i, sufMax[i]-f[i]+1, limit)
			}
		}
	}
}

// The tenant above its ceiling is shed by about its excess, with a stall in the way: admitted is
// within one burst + one cover's credit of the oracle's count, never wholesale and never nothing.
func TestLagCreditShedsAnOverCeilingTenantByItsExcess(t *testing.T) {
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		horizon := 4 * time.Second
		cfg := randomCfg(rng, horizon)
		sends := genSends(rng, cfg.rate, cfg.burst, horizon, 6)
		msgs := buildMsgs(rng, cfg, sends, 0)
		cover := LagCreditMax(TenantCeiling{RatePerSecond: cfg.rate, Burst: cfg.burst})
		oracle := len(conform(sends, cfg.rate, cfg.burst))

		clk := &lagClock{t: simEpoch}
		l := NewLagCreditLimiter(StaticCeiling(cfg.rate, cfg.burst), cfg.sources, WithLagCreditClock(clk.now))
		queues := splitQueues(msgs, cfg.sources)
		heads := make([]int, cfg.sources)
		admitted := 0
		for _, m := range order(rng, msgs, cfg.sources) {
			clk.t = m.read
			srcs := laggingSources(cfg, queues, heads, m.read)
			if l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: m.src, Stamp: m.stamp, Sources: srcs}).Admitted {
				admitted++
			}
			heads[m.src]++
		}
		// never fewer than the oracle (a stall only ever helps a tenant here), never more than the
		// oracle plus one burst and one cover of accrual.
		slack := float64(cfg.burst) + cfg.rate*cover.Seconds()
		require.GreaterOrEqual(t, admitted, oracle, "seed %d: shed more than the offline bucket would", seed)
		require.LessOrEqual(t, float64(admitted-oracle), slack+1, "seed %d: admitted %d against an oracle %d", seed, admitted, oracle)
	}
}

// RAISING CAPACITY MINTS NOTHING. A bucket drained at an instant stays drained at that instant
// however large the credit presented at the same instant: only elapsed time adds tokens.
func TestLagCreditRaisingCapacityAddsNoTokens(t *testing.T) {
	for seed := int64(0); seed < int64(propertySeeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		rate, burst := float64(10+rng.Intn(2000)), 1+rng.Intn(3000)
		clk := &lagClock{t: simEpoch}
		l := NewLagCreditLimiter(StaticCeiling(rate, burst), 2, WithLagCreditClock(clk.now))
		idle := make([]LagCreditSource, 2)
		// Create the bucket with no credit and drain it completely at this instant.
		n := 0
		for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: idle}).Admitted {
			n++
			require.LessOrEqual(t, n, burst+1)
		}
		// Hmm: the bucket was created at capacity = burst (no credit), so exactly burst were admitted.
		require.Equal(t, burst, n)
		// Now present the biggest credit there is, at the same instant.
		lagging := []LagCreditSource{{Lag: time.Minute}, {Lag: time.Minute}}
		for i := 0; i < 5; i++ {
			require.False(t, l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: lagging}).Admitted,
				"seed %d: raising the credit admitted a message with no time elapsed", seed)
		}
		// And elapsed time earns exactly rate·Δ, not more, however large the capacity.
		clk.t = clk.t.Add(time.Second)
		got := 0
		for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: lagging}).Admitted {
			got++
			require.LessOrEqual(t, got, int(rate)+2)
		}
		// one second earns rate tokens, up to the capacity the lagging sources lend: burst + rate·cover
		cover := LagCreditMax(TenantCeiling{RatePerSecond: rate, Burst: burst})
		want := math.Min(rate, float64(burst)+rate*cover.Seconds())
		require.InDelta(t, want, float64(got), 1.0, "seed %d: one second earned %d tokens at rate %v", seed, got, rate)
	}
}

// THE SWEEP BOUNDS MEMORY under tenant churn: a million tenants seen over time never leave more than
// the recently active ones behind.
func TestLagCreditSweepBoundsMemoryUnderChurn(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(100, 10), 1, WithLagCreditClock(clk.now),
		WithLagCreditIdle(10*time.Minute, time.Minute))
	idle := make([]LagCreditSource, 1)
	peak := 0
	for i := 0; i < 60000; i++ {
		clk.t = clk.t.Add(100 * time.Millisecond) // 600 tenants a minute, each seen once
		l.Admit(LagCreditRequest{Tenant: fmt.Sprintf("tenant-%d", i), N: 1, Scale: 1, Source: -1, Sources: idle})
		peak = max(peak, l.Len())
	}
	// ten minutes of idle plus one sweep interval of unswept tail
	require.LessOrEqual(t, peak, 600*11+10, "idle buckets were not evicted")
	require.Less(t, peak, 60000)
}

func TestLagCreditRejectsMisuseLoudly(t *testing.T) {
	l := NewLagCreditLimiter(StaticCeiling(10, 10), 2)
	ok := make([]LagCreditSource, 2)
	require.Panics(t, func() { l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: ok[:1]}) })
	require.Panics(t, func() { l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 0, Source: -1, Sources: ok}) })
	require.Panics(t, func() { l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1.5, Source: -1, Sources: ok}) })
	require.Panics(t, func() { l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 2, Sources: ok}) })
	require.Panics(t, func() { NewLagCreditLimiter(nil, 1) })
	require.Panics(t, func() { NewLagCreditLimiter(StaticCeiling(1, 1), 0) })
	require.True(t, l.Admit(LagCreditRequest{Tenant: "t", N: 0, Scale: 1, Source: -1, Sources: ok}).Admitted, "an empty charge admits")
}

// An unknown last stamp is an infinitely old one: it contributes the source's LAG, not 0. A pod that
// restarted in the middle of a stall has no stamp for any source, yet the readers are 1.5 s behind.
func TestLagCreditUnknownSourceCountsAsItsLag(t *testing.T) {
	const rate, burst = 1000.0, 2000
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(rate, burst), 2, WithLagCreditClock(clk.now))

	// Source 1's reader is 1.5 s behind; the tenant's first message here arrives on source 0, which
	// is current, so source 1 has no stamp in the bucket.
	srcs := []LagCreditSource{{}, {Lag: 1500 * time.Millisecond}}
	d := l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: simEpoch, Sources: srcs})
	require.True(t, d.Admitted)
	require.Equal(t, 1500*time.Millisecond, d.Credit, "an unknown source was credited 0 instead of its lag")
}

// A source the tenant used long ago contributes only what its reader actually lags NOW: a current
// reader credits 0 however old the stamp.
func TestLagCreditStaleStampOnACurrentSourceCreditsNothing(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(1000, 2000), 2, WithLagCreditClock(clk.now))
	cur := make([]LagCreditSource, 2)
	l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 1, Stamp: simEpoch, Sources: cur})
	clk.t = simEpoch.Add(time.Minute)
	d := l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: clk.t, Sources: cur})
	require.Zero(t, d.Credit)
}

// The credit is min(stamp age, lag): a fresh stamp on a lagging source credits only its age, and the
// whole is capped at the tenant's cover.
func TestLagCreditCreditIsBoundedByAgeLagAndCover(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(1000, 2000), 1, WithLagCreditClock(clk.now)) // cover 2 s
	young := l.Admit(LagCreditRequest{Tenant: "a", N: 1, Scale: 1, Source: 0, Stamp: simEpoch.Add(-300 * time.Millisecond),
		Sources: []LagCreditSource{{Lag: 10 * time.Second}}})
	require.Equal(t, 300*time.Millisecond, young.Credit, "credit must not exceed the age of the stamp")
	capped := l.Admit(LagCreditRequest{Tenant: "b", N: 1, Scale: 1, Source: 0, Stamp: simEpoch.Add(-9 * time.Second),
		Sources: []LagCreditSource{{Lag: 10 * time.Second}}})
	require.Equal(t, 2*time.Second, capped.Credit, "credit must not exceed the cover")
	skewed := l.Admit(LagCreditRequest{Tenant: "c", N: 1, Scale: 1, Source: 0, Stamp: simEpoch.Add(-time.Second),
		Sources: []LagCreditSource{{Lag: 10 * time.Second, Skew: 400 * time.Millisecond}}})
	require.Equal(t, 600*time.Millisecond, skewed.Credit, "the stamp's age is corrected by the skew")
	ahead := l.Admit(LagCreditRequest{Tenant: "d", N: 1, Scale: 1, Source: 0, Stamp: simEpoch.Add(time.Hour),
		Sources: []LagCreditSource{{Lag: 10 * time.Second}}})
	require.Zero(t, ahead.Credit, "a stamp from the future credits nothing")
}

// The cover comes from the BASE ceiling: a tiny burst has a tiny cover, a huge one is capped.
func TestLagCreditMaxFollowsTheBaseCeiling(t *testing.T) {
	require.Equal(t, 2*time.Second, LagCreditMax(TenantCeiling{RatePerSecond: 1000, Burst: 2000}))
	require.Equal(t, LagCreditMaxCover, LagCreditMax(TenantCeiling{RatePerSecond: 1, Burst: math.MaxInt32}))
	require.Equal(t, LagCreditMaxCover, LagCreditMax(TenantCeiling{}))
	require.Equal(t, 100*time.Millisecond, LagCreditMax(TenantCeiling{RatePerSecond: 1000, Burst: 100}))
}

// CAPACITY IS APPLIED BEFORE ACCRUAL. A bucket that was full at `burst` when the readers stalled has
// earned 3000 tokens by the time they resume; the clamp must be this admission's capacity
// (burst + rate·2 s), not the capacity the bucket had before the lag was known (burst).
func TestLagCreditCapacityIsSizedBeforeAccrual(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(1000, 2000), 1, WithLagCreditClock(clk.now))
	cur := make([]LagCreditSource, 1)
	// full bucket, drained to ~0 at t0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: simEpoch, Sources: cur}).Admitted {
	}
	// 3 s stall: the reader resumes with a 2 s head-of-line lag (the backlog route took the older part).
	clk.t = simEpoch.Add(3 * time.Second)
	srcs := []LagCreditSource{{Lag: 2 * time.Second}}
	n := 0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: simEpoch.Add(time.Second), Sources: srcs}).Admitted {
		n++
	}
	require.Equal(t, 3000, n, "3 s of accrual must be kept: the capacity is burst + rate·credit = 4000")
}

// EXIT-GRANT SUPPRESSION. A source that leaves its backlog phase must not hand a tenant that spent its
// bucket before the phase the whole phase's accrual as a second burst.
func TestLagCreditExitGrantIsSuppressed(t *testing.T) {
	run := func(exit bool) int {
		clk := &lagClock{t: simEpoch}
		l := NewLagCreditLimiter(StaticCeiling(1000, 2000), 1, WithLagCreditClock(clk.now))
		cur := make([]LagCreditSource, 1)
		for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: simEpoch, Sources: cur}).Admitted {
		}
		// two minutes of backlog phase with no fresh charge, then the phase exits.
		clk.t = simEpoch.Add(2 * time.Minute)
		srcs := []LagCreditSource{{}}
		if exit {
			srcs[0].ExitEpoch, srcs[0].ExitAt = 1, clk.t
		}
		n := 0
		for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: clk.t, Sources: srcs}).Admitted {
			n++
		}
		return n
	}
	require.Equal(t, 2000, run(false), "with no exit marks the bucket refills to its burst (the baseline)")
	got := run(true)
	require.Equal(t, 2000, got, "accrual runs from exitAt − cover = 2 s, which is exactly one burst at this ceiling")

	// A shorter cover than the burst makes the suppression bite: 10/s with a burst of 100 has a 5 s cover
	// and 50 tokens of accrual from exitAt − cover.
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(10, 100), 1, WithLagCreditClock(clk.now))
	cur := make([]LagCreditSource, 1)
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: simEpoch, Sources: cur}).Admitted {
	}
	clk.t = simEpoch.Add(8 * time.Minute) // under the idle TTL, or the bucket is swept and starts full
	srcs := []LagCreditSource{{ExitEpoch: 1, ExitAt: clk.t}}
	n := 0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: clk.t, Sources: srcs}).Admitted {
		n++
	}
	require.Equal(t, 50, n, "after an exit the bucket earned only the last cover (5 s at 10/s), not eight minutes")
	// And only the FIRST admission after the exit is suppressed; later time accrues normally.
	clk.t = clk.t.Add(10 * time.Second)
	n = 0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: 0, Stamp: clk.t, Sources: srcs}).Admitted {
		n++
	}
	require.Equal(t, 100, n, "10 s of ordinary accrual at 10/s fills the burst of 100")
}

// A float64 bucket stays exact at the largest bursts a tier can set.
func TestLagCreditExactAtMaxInt32Burst(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(1000, math.MaxInt32), 1, WithLagCreditClock(clk.now))
	idle := make([]LagCreditSource, 1)
	require.True(t, l.Admit(LagCreditRequest{Tenant: "t", N: math.MaxInt32, Scale: 1, Source: -1, Sources: idle}).Admitted)
	require.False(t, l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: idle}).Admitted)
	clk.t = clk.t.Add(time.Second)
	require.True(t, l.Admit(LagCreditRequest{Tenant: "t", N: 1000, Scale: 1, Source: -1, Sources: idle}).Admitted)
	require.False(t, l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: idle}).Admitted)
}

// A scaled (contention) bucket admits the factor's share of a flood and its capacity scales too.
func TestLagCreditScaleIsAShareOfTheCeiling(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(1000, 2000), 1, WithLagCreditClock(clk.now))
	idle := make([]LagCreditSource, 1)
	n := 0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 0.25, Source: -1, Sources: idle}).Admitted {
		n++
	}
	require.Equal(t, 500, n, "a quarter of the burst")
	clk.t = clk.t.Add(time.Second)
	n = 0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 0.25, Source: -1, Sources: idle}).Admitted {
		n++
	}
	require.InDelta(t, 250, n, 1, "a quarter of the rate")
}

// Retuning: a ceiling that changes is read at the next admission, with the tokens kept.
func TestLagCreditPicksUpAChangedCeiling(t *testing.T) {
	c := TenantCeiling{RatePerSecond: 100, Burst: 100, Source: CeilingStatic}
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(func(string) TenantCeiling { return c }, 1, WithLagCreditClock(clk.now))
	idle := make([]LagCreditSource, 1)
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: idle}).Admitted {
	}
	c = TenantCeiling{RatePerSecond: 1000, Burst: 500, Source: CeilingStatic}
	clk.t = clk.t.Add(time.Second)
	n := 0
	for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: idle}).Admitted {
		n++
	}
	require.Equal(t, 500, n, "one second at the new rate, capped by the new burst")
}

// Tenants are isolated: one tenant's flood spends none of another's bucket.
func TestLagCreditTenantsAreIsolated(t *testing.T) {
	clk := &lagClock{t: simEpoch}
	l := NewLagCreditLimiter(StaticCeiling(10, 10), 1, WithLagCreditClock(clk.now))
	idle := make([]LagCreditSource, 1)
	for i := 0; i < 1000; i++ {
		l.Admit(LagCreditRequest{Tenant: "noisy", N: 1, Scale: 1, Source: -1, Sources: idle})
	}
	for i := 0; i < 10; i++ {
		require.True(t, l.Admit(LagCreditRequest{Tenant: "quiet", N: 1, Scale: 1, Source: -1, Sources: idle}).Admitted)
	}
}

// The reading stage lends the time a message sat between the two stages: Delay is extra credit, up to
// CreditCap, and it sizes the capacity like any other credit.
func TestLagCreditDelayLendsCreditUpToTheCap(t *testing.T) {
	admitted := func(delay, creditCap time.Duration) int {
		clk := &lagClock{t: simEpoch}
		l := NewLagCreditLimiter(StaticCeiling(1000, 2000), 1, WithLagCreditClock(clk.now))
		n := 0
		for l.Admit(LagCreditRequest{Tenant: "t", N: 1, Scale: 1, Source: -1, Sources: make([]LagCreditSource, 1),
			Delay: delay, CreditCap: creditCap}).Admitted {
			n++
		}
		return n
	}
	require.Equal(t, 2000, admitted(0, 0), "no delay, no credit")
	require.Equal(t, 3000, admitted(time.Second, 4*time.Second), "a second of delay lends a second of rate")
	require.Equal(t, 4000, admitted(10*time.Second, 2*time.Second), "capped by the credit cap")
	require.Equal(t, 2000, admitted(-time.Second, 0), "a negative delay lends nothing")
}
