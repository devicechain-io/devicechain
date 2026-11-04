// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"encoding/binary"
	"math/rand"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
)

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func dig(n uint64) Digest {
	var d Digest
	binary.BigEndian.PutUint64(d[:8], n*0x9e3779b97f4a7c15+1)
	return d
}

func TestADuplicateIsRefusedAndADistinctEventIsAdmitted(t *testing.T) {
	g := NewAppliedGuard(0, 0)
	if !g.Admit(t0, dig(1)) || !g.Admit(t0, dig(2)) {
		t.Fatal("distinct events must be admitted")
	}
	if g.Admit(t0.Add(time.Second), dig(1)) {
		t.Fatal("the same identity inside the retention must be refused")
	}
	if g.Len() != 2 {
		t.Fatalf("a refused duplicate must not be recorded again; len=%d", g.Len())
	}
}

func TestAnEventPastTheRetentionIsForgottenAndTheRetentionIsAtLeastAsAsked(t *testing.T) {
	g := NewAppliedGuard(6*time.Minute, 0)
	g.Admit(t0, dig(1))
	// 6 minutes later is still inside, however the generation boundary falls.
	if g.Admit(t0.Add(6*time.Minute), dig(1)) {
		t.Fatal("a duplicate 6 minutes behind must still be caught")
	}
	// Keep the stream moving so the old generation can age out.
	for i := 1; i <= 20; i++ {
		g.Admit(t0.Add(time.Duration(i)*time.Minute), dig(uint64(1000+i)))
	}
	if !g.Admit(t0.Add(21*time.Minute), dig(1)) {
		t.Fatal("an event 21 minutes behind is outside the retention and applies (the stated residual)")
	}
}

func TestTheEntryCapDropsTheOldestGeneration(t *testing.T) {
	g := NewAppliedGuard(time.Hour, 100)
	for i := 0; i < 90; i++ {
		g.Admit(t0, dig(uint64(i)))
	}
	for i := 0; i < 90; i++ {
		g.Admit(t0.Add(time.Minute), dig(uint64(500+i)))
	}
	g.Admit(t0.Add(2*time.Minute), dig(9999))
	if g.Len() > 100+90 {
		t.Fatalf("memory not bounded: %d entries", g.Len())
	}
	if !g.Admit(t0.Add(2*time.Minute), dig(1)) {
		t.Fatal("the oldest generation should have been dropped by the cap")
	}
}

func TestTheDefaultRetentionIsThePlatformsRedeliverySpan(t *testing.T) {
	if DefaultAppliedRetention != time.Duration(streams.RedeliveryDuplicateWindowSeconds)*time.Second {
		t.Fatalf("retention %v is not the redelivery span", DefaultAppliedRetention)
	}
	if g := NewAppliedGuard(0, 0); g.Retention() < DefaultAppliedRetention {
		t.Fatalf("rounded retention %v is below the span", g.Retention())
	}
}

// A clock that jumps far ahead and comes back must not grow one generation without bound, nor
// stop the old ones expiring: appends BEHIND the newest generation go to it, and when it is full
// the next entry opens a numbered successor, which then expires like any other.
func TestASkewedFutureAppendTimeCannotGrowOneGenerationWithoutBound(t *testing.T) {
	g := NewAppliedGuard(time.Minute, 4000) // 2 generations back; head limit 2*4000/3
	g.Admit(t0.AddDate(50, 0, 0), dig(0))   // the skew
	for i := 1; i < 20000; i++ {
		g.Admit(t0, dig(uint64(i))) // real time, far behind
	}
	if g.Len() > 4000+g.headLimit {
		t.Fatalf("memory not bounded under a skewed clock: %d entries", g.Len())
	}
	if g.Generations() > int(g.retentionGens)+2 {
		t.Fatalf("%d generations held; the old ones must keep expiring", g.Generations())
	}
	for _, gen := range g.gens {
		if gen.count > g.headLimit+1 {
			t.Fatalf("a generation holds %d entries, over its limit %d", gen.count, g.headLimit)
		}
	}
}

// Replay == live: a guard primed with only the retention's worth of history before a point makes
// the same decision as the guard that watched the whole stream, for every event after it.
func TestAPrimedGuardDecidesLikeTheOneThatSawTheWholeStream(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	type step struct {
		at time.Time
		id Digest
	}
	var steps []step
	at := t0
	for i := 0; i < 6000; i++ {
		at = at.Add(time.Duration(rng.Intn(400)) * time.Millisecond)
		// A third of the events repeat one seen up to ~8 minutes earlier.
		id := dig(uint64(i))
		if i > 100 && rng.Intn(3) == 0 {
			id = steps[len(steps)-1-rng.Intn(min(len(steps)-1, 1200))].id
		}
		steps = append(steps, step{at, id})
	}
	live := NewAppliedGuard(0, 0)
	want := make([]bool, len(steps))
	for i, s := range steps {
		want[i] = live.Admit(s.at, s.id)
	}
	const cut = 4000
	primed := NewAppliedGuard(0, 0)
	cutoff := steps[cut-1].at.Add(-DefaultAppliedRetention - 2*time.Minute)
	for i := 0; i < cut; i++ {
		if !steps[i].at.Before(cutoff) {
			primed.Admit(steps[i].at, steps[i].id)
		}
	}
	for i := cut; i < len(steps); i++ {
		if i == cut {
			t.Logf("primed gens=%d len=%d", len(primed.gens), primed.Len())
		}
		if got := primed.Admit(steps[i].at, steps[i].id); got != want[i] {
			first := -1
			for j := 0; j < i; j++ {
				if steps[j].id == steps[i].id {
					t.Logf("occurrence %d at %v admitted=%v", j, steps[j].at.Sub(t0), want[j])
					if want[j] {
						first = j
					}
				}
			}
			t.Logf("now at %v", steps[i].at.Sub(t0))
			t.Fatalf("step %d: primed=%v live=%v; last admitted occurrence %d, age %v, cut %d", i, got, want[i], first, steps[i].at.Sub(steps[first].at), cut)
		}
	}
	dups := 0
	for _, w := range want {
		if !w {
			dups++
		}
	}
	if dups < 500 {
		t.Fatalf("the corpus held only %d duplicates; it does not exercise the guard", dups)
	}
}

func TestEventIdentityIgnoresProcessedTimeAndSeparatesEvents(t *testing.T) {
	mk := func(device string, v string, processed time.Time) *resolvedevent.ResolvedEvent {
		return &resolvedevent.ResolvedEvent{
			SourceDeviceToken: device, OccurredTime: t0, ProcessedTime: processed, EventType: eventtype.Measurement,
			Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
				OccurredTime: t0, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: v}},
			}}},
		}
	}
	a, _ := EventIdentity("acme", mk("d1", "20.0", t0))
	b, _ := EventIdentity("acme", mk("d1", "20.0", t0.Add(time.Minute)))
	if a != b {
		t.Fatal("a re-resolution of one event (a later processed time) must keep its identity")
	}
	for name, other := range map[string]*resolvedevent.ResolvedEvent{
		"device": mk("d2", "20.0", t0), "value": mk("d1", "20.1", t0),
	} {
		if c, _ := EventIdentity("acme", other); c == a {
			t.Fatalf("a different %s must have a different identity", name)
		}
	}
	if c, _ := EventIdentity("other", mk("d1", "20.0", t0)); c == a {
		t.Fatal("a different tenant must have a different identity")
	}
}
