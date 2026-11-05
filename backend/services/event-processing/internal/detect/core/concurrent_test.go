// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The concurrent engine is held to the same oracle as the synchronous one: the plain Engine, driven
// through the same operations, must be indistinguishable from it, down to the snapshot bytes. Two
// shapes of run matter:
//
//   - step-locked: every operation is followed by a comparison, which waits for the shards (a
//     barrier). It proves the hand-off is right but gives the shards no room to run ahead;
//   - lazy: the engine is fed every operation and compared only every few. The shards run behind
//     the reference, with their queues holding messages, which is where an ordering or a lost-tick
//     defect would show.

// syncedConcurrent is the concurrent engine as the differential drives it. The one difference is
// DrainLateSamples, which the engine answers without waiting and the harness compares exactly, so
// it waits first.
type syncedConcurrent struct{ *ConcurrentSharded }

func (s syncedConcurrent) DrainLateSamples() uint64 {
	s.Sync()
	return s.ConcurrentSharded.DrainLateSamples()
}

func concurrentUnderTest(k int, salt uint32) engineBuilder {
	return engineBuilder{
		name: fmt.Sprintf("concurrent(K=%d,salt=%#x)", k, salt),
		fresh: func(rules []Rule, lateness time.Duration) detectEngine {
			return syncedConcurrent{wrapConcurrent(newSharded(k, salt, rules, lateness))}
		},
		restore: func(rules []Rule, lateness time.Duration, snap []byte) (detectEngine, error) {
			s, err := restoreSharded(k, salt, rules, lateness, snap)
			if err != nil {
				return nil, err
			}
			return syncedConcurrent{wrapConcurrent(s)}, nil
		},
	}
}

// diffLazyEvery are the comparison intervals of the lazy runs. The long one matters: a window needs
// several messages per device before a series can have two detections in it to put out of order,
// and the shards need room to be far behind before a missing barrier can be seen.
var diffLazyEvery = []int{3, 29, 300}

func TestConcurrentEngineIsTheEngine(t *testing.T) {
	for _, k := range diffShardKs {
		for _, salt := range diffSalts {
			for _, seed := range diffSeeds {
				k, salt, seed := k, salt, seed
				t.Run(fmt.Sprintf("K=%d/salt=%#x/seed=%d", k, salt, seed), func(t *testing.T) {
					if _, err := runDifferential(seed, diffOpCount, plainEngineBuilder(), concurrentUnderTest(k, salt), nil); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// The lazy differential: the shards run behind the reference.
func TestConcurrentEnginePipelinedIsTheEngine(t *testing.T) {
	for _, k := range diffShardKs {
		for _, salt := range diffSalts {
			for _, seed := range diffSeeds {
				for _, every := range diffLazyEvery {
					k, salt, seed, every := k, salt, seed, every
					t.Run(fmt.Sprintf("K=%d/salt=%#x/seed=%d/every=%d", k, salt, seed, every), func(t *testing.T) {
						if _, err := runDifferentialLazy(seed, diffOpCount, plainEngineBuilder(), concurrentUnderTest(k, salt), nil, every); err != nil {
							t.Fatal(err)
						}
					})
				}
			}
		}
	}
}

// A restart changes K and the hash: the concurrent engine resumes from the plain engine's snapshot
// and the plain engine from the concurrent engine's.
func TestConcurrentEngineRestoresAcrossShardCounts(t *testing.T) {
	restartAs := map[int]int{2: 5, 3: 16, 7: 2, 16: 3}
	for _, k := range diffShardKs {
		for _, seed := range diffSeeds {
			for _, every := range []int{0, 11} {
				k, seed, every := k, seed, every
				t.Run(fmt.Sprintf("K=%d-to-K=%d/seed=%d/lazy=%d", k, restartAs[k], seed, every), func(t *testing.T) {
					at := diffOpCount/4 + int(seed*37)%(diffOpCount/2)
					_, err := runDifferentialLazy(seed, diffOpCount, plainEngineBuilder(), concurrentUnderTest(k, diffSalts[0]),
						&diffCrash{at: at, restoreAs: concurrentUnderTest(restartAs[k], diffSalts[1])}, every)
					if err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// Rollback: what a concurrent build wrote, the plain engine reads and continues from.
func TestConcurrentEngineRestoresIntoPlainEngine(t *testing.T) {
	for _, seed := range diffSeeds {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_, err := runDifferentialLazy(seed, diffOpCount, plainEngineBuilder(), concurrentUnderTest(4, 0),
				&diffCrash{at: diffOpCount / 2, restoreAs: plainEngineBuilder()}, 13)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The concurrent engine and the synchronous one, side by side: same snapshot bytes at the end.
func TestConcurrentEngineMatchesSynchronousSharded(t *testing.T) {
	for _, seed := range diffSeeds {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			if _, err := runDifferentialLazy(seed, diffOpCount, saltedShardedUnderTest(4, 0), concurrentUnderTest(4, 0), nil, 17); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A detection carries the tag of the operation that made it, however far behind the dispatcher
// the shard runs.
func TestConcurrentTagsFollowTheirMessage(t *testing.T) {
	g := newDiffGen(7)
	c := NewConcurrentSharded(4, g.rulesNow(), diffLateness)
	defer c.Close()
	plain := NewEngine(g.rulesNow(), diffLateness)
	type id struct {
		rule, series string
		edge         EdgeKind
		at           time.Time
	}
	wantTag := map[id]time.Time{}
	seq := uint64(0)
	for i := 0; i < 600; i++ {
		_, tm, evs := g.message()
		seq++
		stamp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Second)
		c.Tag(stamp, true)
		c.ProcessResolved(seq, tm, evs)
		plain.ProcessResolved(seq, tm, evs)
		for _, d := range plain.Drain() {
			wantTag[id{d.RuleID, d.Series, d.Edge, d.At}] = stamp
		}
	}
	got := c.Collect()
	c.Sync()
	got = append(got, c.Collect()...)
	if len(got) == 0 || len(wantTag) == 0 {
		t.Fatalf("the run detected nothing (%d, %d): the check means nothing", len(got), len(wantTag))
	}
	if len(got) != len(wantTag) {
		t.Fatalf("collected %d detections, the reference made %d", len(got), len(wantTag))
	}
	for _, d := range got {
		w, ok := wantTag[id{d.RuleID, d.Series, d.Edge, d.At}]
		if !ok {
			t.Fatalf("a detection the reference never made: %+v", d.Detection)
		}
		if !d.Tag.At.Equal(w) || !d.Tag.Replayable {
			t.Fatalf("detection %s/%s at %s carries tag %+v, want %s replayable", d.RuleID, d.Series, d.At, d.Tag, w)
		}
	}
}

// Collect returns one series' detections in the order its engine emitted them, however the batches
// were cut.
func TestConcurrentCollectKeepsPerSeriesOrder(t *testing.T) {
	g := newDiffGen(3)
	c := NewConcurrentSharded(5, g.rulesNow(), diffLateness)
	defer c.Close()
	plain := NewEngine(g.rulesNow(), diffLateness)
	var ref, got []Detection
	seq := uint64(0)
	for i := 0; i < 900; i++ {
		_, tm, evs := g.message()
		seq++
		c.ProcessResolved(seq, tm, evs)
		plain.ProcessResolved(seq, tm, evs)
		ref = append(ref, plain.Drain()...)
		if i%37 == 0 {
			for _, d := range c.Collect() { // whatever the shards have finished, without waiting
				got = append(got, d.Detection)
			}
		}
	}
	c.Sync()
	for _, d := range c.Collect() {
		got = append(got, d.Detection)
	}
	if at, ok := detectionsEqual(normDetections(ref), normDetections(got)); !ok || len(ref) == 0 {
		t.Fatalf("collected detections differ from the reference's (first at %d, %d vs %d)", at, len(got), len(ref))
	}
}

func TestConcurrentLateSamplesAreNotLost(t *testing.T) {
	g := newDiffGen(5)
	c := NewConcurrentSharded(4, g.rulesNow(), diffLateness)
	defer c.Close()
	plain := NewEngine(g.rulesNow(), diffLateness)
	var total, want uint64
	seq := uint64(0)
	for i := 0; i < 1500; i++ {
		_, tm, evs := g.message()
		seq++
		c.ProcessResolved(seq, tm, evs)
		plain.ProcessResolved(seq, tm, evs)
		total += c.DrainLateSamples()
	}
	c.Sync()
	total += c.DrainLateSamples()
	want = plain.DrainLateSamples()
	if want == 0 {
		t.Skip("the generator made no late samples on this seed")
	}
	if total != want {
		t.Fatalf("late samples: concurrent %d, plain %d", total, want)
	}
}

type countingObserver struct{ barriers, violations atomic.Int64 }

func (o *countingObserver) BarrierWait(time.Duration) { o.barriers.Add(1) }
func (o *countingObserver) InvariantViolation()       { o.violations.Add(1) }

// A shard that fell out of step is the one thing a snapshot must refuse: it is a state no single
// engine could be in. Tested by breaking the invariant from inside the package.
func TestConcurrentSnapshotRefusesShardsOutOfStep(t *testing.T) {
	for _, brk := range []struct {
		name string
		do   func(c *ConcurrentSharded)
	}{
		{"one shard missed a skip", func(c *ConcurrentSharded) { c.s.shards[2].Skip(c.lastSeq + 5); c.s.shards[1].Skip(c.lastSeq + 6) }},
		{"the dispatcher is ahead of the shards", func(c *ConcurrentSharded) { c.lastSeq += 3 }},
		{"one shard's frontier is ahead", func(c *ConcurrentSharded) {
			c.s.shards[1].Advance(c.s.shards[1].Watermark().Add(time.Hour))
		}},
	} {
		brk := brk
		t.Run(brk.name, func(t *testing.T) {
			g := newDiffGen(2)
			obs := &countingObserver{}
			c := NewConcurrentSharded(3, g.rulesNow(), diffLateness)
			c.SetObserver(obs)
			defer c.Close()
			seq := uint64(0)
			for i := 0; i < 50; i++ {
				_, tm, evs := g.message()
				seq++
				c.ProcessResolved(seq, tm, evs)
			}
			if _, err := c.Snapshot(); err != nil {
				t.Fatalf("a healthy engine refused to snapshot: %v", err)
			}
			brk.do(c)
			if _, err := c.Snapshot(); err == nil {
				t.Fatal("the snapshot was written from shards that disagree")
			}
			if obs.violations.Load() != 1 {
				t.Fatalf("invariant violations counted: %d, want 1", obs.violations.Load())
			}
			if obs.barriers.Load() == 0 {
				t.Fatal("no barrier wait was observed")
			}
		})
	}
}

// Close joins every shard goroutine, and is idempotent.
func TestConcurrentCloseJoinsTheShards(t *testing.T) {
	before := shardWorkers()
	var engines []*ConcurrentSharded
	g := newDiffGen(1)
	for i := 0; i < 8; i++ {
		c := NewConcurrentSharded(16, g.rulesNow(), diffLateness)
		_, tm, evs := g.message()
		c.ProcessResolved(uint64(i+1), tm, evs)
		engines = append(engines, c)
	}
	if during := shardWorkers(); during != before+8*16 {
		t.Fatalf("expected %d shard goroutines, found %d", before+8*16, during)
	}
	for _, c := range engines {
		c.Close()
		c.Close()
	}
	// Close returns when the workers have signalled; give each a moment to leave its frame.
	deadline := time.Now().Add(5 * time.Second)
	for shardWorkers() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := shardWorkers(); after > before {
		t.Fatalf("%d shard goroutines outlived Close", after-before)
	}
}

// shardWorkers counts the running shard goroutines by their stacks; the process's total goroutine
// count also moves with the race detector and the test runner, which is not what is being asked.
func shardWorkers() int {
	buf := make([]byte, 4<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "(*shardWorker).run")
}

// A redelivery at or below the position is ignored by every shard, and by the dispatcher without
// queueing it: the position must not move backwards and the tag set for it must not leak onto the
// next message.
func TestConcurrentStaleMessageIsIgnoredAndDoesNotLeakItsTag(t *testing.T) {
	g := newDiffGen(4)
	c := NewConcurrentSharded(3, g.rulesNow(), diffLateness)
	defer c.Close()
	_, tm, evs := g.message()
	c.Tag(tm, true)
	c.ProcessResolved(5, tm, evs)
	for _, seq := range []uint64{5, 3} { // a redelivery of the last message, and an older one
		c.Tag(tm.Add(time.Hour), true)
		c.ProcessResolved(seq, tm.Add(time.Hour), evs)
		if c.tag != (Tag{}) {
			t.Fatalf("a stale message %d left tag %+v pending", seq, c.tag)
		}
		if c.LastSeq() != 5 {
			t.Fatalf("a stale message %d moved the position to %d", seq, c.LastSeq())
		}
	}
	if c.Watermark().After(tm) {
		t.Fatalf("a stale message moved the frontier to %s", c.Watermark())
	}
	if _, err := c.Snapshot(); err != nil {
		t.Fatalf("snapshot after stale messages: %v", err)
	}
}

// What a barrier call or an in-band control operation makes an engine emit is collectable as soon
// as the call has returned (Advance) or the shards are synced (a dead-man disarm), under no later
// operation. The idle advance counts what Advance fired the moment it returns, and a checkpoint
// publishes what a Sync left behind.
func TestConcurrentControlDetectionsAreCollectableWithoutALaterOperation(t *testing.T) {
	g := newDiffGen(8)
	key := SeriesKey{Rule: diffTenantA + diffRulePrefix + "abs", Series: "d31"}
	since := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	plain := NewEngine(g.rulesNow(), diffLateness)
	c := NewConcurrentSharded(4, g.rulesNow(), diffLateness)
	defer c.Close()

	// Arm a dead-man for a device that never reports and let it fire: Advance raises the absence.
	plain.SetExpected(key, since)
	c.SetExpected(key, since)
	late := since.Add(time.Hour)
	if got, want := c.Advance(late), plain.Advance(late); got != want {
		t.Fatalf("Advance answered %v, the engine %v", got, want)
	}
	want := plain.Drain()
	got := c.Collect() // no Sync, no later operation: Advance is a barrier call
	if len(want) == 0 {
		t.Fatal("the dead-man did not fire in the reference: the check means nothing")
	}
	if at, ok := detectionsEqual(normDetections(want), normDetections(detectionsOf(got))); !ok {
		t.Fatalf("what Advance fired is not collectable right after it (first at %d; %d vs %d)", at, len(got), len(want))
	}

	// Disarming resolves the latch at the watermark, in band; once synced it is collectable.
	plain.RemoveExpected(key)
	c.RemoveExpected(key)
	c.Sync()
	want = plain.Drain()
	got = c.Collect()
	if len(want) != 1 || want[0].Edge != EdgeResolved {
		t.Fatalf("the reference did not resolve on disarm: %+v", want)
	}
	if at, ok := detectionsEqual(want, detectionsOf(got)); !ok {
		t.Fatalf("the disarm's resolve is not collectable after a sync (first at %d; %d vs %d)", at, len(got), len(want))
	}
	if !got[0].Tag.At.IsZero() {
		t.Fatalf("a control operation with no message behind it carries tag %+v", got[0].Tag)
	}
}

func detectionsOf(in []TaggedDetection) []Detection {
	out := make([]Detection, len(in))
	for i := range in {
		out[i] = in[i].Detection
	}
	return out
}

// The merge of K sorted runs is the canonical order even where keys tie on all but their last
// component. Real shards hold disjoint keys, so the tie-breaks only matter at the places the
// canonical order itself has them: timers share a deadline all the time (a roster armed from one
// instant), and then order by rule and series.
func TestMergeSnapshotsOrdersTimersThatShareADeadline(t *testing.T) {
	dl := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	a := snapshot{Timers: []snapTimer{{Deadline: dl, Rule: "r", Series: "b"}, {Deadline: dl.Add(time.Second), Rule: "r", Series: "a"}}}
	b := snapshot{Timers: []snapTimer{{Deadline: dl, Rule: "r", Series: "a"}, {Deadline: dl, Rule: "r", Series: "c"}}}
	for _, order := range [][]snapshot{{a, b}, {b, a}} {
		got := mergeSnapshots(order).Timers
		want := []string{"a", "b", "c", "a"}
		for i, w := range want {
			if got[i].Series != w {
				t.Fatalf("merged timer %d is series %q, want %q (%+v)", i, got[i].Series, w, got)
			}
		}
	}
}
func TestConcurrentShardStats(t *testing.T) {
	g := newDiffGen(6)
	c := NewConcurrentSharded(4, g.rulesNow(), diffLateness)
	defer c.Close()
	for i := 0; i < 200; i++ {
		_, tm, evs := g.message()
		c.ProcessResolved(uint64(i+1), tm, evs)
	}
	c.Sync()
	var busy uint64
	for _, s := range c.ShardStats() {
		busy += s.BusyNanos
		if s.QueueDepth != 0 {
			t.Fatalf("queue depth %d after Sync", s.QueueDepth)
		}
	}
	if busy == 0 {
		t.Fatal("no shard reported any busy time")
	}
	live := 0
	for _, n := range c.ShardLiveKeys() {
		live += n
	}
	total := 0
	for _, n := range c.LiveKeyCounts() {
		total += n
	}
	if live != total || total == 0 {
		t.Fatalf("per-shard live keys sum to %d, total is %d", live, total)
	}
}

// An event envelope is several members under one stream sequence: the first through
// ProcessReceived, the rest through ProcessReceivedMember, each with its own receipt. The
// differential generator drives neither the member entry point nor receipts, so this does: messages
// are cut into members at random and fed to the plain engine and to the concurrent one, and the
// detections and the snapshot must agree.
func TestConcurrentEnvelopeMembersMatchTheEngine(t *testing.T) {
	for _, k := range []int{2, 5} {
		for _, seed := range diffSeeds {
			k, seed := k, seed
			t.Run(fmt.Sprintf("K=%d/seed=%d", k, seed), func(t *testing.T) {
				g := newDiffGen(seed)
				c := NewConcurrentSharded(k, g.rulesNow(), diffLateness)
				defer c.Close()
				plain := NewEngine(g.rulesNow(), diffLateness)
				var ref []Detection
				seq := uint64(0)
				stamp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
				for i := 0; i < 700; i++ {
					_, tm, evs := g.message()
					seq++
					// Alternate receipts so the redelivery test inside the threshold rule sees both.
					r := Receipt{Received: stamp.Add(time.Duration(i) * time.Second), Stored: stamp.Add(time.Duration(i) * time.Second)}
					if i%3 == 0 {
						r = Receipt{}
					}
					cut := 0
					if len(evs) > 1 {
						cut = 1 + g.rng.Intn(len(evs)-1)
					}
					first, rest := evs[:cut], evs[cut:]
					plain.ProcessReceived(seq, tm, r, first)
					c.ProcessReceived(seq, tm, r, first)
					if len(rest) > 0 {
						plain.ProcessReceivedMember(seq, tm.Add(time.Millisecond), r, rest)
						c.ProcessReceivedMember(seq, tm.Add(time.Millisecond), r, rest)
					}
					ref = append(ref, plain.Drain()...)
				}
				got := c.Drain()
				if at, ok := detectionsEqual(normDetections(ref), normDetections(got)); !ok || len(ref) == 0 {
					t.Fatalf("detections differ from the engine's (first at %d; %d vs %d)", at, len(got), len(ref))
				}
				a, err := plain.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				b, err := c.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if string(a) != string(b) {
					t.Fatalf("snapshot bytes differ: %s", firstDiff(a, b))
				}
			})
		}
	}
}

// A closed engine keeps answering, on the caller, with the same results.
func TestConcurrentClosedEngineKeepsWorking(t *testing.T) {
	g := newDiffGen(9)
	c := NewConcurrentSharded(3, g.rulesNow(), diffLateness)
	plain := NewEngine(g.rulesNow(), diffLateness)
	seq := uint64(0)
	step := func(n int) {
		for i := 0; i < n; i++ {
			_, tm, evs := g.message()
			seq++
			c.ProcessResolved(seq, tm, evs)
			plain.ProcessResolved(seq, tm, evs)
		}
	}
	step(200)
	c.Close()
	step(200)
	c.SetExpected(SeriesKey{Rule: diffTenantA + diffRulePrefix + "abs", Series: "d31"}, time.Time{}.Add(time.Hour))
	plain.SetExpected(SeriesKey{Rule: diffTenantA + diffRulePrefix + "abs", Series: "d31"}, time.Time{}.Add(time.Hour))
	if got, want := c.Descope("x/none", "d1", time.Time{}), plain.Descope("x/none", "d1", time.Time{}); got != want {
		t.Fatalf("Descope after Close: %v, want %v", got, want)
	}
	if at, ok := detectionsEqual(normDetections(plain.Drain()), normDetections(c.Drain())); !ok {
		t.Fatalf("detections after Close differ (first at %d)", at)
	}
	a, _ := plain.Snapshot()
	b, err := c.Snapshot()
	if err != nil || string(a) != string(b) {
		t.Fatalf("snapshot after Close: err=%v %s", err, firstDiff(a, b))
	}
}

// BenchmarkConcurrentProcessResolved is the concurrent engine on the stream the synchronous
// benchmarks use, against the plain engine and the synchronous K-way one. The loop's work per
// message here is routing and queueing; the engines run on their own cores, and the timed region
// ends when the last shard has applied the last message. Detections are taken in every 1024
// messages, as a checkpoint interval would.
//
//	go test -run '^$' -bench BenchmarkConcurrentProcessResolved -benchtime 400000x
func BenchmarkConcurrentProcessResolved(b *testing.B) {
	const devices = 1000
	msgs := shardedWorkload(shardedBenchRules, devices, 4096)
	b.Run("plain", func(b *testing.B) {
		e := NewEngine(shardedBenchRules, 0)
		b.ReportAllocs()
		b.ResetTimer()
		drivePast(msgs, b.N, true, e.ProcessResolved, func() { e.Drain() })
	})
	for _, k := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("concurrent-K=%d", k), func(b *testing.B) {
			c := NewConcurrentSharded(k, shardedBenchRules, 0)
			defer c.Close()
			b.ReportAllocs()
			b.ResetTimer()
			n := 0
			drivePast(msgs, b.N, true, c.ProcessResolved, func() {
				if n++; n%1024 == 0 {
					c.Collect()
				}
			})
			c.Sync()
			c.Collect()
		})
	}
}

// Collect hands over a strictly advancing prefix of the (operation, shard) order. With one shard
// held up, a detection a fast shard made for a LATER operation is not collected, because the slow
// shard may still owe an earlier detection for the same series (another rule on the same device).
func TestConcurrentCollectWaitsForTheSlowestShard(t *testing.T) {
	g := newDiffGen(11)
	c := NewConcurrentSharded(2, g.rulesNow(), diffLateness)
	defer c.Close()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()                           // a failing check must not leave shard 1 stuck for Close to wait on
	c.ownerAsync(1, func(*Engine) { <-gate }) // shard 1 is stuck on its first operation
	seq := uint64(0)
	for i := 0; i < 400; i++ {
		_, tm, evs := g.message()
		seq++
		c.ProcessResolved(seq, tm, evs)
	}
	// Shard 0 runs to the end of its queue.
	deadline := time.Now().Add(10 * time.Second)
	for c.workers[0].doneItem.Load() < c.sent[0] {
		if time.Now().After(deadline) {
			t.Fatal("shard 0 never finished its queue")
		}
		time.Sleep(time.Millisecond)
	}
	c.workers[0].mu.Lock()
	made := len(c.workers[0].out)
	c.workers[0].mu.Unlock()
	if made == 0 {
		t.Fatal("shard 0 made no detections: the check means nothing")
	}
	if got := c.Collect(); len(got) != 0 {
		t.Fatalf("collected %d detections while shard 1 had not applied its first operation", len(got))
	}
	release()
	c.Sync()
	if got := c.Collect(); len(got) < made {
		t.Fatalf("after the shards caught up, collected %d, shard 0 alone made %d", len(got), made)
	}
}
