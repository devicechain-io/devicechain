// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

type streamEv struct {
	t  time.Time
	id Digest
}

// rateStep is a phase of a synthetic stream: rate events/s for secs seconds.
type rateStep struct {
	rate int
	secs int
}

// genStream builds a stream whose duplicates are drawn from every age the guard cares about:
// inside a generation, across rotations, at and either side of the retention boundary, and far
// past it. Append times are non-decreasing, as a stream's are; bursts put many events at one
// instant.
func genStream(seed int64, steps []rateStep, retention time.Duration, dupPct int) []streamEv {
	rng := rand.New(rand.NewSource(seed))
	var out []streamEv
	now := time.Unix(1_700_000_000+seed*7, 0)
	next := uint64(seed)<<40 | 1
	for _, st := range steps {
		end := now.Add(time.Duration(st.secs) * time.Second)
		step := time.Second / time.Duration(st.rate)
		for now.Before(end) {
			burst := 1
			if rng.Intn(500) == 0 {
				burst = 1 + rng.Intn(3*st.rate/2+1)
			}
			for b := 0; b < burst; b++ {
				var id Digest
				if len(out) > 0 && rng.Intn(100) < dupPct {
					var age time.Duration
					switch rng.Intn(4) {
					case 0:
						age = time.Duration(rng.Int63n(int64(genSeconds * time.Second)))
					case 1:
						age = time.Duration(rng.Int63n(int64(retention)))
					case 2:
						age = retention + time.Duration(rng.Int63n(int64(2*genSeconds*time.Second))) - genSeconds*time.Second
					default:
						age = time.Duration(rng.Int63n(int64(3 * retention)))
					}
					want := now.Add(-age)
					i := sort.Search(len(out), func(i int) bool { return !out[i].t.Before(want) })
					if i == len(out) {
						i = len(out) - 1
					}
					id = out[i].id
				} else {
					next++
					id = dig(next)
				}
				out = append(out, streamEv{t: now, id: id})
			}
			now = now.Add(step)
			if rng.Intn(50) == 0 {
				now = now.Add(time.Duration(rng.Int63n(int64(step) + 1)))
			}
		}
	}
	return out
}

func runDifferential(t *testing.T, name string, evs []streamEv, retention time.Duration, maxEntries int, skew bool) {
	t.Helper()
	og := newOldGuard(retention, maxEntries)
	ng := NewAppliedGuard(retention, maxEntries)
	dups := 0
	for i, e := range evs {
		at := e.t
		if skew && i%97 == 0 {
			at = at.Add(-time.Duration(i%5) * time.Minute) // behind the newest generation
		}
		o, n := og.Admit(at, e.id), ng.Admit(at, e.id)
		if o != n {
			t.Fatalf("%s: event %d (t=%s): old guard says admit=%v, new says %v", name, i, at, o, n)
		}
		if !o {
			dups++
		}
		if og.Len() != ng.Len() || og.Generations() != ng.Generations() {
			t.Fatalf("%s: event %d: old len/gens %d/%d, new %d/%d", name, i, og.Len(), og.Generations(), ng.Len(), ng.Generations())
		}
	}
	if dups == 0 || dups == len(evs) {
		t.Fatalf("%s: degenerate stream, %d duplicates of %d", name, dups, len(evs))
	}
}

func TestNewGuardAnswersExactlyLikeTheOldOneAcrossRandomStreams(t *testing.T) {
	retention := 150 * time.Second // 5 generations: boundaries are crossed constantly
	shapes := map[string][]rateStep{
		"steady":        {{200, 600}},
		"up10x":         {{50, 200}, {500, 300}},
		"down10x":       {{500, 200}, {50, 300}},
		"updown":        {{40, 150}, {400, 150}, {40, 150}, {400, 150}},
		"tiny":          {{3, 900}},
		"fast-then-gap": {{1000, 100}, {1, 400}, {800, 100}},
	}
	seeds := 6
	if testing.Short() {
		seeds = 2
	}
	for name, steps := range shapes {
		for seed := int64(1); seed <= int64(seeds); seed++ {
			for _, dupPct := range []int{5, 40} {
				evs := genStream(seed, steps, retention, dupPct)
				runDifferential(t, fmt.Sprintf("%s/seed%d/dup%d", name, seed, dupPct), evs, retention, 0, false)
			}
		}
	}
}

func TestNewGuardAnswersLikeTheOldOneWhenTheCapBindsAndTheClockSkews(t *testing.T) {
	retention := 150 * time.Second
	for seed := int64(1); seed <= 4; seed++ {
		evs := genStream(seed, []rateStep{{100, 200}, {600, 200}, {60, 200}}, retention, 30)
		// A cap well under the retained volume: whole generations go early, and the head limit
		// (2*cap/(gens+1)) opens extra generations.
		runDifferential(t, fmt.Sprintf("cap/seed%d", seed), evs, retention, 20_000, false)
		runDifferential(t, fmt.Sprintf("capskew/seed%d", seed), evs, retention, 20_000, true)
		runDifferential(t, fmt.Sprintf("skew/seed%d", seed), evs, retention, 0, true)
	}
}

// A primed guard (a replay of the retention's suffix) must decide like one that saw everything:
// the answer depends on the set, never on how the tables were sized on the way.
func TestNewGuardPrimedFromASuffixDecidesLikeTheLiveOne(t *testing.T) {
	retention := 150 * time.Second
	evs := genStream(11, []rateStep{{80, 300}, {600, 100}, {60, 300}}, retention, 30)
	live := NewAppliedGuard(retention, 0)
	cut := len(evs) / 2
	for _, e := range evs[:cut] {
		live.Admit(e.t, e.id)
	}
	horizon := evs[cut].t.Add(-live.Retention() - 2*genSeconds*time.Second)
	start := sort.Search(cut, func(i int) bool { return !evs[i].t.Before(horizon) })
	primed := NewAppliedGuard(retention, 0)
	for _, e := range evs[start:cut] {
		primed.Admit(e.t, e.id)
	}
	for i, e := range evs[cut:] {
		if a, b := live.Admit(e.t, e.id), primed.Admit(e.t, e.id); a != b {
			t.Fatalf("event %d after the cut: live=%v primed=%v", i, a, b)
		}
	}
}

func TestProbeLengthsAtTheMaximumLoadAreBounded(t *testing.T) {
	for _, n := range []int{5_000, 100_000, 600_000} {
		for seed := int64(1); seed <= 3; seed++ {
			rng := rand.New(rand.NewSource(seed))
			s := &generation{}
			// Fill to the load ceiling: the worst a table is ever read at.
			s.slots = make([]uint64, slotsFor(n, maxLoadPercent))
			keys := make([]uint64, 0, n)
			for len(keys) < n {
				f := rng.Uint64() | 1
				if s.has(f) {
					continue
				}
				s.add(f)
				keys = append(keys, f)
			}
			if got := s.count * 100 / len(s.slots); got < maxLoadPercent-2 || got > maxLoadPercent {
				t.Fatalf("n=%d: load %d%%, wanted ~%d%%", n, got, maxLoadPercent)
			}
			maxMiss, sumMiss, maxHit := 0, 0, 0
			const misses = 200_000
			for i := 0; i < misses; i++ {
				_, p := s.probe(rng.Uint64() | 1)
				sumMiss += p
				maxMiss = max(maxMiss, p)
			}
			for _, f := range keys {
				ok, p := s.probe(f)
				if !ok {
					t.Fatalf("n=%d: stored key %x not found", n, f)
				}
				maxHit = max(maxHit, p)
			}
			t.Logf("n=%d seed=%d: mean slots/miss %.2f, max miss %d, max hit %d", n, seed, float64(sumMiss)/misses, maxMiss, maxHit)
			if avg := float64(sumMiss) / misses; avg > 8 {
				t.Errorf("n=%d seed=%d: mean slots per miss %.2f > 8", n, seed, avg)
			}
			if maxMiss > 96 || maxHit > 96 {
				t.Errorf("n=%d seed=%d: max slots per miss/hit %d/%d > 96", n, seed, maxMiss, maxHit)
			}
		}
	}
}

// A table must never fill (has would spin) and must find every key it holds.
func TestEveryKeyIsFoundAndATableNeverPassesItsCeiling(t *testing.T) {
	s := &generation{}
	var keys []uint64
	for i := uint64(1); i <= 50_000; i++ {
		f := i * 0x9e3779b97f4a7c15
		s.add(f)
		keys = append(keys, f)
		if s.count*100 > len(s.slots)*maxLoadPercent {
			t.Fatalf("load %d/%d over the ceiling", s.count, len(s.slots))
		}
	}
	for _, f := range keys {
		if !s.has(f) {
			t.Fatalf("lost %x", f)
		}
	}
	if s.has(12345) {
		t.Fatal("false positive")
	}
}

// bytesPerEvent is the guard's table memory over its remembered events.
func bytesPerEvent(g *AppliedGuard) float64 {
	b := 0
	for _, gn := range g.gens {
		b += 8 * len(gn.slots)
	}
	return float64(b) / float64(g.Len())
}

func feedRate(g *AppliedGuard, n *uint64, from, secs, rate int) {
	base := time.Unix(1_700_000_000, 0)
	for s := from; s < from+secs; s++ {
		for i := 0; i < rate; i++ {
			*n++
			g.Admit(base.Add(time.Duration(s)*time.Second+time.Duration(i)*time.Second/time.Duration(rate)), dig(*n))
		}
	}
}

func TestTheGuardKeepsAboutNineBytesPerEventAtAnyRate(t *testing.T) {
	// 13.5k/s sits where the old power-of-two tables stepped from ~12 to ~18 B/event.
	for _, rate := range []int{700, 6_000, 13_500} {
		g := NewAppliedGuard(150*time.Second, 0)
		var n uint64
		feedRate(g, &n, 0, 400, rate)
		bpe := bytesPerEvent(g)
		t.Logf("rate %d/s: %d events, %d gens, %.2f B/event", rate, g.Len(), g.Generations(), bpe)
		if bpe > 10.0 {
			t.Errorf("rate %d: %.2f B/event (> 10.0)", rate, bpe)
		}
	}
}

func TestTheTablesShrinkWhenTheRateFalls(t *testing.T) {
	g := NewAppliedGuard(150*time.Second, 0)
	var n uint64
	feedRate(g, &n, 0, 200, 2000)
	feedRate(g, &n, 200, 400, 200) // 10x down, long enough to age the fast generations out
	if bpe := bytesPerEvent(g); bpe > 10.0 {
		t.Fatalf("after a 10x fall: %.2f B/event", bpe)
	}
}

// admitter is what the benchmarks drive: the live guard or the vendored old one.
type admitter interface {
	Admit(time.Time, Digest) bool
}

func BenchmarkAppliedGuardAdmit(b *testing.B) {
	for _, impl := range []struct {
		name string
		mk   func() admitter
	}{
		{"new", func() admitter { return NewAppliedGuard(0, 0) }},
		{"old", func() admitter { return newOldGuard(0, 0) }},
	} {
		for _, rate := range []int{6_000, 20_000} {
			b.Run(fmt.Sprintf("%s/rate%d", impl.name, rate), func(b *testing.B) {
				g := impl.mk()
				base := time.Unix(1_700_000_000, 0)
				at := func(i int) time.Time {
					return base.Add(time.Duration(i/rate)*time.Second + time.Duration(i%rate)*time.Second/time.Duration(rate))
				}
				// Warm to full retention, then measure the steady state: 98% new events (a miss
				// in every generation, the expensive case) and 2% duplicates.
				total := rate * 640
				var n uint64
				ids := make([]Digest, 0, 4096)
				for i := 0; i < total; i++ {
					n++
					d := dig(n)
					g.Admit(at(i), d)
					if len(ids) < cap(ids) && i > total-rate*10 && i%50 == 0 {
						ids = append(ids, d)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if i%50 == 0 && len(ids) > 0 {
						g.Admit(at(total+i), ids[(i/50)%len(ids)])
						continue
					}
					n++
					g.Admit(at(total+i), dig(n))
				}
			})
		}
	}
}

// BenchmarkGuardMissLookup isolates the dominant cost of Admit: the probe of every generation for
// an event that is not there (nearly every event). Read-only, so rounds do not drift the state.
func BenchmarkGuardMissLookup(b *testing.B) {
	for _, rate := range []int{6_000, 20_000} {
		fill := func(admit func(time.Time, Digest) bool) {
			base := time.Unix(1_700_000_000, 0)
			var n uint64
			for i := 0; i < rate*640; i++ {
				n++
				admit(base.Add(time.Duration(i/rate)*time.Second+time.Duration(i%rate)*time.Second/time.Duration(rate)), dig(n))
			}
		}
		probes := make([]uint64, 1<<16)
		rng := rand.New(rand.NewSource(7))
		for i := range probes {
			probes[i] = rng.Uint64() | 1
		}
		b.Run(fmt.Sprintf("new/rate%d", rate), func(b *testing.B) {
			g := NewAppliedGuard(0, 0)
			fill(g.Admit)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f := probes[i&(len(probes)-1)]
				for _, gn := range g.gens {
					if gn.has(f) {
						b.Fatal("hit")
					}
				}
			}
		})
		b.Run(fmt.Sprintf("old/rate%d", rate), func(b *testing.B) {
			g := newOldGuard(0, 0)
			fill(g.Admit)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f := probes[i&(len(probes)-1)]
				for _, gn := range g.gens {
					if gn.has(f) {
						b.Fatal("hit")
					}
				}
			}
		})
	}
}

// checkRobinHood verifies the invariant the early exit of a lookup relies on: a resident at
// distance d>0 has an occupied predecessor at distance >= d-1 (so no run is out of order).
func checkRobinHood(t *testing.T, s *generation) {
	t.Helper()
	n := len(s.slots)
	count := 0
	for i, v := range s.slots {
		if v == 0 {
			continue
		}
		count++
		d := dist(v, i, n)
		if d == 0 {
			continue
		}
		pi := (i + n - 1) % n
		pv := s.slots[pi]
		if pv == 0 || dist(pv, pi, n) < d-1 {
			t.Fatalf("slot %d (distance %d): predecessor breaks the Robin Hood order", i, d)
		}
	}
	if count != s.count {
		t.Fatalf("count %d, but %d residents", s.count, count)
	}
}

func TestTheRobinHoodOrderHoldsThroughGrowthAndResize(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	s := &generation{}
	for i := 0; i < 40_000; i++ {
		s.add(rng.Uint64() | 1)
		if i%997 == 0 {
			checkRobinHood(t, s)
		}
	}
	checkRobinHood(t, s)
	s.seal()
	checkRobinHood(t, s)
}

func TestAHeadIsSizedFromThePreviousGenerationAndNeverGrowsAtASteadyRate(t *testing.T) {
	g := NewAppliedGuard(0, 0)
	base := time.Unix(1_700_000_000, 0)
	var n uint64
	feed := func(gen, count int) {
		for i := 0; i < count; i++ {
			n++
			g.Admit(base.Add(time.Duration(gen*genSeconds)*time.Second), dig(n))
		}
	}
	feed(0, 100)
	feed(1, 5000)
	feed(2, 1) // opens generation 2 from generation 1's count, not generation 0's
	if got, want := len(g.gens[2].slots), slotsFor(5000, sizeLoadPercent); got != want {
		t.Fatalf("head opened at %d slots, want %d (sized from the previous generation's 5000)", got, want)
	}
	feed(2, 4999)
	// A rate within the headroom (11% up) fills the head without a single resize.
	feed(3, 5400)
	feed(4, 1)
	if got, want := len(g.gens[3].slots), slotsFor(5000, 90); got < want {
		t.Fatalf("sealed gen below the size its count needs: %d < %d", got, want)
	}
	if got, want := len(g.gens[3].slots), slotsFor(5400, maxLoadPercent); got > want*105/100 {
		t.Fatalf("generation 3 not sealed to its count: %d slots for 5400 (want <= %d)", got, want*105/100)
	}
}

func TestEveryGenerationIsSealedWhenItStopsBeingTheHead(t *testing.T) {
	// A cap so small that the per-generation limit opens extra generations at one instant.
	g := NewAppliedGuard(150*time.Second, 10_000)
	at := time.Unix(1_700_000_000, 0)
	for i := uint64(1); i <= 8000; i++ {
		g.Admit(at, dig(i))
	}
	if g.Generations() < 2 {
		t.Fatalf("expected the head limit to open extra generations, have %d", g.Generations())
	}
	for _, gn := range g.gens[:len(g.gens)-1] {
		if got, want := len(gn.slots), slotsFor(gn.count, maxLoadPercent); got > want*105/100 {
			t.Fatalf("sealed generation holds %d slots for %d entries (want <= %d)", got, gn.count, want*105/100)
		}
	}
}
